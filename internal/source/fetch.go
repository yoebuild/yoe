package source

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yoebuild/yoe/internal/apkindex"
	"github.com/yoebuild/yoe/internal/gitutil"
	"github.com/yoebuild/yoe/internal/gzipframe"
	"github.com/yoebuild/yoe/internal/httputil"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// apkControlSegment returns the raw bytes of the control segment (the
// second gzip stream) in an apk file. APKINDEX `C:` is sha1 of this
// byte range — NOT of the whole file, and NOT of the data segment.
//
// An apk is three gzip streams concatenated: signature, control, data.
func apkControlSegment(data []byte) ([]byte, error) {
	seg, err := gzipframe.Stream(data, 1)
	if err != nil {
		return nil, fmt.Errorf("apk parse: %w", err)
	}
	return seg, nil
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

	if IsGitURL(unit.Source) {
		return fetchGit(cacheDir, unit, w)
	}
	return fetchHTTP(cacheDir, unit, w)
}

// downloadRetries is how many times a transient download failure is retried
// before the build gives up. Source archives come from volunteer mirror pools
// (savannah, sourceforge, GNU) where a redirect can land on a mirror that is
// briefly unavailable; a single 502 from one mirror should not fail an
// otherwise healthy build, since the next attempt usually lands elsewhere.
const downloadRetries = 4

// retryDelay is the backoff before attempt n (1-based). Linear rather than
// exponential: mirror outages are usually resolved by re-rolling the redirect,
// not by waiting longer.
// It is a var so tests can shrink it rather than sleeping through the real
// backoff.
var retryDelay = func(attempt int) time.Duration {
	return time.Duration(attempt) * 2 * time.Second
}

// transientStatus reports whether an HTTP status is worth retrying. 5xx and
// the two rate-limit/timeout codes are mirror-side conditions that commonly
// clear on a retry; a 404 means the URL is wrong and retrying only wastes time.
func transientStatus(code int) bool {
	return code >= 500 || code == http.StatusRequestTimeout || code == http.StatusTooManyRequests
}

// downloadWithRetry streams url into a temp file inside cacheDir, retrying
// transient failures. Returns the temp file path and the sha256 of its
// contents; the caller owns the temp file and must rename or remove it.
func downloadWithRetry(cacheDir, url string, w io.Writer) (string, []byte, error) {
	var lastErr error
	for attempt := 1; attempt <= downloadRetries; attempt++ {
		if attempt > 1 {
			delay := retryDelay(attempt - 1)
			fmt.Fprintf(w, "  retrying %s in %s (attempt %d/%d): %v\n",
				url, delay, attempt, downloadRetries, lastErr)
			time.Sleep(delay)
		}

		tmpPath, sum, err := downloadOnce(cacheDir, url)
		if err == nil {
			return tmpPath, sum, nil
		}
		lastErr = err

		var se statusError
		if errors.As(err, &se) && !transientStatus(se.code) {
			return "", nil, fmt.Errorf("downloading %s: %w", url, err)
		}
	}
	return "", nil, fmt.Errorf("downloading %s: %w (after %d attempts)", url, lastErr, downloadRetries)
}

// statusError carries the HTTP status so the retry loop can tell a mirror
// hiccup from a genuinely wrong URL.
type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// downloadOnce performs a single GET and streams the body to a fresh temp
// file, returning its path and the sha256 of the bytes written. A sha256 is
// always computed — it is cheap, and provides a fingerprint regardless of
// which integrity mode the unit declares.
func downloadOnce(cacheDir, url string) (string, []byte, error) {
	resp, err := httputil.Client.Get(url)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, statusError{code: resp.StatusCode}
	}

	tmp, err := os.CreateTemp(cacheDir, "download-*")
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmp.Name()
	h256 := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h256), resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", nil, err
	}
	tmp.Close()

	return tmpPath, h256.Sum(nil), nil
}

// fetchFromAny downloads the unit's source, falling back to each mirror in
// turn when the primary URL cannot serve it. Each URL gets the full retry
// budget before the next one is tried, so a mirror that is merely slow to
// recover is still given a fair chance. A unit that declares mirrors should
// also declare sha256; verification happens in the caller and applies
// identically whichever host answered.
func fetchFromAny(cacheDir string, unit *yoestar.Unit, w io.Writer) (string, []byte, error) {
	urls := append([]string{unit.Source}, unit.Mirrors...)
	var firstErr error
	for i, url := range urls {
		if i > 0 {
			fmt.Fprintf(w, "  trying mirror %s\n", url)
		}
		tmpPath, sum, err := downloadWithRetry(cacheDir, url, w)
		if err == nil {
			return tmpPath, sum, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return "", nil, firstErr
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

	// Pre-validate apk_checksum format before paying the download cost.
	var apkExpected []byte
	if unit.APKChecksum != "" {
		raw, err := apkindex.DecodeChecksum(unit.APKChecksum)
		if err != nil {
			return "", fmt.Errorf("unit %q: %w", unit.Name, err)
		}
		apkExpected = raw
	}

	tmpPath, sum256, err := fetchFromAny(cacheDir, unit, w)
	if err != nil {
		return "", err
	}

	switch {
	case unit.SHA256 != "":
		actual := fmt.Sprintf("%x", sum256)
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

		cmd := gitutil.Command("", args...)
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
	if IsGitURL(unit.Source) {
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

// IsGitURL reports whether a unit's source URL is fetched as a git clone
// rather than downloaded as an archive. This is the single definition:
// anything deciding "is this unit git-backed" (the fetcher choosing a
// strategy, `yoe dev` deciding whether a unit can enter dev mode) must
// agree, or a unit fetched as git gets rejected as a non-git source.
//
// A bare github.com/... path counts: those are repo URLs unless they point
// at a generated archive or a release asset, which are plain downloads.
func IsGitURL(url string) bool {
	return strings.HasSuffix(url, ".git") ||
		strings.HasPrefix(url, "git://") ||
		strings.HasPrefix(url, "git@") ||
		strings.HasPrefix(url, "ssh://") ||
		(strings.Contains(url, "github.com/") && !strings.Contains(url, "/archive/") && !strings.Contains(url, "/releases/"))
}

func guessExt(url string) string {
	for _, ext := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".zip", ".apk", ".deb"} {
		if strings.HasSuffix(url, ext) {
			return ext
		}
	}
	return ".tar.gz"
}
