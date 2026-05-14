package tui

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	yoe "github.com/yoebuild/yoe/internal"
	"github.com/yoebuild/yoe/internal/build"
	"github.com/yoebuild/yoe/internal/module"
	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// openSourcePromptForUnit dispatches the right modal flow for a `u`
// keypress on a unit's detail page, based on the unit's current source
// state. Returns the updated model and a command (always nil today —
// network operations live in the post-confirmation handler).
func (m model) openSourcePromptForUnit(unitName string) (tea.Model, tea.Cmd) {
	u, ok := m.proj.Units[unitName]
	if !ok || u.Class == "image" || u.Class == "container" {
		m.message = fmt.Sprintf("%s has no source dir to toggle", unitName)
		return m, nil
	}
	state := m.unitSourceState(unitName)
	switch state {
	case source.StatePin, source.StateEmpty:
		// Pin → dev: ask user whether to use SSH or HTTPS for origin.
		// Show the upstream URL each option resolves to so the user can
		// see what the resulting `origin` will point at.
		m.sourcePrompt = &sourcePrompt{
			kind:       promptSSHHTTPS,
			target:     "unit",
			targetName: unitName,
			header:     fmt.Sprintf("Switch %s to dev mode", unitName),
			subheader:  "upstream: " + u.Source,
			options:    schemePromptOptions(u.Source),
			prevView:   m.view,
		}
		m.view = viewSourcePrompt
		return m, nil
	case source.StateDev:
		// Already dev, clean: pin straight away (no work to discard).
		return m.runDevToPin(unitName, false)
	case source.StateDevMod, source.StateDevDirty:
		// Dirty or has commits beyond upstream — confirm before
		// discarding so the user doesn't lose work to a stray keypress.
		count := m.modifiedCount(unitName)
		var summary string
		if state == source.StateDevDirty {
			summary = fmt.Sprintf("%d uncommitted change(s)", count)
		} else {
			summary = fmt.Sprintf("%d commit(s) beyond upstream", count)
		}
		m.sourcePrompt = &sourcePrompt{
			kind:       promptDiscardDev,
			target:     "unit",
			targetName: unitName,
			header:     fmt.Sprintf("Discard %s and reset %s to its declared ref?", summary, unitName),
			options: []sourcePromptOption{
				{label: "cancel", desc: "keep dev work", value: "cancel"},
				{label: "discard", desc: "lose uncommitted/un-pushed changes", value: "discard"},
			},
			prevView: m.view,
		}
		m.view = viewSourcePrompt
		return m, nil
	case source.StateLocal:
		m.message = fmt.Sprintf("%s is locally overridden — yoe doesn't manage its source", unitName)
		return m, nil
	}
	return m, nil
}

// openSourcePromptForModule mirrors openSourcePromptForUnit for a module
// row on the modules tab. Modules use the same state vocabulary; only
// the action functions differ (module.ModuleToUpstream / ModuleToPin).
func (m model) openSourcePromptForModule(rmName string) (tea.Model, tea.Cmd) {
	rm, ok := m.findModule(rmName)
	if !ok {
		m.message = fmt.Sprintf("module %s not found", rmName)
		return m, nil
	}
	state := m.moduleSourceState(rm)
	switch state {
	case source.StateEmpty, source.StatePin:
		// Prefer the live origin URL — the user may have edited it in
		// the clone — falling back to the declared rm.URL if the live
		// probe fails (clone missing, no .git, etc.).
		upstream := liveRemoteURL(rm.CloneDir)
		if upstream == "" {
			upstream = rm.URL
		}
		m.sourcePrompt = &sourcePrompt{
			kind:       promptSSHHTTPS,
			target:     "module",
			targetName: rmName,
			header:     fmt.Sprintf("Switch module %s to dev mode", rmName),
			subheader:  "upstream: " + upstream,
			options:    schemePromptOptions(upstream),
			prevView:   m.view,
		}
		m.view = viewSourcePrompt
		return m, nil
	case source.StateDev:
		return m.runModuleToPin(rmName, false)
	case source.StateDevMod, source.StateDevDirty:
		m.sourcePrompt = &sourcePrompt{
			kind:       promptDiscardDev,
			target:     "module",
			targetName: rmName,
			header:     fmt.Sprintf("Discard local work and reset module %s?", rmName),
			options: []sourcePromptOption{
				{label: "cancel", desc: "keep dev work", value: "cancel"},
				{label: "discard", desc: "lose uncommitted/un-pushed changes", value: "discard"},
			},
			prevView: m.view,
		}
		m.view = viewSourcePrompt
		return m, nil
	case source.StateLocal:
		m.message = fmt.Sprintf("module %s is local — nothing to toggle", rmName)
		return m, nil
	}
	return m, nil
}

