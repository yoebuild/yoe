package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectState_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := DetectState(filepath.Join(dir, "does-not-exist"))
	if err != nil {
		t.Fatalf("DetectState on missing dir: %v", err)
	}
	if got != StateEmpty {
		t.Errorf("got %q, want %q", got, StateEmpty)
	}
}

func TestDetectState_NoGitDir(t *testing.T) {
	dir := t.TempDir()
	got, err := DetectState(dir)
	if err != nil {
		t.Fatalf("DetectState on non-git dir: %v", err)
	}
	if got != StateEmpty {
		t.Errorf("got %q, want %q", got, StateEmpty)
	}
}

// TestDetectState_Pin covers the freshly-cloned, yoe-managed case: a git
// repo with an `upstream` tag at HEAD, no `origin` remote configured.
func TestDetectState_Pin(t *testing.T) {
	dir := initRepo(t)
	markUpstream(t, dir)

	got, err := DetectState(dir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if got != StatePin {
		t.Errorf("got %q, want %q", got, StatePin)
	}
}

// TestDetectState_Dev covers a dev-mode checkout: origin remote set, HEAD
// on the upstream commit, clean work tree, no commits ahead.
func TestDetectState_Dev(t *testing.T) {
	dir := initRepo(t)
	markUpstream(t, dir)
	addOriginRemote(t, dir)

	got, err := DetectState(dir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if got != StateDev {
		t.Errorf("got %q, want %q", got, StateDev)
	}
}

// TestDetectState_DevMod covers a dev checkout with commits beyond
// upstream, work tree clean.
func TestDetectState_DevMod(t *testing.T) {
	dir := initRepo(t)
	markUpstream(t, dir)
	addOriginRemote(t, dir)
	commitFile(t, dir, "extra.c", "// new content\n", "add extra.c")

	got, err := DetectState(dir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if got != StateDevMod {
		t.Errorf("got %q, want %q", got, StateDevMod)
	}
}

// TestDetectState_DevDirty_Modified covers a dev checkout with an edited
// tracked file (uncommitted).
func TestDetectState_DevDirty_Modified(t *testing.T) {
	dir := initRepo(t)
	markUpstream(t, dir)
	addOriginRemote(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte("int main() { return 1; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DetectState(dir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if got != StateDevDirty {
		t.Errorf("got %q, want %q", got, StateDevDirty)
	}
}

// TestDetectState_DevDirty_Untracked covers a dev checkout with a new
// untracked file.
func TestDetectState_DevDirty_Untracked(t *testing.T) {
	dir := initRepo(t)
	markUpstream(t, dir)
	addOriginRemote(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DetectState(dir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if got != StateDevDirty {
		t.Errorf("got %q, want %q", got, StateDevDirty)
	}
}

// TestDetectState_DevDirtyOverridesDevMod confirms the priority rule from
// the brainstorm: when both commits-ahead AND dirty work tree are true,
// dev-dirty wins (uncommitted work is the higher-risk signal).
func TestDetectState_DevDirtyOverridesDevMod(t *testing.T) {
	dir := initRepo(t)
	markUpstream(t, dir)
	addOriginRemote(t, dir)
	commitFile(t, dir, "extra.c", "// new\n", "add extra.c")
	// Now also dirty the work tree.
	if err := os.WriteFile(filepath.Join(dir, "extra.c"), []byte("// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DetectState(dir)
	if err != nil {
		t.Fatalf("DetectState: %v", err)
	}
	if got != StateDevDirty {
		t.Errorf("got %q, want %q (dirty must win over commits-ahead)", got, StateDevDirty)
	}
}

// TestDetectState_NoUpstreamTag covers the corrupted/hand-edited case:
// origin is set, but the `upstream` tag is missing. DetectState reports
// StateDev with a non-nil error so callers can log without losing the
// rendering.
func TestDetectState_NoUpstreamTag(t *testing.T) {
	dir := initRepo(t)
	// no tagUpstream call
	addOriginRemote(t, dir)

	got, err := DetectState(dir)
	if err == nil {
		t.Fatal("expected non-nil error when upstream tag is missing")
	}
	if got != StateDev {
		t.Errorf("got %q, want %q (best-effort fallback)", got, StateDev)
	}
}

func TestIsDev(t *testing.T) {
	dev := []State{StateDev, StateDevMod, StateDevDirty}
	for _, s := range dev {
		if !IsDev(s) {
			t.Errorf("IsDev(%q) = false, want true", s)
		}
	}
	notDev := []State{StateEmpty, StatePin, StateLocal}
	for _, s := range notDev {
		if IsDev(s) {
			t.Errorf("IsDev(%q) = true, want false", s)
		}
	}
}

// --- Helpers ------------------------------------------------------------

// initRepo creates a fresh git repo under t.TempDir() with a single
// committed file (`main.c`). Returns the repo dir.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte("int main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "commit", "-q", "-m", "initial")
	return dir
}

// markUpstream tags the current HEAD as `upstream`, matching what
// source.Prepare does after a fresh clone. Named to avoid colliding
// with the package-level `tagUpstream` helper in workspace.go.
func markUpstream(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "tag", "upstream")
}

// addOriginRemote configures a stub origin remote — the URL doesn't have
// to be reachable; DetectState only checks that it's non-empty.
func addOriginRemote(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "remote", "add", "origin", "https://example.com/stub.git")
}

// commitFile writes a file and commits it.
func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "commit", "-q", "-m", msg)
}

