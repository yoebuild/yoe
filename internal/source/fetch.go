package source

import (
	"bytes"
	"compress/flate"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// decodeAPKChecksum parses Alpine's APKINDEX `C:` value and returns the
// raw expected sha1 bytes. Format: "Q1<base64-encoded-sha1>=" — the "Q1"
// prefix is a hash-type tag (Q1 = sha1; Q2 = sha256 was reserved but
// never deployed at scale). Returns an error for any other prefix or
// malformed input.
func decodeAPKChecksum(s string) ([]byte, error) {
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

// apkControlSegment returns the raw bytes of the control segment (the
// second gzip stream) in an apk file. APKINDEX `C:` is sha1 of this
// byte range — NOT of the whole file, and NOT of the data segment.
//
// An apk is three gzip streams concatenated: signature, control, data.
// compress/gzip won't tell us precisely where one stream ends in the
// underlying byte slice, so we parse gzip framing by hand and use
// compress/flate to consume each deflate body until its end-of-block
// marker. bytes.Reader implements io.ByteReader, so flate.NewReader
// uses it directly with no buffering — we recover the exact byte
// boundary from br.Len() after each stream.
func apkControlSegment(data []byte) ([]byte, error) {
	bounds, err := gzipStreamBoundaries(data)
	if err != nil {
		return nil, fmt.Errorf("apk parse: %w", err)
	}
	if len(bounds) < 2 {
		return nil, fmt.Errorf("apk has %d gzip stream(s), expected >=2",
			len(bounds))
	}
	s2 := bounds[1]
	return data[s2[0]:s2[1]], nil
}

type gzipBound [2]int

func gzipStreamBoundaries(data []byte) ([]gzipBound, error) {
	var out []gzipBound
	pos := 0
	for pos < len(data) {
		if pos+10 > len(data) || data[pos] != 0x1f || data[pos+1] != 0x8b {
			break
		}
		start := pos
		flg := data[pos+3]
		hdrEnd := pos + 10
		if flg&0x04 != 0 { // FEXTRA
			if hdrEnd+2 > len(data) {
				return nil, fmt.Errorf("truncated FEXTRA")
			}
			xlen := int(binary.LittleEndian.Uint16(data[hdrEnd : hdrEnd+2]))
			hdrEnd += 2 + xlen
		}
		if flg&0x08 != 0 { // FNAME — null-terminated
			for hdrEnd < len(data) && data[hdrEnd] != 0 {
				hdrEnd++
			}
			hdrEnd++
		}
		if flg&0x10 != 0 { // FCOMMENT — null-terminated
			for hdrEnd < len(data) && data[hdrEnd] != 0 {
				hdrEnd++
			}
			hdrEnd++
		}
		if flg&0x02 != 0 { // FHCRC
			hdrEnd += 2
		}
		if hdrEnd > len(data) {
			return nil, fmt.Errorf("truncated gzip header")
		}
		br := bytes.NewReader(data[hdrEnd:])
		zr := flate.NewReader(br)
		if _, err := io.Copy(io.Discard, zr); err != nil {
			zr.Close()
			return nil, fmt.Errorf("deflate stream %d: %w", len(out), err)
		}
		if err := zr.Close(); err != nil {
			return nil, fmt.Errorf("deflate close stream %d: %w", len(out), err)
		}
		// Bytes consumed from data[hdrEnd:] = original-len minus what's left.
		deflateConsumed := (len(data) - hdrEnd) - br.Len()
		end := hdrEnd + deflateConsumed + 8 // +8 for CRC32 + ISIZE trailer
		if end > len(data) {
			return nil, fmt.Errorf("truncated gzip trailer")
		}
		out = append(out, gzipBound{start, end})
		pos = end
	}
	return out, nil
}

// CacheDir returns the source cache directory, creating it if needed.
// Defaults to cache/sources/ in the current working directory.
func CacheDir() (string, error) {
	dir := os.Getenv("YOE_CACHE")
	if dir == "" {
		dir = "cache"
	}
	dir = filepath.Join(dir, "sources")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// Fetch downloads the source for a unit into the cache.
// Returns the path to the cached source (tarball or bare git repo).
func Fetch(unit *yoestar.Unit, w io.Writer) (string, error) {
	cacheDir, err := CacheDir()
	if err != nil {
		return "", err
	}

	if unit.Source == "" {
		return "", fmt.Errorf("unit %q has no source", unit.Name)
	}

	if isGitURL(unit.Source) {
		return fetchGit(cacheDir, unit, w)
	}
	return fetchHTTP(cacheDir, unit, w)
}

// fetchHTTP downloads a tarball and caches it by URL hash.
func fetchHTTP(cacheDir string, unit *yoestar.Unit, w io.Writer) (string, error) {
	// Cache key: sha256 of URL
	urlHash := fmt.Sprintf("%x", sha256.Sum256([]byte(unit.Source)))
	ext := guessExt(unit.Source)
	cachedPath := filepath.Join(cacheDir, urlHash+ext)

	// Already cached?
	if _, err := os.Stat(cachedPath); err == nil {
		return cachedPath, nil
	}

	fmt.Fprintf(w, "Fetching %s...\n", unit.Source)

	resp, err := http.Get(unit.Source)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", unit.Source, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", unit.Source, resp.StatusCode)
	}

	// Pre-validate apk_checksum format before paying the download cost.
	var apkExpected []byte
	if unit.APKChecksum != "" {
		raw, err := decodeAPKChecksum(unit.APKChecksum)
		if err != nil {
			return "", fmt.Errorf("unit %q: %w", unit.Name, err)
		}
		apkExpected = raw
	}

	// Always stream a sha256 during download — cheap, and provides a
	// fingerprint regardless of which integrity mode applies. We only
	// *check* it when SHA256 is the declared format.
	tmp, err := os.CreateTemp(cacheDir, "download-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	h256 := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h256), resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("downloading %s: %w", unit.Source, err)
	}
	tmp.Close()

	switch {
	case unit.SHA256 != "":
		actual := fmt.Sprintf("%x", h256.Sum(nil))
		if actual != unit.SHA256 {
			os.Remove(tmpPath)
			return "", fmt.Errorf("SHA256 mismatch:\n  expected %s\n  got      %s",
				unit.SHA256, actual)
		}
	case unit.APKChecksum != "":
		// APKINDEX `C:` is sha1 of the apk's control segment (second
		// gzip stream), so we can only verify after the file is on
		// disk. Worth the post-download parse: it's the same trust
		// chain apk-tools itself uses.
		raw, err := os.ReadFile(tmpPath)
		if err != nil {
			os.Remove(tmpPath)
			return "", fmt.Errorf("reading %s for apk_checksum verify: %w",
				tmpPath, err)
		}
		ctrl, err := apkControlSegment(raw)
		if err != nil {
			os.Remove(tmpPath)
			return "", fmt.Errorf("apk_checksum verify: %w", err)
		}
		actualRaw := sha1.Sum(ctrl)
		if !bytes.Equal(actualRaw[:], apkExpected) {
			os.Remove(tmpPath)
			return "", fmt.Errorf("apk_checksum mismatch:\n  expected Q1%s\n  got      Q1%s",
				base64.StdEncoding.EncodeToString(apkExpected),
				base64.StdEncoding.EncodeToString(actualRaw[:]))
		}
	}

	if err := os.Rename(tmpPath, cachedPath); err != nil {
		os.Remove(tmpPath)
		return "", err
	}

	return cachedPath, nil
}

// fetchGit clones or updates a bare git repo in the cache.
// Uses shallow clone by default (only the pinned tag/branch) to avoid
// downloading full history. For the Linux kernel this is ~4GB vs ~200MB.
func fetchGit(cacheDir string, unit *yoestar.Unit, w io.Writer) (string, error) {
	// Cache key: sha256 of repo URL + ref (different tags get different clones)
	ref := unit.Tag
	if ref == "" {
		ref = unit.Branch
	}
	if ref == "" {
		ref = "HEAD"
	}
	cacheKey := unit.Source + "#" + ref
	urlHash := fmt.Sprintf("%x", sha256.Sum256([]byte(cacheKey)))
	barePath := filepath.Join(cacheDir, urlHash+".git")

	if _, err := os.Stat(barePath); os.IsNotExist(err) {
		fmt.Fprintf(w, "Cloning %s (ref: %s)...\n", unit.Source, ref)

		// Shallow clone of just the ref we need
		args := []string{"clone", "--bare", "--depth", "1"}
		if unit.Tag != "" {
			args = append(args, "--branch", unit.Tag)
		} else if unit.Branch != "" {
			args = append(args, "--branch", unit.Branch)
		}
		args = append(args, unit.Source, barePath)

		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git clone %s: %s\n%s", unit.Source, err, out)
		}
	} else {
		// Repo already cached — fetch the specific ref if needed
		fmt.Fprintf(w, "Using cached %s (ref: %s)\n", unit.Source, ref)
	}

	return barePath, nil
}

