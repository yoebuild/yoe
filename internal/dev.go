package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// DevExtract extracts local commits in a unit's build directory as patch
// files and updates the unit's patches list. Patches land in <unitDir>/<unit>/
// — alongside the unit's .star file — so the patch paths in `patches = [...]`
// stay relative to the unit and ship with the module that defines it.
func DevExtract(projectDir, arch, unitName string, w io.Writer) error {
	proj, err := yoestar.LoadProject(projectDir)
	if err != nil {
		return err
	}

	// DevExtract operates on the unit's source-control state, not
	// its distro-specific build artifact, so AnyUnit is enough — the
	// patch list and source URL it reads are distro-neutral fields.
	unit := proj.AnyUnit(unitName)
	if unit == nil {
		return fmt.Errorf("unit %q not found", unitName)
	}

	srcDir, err := findUnitSrcDir(projectDir, unitName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); os.IsNotExist(err) {
		return fmt.Errorf("%s is not a git repo — build the recipe first with yoe build", srcDir)
	}
	_ = arch // legacy parameter; unitSrcDir now globs across the per-distro build subtree

	// Check if there are commits beyond upstream
	out, err := gitCmd(srcDir, "rev-list", source.PinTag+"..HEAD")
	if err != nil {
		return fmt.Errorf("no 'upstream' tag in %s — was this source fetched by yoe?", srcDir)
	}
	commits := strings.TrimSpace(out)
	if commits == "" {
		fmt.Fprintf(w, "No local commits beyond upstream in %s\n", unitName)
		return nil
	}

	// Patches live next to the unit's .star file in a directory named after
	// the unit. unit.DefinedIn is already the directory holding the .star
	// file; fall back to the project root if it isn't set.
	unitDir := unit.DefinedIn
	if unitDir == "" {
		unitDir = projectDir
	}
	patchDir := filepath.Join(unitDir, unitName)
	if err := os.MkdirAll(patchDir, 0755); err != nil {
		return fmt.Errorf("creating patch directory: %w", err)
	}

	// Remove old patches
	oldPatches, _ := filepath.Glob(filepath.Join(patchDir, "*.patch"))
	for _, p := range oldPatches {
		os.Remove(p)
	}

	// Extract patches with git format-patch
	_, err = gitCmd(srcDir, "format-patch", "--output-directory", patchDir, source.PinTag+"..HEAD")
	if err != nil {
		return fmt.Errorf("git format-patch: %w", err)
	}

	// List generated patches
	patches, _ := filepath.Glob(filepath.Join(patchDir, "*.patch"))
	if len(patches) == 0 {
		fmt.Fprintf(w, "No patches extracted\n")
		return nil
	}

	// Build the patches list relative to the unit's directory — that's what
	// the unit's `patches = [...]` field expects.
	var patchPaths []string
	for _, p := range patches {
		rel, _ := filepath.Rel(unitDir, p)
		patchPaths = append(patchPaths, rel)
		fmt.Fprintf(w, "  %s\n", rel)
	}

	fmt.Fprintf(w, "\nExtracted %d patch(es) for %s\n", len(patches), unitName)
	fmt.Fprintf(w, "Update your unit's patches list to:\n")
	fmt.Fprintf(w, "    patches = [\n")
	for _, p := range patchPaths {
		fmt.Fprintf(w, "        %q,\n", p)
	}
	fmt.Fprintf(w, "    ],\n")

	// Check if unit already had patches and show diff
	if len(unit.Patches) > 0 {
		fmt.Fprintf(w, "\nPrevious patches were:\n")
		for _, p := range unit.Patches {
			fmt.Fprintf(w, "    %q,\n", p)
		}
	}

	return nil
}

