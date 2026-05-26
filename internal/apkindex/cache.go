package apkindex

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// parserVersion is bumped whenever the on-disk cache format changes
// (Entry field set, encoding, or order). A cache file whose header
// carries a different parserVersion is treated as a miss and rewritten.
const parserVersion uint32 = 1

// cacheMagic is the 4-byte signature at the start of every cache file.
// "YAIC" = Yoe ApkIndex Cache.
var cacheMagic = [4]byte{'Y', 'A', 'I', 'C'}

// cacheHeaderLen is the on-disk header size in bytes. Fixed-width so
// the format never needs a separate length field. Layout:
//
//	[0..4)     magic            "YAIC"
//	[4..8)     parser_version   uint32 little-endian
//	[8..40)    source_sha256    [32]byte
//	[40..44)   entry_count      uint32 LE — informational; bounds check
//	[44..60)   reserved         16 bytes (zero) — room for forward growth
const cacheHeaderLen = 60

// CacheKey identifies a parsed-index cache by the SHA256 of the source
// APKINDEX bytes. Two callers parsing byte-identical APKINDEX content
// produce byte-identical caches.
type CacheKey [32]byte

// HashSource computes a CacheKey from APKINDEX source bytes.
func HashSource(data []byte) CacheKey {
	return sha256.Sum256(data)
}

// LoadCache reads a parsed-index cache from path and returns the
// entries + provides table it carries. Returns (nil, nil, nil) for any
// cache miss — wrong magic, wrong parser version, source hash mismatch,
// truncated body, or missing file. Hard I/O errors (permission denied,
// disk failure) propagate as non-nil err so the caller can distinguish
// "cache absent" from "the disk is on fire."
//
// The wantSource parameter is the SHA256 the caller has already computed
// from the source bytes; mismatch means the on-disk APKINDEX changed
// since the cache was written.
func LoadCache(path string, wantSource CacheKey) ([]Entry, *ProvidesTable, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("apkindex: cache open: %w", err)
	}
	defer f.Close()
	return decodeCache(f, wantSource)
}

// SaveCache writes entries to path in the cache format. Writes via
// `<path>.tmp` + rename so a SIGINT mid-write leaves no partial file at
// the canonical path. Two callers writing the same content key produce
// byte-identical caches, so racing writers are benign.
func SaveCache(path string, source CacheKey, entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("apkindex: cache dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("apkindex: cache create: %w", err)
	}
	defer func() {
		// If we return before rename, drop the tmp so we don't litter.
		if _, statErr := os.Stat(tmp); statErr == nil {
			_ = os.Remove(tmp)
		}
	}()

	if err := encodeCache(f, source, entries); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("apkindex: cache fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("apkindex: cache close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("apkindex: cache rename: %w", err)
	}
	return nil
}

// ParseIndexWithCache reads an APKINDEX from sourcePath, returning the
// parsed entries and a provides table built from them. If a cache file
// exists at sourcePath+".cache" with a matching content hash and parser
// version, it is loaded without re-parsing. Otherwise the source is
// parsed fresh and the cache is rewritten as a side effect.
//
// Cache failures are non-fatal: a corrupt or unreadable cache just
// triggers a fresh parse + rewrite. Parse failures and source I/O
// errors propagate.
func ParseIndexWithCache(sourcePath string) ([]Entry, *ProvidesTable, error) {
	src, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, nil, fmt.Errorf("apkindex: read source: %w", err)
	}
	key := HashSource(src)
	cachePath := sourcePath + ".cache"

	entries, table, err := LoadCache(cachePath, key)
	if err == nil && entries != nil {
		return entries, table, nil
	}

	entries, err = ParseIndex(bytesReader(src))
	if err != nil {
		return nil, nil, err
	}
	table = BuildProvidesTable(entries)

	// Cache write failures are non-fatal — the parse already succeeded
	// and the next run will retry. Surface to stderr so a chronic
	// failure (read-only mount, disk full) is visible.
	if err := SaveCache(cachePath, key, entries); err != nil {
		fmt.Fprintf(os.Stderr, "apkindex: cache write: %v\n", err)
	}
	return entries, table, nil
}

// encodeCache writes header + entries to w. Format details live next to
// the field layout in `parserVersion` / `cacheHeaderLen`.
func encodeCache(w io.Writer, source CacheKey, entries []Entry) error {
	hdr := make([]byte, cacheHeaderLen)
	copy(hdr[0:4], cacheMagic[:])
	binary.LittleEndian.PutUint32(hdr[4:8], parserVersion)
	copy(hdr[8:40], source[:])
	binary.LittleEndian.PutUint32(hdr[40:44], uint32(len(entries)))
	if _, err := w.Write(hdr); err != nil {
		return fmt.Errorf("apkindex: cache write header: %w", err)
	}

	bw := newCacheWriter(w)
	for i := range entries {
		if err := bw.writeEntry(&entries[i]); err != nil {
			return fmt.Errorf("apkindex: cache write entry %d: %w", i, err)
		}
	}
	return bw.flush()
}

// decodeCache reads header + entries from r. Returns the same cache-miss
// rules as LoadCache.
func decodeCache(r io.Reader, wantSource CacheKey) ([]Entry, *ProvidesTable, error) {
	hdr := make([]byte, cacheHeaderLen)
	if _, err := io.ReadFull(r, hdr); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil, nil // truncated → miss
		}
		return nil, nil, fmt.Errorf("apkindex: cache read header: %w", err)
	}
	if hdr[0] != cacheMagic[0] || hdr[1] != cacheMagic[1] ||
		hdr[2] != cacheMagic[2] || hdr[3] != cacheMagic[3] {
		return nil, nil, nil
	}
	if binary.LittleEndian.Uint32(hdr[4:8]) != parserVersion {
		return nil, nil, nil
	}
	var gotSource CacheKey
	copy(gotSource[:], hdr[8:40])
	if gotSource != wantSource {
		return nil, nil, nil
	}
	count := binary.LittleEndian.Uint32(hdr[40:44])

	br := newCacheReader(r)
	entries := make([]Entry, 0, count)
	for range count {
		var e Entry
		if err := br.readEntry(&e); err != nil {
			// Body truncation / corruption: treat as miss, reparse.
			return nil, nil, nil
		}
		entries = append(entries, e)
	}
	return entries, BuildProvidesTable(entries), nil
}

// bytesReader avoids an import cycle on bytes by keeping the call
// site visible. (parse.go already pulls in io / bufio.)
func bytesReader(b []byte) io.Reader {
	return &sliceReader{b: b}
}

type sliceReader struct {
	b []byte
	i int
}

func (s *sliceReader) Read(p []byte) (int, error) {
	if s.i >= len(s.b) {
		return 0, io.EOF
	}
	n := copy(p, s.b[s.i:])
	s.i += n
	return n, nil
}
