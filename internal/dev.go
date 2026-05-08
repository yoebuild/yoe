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
	"strings"

	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// DevExtract extracts local commits in a unit's build directory as patch
// files and updates the unit's patches list.
func DevExtract(projectDir, arch, unitName string, w io.Writer) error {
	proj, err := yoestar.LoadProject(projectDir)
	if err != nil {
		return err
	}

	unit, ok := proj.Units[unitName]
	if !ok {
		return fmt.Errorf("unit %q not found", unitName)
	}

	srcDir := unitSrcDir(projectDir, arch, unitName)
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); os.IsNotExist(err) {
		return fmt.Errorf("%s is not a git repo — build the recipe first with yoe build", srcDir)
	}

	// Check if there are commits beyond upstream
	out, err := gitCmd(srcDir, "rev-list", "upstream..HEAD")
	if err != nil {
		return fmt.Errorf("no 'upstream' tag in %s — was this source fetched by yoe?", srcDir)
	}
	commits := strings.TrimSpace(out)
	if commits == "" {
		fmt.Fprintf(w, "No local commits beyond upstream in %s\n", unitName)
		return nil
	}

	// Create patches directory
	patchDir := filepath.Join(projectDir, "patches", unitName)
	if err := os.MkdirAll(patchDir, 0755); err != nil {
		return fmt.Errorf("creating patch directory: %w", err)
	}

	// Remove old patches
	oldPatches, _ := filepath.Glob(filepath.Join(patchDir, "*.patch"))
	for _, p := range oldPatches {
		os.Remove(p)
	}

	// Extract patches with git format-patch
	_, err = gitCmd(srcDir, "format-patch", "--output-directory", patchDir, "upstream..HEAD")
	if err != nil {
		return fmt.Errorf("git format-patch: %w", err)
	}

	// List generated patches
	patches, _ := filepath.Glob(filepath.Join(patchDir, "*.patch"))
	if len(patches) == 0 {
		fmt.Fprintf(w, "No patches extracted\n")
		return nil
	}

	// Build the patches list relative to project root
	var patchPaths []string
	for _, p := range patches {
		rel, _ := filepath.Rel(projectDir, p)
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
	srcDir := unitSrcDir(projectDir, arch, unitName)
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); os.IsNotExist(err) {
		return fmt.Errorf("%s is not a git repo — build the recipe first", srcDir)
	}

	out, err := gitCmd(srcDir, "log", "--oneline", "upstream..HEAD")
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

// DevStatus shows which units have local modifications.
func DevStatus(projectDir, arch string, w io.Writer) error {
	proj, err := yoestar.LoadProject(projectDir)
	if err != nil {
		return err
	}

	buildDir := filepath.Join(projectDir, "build", arch)
	found := false

	for name := range proj.Units {
		srcDir := filepath.Join(buildDir, name, "src")
		gitDir := filepath.Join(srcDir, ".git")
		if _, err := os.Stat(gitDir); os.IsNotExist(err) {
			continue
		}

		out, err := gitCmd(srcDir, "rev-list", "--count", "upstream..HEAD")
		if err != nil {
			continue
		}

		count := strings.TrimSpace(out)
		if count != "0" {
			fmt.Fprintf(w, "%-20s %s commit(s) ahead of upstream\n", name, count)
			found = true
		}
	}

	if !found {
		fmt.Fprintln(w, "No units with local modifications")
	}

	return nil
}

