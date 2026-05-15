package build

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoebuild/yoe/internal/resolve"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestDryRun(t *testing.T) {
	proj := &yoestar.Project{
		Name: "test",
		Units: map[string]*yoestar.Unit{
			"zlib":    {Name: "zlib", Version: "1.3", Class: "unit", Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "make"}}}}},
			"openssh": {Name: "openssh", Version: "9.6", Class: "unit", Deps: []string{"zlib"}, Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "make"}}}}},
		},
	}

	var buf bytes.Buffer
	opts := Options{
		DryRun:     true,
		ProjectDir: t.TempDir(),
		Arch:       "arm64",
	}

	if err := BuildUnits(proj, nil, opts, &buf); err != nil {
		t.Fatalf("BuildUnits dry run: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "zlib") {
		t.Error("dry run should list zlib")
	}
	if !strings.Contains(output, "openssh") {
		t.Error("dry run should list openssh")
	}
}

func TestCacheMarker(t *testing.T) {
	dir := t.TempDir()
	name := "test-unit"
	hash := "abc123def456"

	arch := "x86_64"

	// Not cached initially
	if IsBuildCached(dir, arch, name, hash) {
		t.Error("should not be cached initially")
	}

	// Write marker
	writeCacheMarker(dir, arch, name, hash)

	// Now cached
	if !IsBuildCached(dir, arch, name, hash) {
		t.Error("should be cached after writing marker")
	}

	// Different hash not cached
	if IsBuildCached(dir, arch, name, "different") {
		t.Error("different hash should not be cached")
	}
}

func TestFilterBuildOrder(t *testing.T) {
	proj := &yoestar.Project{
		Units: map[string]*yoestar.Unit{
			"a": {Name: "a"},
			"b": {Name: "b", Deps: []string{"a"}},
			"c": {Name: "c", Deps: []string{"b"}},
			"d": {Name: "d"},
		},
	}

	dag, _ := resolve.BuildDAG(proj)
	order, _ := dag.TopologicalSort()

	filtered, err := filterBuildOrder(dag, order, []string{"c"})
	if err != nil {
		t.Fatalf("filterBuildOrder: %v", err)
	}

	// c depends on b depends on a — should include all three but not d
	if len(filtered) != 3 {
		t.Errorf("got %d units, want 3 (a, b, c)", len(filtered))
	}

	has := make(map[string]bool)
	for _, n := range filtered {
		has[n] = true
	}
	if !has["a"] || !has["b"] || !has["c"] {
		t.Errorf("filtered = %v, should contain a, b, c", filtered)
	}
	if has["d"] {
		t.Error("filtered should not contain d")
	}
}

func TestBuildUnits_WithDeps(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("requires --privileged container with user namespace support")
	}
	runtime := "docker"
	if _, err := exec.LookPath("docker"); err != nil {
		if _, err := exec.LookPath("podman"); err != nil {
			t.Skip("docker/podman not available")
		}
		runtime = "podman"
	}

	// Check if the toolchain container image exists
	containerImage := "yoe/toolchain-musl:15-x86_64"
	if err := exec.Command(runtime, "image", "inspect", containerImage).Run(); err != nil {
		t.Skipf("container image %s not available", containerImage)
	}

	// Create a project with units that have trivial build steps
	projectDir := t.TempDir()

	proj := &yoestar.Project{
		Name: "test",
		Units: map[string]*yoestar.Unit{
			"hello": {
				Name:          "hello",
				Version:       "1.0",
				Class:         "package",
				Container:     containerImage,
				ContainerArch: "target",
				Tasks:         []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "echo built > built.txt"}}}},
			},
		},
	}

	// Create source directory with a file (simulating prepared source)
	srcDir := filepath.Join(projectDir, "build", "hello.x86_64", "src")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "Makefile"), []byte("all:\n\techo hello\n"), 0644)

	// Init git so Prepare doesn't try to fetch
	run(t, srcDir, "git", "init")
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	run(t, srcDir, "git", "config", "user.name", "Test")
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-m", "upstream")
	run(t, srcDir, "git", "tag", "yoe/pin")
	// Add a local commit so Prepare treats it as dev mode
	os.WriteFile(filepath.Join(srcDir, "local.txt"), []byte("local\n"), 0644)
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-m", "local")

	var buf bytes.Buffer
	opts := Options{
		ProjectDir: projectDir,
		Arch:       "x86_64",
	}

	if err := BuildUnits(proj, []string{"hello"}, opts, &buf); err != nil {
		t.Fatalf("BuildUnits: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "hello") {
		t.Errorf("output should mention hello: %s", output)
	}
	if !strings.Contains(output, "done") {
		t.Errorf("output should mention done: %s", output)
	}

	// Verify cache marker was written
	if !IsBuildCached(projectDir, "x86_64", "hello", "") {
		// The hash won't be "" — just verify the marker file exists
		markerDir := filepath.Join(projectDir, "build", "hello.x86_64")
		entries, _ := os.ReadDir(markerDir)
		found := false
		for _, e := range entries {
			if e.Name() == ".yoe-hash" {
				found = true
			}
		}
		if !found {
			t.Error("cache marker not written")
		}
	}
}

