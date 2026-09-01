package module

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoebuild/yoe/internal/gitutil"
	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// setupSyncFixture builds an upstream repo plus a clone of it wired as
// the module cache Sync will look at, and returns both paths along with
// the ModuleRef naming them.
func setupSyncFixture(t *testing.T, name string) (moduleDir, upstream string, ref yoestar.ModuleRef) {
	t.Helper()
	parent := t.TempDir()
	t.Setenv("YOE_CACHE", parent)
	moduleDir, url := setupModuleClone(t, parent, name, false)
	return moduleDir, filepath.Join(parent, "_upstream", name+".git"), yoestar.ModuleRef{URL: url, Ref: "main"}
}

// markDev puts a clone into dev mode the way ModuleToUpstream does:
// anchor the dev tag at HEAD and record the toggle.
func markDev(t *testing.T, moduleDir string) {
	t.Helper()
	run(t, moduleDir, "git", "tag", "-f", source.PinTag, "HEAD")
	if err := WriteState(moduleDir, source.StateDev); err != nil {
		t.Fatal(err)
	}
	// Keep the state file out of `git status`, as ModuleToUpstream does;
	// otherwise it alone reads as a dirty tree.
	if err := excludeFromGit(moduleDir, stateFile); err != nil {
		t.Fatal(err)
	}
}

// advanceUpstream commits body to file in the upstream repo so a
// following sync has something to pull.
func advanceUpstream(t *testing.T, upstream, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(upstream, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, upstream, "git", "add", "-A")
	run(t, upstream, "git", "commit", "-q", "-m", "upstream change")
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	out, err := gitutil.Run(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func syncFixture(t *testing.T, ref yoestar.ModuleRef) {
	t.Helper()
	if _, err := Sync([]yoestar.ModuleRef{ref}, io.Discard); err != nil {
		t.Fatalf("Sync: %v", err)
	}
}

// A clean dev clone follows its tracking branch forward, stays on that
// branch rather than being detached, and keeps reporting plain `dev`
// because the commits it gained are upstream's, not the user's.
func TestSync_DevCleanFastForwards(t *testing.T) {
	moduleDir, upstream, ref := setupSyncFixture(t, "module-ff")
	markDev(t, moduleDir)
	advanceUpstream(t, upstream, "NEW.star", "new\n")

	syncFixture(t, ref)

	if got, want := headSHA(t, moduleDir), headSHA(t, upstream); got != want {
		t.Errorf("HEAD = %s, want upstream %s", got, want)
	}
	branch, _ := gitutil.Run(moduleDir, "rev-parse", "--abbrev-ref", "HEAD")
	if strings.TrimSpace(branch) != "main" {
		t.Errorf("HEAD detached or moved: branch = %q, want main", strings.TrimSpace(branch))
	}
	if state, _ := source.DetectState(moduleDir, ReadState(moduleDir)); state != source.StateDev {
		t.Errorf("state = %q, want dev — the dev tag should have re-anchored", state)
	}
}

// A dev clone carrying the user's own commit is never fast-forwarded
// over: the commit survives and sync reports rather than fails.
func TestSync_DevModKeepsLocalCommits(t *testing.T) {
	moduleDir, upstream, ref := setupSyncFixture(t, "module-devmod")
	markDev(t, moduleDir)
	if err := os.WriteFile(filepath.Join(moduleDir, "LOCAL.star"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, moduleDir, "git", "add", "-A")
	run(t, moduleDir, "git", "commit", "-q", "-m", "local work")
	local := headSHA(t, moduleDir)
	advanceUpstream(t, upstream, "NEW.star", "new\n")

	syncFixture(t, ref)

	if got := headSHA(t, moduleDir); got != local {
		t.Errorf("HEAD = %s, want local commit %s preserved", got, local)
	}
	if _, err := os.Stat(filepath.Join(moduleDir, "LOCAL.star")); err != nil {
		t.Errorf("local commit's file gone: %v", err)
	}
}

// Uncommitted edits survive a sync even when upstream touched the same
// file, the case where a checkout would have discarded them.
func TestSync_DevDirtyKeepsUncommittedEdits(t *testing.T) {
	moduleDir, upstream, ref := setupSyncFixture(t, "module-dirty")
	markDev(t, moduleDir)
	edited := filepath.Join(moduleDir, "MODULE.star")
	if err := os.WriteFile(edited, []byte("my uncommitted edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := headSHA(t, moduleDir)
	advanceUpstream(t, upstream, "MODULE.star", "upstream rewrite\n")

	syncFixture(t, ref)

	body, err := os.ReadFile(edited)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "my uncommitted edit\n" {
		t.Errorf("uncommitted edit lost: got %q", body)
	}
	if got := headSHA(t, moduleDir); got != before {
		t.Errorf("HEAD moved to %s over a dirty tree, want %s", got, before)
	}
}

// A clone with no dev toggle is yoe-managed and still gets checked out
// onto the declared ref.
func TestSync_PinStillChecksOutRef(t *testing.T) {
	moduleDir, upstream, ref := setupSyncFixture(t, "module-pin")
	advanceUpstream(t, upstream, "NEW.star", "new\n")

	syncFixture(t, ref)

	if got, want := headSHA(t, moduleDir), headSHA(t, upstream); got != want {
		t.Errorf("HEAD = %s, want %s", got, want)
	}
}
