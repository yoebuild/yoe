package alpine

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/yoebuild/yoe/internal/apkindex"
)

// UpdateOptions tunes the `yoe update-feeds` behavior. The defaults
// match what most maintainers want; explicit fields exist so tests
// can substitute fakes (HTTP client, output writer) without dragging
// in a separate testing framework.
type UpdateOptions struct {
	// ModuleDir is the directory containing MODULE.star. The fetched
	// APKINDEX files are written under
	// ModuleDir/<Index>/<alpine-arch>/APKINDEX, matching the layout
	// alpine_feed's Lookup expects.
	ModuleDir string

	// Arches limits the fetch to a subset of yoe-canonical arches
	// (x86_64, arm64, riscv64). Empty means "every arch with an
	// existing directory in ModuleDir/<Index>/ — if none, fall back
	// to every supported arch."
	Arches []string

	// HTTPClient is the client used for downloads. nil means use
	// http.DefaultClient.
	HTTPClient *http.Client

	// Out is where per-feed/per-arch progress is written. nil means
	// os.Stdout.
	Out io.Writer
}

// UpdateFeeds is the body of the `yoe update-feeds` command. Reads
// MODULE.star in opts.ModuleDir, enumerates every alpine_feed call,
// fetches each declared feed's APKINDEX.tar.gz from upstream,
// verifies its signature against the feed's `keys=[...]`, decompresses
// the index, and atomically writes it to the on-disk location.
//
// Writes only; no commit. The maintainer's normal git workflow
// (diff/add/commit/push) follows the run.
//
// Per-apk SHA256SUMS sidecar generation (the "full-file SHA256 over
// every package for fetch-time integrity" defense-in-depth from the
// plan's Key Technical Decisions) is intentionally deferred: it
// requires downloading every package (~hundreds of MiB per arch per
// section) and the resolver's primary integrity check already runs
// against APKINDEX's `C:` value, which the signed index itself
// guarantees. Add a flag to opt in when bandwidth is cheap.
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
		return fmt.Errorf("update-feeds: no alpine_feed() calls in %s/MODULE.star", opts.ModuleDir)
	}

	totalWritten := 0
	totalBytes := int64(0)
	for _, d := range decls {
		fmt.Fprintf(opts.Out, "→ %s (%s, %s)\n", d.Name, d.Branch, d.URL)
		arches := pickArches(opts, d)
		if len(arches) == 0 {
			return fmt.Errorf("update-feeds: %s: no arches to fetch (set --arch or pre-create feed dirs)", d.Name)
		}
		trustedKeys, err := resolveKeyPaths(opts.ModuleDir, d.Keys)
		if err != nil {
			return fmt.Errorf("update-feeds: %s: %w", d.Name, err)
		}
		if len(trustedKeys) == 0 {
			return fmt.Errorf("update-feeds: %s: alpine_feed must declare keys=[...] for signature verification", d.Name)
		}
		for _, yoeArch := range arches {
			alpineArch, ok := archMap[yoeArch]
			if !ok {
				return fmt.Errorf("update-feeds: %s: unsupported arch %q", d.Name, yoeArch)
			}
			n, err := fetchOne(opts, d, yoeArch, alpineArch, trustedKeys)
			if err != nil {
				return fmt.Errorf("update-feeds: %s/%s: %w", d.Name, alpineArch, err)
			}
			totalWritten++
			totalBytes += n
		}
	}
	fmt.Fprintf(opts.Out, "\nWrote %d APKINDEX file(s), %s total.\n",
		totalWritten, humanBytes(totalBytes))
	fmt.Fprintf(opts.Out, "Review with `git diff` and commit when ready.\n")
	return nil
}

