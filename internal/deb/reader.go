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
		},
		Data:  debFile.Data,
		close: closer,
	}
	return out, nil
}
