package source

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yoebuild/yoe/internal/apkindex"
	"github.com/yoebuild/yoe/internal/gzipframe"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// httpClient downloads source archives as opaque bytes. DisableCompression
// stops Go's default transport from advertising `Accept-Encoding: gzip` and
// then transparently inflating the response — savannah and other mirrors
// (e.g. nongnu.askapache.com) serve a `.tar.gz` with `Content-Encoding: gzip`,
// which the default client would decode, leaving a bare tar on disk under a
// `.tar.gz` name. extractTarball later picks gzip by extension and fails with
// "gzip: invalid header". Whether the bug bites depends on which mirror the
// 302 redirect lands on, so it is intermittent. Keeping the bytes raw makes
// the cached archive match its filename regardless of mirror.
var httpClient = &http.Client{
	Transport: &http.Transport{DisableCompression: true},
}

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

	resp, err := httpClient.Get(unit.Source)
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
		raw, err := apkindex.DecodeChecksum(unit.APKChecksum)
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
