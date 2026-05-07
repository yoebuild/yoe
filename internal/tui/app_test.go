package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// writeMeta is a test helper that mirrors build.WriteMeta's on-disk
// layout without dragging the build package in for what we want to
// test (TUI's read side of the same file).
func writeMeta(t *testing.T, buildDir string, installedBytes int64) {
	t.Helper()
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatalf("mkdir buildDir: %v", err)
	}
	data, _ := json.Marshal(map[string]any{
		"status":          "complete",
		"installed_bytes": installedBytes,
		"hash":            "deadbeef",
	})
	if err := os.WriteFile(filepath.Join(buildDir, "build.json"), data, 0o644); err != nil {
		t.Fatalf("write build.json: %v", err)
	}
}

func TestInstalledSize_ReadsMeta(t *testing.T) {
	dir := t.TempDir()
	writeMeta(t, dir, 12345)

	got := installedSize(dir)
	if got != 12345 {
		t.Fatalf("installedSize = %d, want 12345", got)
	}
}

func TestInstalledSize_Unbuilt_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	if got := installedSize(dir); got != 0 {
		t.Fatalf("installedSize = %d, want 0", got)
	}
}

func TestRefreshUnitSize_PicksUpFreshlyWrittenMeta(t *testing.T) {
	projDir := t.TempDir()
	// build/foo.x86_64/build.json
	buildDir := filepath.Join(projDir, "build", "foo.x86_64")
	writeMeta(t, buildDir, 4096)

	m := &model{
		projectDir: projDir,
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"foo": {Name: "foo", Class: "unit"},
			},
		},
	}
	if m.unitSize["foo"] != 0 {
		t.Fatalf("expected empty initial size, got %d", m.unitSize["foo"])
	}

	m.refreshUnitSize("foo")
	if m.unitSize["foo"] != 4096 {
		t.Fatalf("after refresh: unitSize[foo] = %d, want 4096", m.unitSize["foo"])
	}
}

func TestRefreshUnitSize_UnknownUnit_NoOp(t *testing.T) {
	m := &model{
		projectDir: t.TempDir(),
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units:    map[string]*yoestar.Unit{},
		},
	}
	// Should not panic, should not allocate spurious entries.
	m.refreshUnitSize("does-not-exist")
	if _, ok := m.unitSize["does-not-exist"]; ok {
		t.Fatalf("refreshUnitSize created entry for unknown unit")
	}
}

// TestRefreshDetailFiles_WalksDestdir verifies the Files tab walker:
// directories are skipped, regular files are listed with their byte
// size, and symlinks are flagged so the renderer can dim them.
func TestRefreshDetailFiles_WalksDestdir(t *testing.T) {
	projDir := t.TempDir()
	// build/foo.x86_64/destdir/...
	destDir := filepath.Join(projDir, "build", "foo.x86_64", "destdir")
	if err := os.MkdirAll(filepath.Join(destDir, "usr", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(destDir, "usr", "lib"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "usr", "bin", "foo"), make([]byte, 200), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "usr", "lib", "libfoo.so.1"), make([]byte, 5000), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink("libfoo.so.1", filepath.Join(destDir, "usr", "lib", "libfoo.so")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	m := &model{
		projectDir: projDir,
		arch:       "x86_64",
		detailUnit: "foo",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"foo": {Name: "foo", Class: "unit"},
			},
		},
	}
	m.refreshDetailFiles()

	if len(m.detailFiles) != 3 {
		t.Fatalf("got %d files, want 3: %+v", len(m.detailFiles), m.detailFiles)
	}
	// Default sort is by name ascending.
	want := []string{"/usr/bin/foo", "/usr/lib/libfoo.so", "/usr/lib/libfoo.so.1"}
	for i, w := range want {
		if m.detailFiles[i].Path != w {
			t.Fatalf("files[%d] = %q, want %q", i, m.detailFiles[i].Path, w)
		}
	}
	// Symlink flagged.
	if !m.detailFiles[1].Link {
		t.Fatalf("expected /usr/lib/libfoo.so to be flagged Link")
	}
	if m.detailFiles[0].Link || m.detailFiles[2].Link {
		t.Fatalf("regular files should not be flagged Link")
	}
	if m.detailFiles[0].Size != 200 || m.detailFiles[2].Size != 5000 {
		t.Fatalf("unexpected sizes: %d, %d", m.detailFiles[0].Size, m.detailFiles[2].Size)
	}

	// Sort by size descending — biggest first, ties broken by name.
	m.detailFilesSortCol = filesSortBySize
	m.detailFilesSortDesc = true
	m.sortDetailFiles()
	if m.detailFiles[0].Path != "/usr/lib/libfoo.so.1" {
		t.Fatalf("size-desc top = %q, want /usr/lib/libfoo.so.1", m.detailFiles[0].Path)
	}
	if m.detailFiles[len(m.detailFiles)-1].Path == "/usr/lib/libfoo.so.1" {
		t.Fatalf("size-desc should not put libfoo.so.1 last")
	}
}
