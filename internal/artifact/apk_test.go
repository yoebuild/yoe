package artifact

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestCreateAPK(t *testing.T) {
	// Create a fake destdir with some files
	destDir := filepath.Join(t.TempDir(), "destdir")
	os.MkdirAll(filepath.Join(destDir, "usr", "bin"), 0755)
	os.MkdirAll(filepath.Join(destDir, "etc"), 0755)
	os.WriteFile(filepath.Join(destDir, "usr", "bin", "hello"), []byte("#!/bin/sh\necho hello\n"), 0755)
	os.WriteFile(filepath.Join(destDir, "etc", "hello.conf"), []byte("key=value\n"), 0644)

	outputDir := filepath.Join(t.TempDir(), "output")

	unit := &yoestar.Unit{
		Name:        "hello",
		Version:     "1.0.0",
		Description: "Hello world",
		License:     "MIT",
		RuntimeDeps: []string{"glibc"},
	}

	apkPath, err := CreateAPK(unit, destDir, outputDir, "x86_64", "", nil)
	if err != nil {
		t.Fatalf("CreateAPK: %v", err)
	}

	// Verify the .apk file exists
	if _, err := os.Stat(apkPath); os.IsNotExist(err) {
		t.Fatal("apk file not created")
	}

	// Verify it's a valid gzip'd tar
	f, err := os.Open(apkPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Alpine .apk files are concatenated gzip streams, each containing its
	// own tar archive. We must read each gzip stream separately as a
	// separate tar archive. Use a bufio.Reader so the gzip reader doesn't
	// consume bytes from the next stream.
	var files []string
	hasPKGINFO := false
	var pkginfoContent string

	br := bufio.NewReader(f)
	for {
		gr, err := gzip.NewReader(br)
		if err != nil {
			break // no more streams
		}
		gr.Multistream(false)

		tr := tar.NewReader(gr)
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			files = append(files, hdr.Name)
			if hdr.Name == ".PKGINFO" {
				hasPKGINFO = true
				data, _ := io.ReadAll(tr)
				pkginfoContent = string(data)
			}
		}
		// Drain any remaining data in this gzip stream
		io.Copy(io.Discard, gr)
		gr.Close()
	}

	if !hasPKGINFO {
		t.Error(".PKGINFO not found in apk")
	}

	// Check PKGINFO content
	if !strings.Contains(pkginfoContent, "pkgname = hello") {
		t.Errorf("PKGINFO missing pkgname: %s", pkginfoContent)
	}
	if !strings.Contains(pkginfoContent, "pkgver = 1.0.0-r0") {
		t.Errorf("PKGINFO missing pkgver: %s", pkginfoContent)
	}
	if !strings.Contains(pkginfoContent, "depend = glibc") {
		t.Errorf("PKGINFO missing dependency: %s", pkginfoContent)
	}

	// Check files are included
	hasHello := false
	hasConf := false
	for _, f := range files {
		if strings.Contains(f, "hello") && strings.Contains(f, "bin") {
			hasHello = true
		}
		if strings.Contains(f, "hello.conf") {
			hasConf = true
		}
	}
	if !hasHello {
		t.Errorf("usr/bin/hello not found in apk, files: %v", files)
	}
	if !hasConf {
		t.Errorf("etc/hello.conf not found in apk, files: %v", files)
	}
}

func TestCreateAPK_EmptyDestDir(t *testing.T) {
	destDir := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "output")

	unit := &yoestar.Unit{
		Name:    "empty",
		Version: "1.0.0",
	}

	apkPath, err := CreateAPK(unit, destDir, outputDir, "x86_64", "", nil)
	if err != nil {
		t.Fatalf("CreateAPK: %v", err)
	}

	if _, err := os.Stat(apkPath); os.IsNotExist(err) {
		t.Fatal("apk file not created for empty package")
	}
}

func TestAPKHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.apk")
	os.WriteFile(path, []byte("test content"), 0644)

	h, err := APKHash(path)
	if err != nil {
		t.Fatalf("APKHash: %v", err)
	}
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64", len(h))
	}

	// Same file should produce same hash
	h2, _ := APKHash(path)
	if h != h2 {
		t.Error("hash not deterministic")
	}
}

func TestGeneratePKGINFO(t *testing.T) {
	unit := &yoestar.Unit{
		Name:        "test",
		Version:     "2.0",
		Description: "Test package",
		License:     "BSD",
		RuntimeDeps: []string{"zlib", "openssl"},
	}

	info := generatePKGINFO(unit, t.TempDir(), "abc123", "x86_64", "deadbeef")

	if !strings.Contains(info, "pkgname = test") {
		t.Error("missing pkgname")
	}
	if !strings.Contains(info, "pkgver = 2.0-r0") {
		t.Error("missing pkgver")
	}
	if !strings.Contains(info, "pkgdesc = Test package") {
		t.Error("missing pkgdesc")
	}
	if !strings.Contains(info, "depend = zlib") {
		t.Error("missing depend zlib")
	}
	if !strings.Contains(info, "depend = openssl") {
		t.Error("missing depend openssl")
	}
	if !strings.Contains(info, "origin = test") {
		t.Errorf("missing origin: %s", info)
	}
	if !strings.Contains(info, "commit = deadbeef") {
		t.Errorf("missing commit: %s", info)
	}
}

