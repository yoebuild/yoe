package internal

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestDevExtract(t *testing.T) {
	// Create a temp project with a unit
	dir := t.TempDir()
	setupDevTestProject(t, dir)

	// Create a fake build/openssh/src git repo simulating a fetched source
	srcDir := filepath.Join(dir, "build", "x86_64", "openssh", "src")
	os.MkdirAll(srcDir, 0755)

	// Init git repo with upstream content
	run(t, srcDir, "git", "init")
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	run(t, srcDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(srcDir, "main.c"), []byte("int main() { return 0; }\n"), 0644)
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-m", "upstream source")
	run(t, srcDir, "git", "tag", "upstream")

	// Make a local change (simulating developer edits)
	os.WriteFile(filepath.Join(srcDir, "main.c"), []byte("int main() { return 42; }\n"), 0644)
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-m", "fix return value")

	// Extract patches
	var buf bytes.Buffer
	if err := DevExtract(dir, "x86_64", "openssh", &buf); err != nil {
		t.Fatalf("DevExtract: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "1 patch") {
		t.Errorf("output should mention 1 patch, got: %s", output)
	}

	// Verify patch file was created next to the unit's .star file
	// (units/openssh.star → units/openssh/*.patch).
	patches, _ := filepath.Glob(filepath.Join(dir, "units", "openssh", "*.patch"))
	if len(patches) != 1 {
		t.Errorf("expected 1 patch file under units/openssh/, got %d", len(patches))
	}

	// Verify patch content
	if len(patches) > 0 {
		content, _ := os.ReadFile(patches[0])
		if !strings.Contains(string(content), "return 42") {
			t.Error("patch should contain the change")
		}
	}
}

func TestDevExtract_NoCommits(t *testing.T) {
	dir := t.TempDir()
	setupDevTestProject(t, dir)

	srcDir := filepath.Join(dir, "build", "x86_64", "openssh", "src")
	os.MkdirAll(srcDir, 0755)

	run(t, srcDir, "git", "init")
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	run(t, srcDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(srcDir, "main.c"), []byte("int main() {}\n"), 0644)
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-m", "upstream")
	run(t, srcDir, "git", "tag", "upstream")

	var buf bytes.Buffer
	if err := DevExtract(dir, "x86_64", "openssh", &buf); err != nil {
		t.Fatalf("DevExtract: %v", err)
	}

	if !strings.Contains(buf.String(), "No local commits") {
		t.Errorf("should report no local commits, got: %s", buf.String())
	}
}

func TestDevDiff(t *testing.T) {
	dir := t.TempDir()
	setupDevTestProject(t, dir)

	srcDir := filepath.Join(dir, "build", "x86_64", "openssh", "src")
	os.MkdirAll(srcDir, 0755)

	run(t, srcDir, "git", "init")
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	run(t, srcDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(srcDir, "main.c"), []byte("int main() {}\n"), 0644)
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-m", "upstream")
	run(t, srcDir, "git", "tag", "upstream")

	os.WriteFile(filepath.Join(srcDir, "main.c"), []byte("int main() { return 1; }\n"), 0644)
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-m", "my change")

	var buf bytes.Buffer
	if err := DevDiff(dir, "x86_64", "openssh", &buf); err != nil {
		t.Fatalf("DevDiff: %v", err)
	}

	if !strings.Contains(buf.String(), "my change") {
		t.Errorf("should show commit message, got: %s", buf.String())
	}
}

func TestDevStatus(t *testing.T) {
	dir := t.TempDir()
	setupDevTestProject(t, dir)

	// openssh: has local commits
	srcDir := filepath.Join(dir, "build", "x86_64", "openssh", "src")
	os.MkdirAll(srcDir, 0755)
	run(t, srcDir, "git", "init")
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	run(t, srcDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(srcDir, "main.c"), []byte("orig\n"), 0644)
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-m", "upstream")
	run(t, srcDir, "git", "tag", "upstream")
	os.WriteFile(filepath.Join(srcDir, "main.c"), []byte("changed\n"), 0644)
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-m", "local fix")

	var buf bytes.Buffer
	if err := DevStatus(dir, "x86_64", &buf); err != nil {
		t.Fatalf("DevStatus: %v", err)
	}

	if !strings.Contains(buf.String(), "openssh") {
		t.Errorf("should list openssh as modified, got: %s", buf.String())
	}
}

func setupDevTestProject(t *testing.T, dir string) {
	t.Helper()
	// Create a minimal project with an openssh unit
	os.MkdirAll(filepath.Join(dir, "units"), 0755)
	os.MkdirAll(filepath.Join(dir, "machines"), 0755)

	os.WriteFile(filepath.Join(dir, "PROJECT.star"), []byte(
		`project(name = "test", version = "0.1.0")`+"\n",
	), 0644)

	os.WriteFile(filepath.Join(dir, "units", "openssh.star"), []byte(
		`unit(name = "openssh", version = "9.6p1", source = "https://example.com/openssh.tar.gz", build = ["make"])`+"\n",
	), 0644)
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// --- U3: DevToUpstream / DevToPin --------------------------------------
//
// Tests cover the toggle library functions in isolation. Use a local
// file:// URL as the "upstream" so `git fetch` works without network.

func TestHTTPSToSSH(t *testing.T) {
	cases := []struct {
		in         string
		want       string
		wantOK     bool
		wantSSHFmt bool
	}{
		{"https://github.com/foo/bar.git", "git@github.com:foo/bar.git", true, true},
		{"https://gitlab.com/foo/bar.git", "git@gitlab.com:foo/bar.git", true, true},
		{"https://example.com/path/to/repo.git", "git@example.com:path/to/repo.git", true, true},
		{"https://foo.example.com/x.git", "git@foo.example.com:x.git", true, true},
		{"git@github.com:foo/bar.git", "git@github.com:foo/bar.git", false, true},        // already SSH, no rewrite
		{"git://git.kernel.org/linux.git", "git://git.kernel.org/linux.git", false, false}, // git:// not handled
	}
	for _, c := range cases {
		got, ok := httpsToSSH(c.in)
		if got != c.want {
			t.Errorf("httpsToSSH(%q) = %q, want %q", c.in, got, c.want)
		}
		if ok != c.wantOK {
			t.Errorf("httpsToSSH(%q) ok=%v, want %v", c.in, ok, c.wantOK)
		}
	}
}

// setupPinnedSrc creates a stub upstream git repo plus a src/ checkout
// that's been clone'd from it shallow-style: working tree at the
// upstream commit, `upstream` tag, no `origin` remote configured. This
// matches what source.Prepare leaves behind and is what pin state means
// at the git-state level.
func setupPinnedSrc(t *testing.T, projectDir, unitName string) (srcDir, upstreamURL string) {
	t.Helper()
	// Upstream git repo (the "remote" we'll fetch from). Suffix `.git`
	// so the test URL passes devIsGitURL.
	upstream := filepath.Join(projectDir, "_upstream", unitName+".git")
	if err := os.MkdirAll(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, upstream, "git", "init", "-q", "-b", "main")
	run(t, upstream, "git", "config", "user.email", "test@test.com")
	run(t, upstream, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(upstream, "main.c"), []byte("int main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, upstream, "git", "add", "-A")
	run(t, upstream, "git", "commit", "-q", "-m", "upstream commit")

	// Yoe-style pinned src: clone, tag upstream, drop origin.
	srcDir = filepath.Join(projectDir, "build", unitName+".x86_64", "src")
	if err := os.MkdirAll(filepath.Dir(srcDir), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, projectDir, "git", "clone", "-q", "--depth=1", "file://"+upstream, srcDir)
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	run(t, srcDir, "git", "config", "user.name", "Test")
	run(t, srcDir, "git", "tag", "upstream")
	run(t, srcDir, "git", "remote", "remove", "origin")

	return srcDir, "file://" + upstream
}

func TestDevToUpstream_PinToDev(t *testing.T) {
	dir := t.TempDir()
	srcDir, upstreamURL := setupPinnedSrc(t, dir, "openssh")

	unit := &yoestar.Unit{Name: "openssh", Source: upstreamURL}
	if err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{}); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}
	// Origin set?
	out, err := gitCmd(srcDir, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("get-url after DevToUpstream: %v", err)
	}
	if got := strings.TrimSpace(out); got != upstreamURL {
		t.Errorf("origin = %q, want %q", got, upstreamURL)
	}
	// State persisted?
	if state := readUnitState(t, dir, "openssh"); state != "dev" {
		t.Errorf("source_state = %q, want dev", state)
	}
}

func TestDevToUpstream_NonGitSource(t *testing.T) {
	dir := t.TempDir()
	setupPinnedSrc(t, dir, "openssh")

	unit := &yoestar.Unit{Name: "openssh", Source: "https://example.com/openssh.tar.gz"}
	err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{})
	if err == nil {
		t.Fatal("expected DevToUpstream to refuse a non-git source")
	}
	if !strings.Contains(err.Error(), "non-git") {
		t.Errorf("error %v should mention non-git", err)
	}
}

func TestDevToUpstream_Idempotent(t *testing.T) {
	dir := t.TempDir()
	srcDir, upstreamURL := setupPinnedSrc(t, dir, "openssh")
	// Pre-existing origin pointing somewhere else.
	run(t, srcDir, "git", "remote", "add", "origin", "https://stale.example.com/x.git")

	unit := &yoestar.Unit{Name: "openssh", Source: upstreamURL}
	if err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{}); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}
	out, _ := gitCmd(srcDir, "remote", "get-url", "origin")
	if got := strings.TrimSpace(out); got != upstreamURL {
		t.Errorf("stale origin not replaced: got %q, want %q", got, upstreamURL)
	}
}

