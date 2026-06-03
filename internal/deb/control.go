// Package deb reads and writes Debian .deb binary packages and signs
// Debian InRelease files. It is the format-named sibling of
// internal/artifact for the apk side.
//
// A .deb is an `ar` archive containing three members: debian-binary
// (format version "2.0"), control.tar.{gz,xz,zst} (DEBIAN/control plus
// metadata), and data.tar.{gz,xz,zst} (the rootfs payload). Reading
// leans on pault.ag/go/debian/deb; writing shells `dpkg-deb --build`
// after this package stages a destdir.
package deb

import (
	"fmt"
	"io"
	"strings"
)

// Control is the metadata that lands at DEBIAN/control inside a .deb.
// Field set is the union of "required in v1" (Package, Version,
// Architecture, Maintainer, Description) and the optional fields yoe
// emits when the unit provides them.
type Control struct {
	Package       string
	Source        string
	Version       string
	Architecture  string
	Maintainer    string
	Description   string
	Section       string
	Priority      string
	InstalledSize int
	MultiArch     string
	Homepage      string

	// Relations — emitted verbatim. The unit derives these from its
	// RuntimeDeps / Provides / Replaces / Breaks fields.
	Depends    string
	PreDepends string
	Recommends string
	Suggests   string
	Enhances   string
	Conflicts  string
	Breaks     string
	Replaces   string
	Provides   string
}

// WriteControl emits Control as a deb822 DEBIAN/control file. Field
// order follows Debian Policy 5.3 — required fields first, then
// relational fields, then descriptive. Empty optional fields are
// omitted; required fields produce an error.
func WriteControl(w io.Writer, c Control) error {
	if c.Package == "" {
		return fmt.Errorf("deb: control: Package field required")
	}
	if c.Version == "" {
		return fmt.Errorf("deb: control: Version field required")
	}
	if c.Architecture == "" {
		return fmt.Errorf("deb: control: Architecture field required")
	}
	if c.Maintainer == "" {
		return fmt.Errorf("deb: control: Maintainer field required")
	}
	if c.Description == "" {
		return fmt.Errorf("deb: control: Description field required")
	}

	var b strings.Builder
	emit := func(key, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(&b, "%s: %s\n", key, value)
	}
	emit("Package", c.Package)
	emit("Source", c.Source)
	emit("Version", c.Version)
	emit("Architecture", c.Architecture)
	emit("Maintainer", c.Maintainer)
	if c.InstalledSize > 0 {
		fmt.Fprintf(&b, "Installed-Size: %d\n", c.InstalledSize)
	}
	emit("Multi-Arch", c.MultiArch)
	emit("Section", c.Section)
	emit("Priority", c.Priority)
	emit("Homepage", c.Homepage)
	emit("Pre-Depends", c.PreDepends)
	emit("Depends", c.Depends)
	emit("Recommends", c.Recommends)
	emit("Suggests", c.Suggests)
	emit("Enhances", c.Enhances)
	emit("Conflicts", c.Conflicts)
	emit("Breaks", c.Breaks)
	emit("Replaces", c.Replaces)
	emit("Provides", c.Provides)
	writeDescription(&b, c.Description)

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("deb: write control: %w", err)
	}
	return nil
}

// writeDescription emits the folded deb822 Description field. The
// synopsis sits on the `Description:` line; every extended-description
// line is indented one space, with empty lines encoded as " .". The
// reader unfolds Description (drops the indent and turns " ." into an
// empty line), so without re-folding here apt rejects the whole
// Packages stanza with "Encountered a section with no Package: header"
// the moment an empty line inside a description prematurely terminates
// the stanza.
func writeDescription(b *strings.Builder, desc string) {
	lines := strings.Split(strings.TrimRight(desc, "\n"), "\n")
	fmt.Fprintf(b, "Description: %s\n", lines[0])
	for _, line := range lines[1:] {
		if line == "" {
			b.WriteString(" .\n")
		} else {
			fmt.Fprintf(b, " %s\n", line)
		}
	}
}
