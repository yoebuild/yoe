package tui

import (
	"fmt"
	"os/exec"
	"strings"

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
		m.sourcePrompt = &sourcePrompt{
			kind:       promptSSHHTTPS,
			target:     "unit",
			targetName: unitName,
			header:     fmt.Sprintf("Switch %s to dev mode (track upstream)?", unitName),
			options: []sourcePromptOption{
				{label: "HTTPS", desc: "use the original https:// URL", value: "https"},
				{label: "SSH", desc: "rewrite https:// → git@host:… (push access)", value: "ssh"},
				{label: "cancel", desc: "stay pinned", value: "cancel"},
			},
			prevView: m.view,
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
		m.sourcePrompt = &sourcePrompt{
			kind:       promptSSHHTTPS,
			target:     "module",
			targetName: rmName,
			header:     fmt.Sprintf("Switch module %s to dev mode?", rmName),
			options: []sourcePromptOption{
				{label: "HTTPS", desc: "keep current https:// origin", value: "https"},
				{label: "SSH", desc: "rewrite to git@host:… (push access)", value: "ssh"},
				{label: "cancel", desc: "stay pinned", value: "cancel"},
			},
			prevView: m.view,
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
// Promote is only meaningful in dev-mod (HEAD has commits beyond the
// declared upstream and the work tree is clean); other states surface
// a hint instead of a no-op.
func (m model) openPromotePrompt(unitName string) (tea.Model, tea.Cmd) {
	u, ok := m.proj.Units[unitName]
	if !ok || u.Class == "image" || u.Class == "container" {
		m.message = fmt.Sprintf("%s has no source dir to promote", unitName)
		return m, nil
	}
	state := m.unitSourceState(unitName)
	if state != source.StateDevMod {
		m.message = fmt.Sprintf("%s is %s — promote requires dev-mod (commit your work first)", unitName, srcStateToken(state))
		return m, nil
	}
	srcDir := m.unitSrcDir(unitName)
	hasTag := headHasTag(srcDir)
	branch := headBranch(srcDir)
	branchOpt := sourcePromptOption{
		label: "branch", desc: "pin to current branch (mutable)", value: "branch",
	}
	if branch == "HEAD" || branch == "" {
		branchOpt.disabled = true
		branchOpt.desc = "(detached HEAD — no branch to pin)"
	} else {
		branchOpt.desc = fmt.Sprintf("pin to branch '%s' (mutable)", branch)
	}
	tagOpt := sourcePromptOption{
		label: "tag", desc: "pin to git tag at HEAD", value: "tag",
	}
	if !hasTag {
		tagOpt.disabled = true
		tagOpt.desc = "(HEAD has no tag — pick hash or branch)"
	}
	m.sourcePrompt = &sourcePrompt{
		kind:       promptPinKind,
		target:     "unit",
		targetName: unitName,
		header:     fmt.Sprintf("Promote %s's HEAD to its .star pin", unitName),
		options: []sourcePromptOption{
			tagOpt,
			{label: "hash", desc: "pin to the 40-char SHA at HEAD (most reproducible)", value: "hash"},
			branchOpt,
			{label: "cancel", desc: "leave the unit in dev-mod", value: "cancel"},
		},
		prevView: m.view,
	}
	m.view = viewSourcePrompt
	return m, nil
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
// and selected value. On success, refreshes the source-state cache
// for the target so the SRC column repaints to the new state on the
// next render.
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
	prevView := m.sourcePrompt.prevView
	m.sourcePrompt = nil
	m.view = prevView

	switch kind {
	case promptSSHHTTPS:
		ssh := value == "ssh"
		if target == "unit" {
			return m.runDevToUpstream(name, ssh)
		}
		return m.runModuleToUpstream(name, ssh)
	case promptDiscardDev:
		// Only "discard" lands here (cancel returned above).
		if target == "unit" {
			return m.runDevToPin(name, true)
		}
		return m.runModuleToPin(name, true)
	case promptPinKind:
		return m.runDevPromote(name, value)
	}
	return m, nil
}

// runDevToUpstream calls the unit-side dev toggle and reports outcome.
func (m model) runDevToUpstream(unitName string, ssh bool) (tea.Model, tea.Cmd) {
	u, ok := m.proj.Units[unitName]
	if !ok {
		m.message = fmt.Sprintf("unit %s not found", unitName)
		return m, nil
	}
	scope := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	if err := yoe.DevToUpstream(m.projectDir, scope, u, ssh); err != nil {
		m.message = fmt.Sprintf("dev mode failed: %v", err)
		return m, nil
	}
	m.invalidateUnitState(unitName)
	m.message = fmt.Sprintf("%s switched to dev mode", unitName)
	return m, nil
}

// runDevToPin calls the unit-side pin toggle. force=true skips the
// "would discard work" guard inside DevToPin — the modal already
// asked for explicit consent.
func (m model) runDevToPin(unitName string, force bool) (tea.Model, tea.Cmd) {
	u, ok := m.proj.Units[unitName]
	if !ok {
		m.message = fmt.Sprintf("unit %s not found", unitName)
		return m, nil
	}
	scope := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	if err := yoe.DevToPin(m.projectDir, scope, u, force); err != nil {
		m.message = fmt.Sprintf("pin failed: %v", err)
		return m, nil
	}
	m.invalidateUnitState(unitName)
	m.message = fmt.Sprintf("%s switched back to pin", unitName)
	return m, nil
}

// runDevPromote captures HEAD into the .star and clears dev-mod state.
func (m model) runDevPromote(unitName, kindValue string) (tea.Model, tea.Cmd) {
	u, ok := m.proj.Units[unitName]
	if !ok {
		m.message = fmt.Sprintf("unit %s not found", unitName)
		return m, nil
	}
	var kind yoe.PinKind
	switch kindValue {
	case "tag":
		kind = yoe.PinKindTag
	case "hash":
		kind = yoe.PinKindHash
	case "branch":
		kind = yoe.PinKindBranch
	default:
		m.message = fmt.Sprintf("unknown promote kind %q", kindValue)
		return m, nil
	}
	scope := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	if err := yoe.DevPromoteToPin(m.projectDir, scope, u, kind); err != nil {
		m.message = fmt.Sprintf("promote failed: %v", err)
		return m, nil
	}
	m.invalidateUnitState(unitName)
	m.message = fmt.Sprintf("%s promoted to %s pin", unitName, kindValue)
	return m, nil
}

// runModuleToUpstream / runModuleToPin mirror the unit-side helpers.
func (m model) runModuleToUpstream(rmName string, ssh bool) (tea.Model, tea.Cmd) {
	rm, ok := m.findModule(rmName)
	if !ok {
		m.message = fmt.Sprintf("module %s not found", rmName)
		return m, nil
	}
	if err := module.ModuleToUpstream(rm, ssh); err != nil {
		m.message = fmt.Sprintf("module dev failed: %v", err)
		return m, nil
	}
	m.invalidateModuleState(rmName)
	m.message = fmt.Sprintf("module %s switched to dev mode", rmName)
	return m, nil
}

func (m model) runModuleToPin(rmName string, force bool) (tea.Model, tea.Cmd) {
	rm, ok := m.findModule(rmName)
	if !ok {
		m.message = fmt.Sprintf("module %s not found", rmName)
		return m, nil
	}
	if err := module.ModuleToPin(rm, force); err != nil {
		m.message = fmt.Sprintf("module pin failed: %v", err)
		return m, nil
	}
	m.invalidateModuleState(rmName)
	m.message = fmt.Sprintf("module %s switched back to pin", rmName)
	return m, nil
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

// invalidateUnitState drops the cached source state for a unit so the
// next render re-reads BuildMeta.SourceState.
func (m model) invalidateUnitState(name string) {
	if m.unitSrcStates != nil {
		delete(m.unitSrcStates, name)
	}
}

// invalidateModuleState drops the cached source state for a module.
func (m model) invalidateModuleState(name string) {
	if m.moduleSrcStates != nil {
		delete(m.moduleSrcStates, name)
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

// headHasTag reports whether HEAD has at least one tag pointing at it.
// Used to gray out the "tag" option in the promote prompt.
func headHasTag(srcDir string) bool {
	if srcDir == "" {
		return false
	}
	out, err := runGit(srcDir, "tag", "--points-at", "HEAD")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// headBranch returns the current branch name in srcDir, or "HEAD"
// when detached. Empty string on git error.
func headBranch(srcDir string) string {
	if srcDir == "" {
		return ""
	}
	out, err := runGit(srcDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
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
	b.WriteString("\n\n")
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

