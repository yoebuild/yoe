package deb

import (
	"archive/tar"
	"fmt"

	"pault.ag/go/debian/deb"
)

// Deb wraps a parsed .deb file. Carries the parsed Control plus the
// underlying handle so callers can stream data.tar entries without
// re-opening the file.
type Deb struct {
	Control Control
	// Data is the tar reader over the .deb's data.tar member. The
	// stream's compression (gz/xz/zst) is handled transparently by
	// pault.ag/go/debian/deb.
	Data *tar.Reader

	close func() error
}

// Close releases the underlying file handle.
func (d *Deb) Close() error {
	if d == nil || d.close == nil {
		return nil
	}
	return d.close()
}

// ReadDeb opens a .deb at path, parses its control, and returns a Deb
// with a streaming data tar reader. Caller must Close.
func ReadDeb(path string) (*Deb, error) {
	debFile, closer, err := deb.LoadFile(path)
	if err != nil {
		return nil, fmt.Errorf("deb: read %s: %w", path, err)
	}
	c := debFile.Control
	// Relationship fields are read verbatim from the raw control
	// paragraph rather than the library's typed Control struct: the
	// struct exposes Depends/Breaks/Replaces but not Pre-Depends,
	// Provides, Conflicts, or Enhances, and Pre-Depends is exactly the
	// field that orders libc6 before dpkg during rootfs assembly.
	// Carrying the raw strings preserves version constraints and
	// alternatives so the regenerated Packages index drives apt/dpkg's
	// dependency graph the same way the upstream index does. Without
	// this the index has no edges and mmdebstrap configures packages in
	// arbitrary order, forcing dpkg through its dependencies.
	rel := func(field string) string { return c.Values[field] }
	out := &Deb{
		Control: Control{
			Package:       c.Package,
			Source:        c.Source,
			Version:       c.Version.String(),
			Architecture:  c.Architecture.String(),
			Maintainer:    c.Maintainer,
			Description:   c.Description,
			Section:       c.Section,
			Priority:      c.Priority,
			InstalledSize: c.InstalledSize,
			MultiArch:     c.MultiArch,
			Homepage:      c.Homepage,
			Depends:       rel("Depends"),
			PreDepends:    rel("Pre-Depends"),
			Recommends:    rel("Recommends"),
			Suggests:      rel("Suggests"),
			Enhances:      rel("Enhances"),
			Conflicts:     rel("Conflicts"),
			Breaks:        rel("Breaks"),
			Replaces:      rel("Replaces"),
			Provides:      rel("Provides"),
		},
		Data:  debFile.Data,
		close: closer,
	}
	return out, nil
}
