package module

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// run runs cmd in dir and fails the test on non-zero exit.
func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// setupModuleClone creates a stub upstream repo and a clone of it at
// moduleDir. Returns the upstream URL string the clone uses for origin.
func setupModuleClone(t *testing.T, parent, name string, shallow bool) (moduleDir, upstreamURL string) {
	t.Helper()
	upstream := filepath.Join(parent, "_upstream", name+".git")
	if err := os.MkdirAll(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, upstream, "git", "init", "-q", "-b", "main")
	run(t, upstream, "git", "config", "user.email", "test@test.com")
	run(t, upstream, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(upstream, "MODULE.star"), []byte(`module_info(name = "`+name+`")`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, upstream, "git", "add", "-A")
	run(t, upstream, "git", "commit", "-q", "-m", "initial")

	moduleDir = filepath.Join(parent, "modules", name)
	if err := os.MkdirAll(filepath.Dir(moduleDir), 0o755); err != nil {
		t.Fatal(err)
	}
	upstreamURL = "file://" + upstream
	args := []string{"clone", "-q", upstreamURL, moduleDir}
	if shallow {
		args = []string{"clone", "-q", "--depth=1", upstreamURL, moduleDir}
	}
	run(t, parent, "git", args...)
	run(t, moduleDir, "git", "config", "user.email", "test@test.com")
	run(t, moduleDir, "git", "config", "user.name", "Test")
	return moduleDir, upstreamURL
}

func TestModuleToUpstream_UnshallowsShallow(t *testing.T) {
	dir := t.TempDir()
	moduleDir, _ := setupModuleClone(t, dir, "foo", true)
	m := yoestar.ResolvedModule{Name: "foo", Dir: moduleDir, Ref: "main"}

	if err := ModuleToUpstream(m, ModuleUpstreamOpts{}); err != nil {
		t.Fatalf("ModuleToUpstream: %v", err)
	}
	got, err := gitOut(moduleDir, "rev-parse", "--is-shallow-repository")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != "false" {
		t.Errorf("clone should be unshallowed, got %q", got)
	}
	if state := ReadState(moduleDir); state != source.StateDev {
		t.Errorf("state = %q, want %q", state, source.StateDev)
	}
}

func TestModuleToUpstream_AlreadyFullHistory(t *testing.T) {
	dir := t.TempDir()
	moduleDir, _ := setupModuleClone(t, dir, "foo", false)
	m := yoestar.ResolvedModule{Name: "foo", Dir: moduleDir, Ref: "main"}

	// Should be a no-op for the unshallow step.
	if err := ModuleToUpstream(m, ModuleUpstreamOpts{}); err != nil {
		t.Fatalf("ModuleToUpstream: %v", err)
	}
	if state := ReadState(moduleDir); state != source.StateDev {
		t.Errorf("state = %q, want %q", state, source.StateDev)
	}
}

func TestModuleToUpstream_LocalRefuses(t *testing.T) {
	m := yoestar.ResolvedModule{Name: "foo", Local: "../path", Dir: ""}
	err := ModuleToUpstream(m, ModuleUpstreamOpts{})
	if err == nil {
		t.Fatal("expected error for local module")
	}
	if !strings.Contains(err.Error(), "locally overridden") {
		t.Errorf("error should mention local override: %v", err)
	}
}

func TestModuleToUpstream_NotSyncedRefuses(t *testing.T) {
	m := yoestar.ResolvedModule{Name: "foo", Dir: ""}
	err := ModuleToUpstream(m, ModuleUpstreamOpts{})
	if err == nil {
		t.Fatal("expected error for unsynced module")
	}
}

func TestModuleToPin_ResetsToRef(t *testing.T) {
	dir := t.TempDir()
	moduleDir, _ := setupModuleClone(t, dir, "foo", false)
	m := yoestar.ResolvedModule{Name: "foo", Dir: moduleDir, Ref: "main"}

	// Toggle to dev so we have something to reset.
	if err := ModuleToUpstream(m, ModuleUpstreamOpts{}); err != nil {
		t.Fatal(err)
	}
	// State should now be dev.
	if state := ReadState(moduleDir); state != source.StateDev {
		t.Fatalf("pre-pin state = %q, want %q", state, source.StateDev)
	}

	if err := ModuleToPin(m, false); err != nil {
		t.Fatalf("ModuleToPin: %v", err)
	}
	// State file should be cleared (back to pin).
	if state := ReadState(moduleDir); state != source.StateEmpty {
		t.Errorf("post-pin state = %q, want empty", state)
	}
}

func TestModuleToPin_RefusesDevModWithoutForce(t *testing.T) {
	dir := t.TempDir()
	moduleDir, _ := setupModuleClone(t, dir, "foo", false)
	m := yoestar.ResolvedModule{Name: "foo", Dir: moduleDir, Ref: "main"}

	if err := ModuleToUpstream(m, ModuleUpstreamOpts{}); err != nil {
		t.Fatal(err)
	}
	// Add a local commit beyond declared ref.
	if err := os.WriteFile(filepath.Join(moduleDir, "extra.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, moduleDir, "git", "add", "-A")
	run(t, moduleDir, "git", "commit", "-q", "-m", "local")

	err := ModuleToPin(m, false)
	if err == nil {
		t.Fatal("expected error refusing dev-mod without force")
	}
	if !strings.Contains(err.Error(), "force=true") {
		t.Errorf("error should mention force option: %v", err)
	}
}

func TestModuleToPin_LocalRefuses(t *testing.T) {
	m := yoestar.ResolvedModule{Name: "foo", Local: "../path"}
	err := ModuleToPin(m, false)
	if err == nil {
		t.Fatal("expected error for local module")
	}
}

func TestHTTPSToSSH(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"https://github.com/foo/bar.git", "git@github.com:foo/bar.git", true},
		{"https://gitlab.com/foo/bar.git", "git@gitlab.com:foo/bar.git", true},
		{"git@github.com:foo/bar.git", "git@github.com:foo/bar.git", false},
	}
	for _, c := range cases {
		got, ok := httpsToSSH(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("httpsToSSH(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}
