package module

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yoebuild/yoe/internal/gitutil"
	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// ModuleUpstreamOpts mirrors the unit-side DevUpstreamOpts so callers
// can request a depth-limited fetch instead of a full unshallow. See
// internal/dev.go for field semantics.
type ModuleUpstreamOpts struct {
	SSH        bool
	FetchDepth int
}

// ModuleToUpstream switches a module's clone into dev mode: rewrites
// origin to SSH if the user prefers, fetches enough history for `git
// log` / `git blame` to work, and persists `dev` state in the
// module's sibling state file.
//
// Modules differ from units in that they already have a real remote
// (a git clone done at sync time), so the transition is lighter than
// the unit-side DevToUpstream — no remote-add, no fetch needed beyond
// the depth strategy chosen in opts.
//
// Locally-overridden modules (`module(local = "...")`) error out: the
// user's checkout is theirs to manage; yoe doesn't touch its remote.
func ModuleToUpstream(m yoestar.ResolvedModule, opts ModuleUpstreamOpts) error {
	if m.Local != "" {
		return fmt.Errorf("ModuleToUpstream: module %q is locally overridden (local = %q); yoe doesn't manage its remote", m.Name, m.Local)
	}
	// Git operations target the clone root (where .git lives), not the
	// MODULE.star subdir — they differ when the module declares a
	// `path = "..."` field (e.g. module-bsp inside a multi-module repo).
	repo := m.CloneDir
	if repo == "" {
		repo = m.Dir
	}
	if repo == "" {
		return fmt.Errorf("ModuleToUpstream: module %q has no clone dir — was it synced?", m.Name)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		return fmt.Errorf("ModuleToUpstream: %s is not a git repo", repo)
	}

	if opts.SSH {
		current, err := gitutil.Run(repo, "remote", "get-url", "origin")
		if err == nil {
			if rewrote, ok := gitutil.HTTPSToSSH(strings.TrimSpace(current)); ok {
				if _, err := gitutil.Run(repo, "remote", "set-url", "origin", rewrote); err != nil {
					return fmt.Errorf("ModuleToUpstream: switching origin to SSH: %w", err)
				}
			}
		}
	}

	if err := moduleFetchOrigin(repo, opts, m.Ref); err != nil {
		return fmt.Errorf("ModuleToUpstream: %w", err)
	}

	// Tag the current HEAD as `upstream` so source.DetectState's
	// `rev-list upstream..HEAD` query gives the right answer (dev when
	// HEAD == upstream, dev-mod after a local commit, dev-dirty when
	// the work tree is dirty). Modules don't get this tag at sync time
	// — only when the user opts into dev mode.
	if _, err := gitutil.Run(repo, "tag", "-f", source.PinTag, "HEAD"); err != nil {
		return fmt.Errorf("ModuleToUpstream: tagging upstream: %w", err)
	}
	// Hide the state file from `git status` so it doesn't taint the
	// dirty signal. .git/info/exclude is the clone-local gitignore,
	// won't propagate via git add.
	if err := excludeFromGit(repo, stateFile); err != nil {
		// best effort — losing this just makes `git status` slightly
		// noisier; it doesn't break dev-mode functionality once the
		// state file exists.
		_ = err
	}

	return WriteState(repo, source.StateDev)
}

// ModuleToPin resets the module clone to the project-declared ref
// (Sync-equivalent behaviour). Refuses to proceed when state is
// dev-mod or dev-dirty unless force=true so callers can warn the
// user before discarding work — a module is more likely than a unit
// src dir to contain pushed-elsewhere commits the user cares about.
func ModuleToPin(m yoestar.ResolvedModule, force bool) error {
	if m.Local != "" {
		return fmt.Errorf("ModuleToPin: module %q is locally overridden; nothing to reset", m.Name)
	}
	repo := m.CloneDir
	if repo == "" {
		repo = m.Dir
	}
	if repo == "" {
		return fmt.Errorf("ModuleToPin: module %q has no clone dir", m.Name)
	}

	if !force {
		state, _ := source.DetectState(repo, ReadState(repo))
		switch state {
		case source.StateDevDirty:
			return fmt.Errorf("ModuleToPin: %s has uncommitted edits; commit/stash or pass force=true", m.Name)
		case source.StateDevMod:
			return fmt.Errorf("ModuleToPin: %s has commits beyond the declared ref; pass force=true to discard", m.Name)
		}
	}

	ref := m.Ref
	if ref == "" {
		ref = "main"
	}
	if _, err := gitutil.Run(repo, "fetch", "origin", ref); err != nil {
		return fmt.Errorf("ModuleToPin: fetch origin %s: %w", ref, err)
	}
	if _, err := gitutil.Run(repo, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("ModuleToPin: reset --hard: %w", err)
	}
	// Advance the upstream tag to the new HEAD so any future
	// source.DetectState query (during a TUI cold-start before the
	// user re-toggles to dev) doesn't see the old upstream commit and
	// misreport dev-mod against a freshly reset clone.
	if _, err := gitutil.Run(repo, "tag", "-f", source.PinTag, "HEAD"); err != nil {
		// best effort; the state-file clear below is the authoritative
		// signal for the TUI.
		_ = err
	}
	return WriteState(repo, source.StateEmpty)
}

// moduleFetchOrigin brings origin up to date in a module clone,
// honoring the caller's depth preference. Unlike a unit source tree, a
// clone that already has full history is left alone — a module toggle
// has nothing to pick up there.
func moduleFetchOrigin(dir string, opts ModuleUpstreamOpts, pinnedRef string) error {
	return gitutil.FetchOrigin(dir, gitutil.FetchOptions{
		Depth:        opts.FetchDepth,
		PinnedRef:    pinnedRef,
		SkipWhenFull: true,
		// Sync clones modules with --depth 1 --branch <ref>, which
		// narrows the refspec to that one branch. Units don't need this
		// — their dev path re-adds origin and gets the default refspec
		// — but a module clone keeps the remote it was born with, so
		// without widening, no other branch the user pushes can ever
		// appear as origin/<branch>.
		WidenRefspec: true,
	})
}

// excludeFromGit appends entry to <gitDir>/.git/info/exclude so the
// path stops appearing in `git status`. Idempotent — checks for an
// existing identical line before appending.
func excludeFromGit(gitDir, entry string) error {
	excludePath := filepath.Join(gitDir, ".git", "info", "exclude")
	if existing, err := os.ReadFile(excludePath); err == nil {
		for line := range strings.SplitSeq(string(existing), "\n") {
			if strings.TrimSpace(line) == entry {
				return nil // already there
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(entry + "\n")
	return err
}
