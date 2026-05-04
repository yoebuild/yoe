package repo

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoebuild/yoe/internal/artifact"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestGenerateIndex(t *testing.T) {
	// Create a fake .apk using artifact.CreateAPK
	destDir := filepath.Join(t.TempDir(), "destdir")
	os.MkdirAll(filepath.Join(destDir, "usr", "bin"), 0755)
	os.WriteFile(filepath.Join(destDir, "usr", "bin", "hello"), []byte("#!/bin/sh\necho hello\n"), 0755)

	outputDir := filepath.Join(t.TempDir(), "output")

	unit := &yoestar.Unit{
		Name:        "hello",
		Version:     "1.0.0",
		Description: "Hello world",
		License:     "MIT",
	}

	apkPath, err := artifact.CreateAPK(unit, destDir, outputDir, "x86_64", "", nil)
	if err != nil {
		t.Fatalf("CreateAPK: %v", err)
	}

	// Set up repo dir and copy the apk into it
	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Copy apk to repo
	data, err := os.ReadFile(apkPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, filepath.Base(apkPath)), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Generate index
	if err := GenerateIndex(repoDir, nil); err != nil {
		t.Fatalf("GenerateIndex: %v", err)
	}

	// Verify APKINDEX.tar.gz exists and is non-empty
	indexPath := filepath.Join(repoDir, "APKINDEX.tar.gz")
	info, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("APKINDEX.tar.gz not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("APKINDEX.tar.gz is empty")
	}

	// Read and verify the APKINDEX content
	content := readAPKINDEX(t, indexPath)

	if !strings.Contains(content, "P:hello") {
		t.Errorf("APKINDEX missing P:hello, got:\n%s", content)
	}
	if !strings.Contains(content, "V:1.0.0-r0") {
		t.Errorf("APKINDEX missing V:1.0.0-r0, got:\n%s", content)
	}
	if !strings.Contains(content, "A:x86_64") {
		t.Errorf("APKINDEX missing A:x86_64, got:\n%s", content)
	}
	if !strings.Contains(content, "C:Q1") {
		t.Errorf("APKINDEX missing checksum line, got:\n%s", content)
	}
	if !strings.Contains(content, "S:") {
		t.Errorf("APKINDEX missing size line, got:\n%s", content)
	}
	if !strings.Contains(content, "T:Hello world") {
		t.Errorf("APKINDEX missing description, got:\n%s", content)
	}
}

func TestGenerateIndex_EmptyRepo(t *testing.T) {
	repoDir := t.TempDir()

	// Should succeed with no apks
	if err := GenerateIndex(repoDir, nil); err != nil {
		t.Fatalf("GenerateIndex on empty repo: %v", err)
	}

	// APKINDEX.tar.gz should not exist
	indexPath := filepath.Join(repoDir, "APKINDEX.tar.gz")
	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Error("APKINDEX.tar.gz should not exist for empty repo")
	}
}

func readAPKINDEX(t *testing.T, path string) string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("not valid gzip: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar: %v", err)
		}
		if hdr.Name == "APKINDEX" {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	t.Fatal("APKINDEX entry not found in tar")
	return ""
}