// openPromotePrompt handles the `P` keypress on a unit's detail page.
// Pin-to-current is meaningful in dev and dev-mod (HEAD is on a valid
// commit, work tree is clean). Dev-dirty surfaces a hint to commit
// first; pin and empty are no-ops. No modal — the action writes HEAD's
// tag name (when one exists) or 40-char SHA directly to the unit's .star
// `tag` field.
func (m model) openPromotePrompt(unitName string) (tea.Model, tea.Cmd) {
	u, ok := m.proj.Units[unitName]
	if !ok || u.Class == "image" || u.Class == "container" {
		m.message = fmt.Sprintf("%s has no source dir to pin", unitName)
		return m, nil
	}
	state := m.unitSourceState(unitName)
	switch state {
	case source.StateDev, source.StateDevMod:
		return m.runDevPromote(unitName)
	case source.StateDevDirty:
		m.message = fmt.Sprintf("%s has uncommitted edits — commit or stash first to pin current state", unitName)
		return m, nil
	default:
		m.message = fmt.Sprintf("%s is %s — pin requires dev or dev-mod", unitName, srcStateToken(state))
		return m, nil
	}
}

// updateSourcePrompt handles keyboard input while a dev-mode modal is
// active. j/k or arrow keys move the cursor; enter applies the
// selected option; esc cancels and returns to the previous view.
func (m model) updateSourcePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sourcePrompt == nil {
		// Defensive — an empty prompt with view==viewSourcePrompt
		// should never happen, but if it does, ESC out of it cleanly.
		m.view = viewUnits
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		prev := m.sourcePrompt.prevView
		m.sourcePrompt = nil
		m.view = prev
		return m, nil
	case "up", "k":
		m.moveSourcePromptCursor(-1)
		return m, nil
	case "down", "j":
		m.moveSourcePromptCursor(1)
		return m, nil
	case "enter":
		opt := m.sourcePrompt.options[m.sourcePrompt.cursor]
		if opt.disabled {
			return m, nil
		}
		return m.applySourcePromptChoice(opt.value)
	}
	return m, nil
}

// moveSourcePromptCursor advances the cursor by delta, skipping any
// disabled options so the user can't land on one. Wraps cleanly when
// stepping off either end.
func (m *model) moveSourcePromptCursor(delta int) {
	if m.sourcePrompt == nil || len(m.sourcePrompt.options) == 0 {
		return
	}
	n := len(m.sourcePrompt.options)
	cursor := m.sourcePrompt.cursor
	for range n {
		cursor = (cursor + delta + n) % n
		if !m.sourcePrompt.options[cursor].disabled {
			m.sourcePrompt.cursor = cursor
			return
		}
	}
}

// applySourcePromptChoice runs the action keyed off the prompt kind
// and selected value. The pin → dev flow goes through two prompts
// (scheme then depth); other flows complete in one step. On success,
// refreshes the source-state cache for the target so the SRC column
// repaints to the new state on the next render.
func (m model) applySourcePromptChoice(value string) (tea.Model, tea.Cmd) {
	if m.sourcePrompt == nil {
		return m, nil
	}
	if value == "cancel" {
		prev := m.sourcePrompt.prevView
		m.sourcePrompt = nil
		m.view = prev
		return m, nil
	}
	target := m.sourcePrompt.target
	name := m.sourcePrompt.targetName
	kind := m.sourcePrompt.kind

	switch kind {
	case promptSSHHTTPS:
		// Stage 1 of pin → dev. Carry the SSH choice forward and
		// open the depth picker — don't fire the toggle yet.
		ssh := value == "ssh"
		m.sourcePrompt.kind = promptHistoryDepth
		m.sourcePrompt.chosenSSH = ssh
		m.sourcePrompt.header = fmt.Sprintf(
			"How much history should we fetch for %s?", name)
		m.sourcePrompt.options = depthPromptOptions()
		m.sourcePrompt.cursor = 0 // default to "all history"
		return m, nil

	case promptHistoryDepth:
		// Stage 2 of pin → dev. We have both choices; fire the toggle.
		ssh := m.sourcePrompt.chosenSSH
		opts := depthOptionToFetch(value)
		prevView := m.sourcePrompt.prevView
		m.sourcePrompt = nil
		m.view = prevView
		if target == "unit" {
			return m.runDevToUpstream(name, ssh, opts)
		}
		return m.runModuleToUpstream(name, ssh, opts)

	case promptDiscardDev:
		prevView := m.sourcePrompt.prevView
		m.sourcePrompt = nil
		m.view = prevView
		// Only "discard" lands here (cancel returned above).
		if target == "unit" {
			return m.runDevToPin(name, true)
		}
		return m.runModuleToPin(name, true)

	}
	return m, nil
}

