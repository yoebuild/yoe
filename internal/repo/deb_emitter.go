package repo

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yoebuild/yoe/internal/deb"
	"github.com/yoebuild/yoe/internal/dpkg"
)

// debPublishMu serializes PublishDeb across goroutines. Parallel unit
// builds each finish by copying their .deb into the shared pool; the
// lock keeps concurrent directory creation and atomic renames from
// racing. Index regeneration no longer happens here (it is deferred to a
// single scan when the index is consumed), so the lock only guards the
// cheap copy.
var debPublishMu sync.Mutex

// DebRepoOptions configures the project's Debian-format repo emitter.
type DebRepoOptions struct {
	// RepoDir is the repo root: per-project we expect this caller to
	// pass repo/<project>/debian/. Pool layout, Packages and Release
	// files land relative to this directory.
	RepoDir string

	// Suite is the Debian codename emitted into Release / InRelease
	// (e.g. "bookworm"). The deb sources.list on-device references
	// this suite.
	Suite string

	// Components is the list of archive components (typically just
	// ["main"]); each becomes a dists/<suite>/<component>/ subtree.
	Components []string

	// Arches is the list of Debian arch tokens (e.g. ["amd64", "arm64"]).
	// noarch / Architecture: all packages get fanned out into every
	// per-arch Packages file per the noarch-routing pattern.
	Arches []string

	// ValidUntilDays controls Release's Valid-Until field. 0 means
	// "use 30 days" — R24's default that leans toward dev workflow.
	ValidUntilDays int

	// GPGHomedir + GPGKeyID identify the project signing key for
	// InRelease (R16). When GPGHomedir is empty, no InRelease is
	// emitted; only the unsigned Release lands.
	GPGHomedir string
	GPGKeyID   string
}

// GenerateDebianIndex scans RepoDir/pool/ for .deb files, writes
// per-component/per-arch Packages files (plain + .gz), produces the
// suite-level Release file, and signs an InRelease.
//
// Layout:
//
//	<RepoDir>/pool/<component>/<initial>/<src>/<pkg>_<ver>_<arch>.deb
//	<RepoDir>/dists/<suite>/<component>/binary-<arch>/Packages
//	<RepoDir>/dists/<suite>/<component>/binary-<arch>/Packages.gz
//	<RepoDir>/dists/<suite>/Release
//	<RepoDir>/dists/<suite>/InRelease       (signed; only when GPG configured)
//
// The function is idempotent: re-running after a new .deb lands in
// pool/ regenerates the indices and re-signs.
func GenerateDebianIndex(opts DebRepoOptions) error {
	if opts.RepoDir == "" {
		return fmt.Errorf("deb_emitter: RepoDir is required")
	}
	if opts.Suite == "" {
		return fmt.Errorf("deb_emitter: Suite is required")
	}
	if len(opts.Components) == 0 {
		opts.Components = []string{"main"}
	}
	if len(opts.Arches) == 0 {
		return fmt.Errorf("deb_emitter: Arches is required")
	}
	if opts.ValidUntilDays == 0 {
		opts.ValidUntilDays = 30
	}

	distsDir := filepath.Join(opts.RepoDir, "dists", opts.Suite)
	var indices []packagesIndex

	for _, comp := range opts.Components {
		pooled, err := scanPool(filepath.Join(opts.RepoDir, "pool", comp), opts.RepoDir)
		if err != nil {
			return fmt.Errorf("deb_emitter: pool scan: %w", err)
		}
		byArch := map[string][]pooledDeb{}
		for _, pd := range pooled {
			byArch[pd.arch] = append(byArch[pd.arch], pd)
		}
		for _, arch := range opts.Arches {
			entries := append([]pooledDeb(nil), byArch[arch]...)
			entries = append(entries, byArch["all"]...)
			sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
			var bodyBuf bytes.Buffer
			for _, e := range entries {
				bodyBuf.Write(e.stanza)
				bodyBuf.WriteByte('\n')
			}
			body := bodyBuf.Bytes()
			rel := filepath.Join(comp, "binary-"+arch, "Packages")
			dst := filepath.Join(distsDir, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, body, 0o644); err != nil {
				return fmt.Errorf("write Packages: %w", err)
			}
			gz, err := gzipBytes(body)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dst+".gz", gz, 0o644); err != nil {
				return fmt.Errorf("write Packages.gz: %w", err)
			}
			indices = append(indices, packagesIndex{relPath: rel, body: body})
			indices = append(indices, packagesIndex{relPath: rel + ".gz", body: gz})
		}
	}

	releaseBody := buildRelease(opts, indices)
	releasePath := filepath.Join(distsDir, "Release")
	if err := os.MkdirAll(distsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(releasePath, releaseBody, 0o644); err != nil {
		return fmt.Errorf("write Release: %w", err)
	}

	if opts.GPGHomedir != "" {
		signed, err := deb.SignInRelease(releaseBody, opts.GPGHomedir, opts.GPGKeyID)
		if err != nil {
			return fmt.Errorf("sign InRelease: %w", err)
		}
		if err := os.WriteFile(filepath.Join(distsDir, "InRelease"), signed, 0o644); err != nil {
			return fmt.Errorf("write InRelease: %w", err)
		}
	}
	return nil
}

