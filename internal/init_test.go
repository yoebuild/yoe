package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test-project")

	if err := RunInit(dir, ""); err != nil {
		t.Fatalf("RunInit: %v", err)
	}

	for _, path := range []string{
		"PROJECT.star",
		".gitignore",
		"machines",
		"units",
		"classes",
		"overlays",
	} {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); os.IsNotExist(err) {
			t.Errorf("expected %s to exist after init", path)
		}
	}

	// Verify PROJECT.star is valid Starlark
	content, err := os.ReadFile(filepath.Join(dir, "PROJECT.star"))
	if err != nil {
		t.Fatalf("reading PROJECT.star: %v", err)
	}
	if len(content) == 0 {
		t.Error("PROJECT.star is empty")
	}
}

func TestRunInit_WithMachine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test-project")

	if err := RunInit(dir, "qemu-x86_64"); err != nil {
		t.Fatalf("RunInit with machine: %v", err)
	}

	machineFile := filepath.Join(dir, "machines", "qemu-x86_64.star")
	if _, err := os.Stat(machineFile); os.IsNotExist(err) {
		t.Errorf("expected machine file %s to exist", machineFile)
	}
}

func TestRunInit_ExistingProject(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "PROJECT.star"), []byte("project(name=\"exists\")\n"), 0644)

	if err := RunInit(dir, ""); err == nil {
		t.Fatal("expected error when init into existing project, got nil")
	}
}

// The mirror table is part of the template, so a fresh project survives a
// GNU outage the way e2e-project does. Also guards the raw-string
// backtick escaping the comment needs.
func TestRunInitEmitsSourceMirrors(t *testing.T) {
	dir := t.TempDir()
	if err := RunInit(dir, "qemu-x86_64"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "PROJECT.star"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `("https://ftp.gnu.org/gnu", "https://mirrors.kernel.org/gnu")`) {
		t.Errorf("source_mirrors rule missing:\n%s", got)
	}
	if strings.Contains(got, `"+"`) {
		t.Errorf("raw-string escaping leaked into output")
	}
	if !strings.Contains(got, "# `mirrors` list.") {
		t.Errorf("comment backticks did not render:\n%s", got)
	}
}