// schemePromptOptions builds the HTTPS / SSH / cancel option list,
// embedding the URL each choice would set as `origin` so the user
// can verify the destination before committing.
//
// SSH is greyed out when the upstream URL doesn't admit a sensible
// rewrite (non-https scheme, empty path) — picking it would still
// "work" by leaving the URL as-is, but the choice is misleading.
func schemePromptOptions(upstreamURL string) []sourcePromptOption {
	httpsURL := upstreamURL
	sshURL, sshOK := previewHTTPSToSSH(upstreamURL)
	sshOpt := sourcePromptOption{label: "SSH", value: "ssh"}
	if sshOK {
		sshOpt.desc = "use " + sshURL
	} else {
		sshOpt.desc = "(no SSH mapping for this URL — pick HTTPS)"
		sshOpt.disabled = true
	}
	return []sourcePromptOption{
		{label: "HTTPS", desc: "use " + httpsURL, value: "https"},
		sshOpt,
		{label: "cancel", desc: "stay pinned", value: "cancel"},
	}
}

// previewHTTPSToSSH mirrors the rewrite in internal/dev.go's
// httpsToSSH so the prompt can show what origin will be set to
// without going through the actual toggle. Kept here (rather than
// exported from internal/dev.go) so the TUI doesn't need write
// access for read-only previews.
func previewHTTPSToSSH(httpsURL string) (string, bool) {
	if !strings.HasPrefix(httpsURL, "https://") {
		return httpsURL, false
	}
	rest := strings.TrimPrefix(httpsURL, "https://")
	slash := strings.IndexByte(rest, '/')
	if slash < 0 || slash == len(rest)-1 {
		return httpsURL, false
	}
	host := rest[:slash]
	path := rest[slash+1:]
	return "git@" + host + ":" + path, true
}