func TestDevToPin_CleanDev(t *testing.T) {
	dir := t.TempDir()
	srcDir, upstreamURL := setupPinnedSrc(t, dir, "openssh")
	unit := &yoestar.Unit{Name: "openssh", Source: upstreamURL, Tag: "upstream"}
	if err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{}); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}
	if err := DevToPin(dir, "x86_64", unit, false); err != nil {
		t.Fatalf("DevToPin: %v", err)
	}
	// Src dir should exist and be a fresh pin clone (re-cloned by Prepare).
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		t.Errorf("src dir should be re-cloned after DevToPin, got err=%v", err)
	}
	gotState, _ := source.DetectState(srcDir, source.StatePin)
	if gotState != source.StatePin {
		t.Errorf("DetectState after DevToPin = %q, want pin", gotState)
	}
	if state := readUnitState(t, dir, "openssh"); state != "pin" {
		t.Errorf("persisted source_state = %q, want pin", state)
	}
}

func TestDevToPin_RefusesDevModWithoutForce(t *testing.T) {
	dir := t.TempDir()
	srcDir, upstreamURL := setupPinnedSrc(t, dir, "openssh")
	unit := &yoestar.Unit{Name: "openssh", Source: upstreamURL}
	if err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{}); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}
	// Add a local commit beyond upstream.
	if err := os.WriteFile(filepath.Join(srcDir, "extra.c"), []byte("// extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-q", "-m", "local")

	err := DevToPin(dir, "x86_64", unit, false)
	if err == nil {
		t.Fatal("expected DevToPin to refuse dev-mod without force=true")
	}
	if _, statErr := os.Stat(srcDir); statErr != nil {
		t.Errorf("src dir should still exist after refusal, got err=%v", statErr)
	}
}

