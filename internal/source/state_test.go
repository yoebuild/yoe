package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectState_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := DetectState(filepath.Join(dir, "does-not-exist"), "")
	if err != nil {
		t.Fatalf("DetectState on missing dir: %v", err)
	}
	if got != StateEmpty {
		t.Errorf("got %q, want %q", got, StateEmpty)
	}
}

func TestDetectState_NoGitDir(t *testing.T) {
	dir := t.TempDir()
	got, err := DetectState(dir, "")
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

	got, err := DetectState(dir, "")
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

	got, err := DetectState(dir, "")
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

	got, err := DetectState(dir, "")
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

	got, err := DetectState(dir, "")
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

	got, err := DetectState(dir, "")
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

	got, err := DetectState(dir, "")
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

	got, err := DetectState(dir, "")
	if err == nil {
		t.Fatal("expected non-nil error when upstream tag is missing")
	}
	if got != StateDev {
		t.Errorf("got %q, want %q (best-effort fallback)", got, StateDev)
	}
}

// TestDetectState_CachedPinDisambiguatesCleanCheckout covers the new
// design: pin keeps origin configured, so a clean checkout with origin
// could be either pin or dev. The cached toggle decision disambiguates.
func TestDetectState_CachedPinDisambiguatesCleanCheckout(t *testing.T) {
	dir := initRepo(t)
	markUpstream(t, dir)
	addOriginRemote(t, dir)

	// Without a cached state, clean+origin defaults to dev.
	if got, _ := DetectState(dir, ""); got != StateDev {
		t.Errorf("no cache → got %q, want %q", got, StateDev)
	}
	// Cached pin → stays pin even with origin configured.
	if got, _ := DetectState(dir, StatePin); got != StatePin {
		t.Errorf("cached pin → got %q, want %q", got, StatePin)
	}
	// Cached dev → stays dev (no-op vs default but documents intent).
	if got, _ := DetectState(dir, StateDev); got != StateDev {
		t.Errorf("cached dev → got %q, want %q", got, StateDev)
	}
}

// TestDetectState_DirtyBeatsCachedPin: dev-dirty wins even when cached
// state says pin. The user's uncommitted edits are the higher-risk
// signal — pin discipline says don't edit in pin, but if they have,
// we surface dev-dirty.
func TestDetectState_DirtyBeatsCachedPin(t *testing.T) {
	dir := initRepo(t)
	markUpstream(t, dir)
	addOriginRemote(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := DetectState(dir, StatePin)
	if got != StateDevDirty {
		t.Errorf("got %q, want %q (dirty must win over cached pin)", got, StateDevDirty)
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

// TestSrcHashInputs_DirtyEditChangesHash is a regression test for a
// bug where `yoe build` short-circuited a unit with uncommitted
// edits because the hash didn't change between successive edits.
// The hash function only included the dirty diff sha when called
// with state==StateDevDirty, but callers were passing the
// persisted "dev" state from BuildMeta. SrcHashInputs is correct;
// the test pins down the contract callers must honor.
func TestSrcHashInputs_DirtyEditChangesHash(t *testing.T) {
	dir := initRepo(t)
	addOriginRemote(t, dir)
	markUpstream(t, dir)

	// Clean dev state — hash is just the HEAD sha.
	clean := SrcHashInputs(dir, StateDev)
	if clean == "" {
		t.Fatal("SrcHashInputs returned empty for clean dev state")
	}

	// Dirty up the work tree.
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte("int main(){return 1;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Caller passes the live (dirty) state — this is what the
	// executor's srcInputs closure must do.
	dirty := SrcHashInputs(dir, StateDevDirty)
	if dirty == "" {
		t.Fatal("SrcHashInputs returned empty for dirty dev state")
	}
	if dirty == clean {
		t.Errorf("dirty hash equals clean hash — edits would be cached:\n  clean: %s\n  dirty: %s", clean, dirty)
	}

	// A second different edit should produce a third distinct hash.
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte("int main(){return 2;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty2 := SrcHashInputs(dir, StateDevDirty)
	if dirty2 == dirty {
		t.Errorf("two distinct edits produced the same hash:\n  edit1: %s\n  edit2: %s", dirty, dirty2)
	}
}

// TestSrcHashInputs_StateDevSkipsDirtyDiff documents the surprising
// caller contract: passing StateDev when the work tree is actually
// dirty produces a clean-only hash. The fix for the regression
// lives in the caller (executor.go), which now runs DetectState
// before calling SrcHashInputs.
func TestSrcHashInputs_StateDevSkipsDirtyDiff(t *testing.T) {
	dir := initRepo(t)
	addOriginRemote(t, dir)
	markUpstream(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	asDev := SrcHashInputs(dir, StateDev)
	asDirty := SrcHashInputs(dir, StateDevDirty)
	if asDev == asDirty {
		t.Errorf("state argument must affect output:\n  StateDev:      %s\n  StateDevDirty: %s", asDev, asDirty)
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

// markUpstream tags the current HEAD with yoe's internal pin marker
// (yoe/pin), matching what source.Prepare does after a fresh clone.
// Named to avoid colliding with the package-level `tagUpstream`
// helper in workspace.go.
func markUpstream(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "tag", PinTag)
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

