package starlark

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFunction(t *testing.T) {
	// Create temp project with a class file and a unit that loads it.
	tmp := t.TempDir()

	// classes/myclass.star defines a helper function
	classesDir := filepath.Join(tmp, "classes")
	if err := os.MkdirAll(classesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(classesDir, "myclass.star"), []byte(`
def my_builder(name, version):
    unit(name = name, version = version)
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// units/hello.star loads the class and calls it
	unitsDir := filepath.Join(tmp, "units")
	if err := os.MkdirAll(unitsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitsDir, "hello.star"), []byte(`
load("//classes/myclass.star", "my_builder")
my_builder(name = "hello", version = "1.0")
`), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine()
	eng.SetProjectRoot(tmp)

	if err := eng.ExecFile(filepath.Join(unitsDir, "hello.star")); err != nil {
		t.Fatalf("ExecFile: %v", err)
	}

	r, ok := eng.Units()["hello"]
	if !ok {
		t.Fatal("unit 'hello' not registered")
	}
	if r.Class != "unit" {
		t.Errorf("Class = %q, want %q", r.Class, "unit")
	}
	if r.Version != "1.0" {
		t.Errorf("Version = %q, want %q", r.Version, "1.0")
	}
}

func TestLoadFunction_ModuleRef(t *testing.T) {
	tmp := t.TempDir()

	// Create a module directory with a helper class
	layerDir := filepath.Join(tmp, "modules", "mylib")
	classesDir := filepath.Join(layerDir, "classes")
	if err := os.MkdirAll(classesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(classesDir, "helper.star"), []byte(`
def helper(name, version):
    unit(name = name, version = version)
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a unit that loads from the module
	unitsDir := filepath.Join(tmp, "units")
	if err := os.MkdirAll(unitsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitsDir, "widget.star"), []byte(`
load("@mylib//classes/helper.star", "helper")
helper(name = "widget", version = "2.0")
`), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine()
	eng.SetProjectRoot(tmp)
	eng.SetModuleRoot("mylib", layerDir)

	if err := eng.ExecFile(filepath.Join(unitsDir, "widget.star")); err != nil {
		t.Fatalf("ExecFile: %v", err)
	}

	r, ok := eng.Units()["widget"]
	if !ok {
		t.Fatal("unit 'widget' not registered")
	}
	if r.Class != "unit" {
		t.Errorf("Class = %q, want %q", r.Class, "unit")
	}
	if r.Version != "2.0" {
		t.Errorf("Version = %q, want %q", r.Version, "2.0")
	}
}

func TestLoadFunction_Cache(t *testing.T) {
	tmp := t.TempDir()

	// A class file
	classesDir := filepath.Join(tmp, "classes")
	if err := os.MkdirAll(classesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(classesDir, "shared.star"), []byte(`
def shared_builder(name, version):
    unit(name = name, version = version)
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Two units that load the same module
	unitsDir := filepath.Join(tmp, "units")
	if err := os.MkdirAll(unitsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitsDir, "a.star"), []byte(`
load("//classes/shared.star", "shared_builder")
shared_builder(name = "pkg-a", version = "1.0")
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitsDir, "b.star"), []byte(`
load("//classes/shared.star", "shared_builder")
shared_builder(name = "pkg-b", version = "2.0")
`), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine()
	eng.SetProjectRoot(tmp)

	if err := eng.ExecFile(filepath.Join(unitsDir, "a.star")); err != nil {
		t.Fatalf("ExecFile a.star: %v", err)
	}
	if err := eng.ExecFile(filepath.Join(unitsDir, "b.star")); err != nil {
		t.Fatalf("ExecFile b.star: %v", err)
	}

	if _, ok := eng.Units()["pkg-a"]; !ok {
		t.Error("unit 'pkg-a' not registered")
	}
	if _, ok := eng.Units()["pkg-b"]; !ok {
		t.Error("unit 'pkg-b' not registered")
	}

	// Verify cache was used (same module path should have one entry)
	absPath := filepath.Join(tmp, "classes", "shared.star")
	eng.loadCache.mu.Lock()
	entry, ok := eng.loadCache.entries[absPath]
	eng.loadCache.mu.Unlock()
	if !ok {
		t.Error("expected cache entry for shared.star")
	}
	if entry == nil || entry.err != nil {
		t.Error("expected successful cache entry")
	}
}

// TestMergeTasks exercises the merge_tasks helper used by classes to allow
// units to add or replace named tasks without restating the class's defaults.
func TestMergeTasks(t *testing.T) {
	tmp := t.TempDir()

	// Drop in a copy of the merge_tasks helper.
	classesDir := filepath.Join(tmp, "classes")
	if err := os.MkdirAll(classesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	helperSrc, err := os.ReadFile("../../modules/module-core/classes/tasks.star")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(classesDir, "tasks.star"), helperSrc, 0o644); err != nil {
		t.Fatal(err)
	}

	// A class that uses merge_tasks and registers a unit named after the
	// resulting task list, so we can read tasks back through Engine.Units().
	if err := os.WriteFile(filepath.Join(classesDir, "demo.star"), []byte(`
load("//classes/tasks.star", "merge_tasks")

def demo(name, overrides):
    base = [
        task("fetch",     steps = ["echo fetch"]),
        task("build",     steps = ["echo base-build"]),
        task("install",   steps = ["echo install"]),
    ]
    unit(name = name, version = "1.0",
         tasks = merge_tasks(base, overrides))
`), 0o644); err != nil {
		t.Fatal(err)
	}

	unitsDir := filepath.Join(tmp, "units")
	if err := os.MkdirAll(unitsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitsDir, "u.star"), []byte(`
load("//classes/demo.star", "demo")

# Replace 'build', append a brand new task, and remove 'fetch'.
demo(name = "u", overrides = [
    task("build",      steps = ["echo overridden-build"]),
    task("post",       steps = ["echo post"]),
    task("fetch",      remove = True),
])
`), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine()
	eng.SetProjectRoot(tmp)
	if err := eng.ExecFile(filepath.Join(unitsDir, "u.star")); err != nil {
		t.Fatalf("ExecFile: %v", err)
	}

	u, ok := eng.Units()["u"]
	if !ok {
		t.Fatal("unit 'u' not registered")
	}

	gotNames := []string{}
	for _, tk := range u.Tasks {
		gotNames = append(gotNames, tk.Name)
	}
	wantNames := []string{"build", "install", "post"}
	if len(gotNames) != len(wantNames) {
		t.Fatalf("task names = %v, want %v", gotNames, wantNames)
	}
	for i, name := range wantNames {
		if gotNames[i] != name {
			t.Errorf("task[%d] = %q, want %q (full list: %v)", i, gotNames[i], name, gotNames)
		}
	}

	// Verify the replaced 'build' has the override's steps, not the base's.
	var buildSteps []string
	for _, tk := range u.Tasks {
		if tk.Name == "build" {
			for _, s := range tk.Steps {
				buildSteps = append(buildSteps, s.Command)
			}
		}
	}
	if len(buildSteps) != 1 || buildSteps[0] != "echo overridden-build" {
		t.Errorf("build steps = %v, want [\"echo overridden-build\"]", buildSteps)
	}
}
