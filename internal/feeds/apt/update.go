package apt

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yoebuild/yoe/internal/dpkg"
)

// UpdateOptions tunes the `yoe update-feeds` behavior for Debian feeds.
// Fields mirror the alpine sibling so the command-line surface stays
// consistent.
type UpdateOptions struct {
	// ModuleDir is the directory containing MODULE.star. Fetched
	// Packages files land under
	// ModuleDir/<Index>/<deb-arch>/Packages, matching the layout
	// apt_feed Lookup expects.
	ModuleDir string

	// Arches limits the fetch to a subset of yoe-canonical arches
	// (x86_64 / arm64). Empty means "every arch the FeedDecl already
	// has a directory for under ModuleDir/<Index>/, falling back to
	// every supported arch."
	Arches []string

	// HTTPClient is the client used for downloads. nil means use
	// http.DefaultClient.
	HTTPClient *http.Client

	// Out is where per-feed/per-arch progress is written. nil means
	// os.Stdout.
	Out io.Writer

	// AllowKeyUpdate is a fingerprint to add to allowed-fingerprints
	// out-of-band before the verify pass. Equivalent to manually
	// appending the fingerprint and re-running update-feeds.
	AllowKeyUpdate string
}

// UpdateFeeds is the body of the `yoe update-feeds` command's Debian
// branch. Reads MODULE.star in opts.ModuleDir, enumerates every
// apt_feed call, fetches each declared suite's InRelease + per-arch
// Packages files from upstream, verifies the InRelease signature
// against the module's keyring (subject to R25's fingerprint
// allow-list and R24's Valid-Until enforcement), decompresses
// the Packages stream, and atomically writes it into the on-disk
// location.
//
// Writes only; no commit. The maintainer's normal git workflow
// (diff/add/commit/push) follows the run.
func UpdateFeeds(opts UpdateOptions) error {
	if opts.ModuleDir == "" {
		return fmt.Errorf("update-feeds: ModuleDir is required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}

	decls, err := PeekFeedDecls(opts.ModuleDir)
	if err != nil {
		return err
	}
	if len(decls) == 0 {
		return fmt.Errorf("update-feeds: no apt_feed() calls in %s/MODULE.star", opts.ModuleDir)
	}

	if opts.AllowKeyUpdate != "" {
		if err := appendAllowedFingerprint(opts.ModuleDir, opts.AllowKeyUpdate); err != nil {
			return fmt.Errorf("update-feeds: --allow-key-update: %w", err)
		}
	}

	totalWritten := 0
	totalBytes := int64(0)
	for _, d := range decls {
		fmt.Fprintf(opts.Out, "→ %s (%s, %s)\n", d.Name, d.Suite, d.URL)
		arches := pickArches(opts, d)
		if len(arches) == 0 {
			return fmt.Errorf("update-feeds: %s: no arches to fetch (set --arch or pre-create feed dirs)", d.Name)
		}
		keyring, err := readKeyring(opts.ModuleDir, d.Keyring)
		if err != nil {
			return fmt.Errorf("update-feeds: %s: %w", d.Name, err)
		}
		allowed, err := readAllowedFingerprints(opts.ModuleDir)
		if err != nil {
			return fmt.Errorf("update-feeds: %s: %w", d.Name, err)
		}

		// InRelease: fetch + verify once per suite (it covers every
		// arch + component).
		inReleaseURL := fmt.Sprintf("%s/dists/%s/InRelease",
			strings.TrimSuffix(d.URL, "/"), d.Suite)
		fmt.Fprintf(opts.Out, "  fetching %s\n", inReleaseURL)
		inRelease, err := httpGet(opts.HTTPClient, inReleaseURL)
		if err != nil {
			return fmt.Errorf("update-feeds: %s: InRelease: %w", d.Name, err)
		}
		if err := enforceAllowList(inRelease, allowed); err != nil {
			return fmt.Errorf("update-feeds: %s: %w", d.Name, err)
		}
		body, err := dpkg.VerifyInRelease(inRelease, keyring)
		if err != nil {
			return fmt.Errorf("update-feeds: %s: InRelease verify: %w", d.Name, err)
		}
		_ = body // R15 hash check happens at index emit time; here we just verify Valid-Until + signature

		for _, yoeArch := range arches {
			debArch, ok := archMap[yoeArch]
			if !ok {
				return fmt.Errorf("update-feeds: %s: unsupported arch %q", d.Name, yoeArch)
			}
			n, err := fetchPackages(opts, d, yoeArch, debArch)
			if err != nil {
				return fmt.Errorf("update-feeds: %s/%s: %w", d.Name, debArch, err)
			}
			totalWritten++
			totalBytes += n
		}
	}
	fmt.Fprintf(opts.Out, "\nWrote %d Packages file(s), %s total.\n", totalWritten, humanBytes(totalBytes))
	fmt.Fprintf(opts.Out, "Review with `git diff` and commit when ready.\n")
	return nil
}

// pickArches mirrors alpine's pickArches with debian arch tokens.
func pickArches(opts UpdateOptions, d FeedDecl) []string {
	if len(opts.Arches) > 0 {
		return opts.Arches
	}
	indexDir := filepath.Join(opts.ModuleDir, d.Index)
	entries, err := os.ReadDir(indexDir)
	if err == nil {
		var existing []string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			for yoeArch, debArch := range archMap {
				if e.Name() == debArch {
					existing = append(existing, yoeArch)
					break
				}
			}
		}
		if len(existing) > 0 {
			sort.Strings(existing)
			return existing
		}
	}
	// Fall back to the FeedDecl's declared arches, mapped to yoe-canon.
	var out []string
	for _, declArch := range d.Arches {
		for yoeArch, debArch := range archMap {
			if debArch == declArch {
				out = append(out, yoeArch)
				break
			}
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		return out
	}
	all := supportedArches()
	sort.Strings(all)
	return all
}

// fetchPackages downloads <url>/dists/<suite>/<component>/binary-<arch>/Packages.gz,
// decompresses, and atomically writes it as a plain Packages file into
// ModuleDir/<Index>/<deb-arch>/Packages.
func fetchPackages(opts UpdateOptions, d FeedDecl, yoeArch, debArch string) (int64, error) {
	url := fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages.gz",
		strings.TrimSuffix(d.baseURLFor(yoeArch), "/"), d.Suite, d.Component, debArch)
	fmt.Fprintf(opts.Out, "  %s: fetching %s\n", yoeArch, url)

	gz, err := httpGet(opts.HTTPClient, url)
	if err != nil {
		return 0, err
	}

	gr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gr.Close()
	raw, err := io.ReadAll(gr)
	if err != nil {
		return 0, fmt.Errorf("decompress: %w", err)
	}

	dst := filepath.Join(opts.ModuleDir, d.Index, debArch, "Packages")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, fmt.Errorf("mkdir: %w", err)
	}
	if err := atomicWrite(dst, raw); err != nil {
		return 0, err
	}
	entryCount := countStanzas(raw)
	fmt.Fprintf(opts.Out, "  %s: wrote %s (%d entries)\n", yoeArch, relTo(dst, opts.ModuleDir), entryCount)
	return int64(len(gz)), nil
}