func TestDevToPin_ForceDiscardsDevMod(t *testing.T) {
	dir := t.TempDir()
	srcDir, upstreamURL := setupPinnedSrc(t, dir, "openssh")
	unit := &yoestar.Unit{Name: "openssh", Source: upstreamURL, Tag: "upstream"}
	if err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{}); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "extra.c"), []byte("// extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-q", "-m", "local")

	if err := DevToPin(dir, "x86_64", unit, true); err != nil {
		t.Fatalf("DevToPin force=true: %v", err)
	}
	// Src dir re-clones at the pinned ref; the local commit is gone.
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		t.Errorf("src dir should be re-cloned after force pin, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(srcDir, "extra.c")); !os.IsNotExist(err) {
		t.Errorf("local commit's file should be gone after force pin, got err=%v", err)
	}
}

// readUnitState reads the persisted SourceState directly so the test
// doesn't depend on import paths from the build package.
func readUnitState(t *testing.T, projectDir, unitName string) string {
	t.Helper()
	path := filepath.Join(projectDir, "build", unitName+".x86_64", "build.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	type metaShape struct {
		SourceState string `json:"source_state"`
	}
	var m metaShape
	_ = jsonUnmarshalTest(data, &m)
	return m.SourceState
}

func jsonUnmarshalTest(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// --- U5: DevPromoteToPin --------------------------------------------------

// setupDevModUnit produces the same shape as setupPinnedSrc → DevToUpstream
// → add a local commit, so the unit ends up in StateDevMod with a .star
// file the rewriter can target. Returns the .star file path so tests can
// read it back after the rewrite.
func setupDevModUnit(t *testing.T, dir, unitName, starBody string) (srcDir, starPath string, unit *yoestar.Unit) {
	t.Helper()
	srcDir, upstreamURL := setupPinnedSrc(t, dir, unitName)

	// Write the .star file in a definition dir alongside the upstream.
	defDir := filepath.Join(dir, "_units")
	if err := os.MkdirAll(defDir, 0o755); err != nil {
		t.Fatal(err)
	}
	starPath = filepath.Join(defDir, unitName+".star")
	if err := os.WriteFile(starPath, []byte(starBody), 0o644); err != nil {
		t.Fatal(err)
	}

	unit = &yoestar.Unit{Name: unitName, Source: upstreamURL, DefinedIn: defDir}
	if err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{}); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}
	// Add a local commit beyond upstream → state is dev-mod.
	if err := os.WriteFile(filepath.Join(srcDir, "patch.c"), []byte("// patch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, srcDir, "git", "add", "-A")
	run(t, srcDir, "git", "commit", "-q", "-m", "local fix")
	return
}

func TestDevPromoteToPin_HEADWithoutTag_WritesSHA(t *testing.T) {
	dir := t.TempDir()
	starBody := `unit(
    name = "openssh",
    version = "9.6p1",
    tag = "v9.6p1",
    source = "https://example.com/openssh.git",
)
`
	srcDir, starPath, unit := setupDevModUnit(t, dir, "openssh", starBody)
	if err := DevPromoteToPin(dir, "x86_64", unit); err != nil {
		t.Fatalf("DevPromoteToPin: %v", err)
	}
	headSha, _ := gitCmd(srcDir, "rev-parse", "HEAD")
	wantSha := strings.TrimSpace(headSha)
	got, _ := os.ReadFile(starPath)
	if !strings.Contains(string(got), `tag = "`+wantSha+`"`) {
		t.Errorf(".star tag should be HEAD sha %q, got:\n%s", wantSha, got)
	}
	// State should be pin (working tree now matches the new pin in .star).
	state, _ := source.DetectState(srcDir, source.StatePin)
	if state != source.StatePin {
		t.Errorf("post-pin state = %q, want %q", state, source.StatePin)
	}
}

func TestDevPromoteToPin_AlwaysWritesSHA(t *testing.T) {
	dir := t.TempDir()
	starBody := `unit(
    name = "foo",
    tag = "v1.0",
    source = "https://example.com/foo.git",
)
`
	srcDir, starPath, unit := setupDevModUnit(t, dir, "foo", starBody)
	// Even when HEAD has a tag pointing at it, P writes the SHA.
	// Tag names can be rebased/deleted/force-pushed upstream; the SHA
	// is unambiguous and reproducible.
	run(t, srcDir, "git", "tag", "v1.1.0")
	headSha, _ := gitCmd(srcDir, "rev-parse", "HEAD")
	wantSha := strings.TrimSpace(headSha)

	if err := DevPromoteToPin(dir, "x86_64", unit); err != nil {
		t.Fatalf("DevPromoteToPin: %v", err)
	}
	got, _ := os.ReadFile(starPath)
	if !strings.Contains(string(got), `tag = "`+wantSha+`"`) {
		t.Errorf(".star tag should be HEAD sha %q, got:\n%s", wantSha, got)
	}
	if strings.Contains(string(got), `tag = "v1.1.0"`) {
		t.Errorf(".star should not contain the local tag name v1.1.0:\n%s", got)
	}
}

func TestDevPromoteToPin_PreservesBranchField(t *testing.T) {
	dir := t.TempDir()
	starBody := `unit(
    name = "foo",
    tag = "v1.0",
    branch = "main",
    source = "https://example.com/foo.git",
)
`
	srcDir, starPath, unit := setupDevModUnit(t, dir, "foo", starBody)
	headSha, _ := gitCmd(srcDir, "rev-parse", "HEAD")
	wantSha := strings.TrimSpace(headSha)

	if err := DevPromoteToPin(dir, "x86_64", unit); err != nil {
		t.Fatalf("DevPromoteToPin: %v", err)
	}
	got, _ := os.ReadFile(starPath)
	if !strings.Contains(string(got), `tag = "`+wantSha+`"`) {
		t.Errorf(".star tag should be HEAD sha %q, got:\n%s", wantSha, got)
	}
	// branch line must be left untouched — pin command never writes branch.
	if !strings.Contains(string(got), `branch = "main"`) {
		t.Errorf("branch field should be preserved, got:\n%s", got)
	}
}

func TestDevPromoteToPin_RefusesNonDev(t *testing.T) {
	dir := t.TempDir()
	srcDir, upstreamURL := setupPinnedSrc(t, dir, "foo")
	defDir := filepath.Join(dir, "_units")
	os.MkdirAll(defDir, 0o755)
	os.WriteFile(filepath.Join(defDir, "foo.star"), []byte(`unit(name = "foo", tag = "v1", source = "https://example.com/foo.git")`), 0o644)
	unit := &yoestar.Unit{Name: "foo", Source: upstreamURL, DefinedIn: defDir}
	// Stay in pin state — DevToUpstream not called.

	err := DevPromoteToPin(dir, "x86_64", unit)
	if err == nil {
		t.Fatal("expected error pinning from pin state")
	}
	if !strings.Contains(err.Error(), "dev") {
		t.Errorf("error should mention dev requirement: %v", err)
	}
	// Also test from dev-dirty (uncommitted edits).
	if err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{}); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "dirty.c"), []byte("// dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = DevPromoteToPin(dir, "x86_64", unit)
	if err == nil {
		t.Fatal("expected error pinning from dev-dirty")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("dev-dirty error should mention uncommitted edits: %v", err)
	}
}

