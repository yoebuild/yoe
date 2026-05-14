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

// PinTag is the name of yoe's local git tag marking the pin commit
// inside a unit's src dir. Namespaced under "yoe/" so it can never
// collide with real upstream tags (e.g., `v0.18.5`) — which matters
// for DevPromoteToPin's "pick a tag pointing at HEAD" logic.
const PinTag = "yoe/pin"

const (
	// StateEmpty means the src dir doesn't exist or has no .git — the
	// unit hasn't been built yet, or its source dir was wiped.
	StateEmpty State = ""

	// StatePin is a yoe-managed clone whose working tree is the unit's
	// pinned ref + applied patches. Yoe owns this dir and is free to
	// overwrite it on rebuild. Origin may or may not be configured —
	// the pin/dev distinction is the user's toggle decision, persisted
	// in BuildMeta.SourceState, not a git-state observation.
	StatePin State = "pin"

	// StateDev is a dev-mode checkout: user-managed, origin remote
	// configured (so push/pull/fetch work), HEAD wherever the user
	// chose, work tree clean, no local commits beyond the dev anchor.
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
// The result is derived from local git state — `git status --porcelain`,
// `git rev-list --count yoe/pin..HEAD` — plus the caller's `cached`
// toggle decision. No fetch, no network.
//
// `cached` is the unit's previously-persisted BuildMeta.SourceState
// (StatePin or StateDev). It disambiguates clean checkouts, where the
// git state is identical for pin and dev. Pass StateEmpty (or
// equivalently the empty string) when the cache is unknown — the
// result then falls back to:
//   - StatePin if no origin remote is configured (legacy pin clones
//     from before pin kept origin set, or a totally fresh checkout)
//   - StateDev otherwise
//
// StateEmpty is returned (with no error) when the src dir doesn't exist
// or has no `.git` directory. Both are normal conditions for an unbuilt
// unit, not failure modes.
//
// When a git probe fails unexpectedly (e.g., the `upstream` tag is
// missing because the dir was hand-edited into a corrupted state),
// DetectState returns the best-effort state plus a non-nil error so the
// caller can log without losing the visible rendering.
func DetectState(srcDir string, cached State) (State, error) {
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		if os.IsNotExist(err) {
			return StateEmpty, nil
		}
		return StateEmpty, err
	}

	dirty, err := stateGit(srcDir, "status", "--porcelain")
	if err != nil {
		return StateDev, err
	}
	if strings.TrimSpace(dirty) != "" {
		return StateDevDirty, nil
	}

	ahead, err := stateGit(srcDir, "rev-list", "--count", PinTag+"..HEAD")
	if err != nil {
		// Likely no `upstream` tag — surface the error but report
		// dev so the caller can still render something useful.
		return StateDev, err
	}
	if strings.TrimSpace(ahead) != "0" {
		return StateDevMod, nil
	}

	// Clean working tree: pin vs dev is the toggle decision, not
	// derivable from git state alone. Honor the cached value when set.
	if cached == StatePin {
		return StatePin, nil
	}
	if cached == StateDev {
		return StateDev, nil
	}
	// Unknown cache: fall back to the origin-remote heuristic. No
	// origin means a legacy pin (pre-keep-origin-in-pin builds) or a
	// brand-new shallow clone; otherwise default to dev.
	remote, _ := stateGit(srcDir, "remote", "get-url", "origin")
	if strings.TrimSpace(remote) == "" {
		return StatePin, nil
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
