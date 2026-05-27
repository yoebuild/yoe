// Package dpkg parses Debian Packages files into structured entries the
// synthetic-module loader can materialize on demand. It is the format-named
// sibling of internal/apkindex.
//
// A Packages file is the deb822 catalog apt fetches from each
// dists/<suite>/<component>/binary-<arch>/Packages. Each entry is a
// blank-line-separated stanza of "Field: value" lines, with continuation
// lines that begin with whitespace. Entries are documented at
// <https://www.debian.org/doc/debian-policy/ch-controlfields.html>.
//
// Parsing leans on pault.ag/go/debian/control (deb822 unmarshaler) plus
// pault.ag/go/debian/dependency for dependency lines and
// pault.ag/go/debian/version for version comparison; this package wraps
// those into yoe-shaped types.
package dpkg

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"pault.ag/go/debian/control"
)

// Entry is one parsed Packages stanza. Field names follow Debian policy
// (Package, Version, ...) so a reader can cross-reference upstream docs.
// Multi-Arch carries the "Multi-Arch:" value verbatim; dependency lines
// are kept as their raw strings and parsed lazily through deps.go.
type Entry struct {
	Package       string
	Source        string
	Version       string
	Architecture  string
	MultiArch     string
	Maintainer    string
	Description   string
	Section       string
	Priority      string
	Homepage      string
	InstalledSize int
	Size          int

	// Filename is the pool-relative path apt downloads. yoe rewrites this
	// at index-emit time to point into the project's own pool.
	Filename string

	// SHA256 is the upstream-signed hash. The mirror-time verify path
	// (R15) compares this against the SHA256 yoe computes during source
	// fetch.
	SHA256 string
	SHA1   string
	MD5sum string

	// Raw dep strings — kept verbatim; parsed on demand via ParseDependency.
	Depends    string
	PreDepends string `control:"Pre-Depends"`
	Recommends string
	Suggests   string
	Enhances   string
	Conflicts  string
	Breaks     string
	Replaces   string
	Provides   string
}

// ParseIndex reads a Packages text stream and returns one Entry per
// stanza. Empty stanzas are skipped; truly malformed input surfaces as
// an error naming the failing position.
//
// The caller is responsible for decompression — Packages files ship as
// .gz/.xz, but the yoe feed pipeline keeps them decompressed on disk
// for diff-friendliness.
func ParseIndex(r io.Reader) ([]Entry, error) {
	type stanza struct {
		Package       string
		Source        string
		Version       string
		Architecture  string
		MultiArch     string `control:"Multi-Arch"`
		Maintainer    string
		Description   string
		Section       string
		Priority      string
		Homepage      string
		InstalledSize int `control:"Installed-Size"`
		Size          int
		Filename      string
		SHA256        string
		SHA1          string
		MD5sum        string
		Depends       string
		PreDepends    string `control:"Pre-Depends"`
		Recommends    string
		Suggests      string
		Enhances      string
		Conflicts     string
		Breaks        string
		Replaces      string
		Provides      string
	}
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 64*1024)
	}
	var raw []stanza
	if err := control.Unmarshal(&raw, br); err != nil {
		return nil, fmt.Errorf("dpkg: parse Packages: %w", err)
	}
	out := make([]Entry, 0, len(raw))
	for i, s := range raw {
		if s.Package == "" {
			return nil, fmt.Errorf("dpkg: stanza %d: missing Package field", i)
		}
		out = append(out, Entry{
			Package:       s.Package,
			Source:        s.Source,
			Version:       s.Version,
			Architecture:  s.Architecture,
			MultiArch:     s.MultiArch,
			Maintainer:    s.Maintainer,
			Description:   s.Description,
			Section:       s.Section,
			Priority:      s.Priority,
			Homepage:      s.Homepage,
			InstalledSize: s.InstalledSize,
			Size:          s.Size,
			Filename:      s.Filename,
			SHA256:        s.SHA256,
			SHA1:          s.SHA1,
			MD5sum:        s.MD5sum,
			Depends:       s.Depends,
			PreDepends:    s.PreDepends,
			Recommends:    s.Recommends,
			Suggests:      s.Suggests,
			Enhances:      s.Enhances,
			Conflicts:     s.Conflicts,
			Breaks:        s.Breaks,
			Replaces:      s.Replaces,
			Provides:      s.Provides,
		})
	}
	return out, nil
}

// ParseIndexFile opens path (a plain decompressed Packages file) and
// parses it. Decompression of .gz / .xz wrappers happens upstream.
func ParseIndexFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseIndex(f)
}
