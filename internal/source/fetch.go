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
	"slices"
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
//
// mirrors is the project's source-mirror table, expanding the set of hosts
// an archive may be fetched from beyond the unit's own `mirrors` list.
func Fetch(unit *yoestar.Unit, mirrors []yoestar.MirrorRule, w io.Writer) (string, error) {
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
	return fetchHTTP(cacheDir, unit, mirrors, w)
}

// sourceURLs is the ordered list of places a unit's archive may be fetched
// from: the declared source, then the unit's own mirrors, then whatever the
// project's mirror table rewrites the source into. Unit-level mirrors come
// first because the unit author picked them for that specific archive,
// while the table is a blanket rule about a host.
func sourceURLs(unit *yoestar.Unit, rules []yoestar.MirrorRule) []string {
	urls := append([]string{unit.Source}, unit.Mirrors...)
	for _, r := range rules {
		rewritten, ok := r.Apply(unit.Source)
		if !ok || slices.Contains(urls, rewritten) {
			continue
		}
		urls = append(urls, rewritten)
	}
	return urls
}

// fetchRetries is how many times a transient fetch failure is retried before
// the build gives up. It governs both transports. Source archives come from
// volunteer mirror pools (savannah, sourceforge, GNU) where a redirect can
// land on a mirror that is briefly unavailable; a single 502 from one mirror
// should not fail an otherwise healthy build, since the next attempt usually
// lands elsewhere. Git hosts fail the same way — a 503 from a forge that is
// restarting, or a clone that stalls partway — and a git source has no mirror
// to fall back to, so the retry is the only thing standing between a brief
// outage and a failed build.
const fetchRetries = 4

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
	for attempt := 1; attempt <= fetchRetries; attempt++ {
		if attempt > 1 {
			delay := retryDelay(attempt - 1)
			fmt.Fprintf(w, "  retrying %s in %s (attempt %d/%d): %v\n",
				url, delay, attempt, fetchRetries, lastErr)
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
	return "", nil, fmt.Errorf("downloading %s: %w (after %d attempts)", url, lastErr, fetchRetries)
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
func fetchFromAny(cacheDir string, unit *yoestar.Unit, rules []yoestar.MirrorRule, w io.Writer) (string, []byte, error) {
	urls := sourceURLs(unit, rules)
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
func fetchHTTP(cacheDir string, unit *yoestar.Unit, mirrors []yoestar.MirrorRule, w io.Writer) (string, error) {
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

	tmpPath, sum256, err := fetchFromAny(cacheDir, unit, mirrors, w)
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

// fetchGit clones a bare git repo into the cache.
// Uses shallow clone by default (only the pinned tag/branch) to avoid
// downloading full history. For the Linux kernel this is ~4GB vs ~200MB.
//
// Units sharing a (source, ref) share one cache entry and build
// concurrently, so the whole fetch runs under a lock and the clone lands
// via a temp directory. Completeness is decided by asking git whether the
// ref is present, not by the entry's existence: `git clone` creates the
// destination before it has fetched anything into it.
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

	lock, err := acquireCacheLock(barePath+".lock", w,
		fmt.Sprintf("Waiting for another unit to clone %s (ref: %s)...", unit.Source, ref))
	if err != nil {
		return "", err
	}
	defer lock.release()

	if gitHasRef(barePath, ref) {
		fmt.Fprintf(w, "Using cached %s (ref: %s)\n", unit.Source, ref)
		return barePath, nil
	}

	// Either nothing is cached or a previous run left a clone behind that
	// never got the ref. Both are handled by cloning afresh; say so when
	// an entry is being discarded, since that is otherwise invisible.
	if _, err := os.Stat(barePath); err == nil {
		fmt.Fprintf(w, "Discarding incomplete clone of %s (ref: %s)\n", unit.Source, ref)
		if err := os.RemoveAll(barePath); err != nil {
			return "", fmt.Errorf("clearing incomplete clone %s: %w", barePath, err)
		}
	}

	fmt.Fprintf(w, "Cloning %s (ref: %s)...\n", unit.Source, ref)

	// Temp dirs for this key are guarded by the lock we hold, so any that
	// survive are debris from a run that was killed mid-clone.
	if stale, err := filepath.Glob(filepath.Join(cacheDir, urlHash+".tmp-*")); err == nil {
		for _, d := range stale {
			os.RemoveAll(d)
		}
	}

	tmpPath, err := cloneWithRetry(cacheDir, urlHash, ref, unit, w)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpPath)

	// MkdirTemp is 0700; the cache is shared with builds running as other
	// users, so restore the 0755 a plain clone would have produced.
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return "", err
	}

	if err := os.Rename(tmpPath, barePath); err != nil {
		return "", fmt.Errorf("publishing clone to %s: %w", barePath, err)
	}

	return barePath, nil
}

// cloneWithRetry clones the unit's ref into a fresh temp directory under
// cacheDir, retrying transient failures on the same backoff the HTTP path
// uses. Returns the temp directory, which the caller owns and must rename
// or remove.
//
// A git source has no mirror to fall back to — the mirror tables apply only
// to HTTP archive fetches — so retrying the one host is the whole of the
// resilience available here. Each attempt clones into its own temp
// directory: a failed clone leaves a partially populated tree behind, and
// git refuses to clone into a directory that is not empty.
func cloneWithRetry(cacheDir, urlHash, ref string, unit *yoestar.Unit, w io.Writer) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= fetchRetries; attempt++ {
		if attempt > 1 {
			delay := retryDelay(attempt - 1)
			fmt.Fprintf(w, "  retrying clone of %s in %s (attempt %d/%d): %v\n",
				unit.Source, delay, attempt, fetchRetries, lastErr)
			time.Sleep(delay)
		}

		tmpPath, err := cloneOnce(cacheDir, urlHash, ref, unit)
		if err == nil {
			return tmpPath, nil
		}
		lastErr = err

		// A wrong URL or a ref that does not exist upstream fails the same
		// way however many times it is tried, and the fix is to edit the
		// unit. Report it now rather than after the full backoff.
		var pe permanentCloneError
		if errors.As(err, &pe) {
			return "", err
		}
	}
	return "", fmt.Errorf("%w (after %d attempts)", lastErr, fetchRetries)
}