// DevDiff shows local commits beyond upstream in a unit's build directory.
func DevDiff(projectDir, arch, unitName string, w io.Writer) error {
	srcDir, err := findUnitSrcDir(projectDir, unitName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); os.IsNotExist(err) {
		return fmt.Errorf("%s is not a git repo — build the recipe first", srcDir)
	}
	_ = arch

	out, err := gitCmd(srcDir, "log", "--oneline", source.PinTag+"..HEAD")
	if err != nil {
		return fmt.Errorf("no 'upstream' tag in %s", srcDir)
	}

	if strings.TrimSpace(out) == "" {
		fmt.Fprintf(w, "No local changes beyond upstream in %s\n", unitName)
		return nil
	}

	fmt.Fprintf(w, "Local commits in %s (upstream..HEAD):\n\n", unitName)
	fmt.Fprint(w, out)
	return nil
}

// DevStatus shows which units have local modifications. It walks the
// build directory directly rather than loading the project, so it stays
// useful when PROJECT.star or a module has an evaluation error — that's
// often exactly when you want to know which units have uncommitted local
// work waiting to be extracted.
//
// Walks every build/<distro>/<name>.<scope>/src/ subtree so units that
// only built under one consuming distro still surface.
func DevStatus(projectDir, _ string, w io.Writer) error {
	// glob: build/<distro>/<name>.<scope>/src/.git
	matches, err := filepath.Glob(filepath.Join(projectDir, "build", "*", "*.*", "src", ".git"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		if _, statErr := os.Stat(filepath.Join(projectDir, "build")); os.IsNotExist(statErr) {
			fmt.Fprintln(w, "No units with local modifications")
			return nil
		}
	}

	// Deduplicate by unit name so a unit built under both alpine and
	// debian only prints once. If both copies have local commits, use
	// whichever has more.
	type entry struct {
		count  string
		distro string
	}
	byUnit := map[string]entry{}
	for _, gitPath := range matches {
		srcDir := filepath.Dir(gitPath)
		unitDir := filepath.Dir(srcDir)
		base := filepath.Base(unitDir) // <name>.<scope>
		distroDir := filepath.Base(filepath.Dir(unitDir))
		name := base
		if dot := strings.Index(base, "."); dot > 0 {
			name = base[:dot]
		}
		out, err := gitCmd(srcDir, "rev-list", "--count", source.PinTag+"..HEAD")
		if err != nil {
			continue
		}
		count := strings.TrimSpace(out)
		if count == "0" {
			continue
		}
		if prev, ok := byUnit[name]; !ok || count > prev.count {
			byUnit[name] = entry{count: count, distro: distroDir}
		}
	}

	if len(byUnit) == 0 {
		fmt.Fprintln(w, "No units with local modifications")
		return nil
	}
	names := make([]string, 0, len(byUnit))
	for n := range byUnit {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := byUnit[n]
		fmt.Fprintf(w, "%-20s %s commit(s) ahead of upstream (%s)\n", n, e.count, e.distro)
	}
	return nil
}

// unitSrcDir is kept as a small helper for callers that already know
// the unit's distro and scope.
func unitSrcDir(projectDir, scopeDir, unitName, distro string) string {
	return filepath.Join(projectDir, "build", distro, unitName+"."+scopeDir, "src")
}

// findUnitSrcDir locates a unit's src/ directory under build/<distro>/
// without requiring the caller to know which distro or scope the unit
// landed under. If the unit has built under multiple distros, it
// returns the first match (sorted) — sufficient for read-only ops
// like DevExtract/DevDiff/DevStatus which present the same local-
// modifications view regardless of consuming distro.
func findUnitSrcDir(projectDir, unitName string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(projectDir, "build", "*", unitName+".*", "src"))
	if err != nil {
		return "", fmt.Errorf("locating src dir for %s: %w", unitName, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no build directory for %s — build the recipe first", unitName)
	}
	sort.Strings(matches)
	return matches[0], nil
}

func gitCmd(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// devSrcDir returns the src/ path for a unit's build dir. Mirrors
// build.UnitBuildDir without importing the build package (which
// imports this package). distro is the consuming image's effective
// distro per R14a; an empty distro is a programmer error and panics.
func devSrcDir(projectDir, scopeDir, unitName, distro string) string {
	if distro == "" {
		panic("devSrcDir: distro must not be empty (R14a)")
	}
	return filepath.Join(projectDir, "build", distro, unitName+"."+scopeDir, "src")
}

// DevUpstreamOpts configures the upstream-fetch performed when a unit
// (or module) is toggled into dev mode. Zero values keep the
// "rewrite remote, unshallow, fetch everything" behavior.
type DevUpstreamOpts struct {
	// SSH rewrites a github/gitlab-style HTTPS URL to git@host:path
	// before setting origin. Hosts that don't match a known SSH
	// mapping fall back to HTTPS regardless of this flag.
	SSH bool
	// FetchDepth, when > 0, replaces `--unshallow` with
	// `--depth=<N>`. Useful for repos with deep history (linux,
	// chromium) where a full unshallow pulls gigabytes of objects
	// the developer doesn't need.
	FetchDepth int
}

// DevToUpstream switches a unit's src checkout from pin mode (yoe-managed
// shallow clone, no remote) into dev mode: rewrites `origin` to the
// upstream URL the user picks (HTTPS or SSH), fetches enough history
// for `git log` / `git blame` / branch ops to work, and persists
// `dev` state in BuildMeta.
//
// `unit.Source` provides the canonical HTTPS URL; opts.SSH rewrites it
// to git@host:path form for hosts where that mapping is well-defined
// (github.com, gitlab.com, generic SSH-on-:22 servers). Hosts that don't
// fit that pattern fall through to HTTPS regardless of the SSH flag.
//
// Working-tree commit depends on whether the unit declares branch:
//
//   - Tag only: the working tree stays at the pinned commit. Pin and dev
//     build the same source; the transition only adds connectivity.
//   - Tag + Branch: after the fetch, the working tree is checked out
//     (detached HEAD) at origin/<branch>, and the local `upstream` git
//     tag is re-pointed to origin/<branch> so dev-mod counts commits past
//     branch HEAD rather than past the pin tag.
//
// Branch-only (Branch set, Tag empty) is malformed: tag is the pin, branch
// only tracks dev. Returns an error before touching git.
func DevToUpstream(projectDir, scopeDir, distro string, unit *yoestar.Unit, opts DevUpstreamOpts) error {
	srcDir := devSrcDir(projectDir, scopeDir, unit.Name, distro)
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		return fmt.Errorf("DevToUpstream: %s is not a git repo — build the unit first", srcDir)
	}
	if !devIsGitURL(unit.Source) {
		return fmt.Errorf("DevToUpstream: %s has a non-git source (%s); only git-based units support dev mode", unit.Name, unit.Source)
	}
	if unit.Branch != "" && unit.Tag == "" {
		return fmt.Errorf("DevToUpstream: %s declares branch=%q but no tag — tag is the pin, branch only tracks dev", unit.Name, unit.Branch)
	}

	target := unit.Source
	if opts.SSH {
		if rewrote, ok := httpsToSSH(unit.Source); ok {
			target = rewrote
		}
	}

	// `git remote remove origin` exits non-zero if origin doesn't
	// exist; treat that as success since the next `add` will install
	// the right one.
	_, _ = gitCmd(srcDir, "remote", "remove", "origin")
	if _, err := gitCmd(srcDir, "remote", "add", "origin", target); err != nil {
		return fmt.Errorf("DevToUpstream: setting origin: %w", err)
	}

	if err := devFetchOrigin(srcDir, opts, devPinnedRef(unit)); err != nil {
		return fmt.Errorf("DevToUpstream: %w", err)
	}

	// Branch-tracking: check out a local branch named after the
	// declared branch, tracking origin/<branch>, so the user gets a
	// natural dev workflow — `git pull`, `git push`, and `git log @{u}..`
	// all work without thinking about detached HEAD.
	//
	// The initial pin clone was single-branch at the pinned tag, so
	// `refs/remotes/origin/<branch>` may not exist and the repo may
	// still be shallow at the pin's neighborhood. Force a deep fetch
	// of the branch (`--depth=2147483647` deepens to the full history
	// available, ignoring any prior shallow constraint). Then reuse an
	// existing local <branch> if there is one (preserving local
	// commits), or create it from FETCH_HEAD if missing. Either way,
	// configure it to track origin/<branch>.
	if unit.Branch != "" {
		if _, err := gitCmd(srcDir, "fetch", "--depth=2147483647", "origin", unit.Branch); err != nil {
			return fmt.Errorf("DevToUpstream: fetching origin %s: %w", unit.Branch, err)
		}
		// Update the remote-tracking ref so `git log origin/<branch>`
		// works from $-shell without re-fetching, and the local-branch
		// upstream setup below has something to point at.
		_, _ = gitCmd(srcDir, "update-ref", "refs/remotes/origin/"+unit.Branch, "FETCH_HEAD")
		// If a local branch with this name already exists, just check
		// it out — the user may have local commits on it from a prior
		// dev session that we must NOT squash. Only create (from
		// FETCH_HEAD) when the branch is missing. `git checkout
		// <branch>` (no -B, no -f) preserves the branch's existing tip
		// and refuses if uncommitted edits would be clobbered, which
		// is the safe behavior here — pin → dev starts from a clean
		// pin tree so it won't trip, and a dirty tree means the user
		// has work to deal with first.
		if _, err := gitCmd(srcDir, "rev-parse", "--verify", "refs/heads/"+unit.Branch); err == nil {
			if _, err := gitCmd(srcDir, "checkout", unit.Branch); err != nil {
				return fmt.Errorf("DevToUpstream: checking out existing local branch %s (commit/stash local edits first?): %w", unit.Branch, err)
			}
		} else if _, err := gitCmd(srcDir, "checkout", "-B", unit.Branch, "FETCH_HEAD"); err != nil {
			return fmt.Errorf("DevToUpstream: creating local branch %s: %w", unit.Branch, err)
		}
		// Set the local branch's upstream so plain `git pull` /
		// `git push` work. Best-effort — the checkout above is the
		// load-bearing step.
		_, _ = gitCmd(srcDir, "branch", "--set-upstream-to=origin/"+unit.Branch, unit.Branch)
		// Anchor the local `upstream` git tag at the pin commit, not
		// at branch HEAD. dev-mod then counts commits past the pin —
		// answering "would a build here produce different output than
		// pin mode?" at a glance. A branch that's advanced past the
		// pin tag flips the unit to dev-mod immediately on toggle.
		if _, err := gitCmd(srcDir, "tag", "-f", source.PinTag, unit.Tag); err != nil {
			return fmt.Errorf("DevToUpstream: anchoring upstream tag at %s: %w", unit.Tag, err)
		}
	}

	if err := writeUnitSourceState(projectDir, scopeDir, unit.Name, distro, source.StateDev); err != nil {
		return fmt.Errorf("DevToUpstream: persisting state: %w", err)
	}
	return nil
}

// devFetchOrigin runs the upstream fetch with the depth strategy
// chosen in opts. Picks one of:
//   - --depth=N    when FetchDepth > 0
//   - --unshallow  when the clone is currently shallow (default)
//   - plain fetch  when the clone is already full history
//
// Depth fetches narrow the refspec to the unit's pinned ref so we
// get N commits leading up to the pin (passing the broad refspec
// would fan out to every tracked branch — Linux: 100 commits × N
// branches). They also pass `--filter=blob:none` so the transfer
// is commits + trees only; file content is fetched on demand when
// something actually reads it. The full-unshallow path skips both
// — the user explicitly asked for everything.
//
// The `--unshallow` branch errors on a non-shallow repo, so we probe
// is-shallow-repository first instead of paying the round-trip on the
// failing path.
func devFetchOrigin(srcDir string, opts DevUpstreamOpts, pinnedRef string) error {
	shallow, _ := gitCmd(srcDir, "rev-parse", "--is-shallow-repository")
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
		args = []string{"fetch"}
	}
	if useFilter {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, "origin")
	if refspec != "" {
		args = append(args, refspec)
	}
	if _, err := gitCmd(srcDir, args...); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// devPinnedRef returns the ref the unit is pinned to (tag, then
// branch). Empty string means the unit didn't pin anything explicit
// — caller falls through to a broad fetch.
func devPinnedRef(unit *yoestar.Unit) string {
	if unit.Tag != "" {
		return unit.Tag
	}
	if unit.Branch != "" {
		return unit.Branch
	}
	return ""
}

// DevToPin resets the existing dev-mode checkout back to its pinned ref
// in place, without re-cloning from the source cache. The clone already
// has the pin commit in its local history (DevToUpstream's unshallow
// fetch pulled everything), so we just move HEAD, clean any orphaned
// state, re-apply patches, and drop the origin remote.
//
// Without `force=true`, refuses to proceed when there are commits
// beyond `upstream` or uncommitted edits in the work tree — the caller
// (TUI or CLI) is responsible for surfacing a confirmation to the user
// when local work is at stake.
func DevToPin(projectDir, scopeDir, distro string, unit *yoestar.Unit, force bool) error {
	srcDir := devSrcDir(projectDir, scopeDir, unit.Name, distro)
	// Refuse to touch anything if srcDir isn't a self-contained git
	// repo. Without this guard, git commands run with cmd.Dir=srcDir
	// silently walk up to a parent .git (the user's project repo) and
	// destructively operate on the WRONG tree — git clean -fdx wiping
	// the project's build/, cache/, etc.
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		return fmt.Errorf("DevToPin: %s is not a git repo (missing .git) — build the unit first", srcDir)
	}
	state, _ := source.DetectState(srcDir, readUnitSourceState(projectDir, scopeDir, unit.Name, distro))
	if !force {
		switch state {
		case source.StateDevDirty:
			return fmt.Errorf("DevToPin: %s has uncommitted edits; commit/stash or pass force=true to discard", unit.Name)
		case source.StateDevMod:
			return fmt.Errorf("DevToPin: %s has commits beyond upstream; switch back will discard them — pass force=true to confirm", unit.Name)
		}
	}
	if unit.Tag == "" {
		return fmt.Errorf("DevToPin: %s has no tag — nothing to pin to", unit.Name)
	}

	// Move the working tree to the pin tag. --force discards any
	// dev-dirty edits; dev-mod commits become orphaned (still in the
	// git database but unreachable from HEAD). We deliberately do NOT
	// follow this with `git clean -fdx` — that command operates on the
	// whole working tree and, if git's view of the work tree is wrong
	// for any reason, can destructively touch directories outside the
	// unit's src dir. Untracked files that survive the checkout (build
	// output, editor swap files) are tolerable as a pin-mode soft
	// edge; correctness trumps tidiness.
	if _, err := gitCmd(srcDir, "checkout", "--detach", "--force", unit.Tag); err != nil {
		return fmt.Errorf("DevToPin: checking out %s: %w", unit.Tag, err)
	}
	// Re-apply patches on top of the pin tag. They were committed in the
	// original Prepare run but the dev-mode branch checkout orphaned
	// them, so we replay from the unit's patches list.
	if err := source.ApplyPatches(projectDir, srcDir, unit); err != nil {
		return fmt.Errorf("DevToPin: re-applying patches: %w", err)
	}
	// Origin remote stays configured — keeping the user's full history
	// and saving a re-fetch if they toggle back to dev later. The
	// pin/dev distinction is the persisted toggle decision, not whether
	// origin is set.
	// Reset the local `upstream` git tag back to the pin commit (the
	// commit pre-patches), matching what source.Prepare leaves behind
	// for a fresh pin clone.
	if _, err := gitCmd(srcDir, "tag", "-f", source.PinTag, unit.Tag); err != nil {
		return fmt.Errorf("DevToPin: resetting upstream tag: %w", err)
	}
	if err := writeUnitSourceState(projectDir, scopeDir, unit.Name, distro, source.StatePin); err != nil {
		return fmt.Errorf("DevToPin: persisting pin state: %w", err)
	}
	return nil
}