// pickArches picks the arches to fetch for one feed. Priority order:
//
//  1. opts.Arches (explicit caller request, typically --arch flag)
//  2. The arches that already have a directory under
//     ModuleDir/<Index>/ — preserves whatever set the maintainer
//     committed to
//  3. Every supported arch
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
			for yoeArch, alpineArch := range archMap {
				if e.Name() == alpineArch {
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
	all := supportedArches()
	sort.Strings(all)
	return all
}

// resolveKeyPaths turns the relative `keys=[...]` paths declared in
// alpine_feed into absolute filesystem paths anchored at the module
// directory. Missing files are an error here — the caller must have
// committed the keys before running update-feeds, or the signature
// verification can't even start.
func resolveKeyPaths(moduleDir string, relPaths []string) ([]string, error) {
	out := make([]string, 0, len(relPaths))
	for _, rel := range relPaths {
		p := rel
		if !filepath.IsAbs(p) {
			p = filepath.Join(moduleDir, rel)
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("key file %s: %w", rel, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// fetchOne downloads <url>/<branch>/<section>/<alpineArch>/APKINDEX.tar.gz,
// verifies its signature, and atomically writes the decompressed
// APKINDEX into ModuleDir/<Index>/<alpineArch>/APKINDEX. Returns the
// number of bytes downloaded (for the maintainer's progress summary).
//
// Atomic write order: tmpfile → fsync → rename — a SIGINT
// mid-write never strands a partial file at the canonical path.
func fetchOne(opts UpdateOptions, d FeedDecl, yoeArch, alpineArch string, trustedKeys []string) (int64, error) {
	url := fmt.Sprintf("%s/%s/%s/%s/APKINDEX.tar.gz", d.URL, d.Branch, d.Section, alpineArch)
	fmt.Fprintf(opts.Out, "  %s: fetching %s\n", yoeArch, url)

	resp, err := opts.HTTPClient.Get(url)
	if err != nil {
		return 0, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tarball, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read body: %w", err)
	}

	if err := apkindex.VerifySignatureBytes(tarball, trustedKeys); err != nil {
		return 0, fmt.Errorf("signature: %w", err)
	}

	indexBytes, err := extractInnerAPKINDEX(tarball)
	if err != nil {
		return 0, fmt.Errorf("extract APKINDEX: %w", err)
	}

	dst := filepath.Join(opts.ModuleDir, d.Index, alpineArch, "APKINDEX")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, fmt.Errorf("mkdir: %w", err)
	}
	if err := atomicWrite(dst, indexBytes); err != nil {
		return 0, err
	}

	// Lightweight summary — count entries the maintainer can spot-check.
	entryCount := countEntries(indexBytes)
	fmt.Fprintf(opts.Out, "  %s: wrote %s (%d entries, signed by %s)\n",
		yoeArch, relTo(dst, opts.ModuleDir), entryCount, sigKeyName(trustedKeys[0]))
	return int64(len(tarball)), nil
}

// extractInnerAPKINDEX walks the gzip streams of an APKINDEX.tar.gz
// past the .SIGN.RSA.* signature stream and returns the raw bytes of
// the inner APKINDEX file. Used so update-feeds writes the
// human-readable index instead of the wrapped tarball — yoe's
// resolver reads APKINDEX (plain text) at load time per U2/U5.
func extractInnerAPKINDEX(tarball []byte) ([]byte, error) {
	bounds, err := gzipStreamBoundaries(tarball)
	if err != nil {
		return nil, err
	}
	for _, b := range bounds {
		stream := tarball[b[0]:b[1]]
		index, err := extractAPKINDEXFromStream(stream)
		if err != nil {
			return nil, err
		}
		if index != nil {
			return index, nil
		}
	}
	return nil, fmt.Errorf("no APKINDEX entry in tarball")
}

// atomicWrite writes data to path via tmpfile + fsync + rename so a
// SIGINT mid-write never leaves a partial file at the canonical
// location.
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
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// countEntries counts blank-line-separated blocks in APKINDEX text.
// Cheap pre-validation that the file looks structurally like an
// index — full parse + provides build happen at load time.
func countEntries(index []byte) int {
	n := 0
	atStart := true
	for i := 0; i < len(index); i++ {
		if atStart && index[i] == 'P' && i+1 < len(index) && index[i+1] == ':' {
			n++
		}
		atStart = index[i] == '\n'
	}
	return n
}

// relTo prints a friendly relative path for progress output, with a
// safe fallback to the absolute path when relativization fails (e.g.,
// different mount points).
func relTo(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

// sigKeyName extracts the key basename for diagnostics output.
func sigKeyName(keyPath string) string { return filepath.Base(keyPath) }

// humanBytes returns "N B", "N.N KiB", "N.N MiB" — base-2 because
// that's what apk-tools and du -h show. Used in the update-feeds
// summary line.
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

// _ = sha256.Size keeps the import alive for future SHA256SUMS work.
var _ = sha256.Size