// cloneOnce performs a single shallow clone into a fresh temp directory,
// removing it if the clone does not produce a usable repository.
func cloneOnce(cacheDir, urlHash, ref string, unit *yoestar.Unit) (string, error) {
	tmpPath, err := os.MkdirTemp(cacheDir, urlHash+".tmp-")
	if err != nil {
		return "", err
	}

	// Shallow clone of just the ref we need
	args := []string{"clone", "--bare", "--depth", "1"}
	if unit.Tag != "" {
		args = append(args, "--branch", unit.Tag)
	} else if unit.Branch != "" {
		args = append(args, "--branch", unit.Branch)
	}
	args = append(args, unit.Source, tmpPath)

	if out, err := gitutil.Command("", args...).CombinedOutput(); err != nil {
		os.RemoveAll(tmpPath)
		cloneErr := fmt.Errorf("git clone %s: %s\n%s", unit.Source, err, out)
		if isPermanentCloneFailure(string(out)) {
			return "", permanentCloneError{cloneErr}
		}
		return "", cloneErr
	}

	// The clone succeeded but does not carry what the unit asked for. The
	// remote answered, so the host is healthy and another attempt would
	// land in exactly the same place.
	if !gitHasRef(tmpPath, ref) {
		os.RemoveAll(tmpPath)
		return "", permanentCloneError{
			fmt.Errorf("git clone %s: ref %q missing from the clone", unit.Source, ref),
		}
	}

	return tmpPath, nil
}

// permanentCloneError marks a clone failure that retrying cannot clear.
type permanentCloneError struct{ err error }

func (e permanentCloneError) Error() string { return e.err.Error() }
func (e permanentCloneError) Unwrap() error { return e.err }

// permanentCloneMessages are the parts of git's stderr that identify a
// failure the remote will keep reporting: the repository or ref does not
// exist, or the credentials are not accepted. Everything else — a 5xx from
// a forge, a reset connection, a transfer that stalls under the low-speed
// guard — is treated as transient, since those are the failures a retry
// exists to absorb. The list is deliberately narrow: retrying a permanent
// failure only delays a clear error, while treating a transient one as
// permanent brings back the fragility the retry was added to remove.
// permanentCloneMessages. Each entry is a set of substrings that must all
// appear for the entry to match, which is what separates the fatal line from
// the progress git prints above it: every clone announces "Cloning into bare
// repository", so "repository" alone would match a healthy run.
var permanentCloneMessages = [][]string{
	{"fatal: repository", "not found"},         // the URL names no repository
	{"not found in upstream origin"},           // the tag or branch does not exist
	{"could not read username"},                // credential prompt, so private or absent
	{"authentication failed"},                  // the credentials are rejected
	{"does not appear to be a git repository"}, // the URL points at something else
	{"permission denied"},                      // the remote refuses this caller
}

// isPermanentCloneFailure reports whether git's output names a condition
// that another attempt cannot change.
func isPermanentCloneFailure(out string) bool {
	low := strings.ToLower(out)
	for _, msg := range permanentCloneMessages {
		matched := true
		for _, part := range msg {
			if !strings.Contains(low, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// gitHasRef reports whether dir is a git repo holding ref. This is what
// makes a cache entry complete: a clone that exists but does not yet carry
// the ref is a clone still in flight or one interrupted partway.
func gitHasRef(dir, ref string) bool {
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	_, err := gitutil.Run(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
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

// guessExt returns the archive extension the URL carries, so a cached
// download keeps a filename the extractor can dispatch on. A URL with no
// recognised extension gets no extension: the file is cached under its URL
// hash alone and prepareNonGitSource identifies it by its magic bytes.
//
// Returning a fabricated ".tar.gz" here instead would hide the real format
// behind a wrong name — a bare executable published as a release asset
// (the binary class's case) would be handed to the gzip reader and fail
// with "invalid header" before the magic-byte fallback ever ran.
func guessExt(url string) string {
	for _, ext := range []string{
		".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".tbz2", ".tar",
		".zip", ".apk", ".deb",
	} {
		if strings.HasSuffix(url, ext) {
			return ext
		}
	}
	return ""
}
