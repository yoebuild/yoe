package alpine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// realisticFixture is a tiny APKINDEX: musl + openssh-server, enough
// for end-to-end registration and Lookup checks.
const realisticFixture = `C:Q1wmRLywlDhwD28lS6Qlp6nGlzzIk=
P:openssh-server
V:9.9_p2-r0
A:x86_64
S:482001
I:1138688
T:OpenBSD SSH server
L:BSD-2-Clause
o:openssh
D:musl

C:Q1gccWqxnp4T7mk08WsE7/XtS4YI4=
P:musl
V:1.2.5-r10
A:x86_64
S:128000
I:300000
T:musl libc
L:MIT
o:musl
p:so:libc.musl-x86_64.so.1=1
`

// projectWithFeed builds a temp project tree:
//
//	PROJECT.star            (declares module-alpine local)
//	machines/qemu.star      (x86_64 machine)
//	modules/alpine/MODULE.star (calls alpine_feed)
//	modules/alpine/feeds/main/x86_64/APKINDEX
//
// Returns the project root. Each test gets a fresh tree under t.TempDir.
func projectWithFeed(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"PROJECT.star": `project(name = "p", version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64"),
    modules = [
        module("https://example.com/alpine.git", local = "modules/alpine"),
    ],
)`,
		"machines/qemu.star": `machine(name = "qemu-x86_64", arch = "x86_64")`,
		"modules/alpine/MODULE.star": `module_info(name = "alpine")
alpine_feed(
    name = "main",
    url = "https://dl-cdn.alpinelinux.org/alpine",
    branch = "v3.21",
    section = "main",
    index = "feeds/main",
)`,
		"modules/alpine/feeds/main/x86_64/APKINDEX": realisticFixture,
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAlpineFeed_RegistersSyntheticModule(t *testing.T) {
	dir := projectWithFeed(t)
	proj, err := yoestar.LoadProject(dir, yoestar.WithBuiltin("alpine_feed", Builtin))
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if len(proj.SyntheticModules) != 1 {
		t.Fatalf("SyntheticModules: got %d, want 1", len(proj.SyntheticModules))
	}
	sm := proj.SyntheticModules[0]
	if sm.Name != "alpine.main" {
		t.Errorf("Name: got %q, want %q", sm.Name, "alpine.main")
	}
	if sm.Parent != "alpine" {
		t.Errorf("Parent: got %q, want %q", sm.Parent, "alpine")
	}
}

func TestAlpineFeed_LookupMaterializes(t *testing.T) {
	dir := projectWithFeed(t)
	proj, err := yoestar.LoadProject(dir, yoestar.WithBuiltin("alpine_feed", Builtin))
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	sm := proj.SyntheticModules[0]

	u, err := sm.Lookup("openssh-server")
	if err != nil {
		t.Fatalf("Lookup openssh-server: %v", err)
	}
	if u == nil {
		t.Fatal("Lookup openssh-server: nil")
	}
	if u.Name != "openssh-server" || u.Version != "9.9_p2" {
		t.Errorf("openssh-server: name=%q version=%q (want openssh-server, 9.9_p2)", u.Name, u.Version)
	}
	if u.Module != "alpine.main" {
		t.Errorf("Module: got %q, want %q", u.Module, "alpine.main")
	}
	if len(u.RuntimeDeps) != 1 || u.RuntimeDeps[0] != "musl" {
		t.Errorf("RuntimeDeps: got %v, want [musl]", u.RuntimeDeps)
	}
	// Build-transport fields the executor reads to fetch + repack the
	// upstream apk. Without these the build runs but produces no
	// destdir contents, and downstream units' sysroots are empty for
	// this dep.
	wantAsset := "openssh-server-9.9_p2-r0.apk"
	if u.PassthroughAPK != wantAsset {
		t.Errorf("PassthroughAPK: got %q, want %q", u.PassthroughAPK, wantAsset)
	}
	if !strings.HasSuffix(u.Source, "/main/x86_64/"+wantAsset) {
		t.Errorf("Source: got %q, want suffix /main/x86_64/%s", u.Source, wantAsset)
	}
	if u.Container != "toolchain-musl" {
		t.Errorf("Container: got %q, want toolchain-musl", u.Container)
	}
	if u.ContainerArch != "target" {
		t.Errorf("ContainerArch: got %q, want target", u.ContainerArch)
	}
	if len(u.Tasks) != 1 || u.Tasks[0].Name != "install" {
		t.Errorf("Tasks: got %+v, want one install task", u.Tasks)
	}

	musl, err := sm.Lookup("musl")
	if err != nil {
		t.Fatalf("Lookup musl: %v", err)
	}
	if musl == nil || musl.Name != "musl" {
		t.Errorf("musl: %+v", musl)
	}

	miss, err := sm.Lookup("not-a-package")
	if err != nil {
		t.Errorf("miss: got err %v, want nil", err)
	}
	if miss != nil {
		t.Errorf("miss: got %+v, want nil", miss)
	}
}

func TestAlpineFeed_Names(t *testing.T) {
	dir := projectWithFeed(t)
	proj, err := yoestar.LoadProject(dir, yoestar.WithBuiltin("alpine_feed", Builtin))
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	sm := proj.SyntheticModules[0]
	names := sm.Names()

	want := map[string]bool{"musl": false, "openssh-server": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, ok := range want {
		if !ok {
			t.Errorf("Names missing %q (got %v)", n, names)
		}
	}
}

func TestAlpineFeed_MissingArgs(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"PROJECT.star":               `project(name = "p", version = "0.1.0", defaults = defaults(machine = "qemu"), modules = [module("https://example.com/alpine.git", local = "modules/alpine")])`,
		"machines/qemu.star":         `machine(name = "qemu", arch = "x86_64")`,
		"modules/alpine/MODULE.star": `module_info(name = "alpine")
alpine_feed(name = "main")  # missing url, branch, section, index`,
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(content), 0o644)
	}
	_, err := yoestar.LoadProject(dir, yoestar.WithBuiltin("alpine_feed", Builtin))
	if err == nil {
		t.Fatal("want error for missing args")
	}
	if !strings.Contains(err.Error(), "alpine_feed") {
		t.Errorf("err: %v, want alpine_feed in message", err)
	}
}

