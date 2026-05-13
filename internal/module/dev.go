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
	// `path = "..."` field (e.g. module-rpi inside a multi-module repo).
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
		current, err := gitOut(repo, "remote", "get-url", "origin")
		if err == nil {
			if rewrote, ok := httpsToSSH(strings.TrimSpace(current)); ok {
				if _, err := gitOut(repo, "remote", "set-url", "origin", rewrote); err != nil {
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
	if _, err := gitOut(repo, "tag", "-f", "upstream", "HEAD"); err != nil {
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
	if _, err := gitOut(repo, "fetch", "origin", ref); err != nil {
		return fmt.Errorf("ModuleToPin: fetch origin %s: %w", ref, err)
	}
	if _, err := gitOut(repo, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("ModuleToPin: reset --hard: %w", err)
	}
	// Advance the upstream tag to the new HEAD so any future
	// source.DetectState query (during a TUI cold-start before the
	// user re-toggles to dev) doesn't see the old upstream commit and
	// misreport dev-mod against a freshly reset clone.
	if _, err := gitOut(repo, "tag", "-f", "upstream", "HEAD"); err != nil {
		// best effort; the state-file clear below is the authoritative
		// signal for the TUI.
		_ = err
	}
	return WriteState(repo, source.StateEmpty)
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

// moduleFetchOrigin runs the upstream fetch with the depth strategy
// chosen in opts — mirrors devFetchOrigin in internal/dev.go but
// keeps a thin private copy here to avoid pulling internal/dev's
// dependency tree (yoestar.Unit, source state writers) into the
// module package, which is meant to stay narrow.
//
// Depth fetches narrow the refspec to the module's pinned ref and
// pass `--filter=blob:none` so the transfer is commits + trees only.
// Full-unshallow paths skip both — the user asked for everything.
func moduleFetchOrigin(dir string, opts ModuleUpstreamOpts, pinnedRef string) error {
	shallow, _ := gitOut(dir, "rev-parse", "--is-shallow-repository")
	isShallow := strings.TrimSpace(shallow) == "true"

	var args []string
	var refspec string
	useFilter := false
	switch {
	case opts.FetchDepth > 0:
		args = []string{"fetch", fmt.Sprintf("--depth=%d", opts.FetchDepth)}
		refspec = pinnedRef
		useFilter = true
	case isShallow:
		args = []string{"fetch", "--unshallow"}
	default:
		// Already full-history clones don't need a re-fetch on toggle —
		// the user already has everything. Skip silently.
		return nil
	}
	if useFilter {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, "origin")
	if refspec != "" {
		args = append(args, refspec)
	}
	if _, err := gitOut(dir, args...); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
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
