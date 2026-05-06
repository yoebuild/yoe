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

func TestInstalledSize_NonImage_ReadsMeta(t *testing.T) {
	dir := t.TempDir()
	writeMeta(t, dir, 12345)

	u := &yoestar.Unit{Name: "foo", Class: "unit"}
	got := installedSize(u, dir)
	if got != 12345 {
		t.Fatalf("installedSize = %d, want 12345", got)
	}
}

func TestInstalledSize_Image_PrefersImgFile(t *testing.T) {
	dir := t.TempDir()
	destDir := filepath.Join(dir, "destdir")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("mkdir destdir: %v", err)
	}
	imgPath := filepath.Join(destDir, "myimage.img")
	if err := os.WriteFile(imgPath, make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("write img: %v", err)
	}
	// Meta with a different value to prove the .img stat wins.
	writeMeta(t, dir, 999)

	u := &yoestar.Unit{Name: "myimage", Class: "image"}
	got := installedSize(u, dir)
	if got != 1024 {
		t.Fatalf("installedSize = %d, want 1024 (.img size)", got)
	}
}

func TestInstalledSize_Unbuilt_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	u := &yoestar.Unit{Name: "foo", Class: "unit"}
	if got := installedSize(u, dir); got != 0 {
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
