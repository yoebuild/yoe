package source

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// State classifies the source state of a unit's build/<name>/src/ checkout
// (or a module clone). Pure observation — no side effects on the working
// tree, the git index, or any state file.
type State string

const (
	// StateEmpty means the src dir doesn't exist or has no .git — the
	// unit hasn't been built yet, or its source dir was wiped.
	StateEmpty State = ""

	// StatePin is a yoe-managed shallow clone: no upstream remote
	// configured. Yoe owns this dir and is free to overwrite it on
	// rebuild.
	StatePin State = "pin"

	// StateDev is a dev-mode checkout: upstream remote configured,
	// HEAD on the upstream tag's commit, work tree clean, no local
	// commits beyond upstream.
	StateDev State = "dev"

	// StateDevMod is dev mode plus commits beyond the upstream tag,
	// work tree clean.
	StateDevMod State = "dev-mod"

	// StateDevDirty is dev mode plus uncommitted edits in the work
	// tree (regardless of whether there are commits ahead). Takes
	// priority over StateDevMod when both conditions are true — the
	// uncommitted work is the higher-risk signal.
	StateDevDirty State = "dev-dirty"

	// StateLocal is for module clones overridden via `module(local =
	// "../path")` — the user's checkout, not yoe-managed. DetectState
	// never returns this; callers determine local-ness from the
	// module config and short-circuit before probing git.
	StateLocal State = "local"
)

// IsDev reports whether s is one of the dev variants. Used to gate the
// build-time guard, the fsnotify watcher's scope, and the unit-hash
// src-tree fold.
func IsDev(s State) bool {
	return s == StateDev || s == StateDevMod || s == StateDevDirty
}

// DetectState returns the source state for the working tree at srcDir.
// The result is derived purely from local git state — `git remote
// get-url origin`, `git status --porcelain`, `git rev-list --count
// upstream..HEAD`. No fetch, no network.
//
// StateEmpty is returned (with no error) when the src dir doesn't exist
// or has no `.git` directory. Both are normal conditions for an unbuilt
// unit, not failure modes.
//
// When a git probe fails unexpectedly (e.g., the `upstream` tag is
// missing because the dir was hand-edited into a corrupted state),
// DetectState returns the best-effort state plus a non-nil error so the
// caller can log without losing the visible rendering.
func DetectState(srcDir string) (State, error) {
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		if os.IsNotExist(err) {
			return StateEmpty, nil
		}
		return StateEmpty, err
	}

	// `git remote get-url origin` exits non-zero when origin isn't
	// configured — that's the pin signal, not an error. We swallow
	// the error and look at the output: empty string means no remote.
	remote, _ := stateGit(srcDir, "remote", "get-url", "origin")
	if strings.TrimSpace(remote) == "" {
		return StatePin, nil
	}

	dirty, err := stateGit(srcDir, "status", "--porcelain")
	if err != nil {
		return StateDev, err
	}
	if strings.TrimSpace(dirty) != "" {
		return StateDevDirty, nil
	}

	ahead, err := stateGit(srcDir, "rev-list", "--count", "upstream..HEAD")
	if err != nil {
		// Likely no `upstream` tag — surface the error but report
		// dev so the caller can still render something useful.
		return StateDev, err
	}
	if strings.TrimSpace(ahead) != "0" {
		return StateDevMod, nil
	}
	return StateDev, nil
}

// stateGit runs git in dir and returns stdout. On non-zero exit, returns
// the trimmed stderr as the error message — easier to surface in logs
// than the raw exec.ExitError.
func stateGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}
