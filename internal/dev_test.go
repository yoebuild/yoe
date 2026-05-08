package internal

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

	// Verify patch file was created
	patches, _ := filepath.Glob(filepath.Join(dir, "patches", "openssh", "*.patch"))
	if len(patches) != 1 {
		t.Errorf("expected 1 patch file, got %d", len(patches))
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
	if err := DevToUpstream(dir, "x86_64", unit, false); err != nil {
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
	err := DevToUpstream(dir, "x86_64", unit, false)
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
	if err := DevToUpstream(dir, "x86_64", unit, false); err != nil {
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
	unit := &yoestar.Unit{Name: "openssh", Source: upstreamURL}
	if err := DevToUpstream(dir, "x86_64", unit, false); err != nil {
		t.Fatalf("DevToUpstream: %v", err)
	}
	if err := DevToPin(dir, "x86_64", unit, false); err != nil {
		t.Fatalf("DevToPin: %v", err)
	}
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Errorf("src dir should be removed after DevToPin, got err=%v", err)
	}
	if state := readUnitState(t, dir, "openssh"); state != "" {
		t.Errorf("source_state = %q, want empty", state)
	}
}

func TestDevToPin_RefusesDevModWithoutForce(t *testing.T) {
	dir := t.TempDir()
	srcDir, upstreamURL := setupPinnedSrc(t, dir, "openssh")
	unit := &yoestar.Unit{Name: "openssh", Source: upstreamURL}
	if err := DevToUpstream(dir, "x86_64", unit, false); err != nil {
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
	unit := &yoestar.Unit{Name: "openssh", Source: upstreamURL}
	if err := DevToUpstream(dir, "x86_64", unit, false); err != nil {
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
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Errorf("src dir should be removed after force pin, got err=%v", err)
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
