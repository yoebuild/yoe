package starlark

import (
	"path/filepath"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// TestMachineConfigDistroUnit: a per-distro machine (Kernel.DistroUnit set,
// Unit empty) still emits ctx.machine_config.kernel, exposing distro_unit as
// a dict so image() can resolve the kernel per effective distro.
func TestMachineConfigDistroUnit(t *testing.T) {
	m := &Machine{
		Name: "qemu-x86_64",
		Arch: "x86_64",
		Kernel: KernelConfig{
			Provides:   "linux",
			DistroUnit: map[string]string{"alpine": "linux-qemu", "debian": "linux-image-amd64"},
		},
	}
	mc := buildMachineConfigStruct(m)
	kv, err := mc.Attr("kernel")
	if err != nil {
		t.Fatalf("machine_config has no kernel attr: %v", err)
	}
	ks, ok := kv.(*starlarkstruct.Struct)
	if !ok {
		t.Fatalf("kernel attr is %T, want *starlarkstruct.Struct", kv)
	}
	duv, err := ks.Attr("distro_unit")
	if err != nil {
		t.Fatalf("kernel has no distro_unit attr: %v", err)
	}
	du, ok := duv.(*starlark.Dict)
	if !ok {
		t.Fatalf("distro_unit is %T, want *starlark.Dict", duv)
	}
	got, _, _ := du.Get(starlark.String("debian"))
	if got != starlark.String("linux-image-amd64") {
		t.Errorf("distro_unit[debian] = %v, want linux-image-amd64", got)
	}
}

// countAllUnits returns the count of distinct unit names in the
// project's catalog — deduplicated across modules so a name
// registered for multiple distros yields one count, matching the
// flat-catalog cardinality tests historically asserted against.
func countAllUnits(p *Project) int {
	seen := map[string]struct{}{}
	for name := range p.AllUnits() {
		seen[name] = struct{}{}
	}
	return len(seen)
}

func TestLoadProject(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "valid-project")
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	if proj.Name != "test-distro" {
		t.Errorf("Name = %q, want %q", proj.Name, "test-distro")
	}
	if proj.Defaults.Machine != "qemu-x86_64" {
		t.Errorf("Defaults.Machine = %q, want %q", proj.Defaults.Machine, "qemu-x86_64")
	}

	// Machines
	if len(proj.Machines) != 2 {
		t.Errorf("got %d machines, want 2", len(proj.Machines))
	}
	if m, ok := proj.Machines["beaglebone-black"]; !ok {
		t.Error("expected machine 'beaglebone-black'")
	} else if m.Arch != "arm64" {
		t.Errorf("bbb arch = %q, want %q", m.Arch, "arm64")
	}
	if m, ok := proj.Machines["qemu-x86_64"]; !ok {
		t.Error("expected machine 'qemu-x86_64'")
	} else if m.QEMU == nil {
		t.Error("expected QEMU config on qemu-x86_64")
	}

	// Units
	if countAllUnits(proj) != 7 {
		t.Errorf("got %d units, want 7", countAllUnits(proj))
	}
	if proj.AnyUnit("testlib") == nil {
		t.Error("expected unit 'testlib' from units/libs/ subdirectory")
	}
	if r := proj.AnyUnit("openssh"); r == nil {
		t.Error("expected unit 'openssh'")
	} else if r.Class != "unit" {
		t.Errorf("openssh class = %q, want %q", r.Class, "unit")
	}
	if r := proj.AnyUnit("myapp"); r == nil {
		t.Error("expected unit 'myapp'")
	} else if r.Class != "unit" {
		t.Errorf("myapp class = %q, want %q", r.Class, "unit")
	}
	if r := proj.AnyUnit("base-image"); r == nil {
		t.Error("expected unit 'base-image'")
	} else {
		if r.Class != "image" {
			t.Errorf("base-image class = %q, want %q", r.Class, "image")
		}
		if len(r.Partitions) != 2 {
			t.Errorf("base-image partitions = %d, want 2", len(r.Partitions))
		}
	}
}

func TestLoadMinimalProject(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "minimal-project")
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if proj.Name != "minimal" {
		t.Errorf("Name = %q, want %q", proj.Name, "minimal")
	}
}

func TestLoadProject_NotFound(t *testing.T) {
	_, err := LoadProject("/tmp")
	if err == nil {
		t.Fatal("expected error when no PROJECT.star found, got nil")
	}
}

func TestLoadProject_ProvidesDuplicate(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "provides-conflict")
	_, err := LoadProject(dir)
	if err == nil {
		t.Fatal("expected error for duplicate provides in same module, got nil")
	}
	if !strings.Contains(err.Error(), "virtual package") {
		t.Errorf("error = %q, want it to contain 'virtual package'", err)
	}
}

func TestLoadProject_ProvidesOverride(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "provides-override")
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	// Both units should exist
	if proj.AnyUnit("base-files") == nil {
		t.Error("expected unit 'base-files'")
	}
	if proj.AnyUnit("base-files-custom") == nil {
		t.Error("expected unit 'base-files-custom'")
	}
	// base-files-custom should have higher module index than base-files
	bf := proj.AnyUnit("base-files")
	bfc := proj.AnyUnit("base-files-custom")
	if bfc.ModuleIndex <= bf.ModuleIndex {
		t.Errorf("base-files-custom ModuleIndex=%d should be > base-files ModuleIndex=%d",
			bfc.ModuleIndex, bf.ModuleIndex)
	}
}

// Two modules define a unit with the same real name. The later-listed module
// must win; the earlier one is silently dropped from the project's unit map.
func TestLoadProject_NameShadowing(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "name-shadowing")
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	u := proj.AnyUnit("musl")
	if u == nil {
		t.Fatal("expected unit 'musl'")
	}
	if u.Version != "2.0.0-override" {
		t.Errorf("musl Version = %q, want %q (override module should win)",
			u.Version, "2.0.0-override")
	}
	if u.Module != "override" {
		t.Errorf("musl Module = %q, want %q", u.Module, "override")
	}
}

// A project-root unit must shadow same-named units from every included
// module — project priority is strictly higher than any module.
func TestLoadProject_ProjectShadowsModules(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "project-shadow")
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	u := proj.AnyUnit("musl")
	if u == nil {
		t.Fatal("expected unit 'musl'")
	}
	if u.Version != "3.0.0-project" {
		t.Errorf("musl Version = %q, want %q (project root should win)",
			u.Version, "3.0.0-project")
	}
	if u.Module != "" {
		t.Errorf("musl Module = %q, want \"\" (project root)", u.Module)
	}
}