// liveRemoteURL returns `git remote get-url origin` for a module dir.
// Empty when the dir isn't a git repo or origin isn't configured.
func liveRemoteURL(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := runGit(dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// depthFetchSpec is the depth strategy a prompt option resolves to.
// FetchDepth (>0) is passed straight to internal/dev.go's
// DevUpstreamOpts; zero means "all history".
type depthFetchSpec struct {
	FetchDepth int
}

// depthPromptOptions returns the standard fetch-depth menu.
// Cursor 0 ("all history") preserves the legacy behavior — pressing
// Enter through both prompts is identical to the pre-depth flow.
func depthPromptOptions() []sourcePromptOption {
	return []sourcePromptOption{
		{label: "all", desc: "full history (slowest, but `git log` shows everything)", value: "all"},
		{label: "1000", desc: "last 1000 commits — much faster, plenty for most work", value: "depth=1000"},
		{label: "100", desc: "last 100 commits — quickest", value: "depth=100"},
		{label: "cancel", desc: "stay pinned", value: "cancel"},
	}
}

// depthOptionToFetch parses a value emitted by depthPromptOptions
// back into the structured fetch spec. Unknown / "all" → zero spec.
func depthOptionToFetch(value string) depthFetchSpec {
	if n, ok := strings.CutPrefix(value, "depth="); ok {
		var v int
		fmt.Sscanf(n, "%d", &v)
		return depthFetchSpec{FetchDepth: v}
	}
	return depthFetchSpec{}
}

// runDevToUpstream backgrounds DevToUpstream in a goroutine and parks
// the TUI in viewSourceProgress with a spinner. The op is potentially
// slow — `git fetch --unshallow` against a multi-GB repo can run for
// tens of seconds — so blocking the Update loop would freeze the UI.
func (m model) runDevToUpstream(unitName string, ssh bool, depth depthFetchSpec) (tea.Model, tea.Cmd) {
	u, ok := m.proj.Units[unitName]
	if !ok {
		m.message = fmt.Sprintf("unit %s not found", unitName)
		return m, nil
	}
	scope := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	prevView := m.view
	m.view = viewSourceProgress
	m.sourceOp = newSourceOp(targetUnit, unitName,
		fmt.Sprintf("Fetching %s for %s", depthLabel(depth), unitName),
		prevView)
	opts := yoe.DevUpstreamOpts{SSH: ssh, FetchDepth: depth.FetchDepth}
	cmd := func() tea.Msg {
		err := yoe.DevToUpstream(m.projectDir, scope, u, opts)
		return sourceOpDoneMsg{
			target:     targetUnit,
			name:       unitName,
			err:        wrapDevErr(err, "dev mode"),
			successMsg: fmt.Sprintf("%s switched to dev mode", unitName),
		}
	}
	return m, tea.Batch(m.sourceOp.spinner.Tick, cmd)
}

// depthLabel renders a fetch spec as a short user-facing phrase for
// the spinner label ("full history", "last 1000 commits"). Keeps the
// spinner explanatory without duplicating the option list verbiage.
func depthLabel(d depthFetchSpec) string {
	if d.FetchDepth > 0 {
		return fmt.Sprintf("last %d commits", d.FetchDepth)
	}
	return "full upstream history (this may take a while)"
}

// runDevToPin runs DevToPin in the background. Pin transitions are
// usually quick (rm -rf the src dir) but big workspaces can take a
// few seconds, so we keep the same spinner machinery for parity with
// the upstream toggle.
func (m model) runDevToPin(unitName string, force bool) (tea.Model, tea.Cmd) {
	u, ok := m.proj.Units[unitName]
	if !ok {
		m.message = fmt.Sprintf("unit %s not found", unitName)
		return m, nil
	}
	scope := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	prevView := m.view
	m.view = viewSourceProgress
	m.sourceOp = newSourceOp(targetUnit, unitName,
		fmt.Sprintf("Resetting %s to its pinned ref", unitName), prevView)
	cmd := func() tea.Msg {
		err := yoe.DevToPin(m.projectDir, scope, u, force)
		return sourceOpDoneMsg{
			target:     targetUnit,
			name:       unitName,
			err:        wrapDevErr(err, "pin"),
			successMsg: fmt.Sprintf("%s switched back to pin", unitName),
		}
	}
	return m, tea.Batch(m.sourceOp.spinner.Tick, cmd)
}

// runDevPromote rewrites the .star pin from HEAD. Local-only, fast,
// but routed through the same background path so the user always sees
// a "working…" hint instead of a frozen screen.
func (m model) runDevPromote(unitName string) (tea.Model, tea.Cmd) {
	u, ok := m.proj.Units[unitName]
	if !ok {
		m.message = fmt.Sprintf("unit %s not found", unitName)
		return m, nil
	}
	scope := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	prevView := m.view
	m.view = viewSourceProgress
	m.sourceOp = newSourceOp(targetUnit, unitName,
		fmt.Sprintf("Pinning %s to current HEAD", unitName), prevView)
	cmd := func() tea.Msg {
		err := yoe.DevPromoteToPin(m.projectDir, scope, u)
		return sourceOpDoneMsg{
			target:     targetUnit,
			name:       unitName,
			err:        wrapDevErr(err, "pin"),
			successMsg: fmt.Sprintf("%s pinned to current HEAD", unitName),
		}
	}
	return m, tea.Batch(m.sourceOp.spinner.Tick, cmd)
}

// runModuleToUpstream / runModuleToPin mirror the unit-side async
// dispatch. ModuleToUpstream's `git fetch --unshallow` is the most
// common reason the TUI ever feels slow, so the spinner is most
// valuable here.
func (m model) runModuleToUpstream(rmName string, ssh bool, depth depthFetchSpec) (tea.Model, tea.Cmd) {
	rm, ok := m.findModule(rmName)
	if !ok {
		m.message = fmt.Sprintf("module %s not found", rmName)
		return m, nil
	}
	prevView := m.view
	m.view = viewSourceProgress
	m.sourceOp = newSourceOp(targetModule, rmName,
		fmt.Sprintf("Fetching %s for module %s", depthLabel(depth), rmName),
		prevView)
	opts := module.ModuleUpstreamOpts{SSH: ssh, FetchDepth: depth.FetchDepth}
	cmd := func() tea.Msg {
		err := module.ModuleToUpstream(rm, opts)
		return sourceOpDoneMsg{
			target:     targetModule,
			name:       rmName,
			err:        wrapDevErr(err, "module dev"),
			successMsg: fmt.Sprintf("module %s switched to dev mode", rmName),
		}
	}
	return m, tea.Batch(m.sourceOp.spinner.Tick, cmd)
}

func (m model) runModuleToPin(rmName string, force bool) (tea.Model, tea.Cmd) {
	rm, ok := m.findModule(rmName)
	if !ok {
		m.message = fmt.Sprintf("module %s not found", rmName)
		return m, nil
	}
	prevView := m.view
	m.view = viewSourceProgress
	m.sourceOp = newSourceOp(targetModule, rmName,
		fmt.Sprintf("Resetting module %s to its declared ref", rmName), prevView)
	cmd := func() tea.Msg {
		err := module.ModuleToPin(rm, force)
		return sourceOpDoneMsg{
			target:     targetModule,
			name:       rmName,
			err:        wrapDevErr(err, "module pin"),
			successMsg: fmt.Sprintf("module %s switched back to pin", rmName),
		}
	}
	return m, tea.Batch(m.sourceOp.spinner.Tick, cmd)
}

// newSourceOp constructs a sourceOp with a freshly initialized
// spinner. Caller is responsible for kicking off the first tick via
// op.spinner.Tick — usually batched with the work goroutine cmd.
func newSourceOp(target sourceTarget, name, label string, prevView viewKind) *sourceOp {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#e8863a")) // amber, matches yoe logo
	return &sourceOp{
		target:   target,
		name:     name,
		label:    label,
		spinner:  s,
		prevView: prevView,
	}
}

// wrapDevErr formats a dev-mode action's error with a human prefix
// for the status strip. nil errors pass through unchanged.
func wrapDevErr(err error, action string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s failed: %w", action, err)
}

// viewSourceProgress renders the in-flight dev-mode action: the
// spinner, a one-line description of what's happening, and a hint
// about why we're sitting here.
func (m model) viewSourceProgress() string {
	if m.sourceOp == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n  ")
	b.WriteString(titleStyle.Render("Working…"))
	b.WriteString("\n\n  ")
	b.WriteString(m.sourceOp.spinner.View())
	b.WriteString("  ")
	b.WriteString(m.sourceOp.label)
	b.WriteString("\n\n  ")
	b.WriteString(dimStyle.Render("(this can take a moment for large repos — please wait)"))
	return b.String()
}

// armWatcherFromInitialStates seeds the source watcher with every
// unit/module that's already in dev* state on startup. The cache
// helpers populate the underlying maps as a side effect, which is
// what we want — the watcher and the SRC column should agree on
// initial membership.
//
// Seeds both the SRC-column state cache and the watcher's "last"
// observation from DetectState (the live working-tree state), not
// from BuildMeta's collapsed toggle decision. Without this, the
// watcher's first poll sees the live dev-dirty state, compares it to
// the cached "dev" from BuildMeta, treats it as a fresh state change,
// and clears the just-loaded [cached] indicator.
func (m *model) armWatcherFromInitialStates() {
	if m.srcWatcher == nil {
		return
	}
	for name := range m.proj.Units {
		srcDir := m.unitSrcDir(name)
		cached := m.persistedUnitSourceState(name)
		live, _ := source.DetectState(srcDir, cached)
		if m.unitSrcStates != nil {
			m.unitSrcStates[name] = live
		}
		if source.IsDev(live) {
			m.srcWatcher.Arm(targetUnit, name, srcDir, live)
		}
	}
	for _, rm := range m.proj.ResolvedModules {
		state := m.moduleSourceState(rm)
		if source.IsDev(state) {
			m.srcWatcher.Arm(targetModule, rm.Name, rm.CloneDir, state)
		}
	}
}

// findModule returns the resolved module by name and a found-bool.
func (m model) findModule(name string) (yoestar.ResolvedModule, bool) {
	for _, r := range m.proj.ResolvedModules {
		if r.Name == name {
			return r, true
		}
	}
	return yoestar.ResolvedModule{}, false
}

// invalidateUnitState refreshes the cached source state for a unit
// from the live working tree (not just BuildMeta's persisted toggle
// decision) and reconciles the background watcher: if the new state is
// dev*, arm; otherwise disarm.
//
// Seeding from DetectState directly — rather than from BuildMeta's
// collapsed "dev" / "pin" value — prevents the watcher's first
// post-build poll from "discovering" dev-mod or dev-dirty as a state
// change and incorrectly clearing the just-set [cached] status. The
// live state is what the SRC column should show anyway.
func (m model) invalidateUnitState(name string) {
	if m.unitSrcStates != nil {
		delete(m.unitSrcStates, name)
	}
	// Tests construct models directly without populating proj — bail
	// out early in that case rather than dereferencing nil through
	// unitSrcDir.
	if m.proj == nil {
		return
	}
	if _, ok := m.proj.Units[name]; !ok {
		return
	}
	srcDir := m.unitSrcDir(name)
	cached := m.persistedUnitSourceState(name)
	live, _ := source.DetectState(srcDir, cached)
	if m.unitSrcStates != nil {
		m.unitSrcStates[name] = live
	}
	if m.srcWatcher == nil {
		return
	}
	if source.IsDev(live) {
		m.srcWatcher.Arm(targetUnit, name, srcDir, live)
	} else {
		m.srcWatcher.Disarm(targetUnit, name)
	}
}

// persistedUnitSourceState reads the BuildMeta-persisted toggle
// decision for a unit ("pin" / "dev" / empty). Used as the `cached`
// argument to DetectState so a clean checkout is disambiguated
// correctly.
func (m model) persistedUnitSourceState(name string) source.State {
	u, ok := m.proj.Units[name]
	if !ok {
		return source.StateEmpty
	}
	sd := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	buildDir := build.UnitBuildDir(m.projectDir, sd, name)
	if meta := build.ReadMeta(buildDir); meta != nil {
		return source.State(meta.SourceState)
	}
	return source.StateEmpty
}

// invalidateModuleState drops the cached source state for a module
// and reconciles the watcher membership.
func (m model) invalidateModuleState(name string) {
	if m.moduleSrcStates != nil {
		delete(m.moduleSrcStates, name)
	}
	if m.srcWatcher == nil {
		return
	}
	rm, ok := m.findModule(name)
	if !ok {
		return
	}
	state := m.moduleSourceState(rm)
	if source.IsDev(state) {
		m.srcWatcher.Arm(targetModule, name, rm.CloneDir, state)
	} else {
		m.srcWatcher.Disarm(targetModule, name)
	}
}

// modifiedCount returns a rough "how much will be discarded?" count
// for the discard-confirm message. For dev-dirty it's the number of
// porcelain entries (untracked + modified). For dev-mod it's the
// number of commits beyond upstream. Returns 0 on any git error so
// the prompt still shows even if we can't sharpen the wording.
func (m model) modifiedCount(unitName string) int {
	srcDir := m.unitSrcDir(unitName)
	if srcDir == "" {
		return 0
	}
	state := m.unitSourceState(unitName)
	switch state {
	case source.StateDevDirty:
		out, err := runGit(srcDir, "status", "--porcelain")
		if err != nil {
			return 0
		}
		return strings.Count(strings.TrimSpace(out), "\n") + 1
	case source.StateDevMod:
		out, err := runGit(srcDir, "rev-list", "--count", "upstream..HEAD")
		if err != nil {
			return 0
		}
		var n int
		fmt.Sscanf(strings.TrimSpace(out), "%d", &n)
		return n
	}
	return 0
}

// runGit runs git in dir and returns combined output. Mirrors
// internal/dev.go's gitCmd shape; kept private to this file so we
// don't need to widen yoe's API surface.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// viewSourcePrompt renders the active modal: a header, a vertical
// list of options (one per row, with secondary descriptions in dim),
// and a footer hint. Disabled options are dimmed and prefixed
// differently so the user knows they aren't selectable.
func (m model) viewSourcePrompt() string {
	if m.sourcePrompt == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n  ")
	b.WriteString(titleStyle.Render(m.sourcePrompt.header))
	b.WriteString("\n")
	if m.sourcePrompt.subheader != "" {
		b.WriteString("  ")
		b.WriteString(dimStyle.Render(m.sourcePrompt.subheader))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	for i, opt := range m.sourcePrompt.options {
		marker := "  "
		labelStyle := dimStyle
		if i == m.sourcePrompt.cursor && !opt.disabled {
			marker = "→ "
			labelStyle = selectedStyle
		}
		if opt.disabled {
			labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
		}
		b.WriteString("  ")
		b.WriteString(marker)
		b.WriteString(labelStyle.Render(fmt.Sprintf("%-8s", opt.label)))
		if opt.desc != "" {
			b.WriteString("  ")
			b.WriteString(dimStyle.Render(opt.desc))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpItem{
		{"j/k", "move"}, {"enter", "apply"}, {"esc", "cancel"},
	}))
	return b.String()
}