// TestBuildUnits_ParallelRespectsDAG builds a small graph with the
// scheduler set to a low concurrency cap and asserts that every
// dependency reaches [done] before its dependent reaches [building].
// The syncWriter serializes those lines, so their relative order in the
// captured output is a faithful witness of the scheduling decision.
func TestBuildUnits_ParallelRespectsDAG(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("requires --privileged container with user namespace support")
	}
	runtime := "docker"
	if _, err := exec.LookPath("docker"); err != nil {
		if _, err := exec.LookPath("podman"); err != nil {
			t.Skip("docker/podman not available")
		}
		runtime = "podman"
	}
	containerImage := "yoe/toolchain-musl:15-x86_64"
	if err := exec.Command(runtime, "image", "inspect", containerImage).Run(); err != nil {
		t.Skipf("container image %s not available", containerImage)
	}

	projectDir := t.TempDir()
	// leaf <- midA, leaf <- midB, top <- midA, top <- midB.
	names := []string{"leaf", "mida", "midb", "top"}
	deps := map[string][]string{
		"mida": {"leaf"},
		"midb": {"leaf"},
		"top":  {"mida", "midb"},
	}
	units := map[string]*yoestar.Unit{}
	for _, n := range names {
		units[n] = &yoestar.Unit{
			Name:          n,
			Version:       "1.0",
			Class:         "package",
			Container:     containerImage,
			ContainerArch: "target",
			Deps:          deps[n],
			Tasks:         []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "echo built > built.txt"}}}},
		}
		srcDir := filepath.Join(projectDir, "build", n+".x86_64", "src")
		os.MkdirAll(srcDir, 0755)
		os.WriteFile(filepath.Join(srcDir, "Makefile"), []byte("all:\n\techo "+n+"\n"), 0644)
		run(t, srcDir, "git", "init")
		run(t, srcDir, "git", "config", "user.email", "test@test.com")
		run(t, srcDir, "git", "config", "user.name", "Test")
		run(t, srcDir, "git", "add", "-A")
		run(t, srcDir, "git", "commit", "-m", "upstream")
		run(t, srcDir, "git", "tag", "yoe/pin")
	}
	proj := &yoestar.Project{Name: "test", Units: units}

	var buf bytes.Buffer
	opts := Options{ProjectDir: projectDir, Arch: "x86_64", Parallel: 3}
	if err := BuildUnits(proj, nil, opts, &buf); err != nil {
		t.Fatalf("BuildUnits: %v", err)
	}

	out := buf.String()
	building := func(n string) int {
		// first "<name> ... [building]" occurrence
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, n+" ") && strings.Contains(line, "[building]") {
				return strings.Index(out, line)
			}
		}
		return -1
	}
	doneAt := func(n string) int {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, n+" ") && strings.Contains(line, "[done]") {
				return strings.Index(out, line)
			}
		}
		return -1
	}
	for dependent, ds := range deps {
		db := building(dependent)
		if db < 0 {
			t.Fatalf("%s never built; output:\n%s", dependent, out)
		}
		for _, d := range ds {
			dd := doneAt(d)
			if dd < 0 {
				t.Fatalf("dependency %s never completed; output:\n%s", d, out)
			}
			if dd > db {
				t.Errorf("%s started building before its dependency %s finished:\n%s", dependent, d, out)
			}
		}
	}
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// TestFinalizeSourceState verifies the helper projects the toggle
// decision into BuildMeta.SourceState. The build itself doesn't
// re-detect — it just persists whichever toggle decision was already
// in effect, so untracked build artifacts (configure / make output)
// can't flip a pin unit to dev.
func TestFinalizeSourceState(t *testing.T) {
	t.Run("missing src dir → empty", func(t *testing.T) {
		got := finalizeSourceState(filepath.Join(t.TempDir(), "no-src"), "pin")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("cached pin → pin", func(t *testing.T) {
		got := finalizeSourceState(setupPinClone(t), "pin")
		if got != "pin" {
			t.Errorf("got %q, want pin", got)
		}
	})

	t.Run("cached pin + dirty work tree still pin", func(t *testing.T) {
		// Simulates a build that left untracked artifacts in the src
		// dir — DetectState would call this dev-dirty, but the toggle
		// decision is pin so finalize must persist pin.
		srcDir := setupPinClone(t)
		os.WriteFile(filepath.Join(srcDir, "build-output.o"), []byte("\x00"), 0o644)
		got := finalizeSourceState(srcDir, "pin")
		if got != "pin" {
			t.Errorf("untracked artifacts must not flip pin to dev, got %q", got)
		}
	})

	t.Run("cached dev → dev", func(t *testing.T) {
		got := finalizeSourceState(setupPinClone(t), "dev")
		if got != "dev" {
			t.Errorf("got %q, want dev", got)
		}
	})

	t.Run("cached dev-mod collapses to dev", func(t *testing.T) {
		got := finalizeSourceState(setupPinClone(t), "dev-mod")
		if got != "dev" {
			t.Errorf("dev-mod should collapse to dev, got %q", got)
		}
	})

	t.Run("cached dev-dirty collapses to dev", func(t *testing.T) {
		got := finalizeSourceState(setupPinClone(t), "dev-dirty")
		if got != "dev" {
			t.Errorf("dev-dirty should collapse to dev, got %q", got)
		}
	})

	t.Run("cached empty + valid src dir → empty (defer to caller default)", func(t *testing.T) {
		// Caller (executor) defaults empty to pin before calling.
		// finalizeSourceState itself returns empty for empty cached.
		got := finalizeSourceState(setupPinClone(t), "")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// setupPinClone makes a pin-state src dir: a git repo with one commit
// tagged `upstream`, no origin remote, clean work tree.
func setupPinClone(t *testing.T) string {
	t.Helper()
	srcDir := t.TempDir()
	run(t, srcDir, "git", "init", "-q", "-b", "main")
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	run(t, srcDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(srcDir, "main.c"), []byte("int main() {}\n"), 0o644)
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-q", "-m", "upstream commit")
	run(t, srcDir, "git", "tag", "yoe/pin")
	return srcDir
}
