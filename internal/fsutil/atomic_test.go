package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "APKINDEX")

	if err := WriteFileAtomic(path, []byte("P:musl\n"), 0644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "P:musl\n" {
		t.Errorf("content = %q", got)
	}

	// Replacing an existing file is the common case — an index is
	// rewritten every time the repo changes.
	if err := WriteFileAtomic(path, []byte("P:busybox\n"), 0644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "P:busybox\n" {
		t.Errorf("after rewrite, content = %q", got)
	}

	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestCopyFileAtomic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "hello-1.0-r0.apk")
	dst := filepath.Join(dir, "x86_64", "hello-1.0-r0.apk")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("package bytes\n", 500)
	if err := os.WriteFile(src, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	if err := CopyFileAtomic(src, dst, 0644); err != nil {
		t.Fatalf("CopyFileAtomic: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != body {
		t.Errorf("copied %d bytes, want %d", len(got), len(body))
	}
}

// No temp file may survive a completed write. A leftover in a repo
// directory is what a package-index scan would trip over.
func TestNoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFileAtomic(filepath.Join(dir, "Packages"), []byte("Package: bash\n"), 0644); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "Packages" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want just [Packages]", names)
	}
}

// A failed copy must leave the destination untouched rather than
// replacing good content with a truncated file.
func TestCopyFileAtomic_MissingSourceLeavesDestination(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "existing")
	if err := os.WriteFile(dst, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CopyFileAtomic(filepath.Join(dir, "does-not-exist"), dst, 0644); err == nil {
		t.Fatal("expected an error copying a missing source")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "original\n" {
		t.Errorf("destination was modified: %q", got)
	}
}