func unitSrcDir(projectDir, arch, unitName string) string {
	return filepath.Join(projectDir, "build", arch, unitName, "src")
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
// imports this package).
func devSrcDir(projectDir, scopeDir, unitName string) string {
	return filepath.Join(projectDir, "build", unitName+"."+scopeDir, "src")
}

// DevUpstreamOpts configures the upstream-fetch performed when a unit
// (or module) is toggled into dev mode. Zero values keep the legacy
// "rewrite remote, unshallow, fetch everything" behavior, so callers
// that don't care about depth keep working without changes.
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
	// FetchSince, when non-empty, replaces `--unshallow` with
	// `--shallow-since=<spec>` (e.g. "1.year.ago", "2024-01-01").
	// Mutually exclusive with FetchDepth — FetchDepth wins if both
	// are set, on the assumption an explicit commit count is more
	// predictable than a date heuristic.
	FetchSince string
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
// The unit's working tree is not moved — pin and dev mode for the same
// commit produce bit-identical builds. The transition adds connectivity
// and history; it doesn't change source content.
func DevToUpstream(projectDir, scopeDir string, unit *yoestar.Unit, opts DevUpstreamOpts) error {
	srcDir := devSrcDir(projectDir, scopeDir, unit.Name)
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		return fmt.Errorf("DevToUpstream: %s is not a git repo — build the unit first", srcDir)
	}
	if !devIsGitURL(unit.Source) {
		return fmt.Errorf("DevToUpstream: %s has a non-git source (%s); only git-based units support dev mode", unit.Name, unit.Source)
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

	if err := devFetchOrigin(srcDir, opts); err != nil {
		return fmt.Errorf("DevToUpstream: %w", err)
	}

	if err := writeUnitSourceState(projectDir, scopeDir, unit.Name, source.StateDev); err != nil {
		return fmt.Errorf("DevToUpstream: persisting state: %w", err)
	}
	return nil
}

// devFetchOrigin runs the upstream fetch with the depth strategy
// chosen in opts. Picks one of:
//   - --depth=N        when FetchDepth > 0
//   - --shallow-since  when FetchSince is set
//   - --unshallow      when the clone is currently shallow (default)
//   - plain fetch      when the clone is already full history
//
// The `--unshallow` branch errors on a non-shallow repo, so we probe
// is-shallow-repository first instead of paying the round-trip on the
// failing path. The depth and since branches are safe on either
// shape — git just deepens to the requested boundary.
func devFetchOrigin(srcDir string, opts DevUpstreamOpts) error {
	shallow, _ := gitCmd(srcDir, "rev-parse", "--is-shallow-repository")
	isShallow := strings.TrimSpace(shallow) == "true"

	var args []string
	switch {
	case opts.FetchDepth > 0:
		args = []string{"fetch", fmt.Sprintf("--depth=%d", opts.FetchDepth), "origin"}
	case opts.FetchSince != "":
		args = []string{"fetch", "--shallow-since=" + opts.FetchSince, "origin"}
	case isShallow:
		args = []string{"fetch", "--unshallow", "origin"}
	default:
		args = []string{"fetch", "origin"}
	}
	if _, err := gitCmd(srcDir, args...); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// DevToPin throws away the dev-mode checkout and re-runs source.Prepare
// on the next build. Without `force=true`, refuses to proceed when there
// are commits beyond `upstream` or uncommitted edits in the work tree —
// the caller (TUI or CLI) is responsible for surfacing a confirmation
// to the user when local work is at stake.
//
// Implementation is dead simple: remove the src dir and clear the
// cached state. The next build's source.Prepare re-clones at the
// pinned ref.
func DevToPin(projectDir, scopeDir string, unit *yoestar.Unit, force bool) error {
	srcDir := devSrcDir(projectDir, scopeDir, unit.Name)
	state, _ := source.DetectState(srcDir)
	if !force {
		switch state {
		case source.StateDevDirty:
			return fmt.Errorf("DevToPin: %s has uncommitted edits; commit/stash or pass force=true to discard", unit.Name)
		case source.StateDevMod:
			return fmt.Errorf("DevToPin: %s has commits beyond upstream; switch back will discard them — pass force=true to confirm", unit.Name)
		}
	}
	if err := os.RemoveAll(srcDir); err != nil {
		return fmt.Errorf("DevToPin: removing %s: %w", srcDir, err)
	}
	if err := writeUnitSourceState(projectDir, scopeDir, unit.Name, source.StateEmpty); err != nil {
		return fmt.Errorf("DevToPin: clearing state: %w", err)
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

// writeUnitSourceState updates BuildMeta.SourceState in the unit's
// build dir, leaving every other meta field intact. Used by DevTo*
// to mark a unit as dev or clear it back to pin without re-running
// the executor's full meta finalize.
func writeUnitSourceState(projectDir, scopeDir, unitName string, state source.State) error {
	buildDir := filepath.Join(projectDir, "build", unitName+"."+scopeDir)
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

// PinKind selects which form of pin to write when promoting a dev-mod
// unit's HEAD into the .star.
type PinKind string

const (
	// PinKindTag pins to the git tag at HEAD. Errors if HEAD has no
	// tag — the caller should disable this option in the UI prompt.
	PinKindTag PinKind = "tag"
	// PinKindHash pins to the 40-char sha at HEAD. Always available;
	// most reproducible.
	PinKindHash PinKind = "hash"
	// PinKindBranch pins to the current branch name. Mutable —
	// breaks build reproducibility — surface a warning in the UI.
	PinKindBranch PinKind = "branch"
)

// DevPromoteToPin captures the dev-mode checkout's current HEAD into
// the unit's .star definition. State must be StateDevMod (commits
// beyond upstream, work tree clean); other states return an error so
// the caller can render an appropriate UI hint instead of silently
// no-oping.
//
// On success: rewrites the unit's `tag` (PinKindTag/Hash) or `branch`
// (PinKindBranch) field, removes the now-stale alternate field, and
// advances the local `upstream` git tag to HEAD so
// `git rev-list upstream..HEAD` returns 0 — the unit transitions from
// `dev-mod` to plain `dev` as the visible acknowledgement that
// pinned == checked-out.
//
// The unit's working tree commit is unchanged. A subsequent
// pin-mode rebuild will re-clone shallow at the new ref; the user's
// branch and any uncommitted state-dirty work would survive in dev
// mode (uncommitted edits aren't possible in StateDevMod by
// definition).
func DevPromoteToPin(projectDir, scopeDir string, unit *yoestar.Unit, kind PinKind) error {
	srcDir := devSrcDir(projectDir, scopeDir, unit.Name)
	state, _ := source.DetectState(srcDir)
	if state != source.StateDevMod {
		return fmt.Errorf("DevPromoteToPin: unit %q is in state %q; promote requires dev-mod (commit dirty edits or there's nothing to promote)",
			unit.Name, state)
	}

	starPath, err := findUnitStarFile(unit.DefinedIn, unit.Name)
	if err != nil {
		return fmt.Errorf("DevPromoteToPin: %w", err)
	}

	var newField, newValue, removeField string
	switch kind {
	case PinKindTag:
		// Pick a git tag pointing at HEAD. If there are several, pick
		// the first sorted lexicographically — deterministic without
		// requiring annotated tags.
		out, err := gitCmd(srcDir, "tag", "--points-at", "HEAD")
		if err != nil {
			return fmt.Errorf("DevPromoteToPin: %w", err)
		}
		tags := strings.Fields(out)
		if len(tags) == 0 {
			return fmt.Errorf("DevPromoteToPin: HEAD has no tag — pick PinKindHash or PinKindBranch instead")
		}
		newField, newValue = "tag", tags[0]
		removeField = "branch"
	case PinKindHash:
		out, err := gitCmd(srcDir, "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("DevPromoteToPin: %w", err)
		}
		newField, newValue = "tag", strings.TrimSpace(out)
		removeField = "branch"
	case PinKindBranch:
		out, err := gitCmd(srcDir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return fmt.Errorf("DevPromoteToPin: %w", err)
		}
		branch := strings.TrimSpace(out)
		if branch == "HEAD" {
			return fmt.Errorf("DevPromoteToPin: detached HEAD has no branch — pick PinKindTag or PinKindHash")
		}
		newField, newValue = "branch", branch
		removeField = "tag"
	default:
		return fmt.Errorf("DevPromoteToPin: unknown PinKind %q", kind)
	}

	if err := yoestar.RewriteUnitField(starPath, unit.Name, newField, newValue); err != nil {
		return fmt.Errorf("DevPromoteToPin: rewriting %s: %w", newField, err)
	}
	if err := yoestar.RemoveUnitField(starPath, unit.Name, removeField); err != nil {
		return fmt.Errorf("DevPromoteToPin: removing stale %s: %w", removeField, err)
	}

	// Move upstream tag forward so the state transitions from dev-mod
	// to dev. -f overwrites the existing upstream tag.
	if _, err := gitCmd(srcDir, "tag", "-f", "upstream", "HEAD"); err != nil {
		return fmt.Errorf("DevPromoteToPin: advancing upstream tag: %w", err)
	}

	return writeUnitSourceState(projectDir, scopeDir, unit.Name, source.StateDev)
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
