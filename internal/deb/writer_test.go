package deb

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireDpkgDeb skips the test when dpkg-deb isn't on PATH. The
// runtime workflow runs BuildDeb inside the toolchain-glibc container;
// dev hosts may not have dpkg-deb installed natively.
func requireDpkgDeb(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("dpkg-deb not on PATH (BuildDeb shells to it; runs in toolchain-glibc container in production)")
	}
}

func TestBuildDeb_Roundtrip(t *testing.T) {
	requireDpkgDeb(t)

	destDir := t.TempDir()
	usrBin := filepath.Join(destDir, "usr", "bin")
	if err := os.MkdirAll(usrBin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(usrBin, "hello"), []byte("#!/bin/sh\necho hello\n"), 0755); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "hello_1.0_amd64.deb")
	c := Control{
		Package:      "hello",
		Version:      "1.0",
		Architecture: "amd64",
		Maintainer:   "Yoe <yoe@example.com>",
		Description:  "test",
	}
	if err := BuildDeb(destDir, c, out, ""); err != nil {
		t.Fatalf("BuildDeb: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output deb missing: %v", err)
	}

	// Round-trip: ReadDeb must agree on control fields.
	d, err := ReadDeb(out)
	if err != nil {
		t.Fatalf("ReadDeb: %v", err)
	}
	defer d.Close()
	if d.Control.Package != "hello" {
		t.Errorf("roundtrip Package: got %q", d.Control.Package)
	}
	if d.Control.Architecture != "amd64" {
		t.Errorf("roundtrip Architecture: got %q", d.Control.Architecture)
	}
}

func TestBuildDeb_DpkgDebMissing(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err == nil {
		t.Skip("dpkg-deb present; this test only runs when absent")
	}
	destDir := t.TempDir()
	c := Control{
		Package:      "x",
		Version:      "1",
		Architecture: "amd64",
		Maintainer:   "m",
		Description:  "d",
	}
	err := BuildDeb(destDir, c, filepath.Join(t.TempDir(), "x.deb"), "")
	if err == nil {
		t.Fatal("expected error when dpkg-deb missing")
	}
}