// pooledDeb is one .deb found in the pool: its declared architecture,
// its path (for stable ordering), and its fully rendered Packages
// stanza. Built in a single pass so each .deb is parsed once per index
// generation rather than once for arch routing and again for the stanza
// — the prior two-read shape turned index emit into an O(pool) ×
// (decompress + hash) cost paid twice.
type pooledDeb struct {
	arch   string
	path   string
	stanza []byte
}

// scanPool walks componentPool and returns one pooledDeb per .deb,
// reading and decompressing each file a single time. Architecture: "all"
// packages are returned under their declared arch ("all") for the caller
// to fan out into every per-arch Packages file.
func scanPool(componentPool, repoDir string) ([]pooledDeb, error) {
	var out []pooledDeb
	if _, err := os.Stat(componentPool); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.WalkDir(componentPool, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".deb") {
			return nil
		}
		stanza, arch, err := stanzaForDeb(repoDir, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		out = append(out, pooledDeb{arch: arch, path: p, stanza: stanza})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// stanzaForDeb renders the Packages stanza for a single .deb and returns
// the package's declared architecture alongside it. The file is opened
// once: its control paragraph supplies the stanza body and the arch, and
// its raw bytes supply Size / SHA256.
func stanzaForDeb(repoDir, debPath string) (stanza []byte, arch string, err error) {
	d, err := deb.ReadDeb(debPath)
	if err != nil {
		return nil, "", err
	}
	defer d.Close()
	arch = d.Control.Architecture

	relFilename, err := filepath.Rel(repoDir, debPath)
	if err != nil {
		return nil, "", err
	}
	stat, err := os.Stat(debPath)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(debPath)
	if err != nil {
		return nil, "", err
	}
	sha := sha256.Sum256(raw)

	var b bytes.Buffer
	if err := deb.WriteControl(&b, d.Control); err != nil {
		return nil, "", err
	}
	fmt.Fprintf(&b, "Filename: %s\n", filepath.ToSlash(relFilename))
	fmt.Fprintf(&b, "Size: %d\n", stat.Size())
	fmt.Fprintf(&b, "SHA256: %x\n", sha[:])
	return b.Bytes(), arch, nil
}

// buildRelease produces the Release file body. Fields follow Debian
// Policy 5.4 / apt-secure conventions: Origin, Label, Suite, Codename,
// Date, Valid-Until, Components, Architectures, plus SHA256/SHA512
// blocks covering every Packages and Packages.gz.
func buildRelease(opts DebRepoOptions, indices []packagesIndex) []byte {
	now := time.Now().UTC()
	validUntil := now.Add(time.Duration(opts.ValidUntilDays) * 24 * time.Hour)

	var b bytes.Buffer
	fmt.Fprintf(&b, "Origin: %s\n", opts.Suite)
	fmt.Fprintf(&b, "Label: %s\n", opts.Suite)
	fmt.Fprintf(&b, "Suite: %s\n", opts.Suite)
	fmt.Fprintf(&b, "Codename: %s\n", opts.Suite)
	fmt.Fprintf(&b, "Date: %s\n", now.Format(time.RFC1123))
	fmt.Fprintf(&b, "Valid-Until: %s\n", validUntil.Format(time.RFC1123))
	fmt.Fprintf(&b, "Components: %s\n", strings.Join(opts.Components, " "))
	fmt.Fprintf(&b, "Architectures: %s\n", strings.Join(opts.Arches, " "))
	fmt.Fprintln(&b, "Acquire-By-Hash: no")

	// SHA256 block
	fmt.Fprintln(&b, "SHA256:")
	for _, idx := range indices {
		sum := sha256.Sum256(idx.body)
		fmt.Fprintf(&b, " %x %d %s\n", sum[:], len(idx.body), idx.relPath)
	}
	// SHA512 block
	fmt.Fprintln(&b, "SHA512:")
	for _, idx := range indices {
		sum := sha512.Sum512(idx.body)
		fmt.Fprintf(&b, " %x %d %s\n", sum[:], len(idx.body), idx.relPath)
	}
	return b.Bytes()
}

type packagesIndex = struct {
	relPath string
	body    []byte
}

func gzipBytes(data []byte) ([]byte, error) {
	var b bytes.Buffer
	gw := gzip.NewWriter(&b)
	if _, err := gw.Write(data); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// PublishDeb copies debPath into the project pool at
// pool/<component>/<initial>/<src>/<basename>.deb. It does NOT regenerate
// the Packages/Release index: that is an O(pool) full re-scan, and doing
// it once per published .deb made a fresh image build O(units²). The
// index is instead refreshed once from the pool when it is actually
// consumed — immediately before image assembly, and once at the end of a
// build that published .debs without building an image — so the on-disk
// index always reflects the pool without the quadratic re-emit.
//
// debPublishMu still serializes the copy so concurrent unit builds don't
// race the directory creation; each .deb lands at a distinct path via an
// atomic temp+rename, so the pool stays consistent for the later scan.
func PublishDeb(debPath string, opts DebRepoOptions, component string) error {
	debPublishMu.Lock()
	defer debPublishMu.Unlock()

	d, err := deb.ReadDeb(debPath)
	if err != nil {
		return fmt.Errorf("PublishDeb: read %s: %w", debPath, err)
	}
	src := sourceNameOf(d.Control)
	_ = d.Close()
	initial := initialOf(src)
	poolDir := filepath.Join(opts.RepoDir, "pool", component, initial, src)
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(poolDir, filepath.Base(debPath))
	if err := copyFile(debPath, dst); err != nil {
		return fmt.Errorf("PublishDeb: copy: %w", err)
	}
	return nil
}

// sourceNameOf returns the source package name (Source field on the
// .deb control, falling back to Package when Source is empty).
func sourceNameOf(c deb.Control) string {
	if c.Source == "" {
		return c.Package
	}
	return c.Source
}

// initialOf returns the first letter of src (or "lib<initial>" for
// lib* packages). Matches Debian conventional pool layout.
func initialOf(src string) string {
	if strings.HasPrefix(src, "lib") && len(src) > 3 {
		return "lib" + string(src[3])
	}
	if src == "" {
		return "_"
	}
	return string(src[0])
}

// copyFile atomically copies src to dst via tmpfile + rename so a
// concurrent reader never sees a partial file at the canonical path.
// The pool-side .deb is read by GenerateDebianIndex right after copy,
// and parallel publishes scan the same tree.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// VerifyMirrorSHA256 is the R15 sanity hook: before adding a
// mirror-fetched .deb to pool, the caller compares the computed
// SHA256 against the upstream-signed Packages entry. Mismatch is a
// hard error — yoe refuses to publish a project InRelease that points
// at bytes the upstream catalog doesn't know.
func VerifyMirrorSHA256(debPath, upstreamSHA256 string) error {
	if upstreamSHA256 == "" {
		return fmt.Errorf("VerifyMirrorSHA256: empty upstream SHA256")
	}
	raw, err := os.ReadFile(debPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	have := fmt.Sprintf("%x", sum[:])
	if !strings.EqualFold(have, upstreamSHA256) {
		return fmt.Errorf("VerifyMirrorSHA256: %s: computed %s != upstream %s", debPath, have, upstreamSHA256)
	}
	return nil
}

// sha256Hex computes a hex SHA256 string for use in tests.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

// PackagesParseDelete is a small unused export to ensure the dpkg
// dependency stays compiled into the binary. Remove when project repo
// reads use this for sanity checks.
var _ = dpkg.ParseIndex
