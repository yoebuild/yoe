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