func TestFindUnitStarFile_PrefersConvention(t *testing.T) {
	dir := t.TempDir()
	// Write two files: foo.star (matching convention) and bar.star
	// (also defines foo). Convention path wins.
	os.WriteFile(filepath.Join(dir, "foo.star"), []byte(`unit(name = "foo")`), 0o644)
	os.WriteFile(filepath.Join(dir, "other.star"), []byte(`unit(name = "foo", tag = "v0")`), 0o644)
	got, err := findUnitStarFile(dir, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "foo.star" {
		t.Errorf("convention path should win, got %s", got)
	}
}

func TestFindUnitStarFile_FallsBackToScan(t *testing.T) {
	dir := t.TempDir()
	// foo.star doesn't exist; bar.star defines foo.
	os.WriteFile(filepath.Join(dir, "bar.star"), []byte(`
def helper():
    unit(name = "foo", tag = "v1")
`), 0o644)
	got, err := findUnitStarFile(dir, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "bar.star" {
		t.Errorf("fallback scan should find bar.star, got %s", got)
	}
}

// --- Branch-aware dev mode ---------------------------------------------
//
// Tests that DevToUpstream advances the working tree to origin/<branch>
// when the unit declares a branch, and re-points the local `upstream`
// tag accordingly.

// runOut runs a command and returns its trimmed stdout, fataling on
// failure. Same shape as `run` but for cases that need the output.
func runOut(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupPinnedSrcWithBranch is setupPinnedSrc plus two more commits on
// the upstream's branch beyond the pin. The pin clone still sits at
// the first commit (tagged `upstream`); origin/<branch> in the
// upstream repo is two commits ahead.
func setupPinnedSrcWithBranch(t *testing.T, projectDir, unitName, branch string) (srcDir, upstreamURL, branchHead string) {
	t.Helper()
	srcDir, upstreamURL = setupPinnedSrc(t, projectDir, unitName)

	// The upstream repo was initialized with main; if the test wants a
	// different branch, create it here. Either way, add two commits.
	upstream := filepath.Join(projectDir, "_upstream", unitName+".git")
	if branch != "main" {
		run(t, upstream, "git", "checkout", "-q", "-b", branch)
	}
	if err := os.WriteFile(filepath.Join(upstream, "feat.c"), []byte("/* new */\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, upstream, "git", "add", "-A")
	run(t, upstream, "git", "commit", "-q", "-m", "branch commit 1")
	if err := os.WriteFile(filepath.Join(upstream, "feat2.c"), []byte("/* more */\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, upstream, "git", "add", "-A")
	run(t, upstream, "git", "commit", "-q", "-m", "branch commit 2")

	out, err := gitCmd(upstream, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return srcDir, upstreamURL, strings.TrimSpace(out)
}

func TestDevToUpstream_BranchDeclared_ChecksOutBranchHead(t *testing.T) {
	dir := t.TempDir()
	srcDir, upstreamURL, branchHead := setupPinnedSrcWithBranch(t, dir, "openssh", "main")
	// Capture the pin commit before the toggle so we can verify the
	// `upstream` tag stays anchored at it.
	pinCommit, err := gitCmd(srcDir, "rev-parse", "upstream")
	if err != nil {
		t.Fatal(err)
	}
	pinCommit = strings.TrimSpace(pinCommit)

	unit := &yoestar.Unit{
		Name:   "openssh",
		Source: upstreamURL,
		Tag:    "upstream", // arbitrary pin name; the existing `upstream` git tag in the src clone stands in
		Branch: "main",
	}
	if err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{}); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}

	// Working tree should be at branch HEAD (two commits past the pin).
	head := strings.TrimSpace(runOut(t, srcDir, "git", "rev-parse", "HEAD"))
	if head != branchHead {
		t.Errorf("HEAD = %s, want branch HEAD %s", head, branchHead)
	}
	// `upstream` tag should stay at the pin commit so dev-mod counts
	// commits past pin — surfacing "build would differ from pin" at a
	// glance.
	upstreamSha := strings.TrimSpace(runOut(t, srcDir, "git", "rev-parse", "upstream"))
	if upstreamSha != pinCommit {
		t.Errorf("upstream tag = %s, want pin commit %s (must stay at pin, not move to branch HEAD)", upstreamSha, pinCommit)
	}
	// rev-list upstream..HEAD should now be 2 (two commits past pin)
	// → DetectState returns dev-mod, signalling divergence from pin.
	count := strings.TrimSpace(runOut(t, srcDir, "git", "rev-list", "--count", "upstream..HEAD"))
	if count != "2" {
		t.Errorf("rev-list upstream..HEAD = %s, want 2 (branch is 2 commits past pin)", count)
	}
	// User should land on a local branch named `main`, not detached HEAD,
	// so `git pull`/`git push` work in the $-shell.
	branch := strings.TrimSpace(runOut(t, srcDir, "git", "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "main" {
		t.Errorf("HEAD is on %q, want local branch main (not detached)", branch)
	}
}

func TestDevToUpstream_TagOnly_LeavesWorkingTreeAtPin(t *testing.T) {
	dir := t.TempDir()
	srcDir, upstreamURL, _ := setupPinnedSrcWithBranch(t, dir, "openssh", "main")
	pinHead := strings.TrimSpace(runOut(t, srcDir, "git", "rev-parse", "HEAD"))

	unit := &yoestar.Unit{
		Name:   "openssh",
		Source: upstreamURL,
		Tag:    "upstream",
		// No Branch — today's behavior preserved.
	}
	if err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{}); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}

	head := strings.TrimSpace(runOut(t, srcDir, "git", "rev-parse", "HEAD"))
	if head != pinHead {
		t.Errorf("HEAD = %s, want pinned commit %s (unchanged)", head, pinHead)
	}
}

func TestDevToUpstream_BranchWithoutTag_Rejects(t *testing.T) {
	dir := t.TempDir()
	_, upstreamURL := setupPinnedSrc(t, dir, "openssh")

	unit := &yoestar.Unit{
		Name:   "openssh",
		Source: upstreamURL,
		Branch: "main",
		// No Tag — malformed.
	}
	err := DevToUpstream(dir, "x86_64", unit, DevUpstreamOpts{})
	if err == nil {
		t.Fatal("expected DevToUpstream to refuse a branch-only unit")
	}
	if !strings.Contains(err.Error(), "branch") || !strings.Contains(err.Error(), "tag") {
		t.Errorf("error %v should mention branch and tag", err)
	}
}
