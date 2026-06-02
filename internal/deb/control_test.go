package deb

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteControl_Required(t *testing.T) {
	c := Control{
		Package:      "foo",
		Version:      "1.0-1",
		Architecture: "amd64",
		Maintainer:   "Yoe <yoe@example.com>",
		Description:  "test package",
	}
	var buf bytes.Buffer
	if err := WriteControl(&buf, c); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	s := buf.String()
	for _, want := range []string{
		"Package: foo",
		"Version: 1.0-1",
		"Architecture: amd64",
		"Maintainer: Yoe <yoe@example.com>",
		"Description: test package",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestWriteControl_MissingRequired(t *testing.T) {
	cases := map[string]Control{
		"no Package":      {Version: "1", Architecture: "amd64", Maintainer: "m", Description: "d"},
		"no Version":      {Package: "p", Architecture: "amd64", Maintainer: "m", Description: "d"},
		"no Architecture": {Package: "p", Version: "1", Maintainer: "m", Description: "d"},
		"no Maintainer":   {Package: "p", Version: "1", Architecture: "amd64", Description: "d"},
		"no Description":  {Package: "p", Version: "1", Architecture: "amd64", Maintainer: "m"},
	}
	for name, c := range cases {
		var buf bytes.Buffer
		if err := WriteControl(&buf, c); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// TestWriteControl_DescriptionFolding verifies the multi-line
// Description is re-folded to valid deb822: the synopsis stays on the
// Description: line, extended lines are indented one space, and blank
// lines become " .". Without folding, apt rejects the Packages stanza
// with "Encountered a section with no Package: header".
func TestWriteControl_DescriptionFolding(t *testing.T) {
	c := Control{
		Package:      "adduser",
		Version:      "3.134",
		Architecture: "all",
		Maintainer:   "Yoe <yoe@example.com>",
		// Reader stores the unfolded form: indent stripped, " ." -> "".
		Description: "add and remove users and groups\n" +
			"This package includes the 'adduser' command.\n" +
			"\n" +
			" - 'adduser' creates new users;",
	}
	var buf bytes.Buffer
	if err := WriteControl(&buf, c); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	s := buf.String()
	for _, want := range []string{
		"Description: add and remove users and groups\n",
		"\n This package includes the 'adduser' command.\n",
		"\n .\n",                               // blank line re-encoded
		"\n  - 'adduser' creates new users;\n", // already-indented line keeps its space + fold space
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
	// No bare blank line may appear before the stanza's trailing newline:
	// every continuation line must start with a space.
	for line := range strings.SplitSeq(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			t.Errorf("bare blank line inside stanza would terminate it early; got:\n%s", s)
		}
	}
}

func TestWriteControl_Optional(t *testing.T) {
	c := Control{
		Package:       "libc6",
		Version:       "2.36-9",
		Architecture:  "arm64",
		Maintainer:    "Yoe <yoe@example.com>",
		Description:   "GNU C Library",
		Section:       "libs",
		Priority:      "optional",
		InstalledSize: 13608,
		MultiArch:     "same",
		Depends:       "libgcc-s1",
		Provides:      "libc6-2.36",
	}
	var buf bytes.Buffer
	if err := WriteControl(&buf, c); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	s := buf.String()
	for _, want := range []string{
		"Section: libs",
		"Priority: optional",
		"Installed-Size: 13608",
		"Multi-Arch: same",
		"Depends: libgcc-s1",
		"Provides: libc6-2.36",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}
