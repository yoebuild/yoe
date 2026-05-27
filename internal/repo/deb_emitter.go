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
	"time"

	"github.com/yoebuild/yoe/internal/deb"
	"github.com/yoebuild/yoe/internal/dpkg"
)

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
		debsByArch, err := walkPool(filepath.Join(opts.RepoDir, "pool", comp))
		if err != nil {
			return fmt.Errorf("deb_emitter: pool scan: %w", err)
		}
		for _, arch := range opts.Arches {
			entries := append([]string(nil), debsByArch[arch]...)
			entries = append(entries, debsByArch["all"]...)
			sort.Strings(entries)
			body, err := buildPackagesFile(opts.RepoDir, entries)
			if err != nil {
				return fmt.Errorf("deb_emitter: Packages for %s/%s: %w", comp, arch, err)
			}
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

// walkPool returns a map of arch -> []relative-to-RepoDir deb paths.
// Architecture: "all" entries live under their natural pool path and
// are surfaced under the "all" key for the caller to fan out.
func walkPool(componentPool string) (map[string][]string, error) {
	out := map[string][]string{}
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
		d2, err := deb.ReadDeb(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		arch := d2.Control.Architecture
		_ = d2.Close()
		out[arch] = append(out[arch], p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// buildPackagesFile concatenates the control stanzas for every deb in
// `debs`, appending Filename / Size / SHA256 derived from the on-disk
// file.
func buildPackagesFile(repoDir string, debs []string) ([]byte, error) {
	var buf bytes.Buffer
	for _, p := range debs {
		stanza, err := packageStanza(repoDir, p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		buf.Write(stanza)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func packageStanza(repoDir, debPath string) ([]byte, error) {
	d, err := deb.ReadDeb(debPath)
	if err != nil {
		return nil, err
	}
	defer d.Close()

	relFilename, err := filepath.Rel(repoDir, debPath)
	if err != nil {
		return nil, err
	}

	stat, err := os.Stat(debPath)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(debPath)
	if err != nil {
		return nil, err
	}
	sha := sha256.Sum256(raw)

	var b bytes.Buffer
	if err := deb.WriteControl(&b, d.Control); err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "Filename: %s\n", filepath.ToSlash(relFilename))
	fmt.Fprintf(&b, "Size: %d\n", stat.Size())
	fmt.Fprintf(&b, "SHA256: %x\n", sha[:])
	return b.Bytes(), nil
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
// pool/<component>/<initial>/<src>/<basename>.deb, then re-runs
// GenerateDebianIndex against opts.
func PublishDeb(debPath string, opts DebRepoOptions, component string) error {
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
	return GenerateDebianIndex(opts)
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
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
