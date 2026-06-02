package deb

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildDeb_Roundtrip(t *testing.T) {
	destDir := t.TempDir()
	usrBin := filepath.Join(destDir, "usr", "bin")
	if err := os.MkdirAll(usrBin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(usrBin, "hello"), []byte("#!/bin/sh\necho hello\n"), 0755); err != nil {
		t.Fatal(err)
	}
	// A symlink in the tree exercises the non-regular file path.
	if err := os.Symlink("hello", filepath.Join(usrBin, "hi")); err != nil {
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

	// Round-trip through the canonical reader: it must agree on the
	// control fields and be able to walk data.tar.
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

	var sawFile, sawSymlink bool
	for {
		hdr, err := d.Data.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("data.tar walk: %v", err)
		}
		switch hdr.Name {
		case "./usr/bin/hello":
			sawFile = true
			body, err := io.ReadAll(d.Data)
			if err != nil {
				t.Fatalf("read hello: %v", err)
			}
			if string(body) != "#!/bin/sh\necho hello\n" {
				t.Errorf("hello contents: got %q", body)
			}
			if hdr.Uid != 0 || hdr.Gid != 0 {
				t.Errorf("hello ownership: got uid=%d gid=%d, want root:root", hdr.Uid, hdr.Gid)
			}
		case "./usr/bin/hi":
			sawSymlink = true
			if hdr.Linkname != "hello" {
				t.Errorf("symlink target: got %q, want hello", hdr.Linkname)
			}
		}
	}
	if !sawFile {
		t.Error("data.tar missing ./usr/bin/hello")
	}
	if !sawSymlink {
		t.Error("data.tar missing ./usr/bin/hi symlink")
	}
}
