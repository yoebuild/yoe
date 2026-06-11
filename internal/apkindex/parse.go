// Package apkindex parses Alpine APKINDEX files into structured entries
// the synthetic-module loader can materialize on demand.
//
// APKINDEX is a tar.gz of two files: DESCRIPTION (free-form text) and
// APKINDEX (line-oriented, deb822-ish, single-letter keys). Each entry
// is separated by a blank line. Within an entry, lines are "K:value"
// where K is a single ASCII letter.
//
// Documented at <https://wiki.alpinelinux.org/wiki/Apk_spec#Index_format>.
package apkindex

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Entry is one parsed APKINDEX block. Field names mirror Alpine's
// single-letter keys (P, V, T, ...) by intent — see the K-to-field map
// in parseEntry — so a reader can cross-reference upstream docs.
type Entry struct {
	Name          string   // P
	Version       string   // V
	Description   string   // T (title)
	URL           string   // U
	License       string   // L
	Arch          string   // A
	Size          int64    // S — apk file size in bytes
	InstalledSize int64    // I — installed footprint
	Origin        string   // o — source-package origin
	Maintainer    string   // m
	BuildTime     int64    // t — unix timestamp
	Commit        string   // c — aports commit sha

	// Checksum is the raw SHA1 bytes decoded from the APKINDEX `C:`
	// field. The on-disk format is "Q1<base64-sha1>="; we keep both the
	// raw bytes (for verification) and the encoded form (for hashing
	// into the unit cache key, where it must match what alpine_pkg
	// units already write).
	Checksum     []byte
	ChecksumText string // verbatim `C:` value, including "Q1" prefix

	// Raw dep strings — split on whitespace, not parsed. ParseDep
	// turns each token into a Dep.
	Deps      []string // D
	Provides  []string // p
	Replaces  []string // r
	InstallIf []string // i
}

// ParseIndex reads APKINDEX text (the inner file, not the tar.gz wrapper)
// and returns one Entry per blank-separated block. Entries with no `P:`
// line are dropped silently — Alpine occasionally emits a leading
// "DESCRIPTION"-like preface; the absence of P signals "not a package."
func ParseIndex(r io.Reader) ([]Entry, error) {
	var entries []Entry
	sc := bufio.NewScanner(r)
	// APKINDEX `T:` fields can carry long descriptions; default 64 KiB
	// scanner buffer is enough for any real-world line but bump the
	// max anyway so a pathological future line doesn't fail mid-parse.
	sc.Buffer(make([]byte, 64*1024), 1<<20)

	var (
		cur     Entry
		curHas  bool // has at least one field set
		lineNum int
		blockLn int // line number where the current block began
	)

	flush := func() error {
		if !curHas {
			return nil
		}
		if cur.Name == "" {
			return fmt.Errorf("apkindex: line %d: block has no P: (package name)", blockLn)
		}
		if cur.ChecksumText != "" {
			raw, err := decodeChecksum(cur.ChecksumText)
			if err != nil {
				return fmt.Errorf("apkindex: line %d: %s: %w", blockLn, cur.Name, err)
			}
			cur.Checksum = raw
		}
		entries = append(entries, cur)
		cur = Entry{}
		curHas = false
		return nil
	}

	for sc.Scan() {
		lineNum++
		line := sc.Text()
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		// Lines too short to carry "K:..." are skipped — Alpine indices
		// don't include comments, but a stray malformed line shouldn't
		// kill the parse.
		if len(line) < 2 || line[1] != ':' {
			continue
		}
		if !curHas {
			blockLn = lineNum
			curHas = true
		}
		key := line[0]
		val := line[2:]
		if err := setField(&cur, key, val, lineNum); err != nil {
			return nil, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("apkindex: scan: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return entries, nil
}

// ParseIndexFile opens path (a plain APKINDEX text file) and parses it.
// Use ParseIndexTarGz for the `APKINDEX.tar.gz` Alpine ships upstream.
func ParseIndexFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseIndex(f)
}

// ParseIndexTarGz reads an Alpine APKINDEX.tar.gz from r and parses the
// inner APKINDEX member. The tarball also contains a DESCRIPTION file and
// (for signed indices) a `.SIGN.RSA.<key>` entry; those are ignored here.
// Signature verification is the caller's job (internal/apkindex/verify.go).
func ParseIndexTarGz(r io.Reader) ([]Entry, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("apkindex: gzip open: %w", err)
	}
	defer gz.Close()

	// An APKINDEX.tar.gz is a concatenation of two (or three, with
	// signature) gzip streams. `gzip.Reader.Multistream(true)` (default)
	// stitches them transparently, so a single tar.NewReader walks all
	// members regardless of stream boundaries.
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("apkindex: tar.gz has no APKINDEX member")
		}
		if err != nil {
			return nil, fmt.Errorf("apkindex: tar: %w", err)
		}
		if hdr.Name != "APKINDEX" {
			continue
		}
		return ParseIndex(tr)
	}
}

// setField writes one K:value pair into cur. Unknown keys are tolerated
// silently — Alpine occasionally adds fields and a strict parser would
// refuse to load any newer index.
func setField(cur *Entry, key byte, val string, lineNum int) error {
	switch key {
	case 'P':
		cur.Name = val
	case 'V':
		cur.Version = val
	case 'T':
		cur.Description = val
	case 'U':
		cur.URL = val
	case 'L':
		cur.License = val
	case 'A':
		cur.Arch = val
	case 'S':
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("apkindex: line %d: S: %w", lineNum, err)
		}
		cur.Size = n
	case 'I':
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("apkindex: line %d: I: %w", lineNum, err)
		}
		cur.InstalledSize = n
	case 'o':
		cur.Origin = val
	case 'm':
		cur.Maintainer = val
	case 't':
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("apkindex: line %d: t: %w", lineNum, err)
		}
		cur.BuildTime = n
	case 'c':
		cur.Commit = val
	case 'C':
		cur.ChecksumText = val
	case 'D':
		cur.Deps = splitTokens(val)
	case 'p':
		cur.Provides = splitTokens(val)
	case 'r':
		cur.Replaces = splitTokens(val)
	case 'i':
		cur.InstallIf = splitTokens(val)
	}
	return nil
}

// splitTokens splits on whitespace and drops empty tokens. APKINDEX
// dep lists are space-separated; defensive against trailing spaces.
func splitTokens(s string) []string {
	fs := strings.Fields(s)
	if len(fs) == 0 {
		return nil
	}
	return fs
}

// decodeChecksum parses Alpine's `C:` value. Format: "Q1<base64-sha1>="
// — the "Q1" prefix tags hash type (Q1=sha1). Returns the raw 20 sha1
// bytes. Mirrors the same parsing in internal/source/fetch.go so the
// two stay byte-identical for cache-key purposes.
func decodeChecksum(s string) ([]byte, error) {
	if !strings.HasPrefix(s, "Q1") {
		return nil, fmt.Errorf("apk_checksum: expected Q1 (sha1) prefix, got %q", s)
	}
	raw, err := base64.StdEncoding.DecodeString(s[2:])
	if err != nil {
		return nil, fmt.Errorf("apk_checksum: base64 decode: %w", err)
	}
	if len(raw) != sha1.Size {
		return nil, fmt.Errorf("apk_checksum: wanted %d sha1 bytes, got %d",
			sha1.Size, len(raw))
	}
	return raw, nil
}

