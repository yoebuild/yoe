package starlark

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProject_TransitiveDeps(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "transitive-deps"))
	if err != nil {
		t.Fatal(err)
	}
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	// After the iterated sync↔peek fixpoint, the expanded module list
	// should carry the project-declared module 'a' plus its transitive
	// closure 'b' and 'c'.
	names := make([]string, 0, len(proj.ResolvedModules))
	for _, m := range proj.ResolvedModules {
		names = append(names, m.Name)
	}

	want := map[string]bool{"a": false, "b": false, "c": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, present := range want {
		if !present {
			t.Errorf("module %q missing from expanded list (got %v)", n, names)
		}
	}
}

func TestLoadProject_TransitiveCycle(t *testing.T) {
	// Build a temp project on the fly that declares a -> b -> a.
	dir := t.TempDir()
	if err := writeProjectFiles(dir, map[string]string{
		"PROJECT.star": `project(name = "cyc", version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64"),
    modules = [
        module("https://example.com/a.git", local = "modules/a"),
    ],
)`,
		"machines/qemu.star": `machine(name = "qemu-x86_64", arch = "x86_64")`,
		"modules/a/MODULE.star": `module_info(name = "a", deps = [module("https://example.com/b.git", local = "modules/b")])`,
		"modules/b/MODULE.star": `module_info(name = "b", deps = [module("https://example.com/a.git", local = "modules/a")])`,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProject(dir)
	if err == nil {
		t.Fatal("want cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("err = %v, want 'cycle' in message", err)
	}
	if !strings.Contains(err.Error(), "→") {
		t.Errorf("err = %v, want cycle path with →", err)
	}
}

func TestLoadProject_TransitiveConflict(t *testing.T) {
	// Two transitive deps both declare a module named "shared", at
	// different paths. Without a project-level pin, the loader errors.
	dir := t.TempDir()
	if err := writeProjectFiles(dir, map[string]string{
		"PROJECT.star": `project(name = "conf", version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64"),
    modules = [
        module("https://example.com/a.git", local = "modules/a"),
        module("https://example.com/b.git", local = "modules/b"),
    ],
)`,
		"machines/qemu.star": `machine(name = "qemu-x86_64", arch = "x86_64")`,
		"modules/a/MODULE.star": `module_info(name = "a", deps = [module("https://example.com/shared.git", local = "modules/shared-x")])`,
		"modules/b/MODULE.star": `module_info(name = "b", deps = [module("https://example.com/shared.git", local = "modules/shared-y")])`,
		"modules/shared-x/MODULE.star": `module_info(name = "shared")`,
		"modules/shared-y/MODULE.star": `module_info(name = "shared")`,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProject(dir)
	// Different local paths → different canonical IDs → name collision
	// with no project-level winner → error.
	if err == nil {
		t.Fatal("want conflict error")
	}
	if !strings.Contains(err.Error(), "shared") {
		t.Errorf("err = %v, want conflict named for 'shared'", err)
	}
}

func TestLoadProject_TransitiveProjectWins(t *testing.T) {
	// Project pins `shared` to a specific local; a transitive dep
	// declares `shared` at a different local. Project wins — no error.
	dir := t.TempDir()
	if err := writeProjectFiles(dir, map[string]string{
		"PROJECT.star": `project(name = "win", version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64"),
    modules = [
        module("https://example.com/shared.git", local = "modules/shared-project"),
        module("https://example.com/a.git", local = "modules/a"),
    ],
)`,
		"machines/qemu.star": `machine(name = "qemu-x86_64", arch = "x86_64")`,
		"modules/shared-project/MODULE.star": `module_info(name = "shared")`,
		"modules/a/MODULE.star": `module_info(name = "a", deps = [module("https://example.com/shared.git", local = "modules/shared-other")])`,
		"modules/shared-other/MODULE.star": `module_info(name = "shared")`,
	}); err != nil {
		t.Fatal(err)
	}
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v (project pin should win over transitive)", err)
	}
	// Verify the project's pin was kept, not the transitive one.
	for _, m := range proj.ResolvedModules {
		if m.Name == "shared" && !strings.Contains(m.Dir, "shared-project") {
			t.Errorf("shared module Dir=%s, want shared-project", m.Dir)
		}
	}
}

func TestPeekModuleInfo_TolerantOfFeedBuiltins(t *testing.T) {
	// MODULE.star that uses alpine_feed must still let peekModuleInfo
	// capture the declared module name. Starlark resolves identifiers
	// at compile time, so without a no-op stub for alpine_feed the
	// peek aborts before module_info runs and the loader falls back
	// to the basename (breaking <parent>.<feed> synthetic names).
	dir := t.TempDir()
	src := `module_info(name = "alpine", description = "test")

alpine_feed(
    name = "main",
    url = "https://example.com/alpine",
    branch = "v3.21",
    section = "main",
    index = "feeds/main",
    keys = ["keys/k.rsa.pub"],
)
`
	if err := os.WriteFile(filepath.Join(dir, "MODULE.star"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	info := peekModuleInfo(dir)
	if info == nil {
		t.Fatal("peekModuleInfo returned nil")
	}
	if info.Name != "alpine" {
		t.Errorf("Name = %q, want %q", info.Name, "alpine")
	}
	if info.Description != "test" {
		t.Errorf("Description = %q", info.Description)
	}
}

func TestPeekModuleInfo_TolerantOfDebianFeed(t *testing.T) {
	// Same contract as the alpine_feed peek test: without a no-op stub
	// for apt_feed, Starlark's compile-time resolver aborts the peek
	// before module_info runs. The loader then falls back to the
	// directory basename ("module-debian") and the synthetic feeds end
	// up with Parent = "module-debian", surfacing the wrong distro name
	// in the TUI's Default Distro picker.
	dir := t.TempDir()
	src := `module_info(name = "debian", description = "test")

apt_feed(
    name = "main",
    url = "https://deb.debian.org/debian",
    suite = "bookworm",
    component = "main",
    arches = ["amd64"],
    index = "feeds/main",
    keyring = "keys/k.gpg",
)
`
	if err := os.WriteFile(filepath.Join(dir, "MODULE.star"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	info := peekModuleInfo(dir)
	if info == nil {
		t.Fatal("peekModuleInfo returned nil")
	}
	if info.Name != "debian" {
		t.Errorf("Name = %q, want %q", info.Name, "debian")
	}
	if info.Description != "test" {
		t.Errorf("Description = %q", info.Description)
	}
}

// writeProjectFiles materializes a {relpath: content} map under root.
func writeProjectFiles(root string, files map[string]string) error {
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