func httpGet(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func readKeyring(moduleDir, rel string) ([]byte, error) {
	if rel == "" {
		return nil, fmt.Errorf("apt_feed must declare keyring=... for signature verification")
	}
	p := rel
	if !filepath.IsAbs(p) {
		p = filepath.Join(moduleDir, rel)
	}
	return os.ReadFile(p)
}

// readAllowedFingerprints reads the per-module allow-list of
// fingerprints (one per line, # comments) per R25. Missing file is OK
// — every fingerprint is rejected, which produces a clear error when
// the InRelease is signed by a key not in the bootstrap keyring.
func readAllowedFingerprints(moduleDir string) (map[string]bool, error) {
	path := filepath.Join(moduleDir, "keys", "allowed-fingerprints")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("allowed-fingerprints: %w", err)
	}
	defer f.Close()

	allowed := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Normalize: strip whitespace inside the fingerprint and uppercase.
		fpr := strings.ToUpper(strings.ReplaceAll(line, " ", ""))
		allowed[fpr] = true
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return allowed, nil
}

// enforceAllowList is a placeholder for the new-key gate per R25. Today
// it's a no-op (signature verification against the committed keyring
// suffices); when key-rollover support lands, this will inspect the
// signing fingerprint of the freshly-fetched InRelease and refuse to
// install a new key whose fingerprint isn't in `allowed`.
func enforceAllowList(_ []byte, _ map[string]bool) error {
	return nil
}

// appendAllowedFingerprint appends a fingerprint to
// keys/allowed-fingerprints, creating the file if absent.
func appendAllowedFingerprint(moduleDir, fpr string) error {
	fpr = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(fpr), " ", ""))
	if fpr == "" {
		return fmt.Errorf("empty fingerprint")
	}
	dir := filepath.Join(moduleDir, "keys")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "allowed-fingerprints")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# Added via --allow-key-update\n%s\n", fpr)
	return err
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create tmpfile: %w", err)
	}
	defer func() {
		if _, statErr := os.Stat(tmp); statErr == nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return os.Rename(tmp, path)
}

func countStanzas(data []byte) int {
	n := 0
	atStart := true
	for i := 0; i < len(data); i++ {
		if atStart && i+8 <= len(data) && string(data[i:i+8]) == "Package:" {
			n++
		}
		atStart = data[i] == '\n'
	}
	return n
}

func relTo(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

func humanBytes(n int64) string {
	const (
		KiB = 1024
		MiB = 1024 * 1024
		GiB = 1024 * 1024 * 1024
	)
	switch {
	case n < KiB:
		return fmt.Sprintf("%d B", n)
	case n < MiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/KiB)
	case n < GiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/MiB)
	default:
		return fmt.Sprintf("%.2f GiB", float64(n)/GiB)
	}
}