// httpsToSSH rewrites a github/gitlab-style HTTPS git URL into the
// equivalent SSH form. Returns (rewritten, true) on a recognized
// host; (original, false) otherwise so the caller can fall through
// to HTTPS without a separate error path.
//
//	https://github.com/foo/bar.git → git@github.com:foo/bar.git
//	https://gitlab.com/foo/bar.git → git@gitlab.com:foo/bar.git
func httpsToSSH(httpsURL string) (string, bool) {
	u, err := url.Parse(httpsURL)
	if err != nil || u.Scheme != "https" {
		return httpsURL, false
	}
	// Path always starts with /; strip it.
	path := strings.TrimPrefix(u.Path, "/")
	if path == "" {
		return httpsURL, false
	}
	return "git@" + u.Host + ":" + path, true
}

// readUnitSourceState reads the cached BuildMeta.SourceState for a
// unit. Returns StateEmpty when the meta file is missing or
// unreadable — the caller passes that to DetectState, which falls
// back to the origin-remote heuristic.
func readUnitSourceState(projectDir, scopeDir, unitName, distro string) source.State {
	buildDir := filepath.Join(projectDir, "build", distro, unitName+"."+scopeDir)
	data, err := os.ReadFile(filepath.Join(buildDir, "build.json"))
	if err != nil {
		return source.StateEmpty
	}
	var meta struct {
		SourceState string `json:"source_state,omitempty"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return source.StateEmpty
	}
	return source.State(meta.SourceState)
}

// writeUnitSourceState updates BuildMeta.SourceState in the unit's
// build dir, leaving every other meta field intact. Used by DevTo*
// to mark a unit as dev or clear it back to pin without re-running
// the executor's full meta finalize.
func writeUnitSourceState(projectDir, scopeDir, unitName, distro string, state source.State) error {
	buildDir := filepath.Join(projectDir, "build", distro, unitName+"."+scopeDir)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}
	metaPath := filepath.Join(buildDir, "build.json")
	// Read whatever's there (or start with an empty struct).
	type metaShape struct {
		Status         string  `json:"status,omitempty"`
		Started        any     `json:"started,omitempty"`
		Finished       any     `json:"finished,omitempty"`
		Duration       float64 `json:"duration_seconds,omitempty"`
		DiskBytes      int64   `json:"disk_bytes,omitempty"`
		InstalledBytes int64   `json:"installed_bytes,omitempty"`
		Hash           string  `json:"hash,omitempty"`
		Error          string  `json:"error,omitempty"`
		SourceState    string  `json:"source_state,omitempty"`
		SourceDescribe string  `json:"source_describe,omitempty"`
	}
	var meta metaShape
	if data, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	meta.SourceState = string(state)
	if state == source.StateEmpty {
		meta.SourceDescribe = ""
	}
	out, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, out, 0o644)
}

// DevPromoteToPin captures the dev-mode checkout's current HEAD into
// the unit's .star `tag` field. Prefers a real upstream tag name when
// HEAD has one (e.g., `v0.18.5`) — those are human-readable and
// survive a clean re-pin. yoe-internal markers (`yoe/pin` and any
// future `yoe/*` tags) are skipped. When HEAD has no real upstream
// tag, falls back to the 40-char SHA, which is always unambiguous.
//
// Never writes the `branch` field — branch tracking is declared by the
// unit author, the pin command only updates the pin.
//
// Allowed in StateDev and StateDevMod. StateDevDirty returns an error
// (commit or stash first so the captured state is reproducible). Other
// states (pin, empty) are no-ops with an informative error.
//
// On success: rewrites the unit's `tag` field, advances the local
// `upstream` git tag to HEAD so `git rev-list upstream..HEAD` returns 0,
// and persists pin state to BuildMeta — the working tree, the .star,
// and the source-state column now agree on the new pin. The working
// tree commit is unchanged; the user can toggle `u` to go back to dev
// mode if they want to keep iterating from this point.
func DevPromoteToPin(projectDir, scopeDir, distro string, unit *yoestar.Unit) error {
	srcDir := devSrcDir(projectDir, scopeDir, unit.Name, distro)
	state, _ := source.DetectState(srcDir, readUnitSourceState(projectDir, scopeDir, unit.Name, distro))
	switch state {
	case source.StateDev, source.StateDevMod:
		// proceed
	case source.StateDevDirty:
		return fmt.Errorf("DevPromoteToPin: %s has uncommitted edits; commit or stash first to pin current state", unit.Name)
	default:
		return fmt.Errorf("DevPromoteToPin: unit %q is in state %q; pin requires dev or dev-mod",
			unit.Name, state)
	}

	starPath, err := findUnitStarFile(unit.DefinedIn, unit.Name)
	if err != nil {
		return fmt.Errorf("DevPromoteToPin: %w", err)
	}

	// Pick the first upstream tag pointing at HEAD that isn't in the
	// yoe-internal namespace. yoe/pin (and any future yoe/* markers)
	// must be filtered out — they're our own bookkeeping, not upstream
	// references. If HEAD has no real tag, fall back to the 40-char
	// SHA, which is always unambiguous.
	var value string
	if out, err := gitCmd(srcDir, "tag", "--points-at", "HEAD"); err == nil {
		for _, t := range strings.Fields(out) {
			if strings.HasPrefix(t, "yoe/") {
				continue
			}
			value = t
			break
		}
	}
	if value == "" {
		out, err := gitCmd(srcDir, "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("DevPromoteToPin: %w", err)
		}
		value = strings.TrimSpace(out)
	}

	if err := yoestar.RewriteUnitField(starPath, unit.Name, "tag", value); err != nil {
		return fmt.Errorf("DevPromoteToPin: rewriting tag: %w", err)
	}

	// Move upstream tag forward so rev-list upstream..HEAD returns 0
	// and DetectState reports pin (matching the new .star). -f
	// overwrites the existing upstream tag.
	if _, err := gitCmd(srcDir, "tag", "-f", source.PinTag, "HEAD"); err != nil {
		return fmt.Errorf("DevPromoteToPin: advancing %s tag: %w", source.PinTag, err)
	}

	// Persist pin state. The action is called "pin to current" — the
	// .star and the working tree now agree on the new pin, and the
	// SRC column should reflect that. If the user wants to keep
	// iterating in dev mode after pinning, they can toggle `u` again.
	return writeUnitSourceState(projectDir, scopeDir, unit.Name, distro, source.StatePin)
}

// findUnitStarFile locates the .star file that registers a unit with
// the given name within a directory. Tries the convention
// (`<dir>/<unitName>.star`) first, falls back to scanning every .star
// file in the dir for a matching `name = "<unitName>"` declaration.
//
// The fallback covers cases where a helper function (e.g.,
// base_files() in modules/module-core/units/base/base-files.star)
// registers a unit with a different name than the file that defines
// the helper.
func findUnitStarFile(dir, unitName string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("unit %q has no DefinedIn — was it loaded from disk?", unitName)
	}
	nameRE := regexp.MustCompile(`(?m)name\s*=\s*"` + regexp.QuoteMeta(unitName) + `"`)

	candidate := filepath.Join(dir, unitName+".star")
	if data, err := os.ReadFile(candidate); err == nil && nameRE.Match(data) {
		return candidate, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("scanning %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".star") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if nameRE.Match(data) {
			return path, nil
		}
	}
	return "", fmt.Errorf("could not locate .star file defining unit %q in %s", unitName, dir)
}

// devIsGitURL is a small mirror of source.isGitURL (which is unexported).
// Inlined here to avoid widening the source package's API just for this
// caller — the check is two lines and the failure mode is informational.
func devIsGitURL(u string) bool {
	if strings.HasSuffix(u, ".git") {
		return true
	}
	return strings.HasPrefix(u, "git@") || strings.HasPrefix(u, "git://") || strings.HasPrefix(u, "ssh://")
}