// Verify checks the SHA256 of a cached source file.
func Verify(unit *yoestar.Unit) error {
	if unit.SHA256 == "" {
		return nil // no hash to verify
	}
	if isGitURL(unit.Source) {
		return nil // git sources verified by commit hash
	}

	cacheDir, err := CacheDir()
	if err != nil {
		return err
	}

	urlHash := fmt.Sprintf("%x", sha256.Sum256([]byte(unit.Source)))
	ext := guessExt(unit.Source)
	cachedPath := filepath.Join(cacheDir, urlHash+ext)

	f, err := os.Open(cachedPath)
	if err != nil {
		return fmt.Errorf("source not cached: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actual := fmt.Sprintf("%x", h.Sum(nil))
	if actual != unit.SHA256 {
		return fmt.Errorf("SHA256 mismatch for %s:\n  expected %s\n  got      %s",
			unit.Name, unit.SHA256, actual)
	}

	return nil
}

func isGitURL(url string) bool {
	return strings.HasSuffix(url, ".git") ||
		strings.HasPrefix(url, "git://") ||
		strings.HasPrefix(url, "git@") ||
		(strings.Contains(url, "github.com/") && !strings.Contains(url, "/archive/") && !strings.Contains(url, "/releases/"))
}

func guessExt(url string) string {
	for _, ext := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".zip", ".apk"} {
		if strings.HasSuffix(url, ext) {
			return ext
		}
	}
	return ".tar.gz"
}
