package module

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// ModuleToUpstream switches a module's clone into dev mode: rewrites
// origin to SSH if the user prefers, unshallows the clone if it was
// shallow, and persists `dev` state in the module's sibling state file.
//
// Modules differ from units in that they already have a real remote
// (a git clone done at sync time), so the transition is lighter than
// the unit-side DevToUpstream — no remote-add, no fetch needed beyond
// unshallow.
//
// Locally-overridden modules (`module(local = "...")`) error out: the
// user's checkout is theirs to manage; yoe doesn't touch its remote.
func ModuleToUpstream(m yoestar.ResolvedModule, ssh bool) error {
	if m.Local != "" {
		return fmt.Errorf("ModuleToUpstream: module %q is locally overridden (local = %q); yoe doesn't manage its remote", m.Name, m.Local)
	}
	if m.Dir == "" {
		return fmt.Errorf("ModuleToUpstream: module %q has no clone dir — was it synced?", m.Name)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, ".git")); err != nil {
		return fmt.Errorf("ModuleToUpstream: %s is not a git repo", m.Dir)
	}

	if ssh {
		current, err := gitOut(m.Dir, "remote", "get-url", "origin")
		if err == nil {
			if rewrote, ok := httpsToSSH(strings.TrimSpace(current)); ok {
				if _, err := gitOut(m.Dir, "remote", "set-url", "origin", rewrote); err != nil {
					return fmt.Errorf("ModuleToUpstream: switching origin to SSH: %w", err)
				}
			}
		}
	}

	shallow, _ := gitOut(m.Dir, "rev-parse", "--is-shallow-repository")
	if strings.TrimSpace(shallow) == "true" {
		if _, err := gitOut(m.Dir, "fetch", "--unshallow", "origin"); err != nil {
			return fmt.Errorf("ModuleToUpstream: fetch --unshallow: %w", err)
		}
	}

	// Tag the current HEAD as `upstream` so source.DetectState's
	// `rev-list upstream..HEAD` query gives the right answer (dev when
	// HEAD == upstream, dev-mod after a local commit, dev-dirty when
	// the work tree is dirty). Modules don't get this tag at sync time
	// — only when the user opts into dev mode.
	if _, err := gitOut(m.Dir, "tag", "-f", "upstream", "HEAD"); err != nil {
		return fmt.Errorf("ModuleToUpstream: tagging upstream: %w", err)
	}
	// Hide the state file from `git status` so it doesn't taint the
	// dirty signal. .git/info/exclude is the clone-local gitignore,
	// won't propagate via git add.
	if err := excludeFromGit(m.Dir, stateFile); err != nil {
		// best effort — losing this just makes `git status` slightly
		// noisier; it doesn't break dev-mode functionality once the
		// state file exists.
		_ = err
	}

	return WriteState(m.Dir, source.StateDev)
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
	if m.Dir == "" {
		return fmt.Errorf("ModuleToPin: module %q has no clone dir", m.Name)
	}

	if !force {
		state, _ := source.DetectState(m.Dir)
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
	if _, err := gitOut(m.Dir, "fetch", "origin", ref); err != nil {
		return fmt.Errorf("ModuleToPin: fetch origin %s: %w", ref, err)
	}
	if _, err := gitOut(m.Dir, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("ModuleToPin: reset --hard: %w", err)
	}
	// Advance the upstream tag to the new HEAD so any future
	// source.DetectState query (during a TUI cold-start before the
	// user re-toggles to dev) doesn't see the old upstream commit and
	// misreport dev-mod against a freshly reset clone.
	if _, err := gitOut(m.Dir, "tag", "-f", "upstream", "HEAD"); err != nil {
		// best effort; the state-file clear below is the authoritative
		// signal for the TUI.
		_ = err
	}
	return WriteState(m.Dir, source.StateEmpty)
}

// httpsToSSH rewrites a github/gitlab-style HTTPS URL to SSH.
// Mirror of the helper in internal/dev.go — kept private to this
// package to avoid a circular-import chain.
//
//	https://github.com/foo/bar.git → git@github.com:foo/bar.git
func httpsToSSH(httpsURL string) (string, bool) {
	u, err := url.Parse(httpsURL)
	if err != nil || u.Scheme != "https" {
		return httpsURL, false
	}
	path := strings.TrimPrefix(u.Path, "/")
	if path == "" {
		return httpsURL, false
	}
	return "git@" + u.Host + ":" + path, true
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

// gitOut runs git in dir and returns combined output. Used by both
// ModuleTo* paths; mirrors internal/dev.go's gitCmd shape.
func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
