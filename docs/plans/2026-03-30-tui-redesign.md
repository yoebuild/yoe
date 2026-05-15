# TUI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the TUI to show a unit list with inline build status,
background builds, live log tail, and quick-action keys for editing/diagnosing.

**Architecture:** Single-file Bubble Tea app (`internal/tui/app.go`) with two
views (unit list + log detail), a 500ms ticker for flashing/log refresh,
background build subprocesses that send `buildDoneMsg` on completion, and
`tea.Exec` for suspend/resume of external tools.

**Tech Stack:** Go, Bubble Tea v1.3.10, Lipgloss v1.1.0

**Spec:** `docs/superpowers/specs/2026-03-30-tui-redesign-design.md`

---

### Task 1: Export `IsBuildCached` from build package

The TUI needs to check cache status on startup. Currently `isBuildCached` is
unexported.

**Files:**

- Modify: `internal/build/executor.go:308-318`

- [ ] **Step 1: Export the cache functions**

In `internal/build/executor.go`, rename the unexported functions and add a new
public helper:

```go
// CacheMarkerPath returns the path to a unit's cache marker file.
func CacheMarkerPath(projectDir, name string) string {
	return filepath.Join(projectDir, "build", name, ".yoe-hash")
}

// IsBuildCached returns true if the unit has a valid cache marker matching hash.
func IsBuildCached(projectDir, name, hash string) bool {
	data, err := os.ReadFile(CacheMarkerPath(projectDir, name))
	if err != nil {
		return false
	}
	return string(data) == hash
}

// HasBuildLog returns true if a build log exists for this unit.
func HasBuildLog(projectDir, name string) bool {
	_, err := os.Stat(filepath.Join(projectDir, "build", name, "build.log"))
	return err == nil
}
```

Update all internal callers (`isBuildCached` → `IsBuildCached`,
`cacheMarkerPath` → `CacheMarkerPath`, `writeCacheMarker` calls use
`CacheMarkerPath`).

- [ ] **Step 2: Run tests**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/build/...`

Expected: all tests pass.

- [ ] **Step 3: Commit**

```
git add internal/build/executor.go
git commit -m "build: export IsBuildCached and cache helpers for TUI"
```

---

### Task 2: Rewrite TUI — model, types, and Init

Replace the entire `internal/tui/app.go` with the new model structure, types,
styles, and Init function.

**Files:**

- Rewrite: `internal/tui/app.go`

- [ ] **Step 1: Write the new app.go with types and Init**

Replace `internal/tui/app.go` entirely:

```go
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/YoeDistro/yoe-ng/internal/build"
	"github.com/YoeDistro/yoe-ng/internal/resolve"
	yoestar "github.com/YoeDistro/yoe-ng/internal/starlark"
)

// --- Styles ---

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	failedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	buildingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	cachedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// --- Types ---

type unitStatus int

const (
	statusNone unitStatus = iota
	statusCached
	statusBuilding
	statusFailed
)

type view int

const (
	viewUnits view = iota
	viewDetail
)

type model struct {
	proj       *yoestar.Project
	projectDir string
	units      []string
	hashes     map[string]string
	statuses   map[string]unitStatus
	cursor     int
	view       view
	detailUnit string
	logLines   []string
	tick       bool
	width      int
	height     int
	message    string
	builds     map[string]*exec.Cmd
}

// --- Messages ---

type buildDoneMsg struct {
	unit string
	err  error
}

type tickMsg time.Time

func doTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// --- Run ---

func Run(proj *yoestar.Project, projectDir string) error {
	units := sortedKeys(proj.Units)

	// Compute hashes for initial status detection
	dag, err := resolve.BuildDAG(proj)
	if err != nil {
		return fmt.Errorf("building DAG: %w", err)
	}
	hashes, err := resolve.ComputeAllHashes(dag, build.Arch())
	if err != nil {
		return fmt.Errorf("computing hashes: %w", err)
	}

	// Detect initial statuses
	statuses := make(map[string]unitStatus, len(units))
	for _, name := range units {
		hash := hashes[name]
		if build.IsBuildCached(projectDir, name, hash) {
			statuses[name] = statusCached
		} else if build.HasBuildLog(projectDir, name) {
			statuses[name] = statusFailed
		}
	}

	m := model{
		proj:       proj,
		projectDir: projectDir,
		units:      units,
		hashes:     hashes,
		statuses:   statuses,
		builds:     make(map[string]*exec.Cmd),
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func (m model) Init() tea.Cmd {
	return doTick()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
```

- [ ] **Step 2: Verify it compiles (Update will be added next task)**

Add stub Update and View to make it compile:

```go
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

func (m model) View() string {
	return "TUI loading..."
}
```

Run: `cd /scratch4/yoe/yoe-ng && go build ./internal/tui/...`

Expected: compiles without errors.

- [ ] **Step 3: Commit**

```
git add internal/tui/app.go
git commit -m "tui: rewrite model, types, and Init with status detection"
```

---

### Task 3: Unit list view (View function)

**Files:**

- Modify: `internal/tui/app.go`

- [ ] **Step 1: Replace the stub View with the unit list renderer**

Replace the `View()` stub:

```go
func (m model) View() string {
	switch m.view {
	case viewDetail:
		return m.viewDetail()
	default:
		return m.viewUnits()
	}
}

func (m model) viewUnits() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render("  Yoe-NG"))
	selMachine := m.proj.Defaults.Machine
	selImage := m.proj.Defaults.Image
	b.WriteString(fmt.Sprintf("  Machine: %s  Image: %s\n\n",
		headerStyle.Render(selMachine),
		headerStyle.Render(selImage)))

	// Column headers
	b.WriteString(fmt.Sprintf("  %-22s %-12s %s\n",
		headerStyle.Render("NAME"),
		headerStyle.Render("CLASS"),
		headerStyle.Render("STATUS")))

	// Scrollable unit list
	visible := m.height - 7 // header + footer
	if visible < 5 {
		visible = 5
	}
	start := 0
	if m.cursor >= visible {
		start = m.cursor - visible + 1
	}
	end := start + visible
	if end > len(m.units) {
		end = len(m.units)
	}

	for i := start; i < end; i++ {
		name := m.units[i]
		class := ""
		if u, ok := m.proj.Units[name]; ok {
			class = u.Class
		}

		cursor := "  "
		nameStyle := dimStyle
		if i == m.cursor {
			cursor = "→ "
			nameStyle = selectedStyle
		}

		status := m.renderStatus(name)
		b.WriteString(fmt.Sprintf("%s%-22s %-12s %s\n",
			cursor, nameStyle.Render(name), dimStyle.Render(class), status))
	}

	// Footer
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  b build  e edit  d diagnose  l log  a add  enter details  B all  q quit"))
	b.WriteString("\n")

	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  " + m.message))
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) renderStatus(name string) string {
	switch m.statuses[name] {
	case statusCached:
		return cachedStyle.Render("● cached")
	case statusBuilding:
		if m.tick {
			return buildingStyle.Render("▌building...")
		}
		return "            " // blank on off-tick = flash
	case statusFailed:
		return failedStyle.Render("● failed")
	default:
		return ""
	}
}

func (m model) viewDetail() string {
	var b strings.Builder

	status := ""
	switch m.statuses[m.detailUnit] {
	case statusBuilding:
		status = buildingStyle.Render("(building...)")
	case statusFailed:
		status = failedStyle.Render("(failed)")
	case statusCached:
		status = cachedStyle.Render("(cached)")
	}

	b.WriteString(fmt.Sprintf("  ← %s %s\n\n", headerStyle.Render(m.detailUnit), status))

	if len(m.logLines) == 0 {
		b.WriteString(dimStyle.Render("  No build log available.\n"))
	} else {
		for _, line := range m.logLines {
			b.WriteString("  " + line + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  esc back  b build  d diagnose"))
	b.WriteString("\n")

	return b.String()
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /scratch4/yoe/yoe-ng && go build ./internal/tui/...`

- [ ] **Step 3: Commit**

```
git add internal/tui/app.go
git commit -m "tui: add unit list and detail views"
```

---

### Task 4: Update function — navigation, builds, tick, suspend/resume

**Files:**

- Modify: `internal/tui/app.go`

- [ ] **Step 1: Replace the stub Update with the full implementation**

Replace the `Update()` stub:

```go
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		m.tick = !m.tick
		// Refresh log lines if in detail view
		if m.view == viewDetail {
			m.logLines = readLogTail(m.projectDir, m.detailUnit, m.height-6)
		}
		return m, doTick()

	case buildDoneMsg:
		delete(m.builds, msg.unit)
		if msg.err != nil {
			m.statuses[msg.unit] = statusFailed
			m.message = fmt.Sprintf("%s build failed", msg.unit)
		} else {
			m.statuses[msg.unit] = statusCached
			m.message = fmt.Sprintf("%s build complete", msg.unit)
		}
		return m, nil

	case tea.KeyMsg:
		m.message = ""
		switch m.view {
		case viewUnits:
			return m.updateUnits(msg)
		case viewDetail:
			return m.updateDetail(msg)
		}
	}

	return m, nil
}

func (m model) updateUnits(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		// Kill running builds
		for _, cmd := range m.builds {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		}
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case "down", "j":
		if m.cursor < len(m.units)-1 {
			m.cursor++
		}
		return m, nil

	case "enter":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			m.view = viewDetail
			m.detailUnit = name
			m.logLines = readLogTail(m.projectDir, name, m.height-6)
		}
		return m, nil

	case "b":
		return m.startBuild(m.units[m.cursor])

	case "B":
		var cmds []tea.Cmd
		for _, name := range m.units {
			if m.statuses[name] != statusBuilding {
				updated, cmd := m.startBuild(name)
				m = updated
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return m, tea.Batch(cmds...)

	case "e":
		return m.execEditor(m.units[m.cursor])

	case "l":
		return m.execLog(m.units[m.cursor])

	case "d":
		return m.execDiagnose(m.units[m.cursor])

	case "a":
		return m.execAddUnit()
	}

	return m, nil
}

func (m model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.view = viewUnits
		m.logLines = nil
		return m, nil

	case "b":
		return m.startBuild(m.detailUnit)

	case "d":
		return m.execDiagnose(m.detailUnit)
	}

	return m, nil
}
```

- [ ] **Step 2: Add the build launcher**

```go
func (m model) startBuild(name string) (model, tea.Cmd) {
	if m.statuses[name] == statusBuilding {
		m.message = fmt.Sprintf("%s is already building", name)
		return m, nil
	}

	m.statuses[name] = statusBuilding

	cmd := exec.Command("yoe", "build", "--force", name)
	cmd.Dir = m.projectDir
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		m.statuses[name] = statusFailed
		m.message = fmt.Sprintf("failed to start build: %v", err)
		return m, nil
	}

	m.builds[name] = cmd

	unit := name
	return m, func() tea.Msg {
		err := cmd.Wait()
		return buildDoneMsg{unit: unit, err: err}
	}
}
```

- [ ] **Step 3: Add suspend/resume helpers**

```go
func (m model) execEditor(name string) (model, tea.Cmd) {
	path := findUnitFile(m.projectDir, name)
	if path == "" {
		m.message = fmt.Sprintf("unit file not found for %s", name)
		return m, nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		return tea.KeyMsg{} // no-op msg to resume
	})
}

func (m model) execLog(name string) (model, tea.Cmd) {
	logPath := filepath.Join(m.projectDir, "build", name, "build.log")
	if _, err := os.Stat(logPath); err != nil {
		m.message = fmt.Sprintf("no build log for %s", name)
		return m, nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, logPath)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		return tea.KeyMsg{}
	})
}

func (m model) execDiagnose(name string) (model, tea.Cmd) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		m.message = "claude not found in PATH"
		return m, nil
	}
	c := exec.Command(claudePath, "/diagnose", name)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		return tea.KeyMsg{}
	})
}

func (m model) execAddUnit() (model, tea.Cmd) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		m.message = "claude not found in PATH"
		return m, nil
	}
	c := exec.Command(claudePath, "/new-unit")
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		return tea.KeyMsg{}
	})
}
```

- [ ] **Step 4: Add helper functions**

```go
func readLogTail(projectDir, name string, lines int) []string {
	if lines <= 0 {
		lines = 20
	}
	logPath := filepath.Join(projectDir, "build", name, "build.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return all
}

func findUnitFile(projectDir, name string) string {
	// Search layers for the unit's .star file
	layersDir := filepath.Join(projectDir, "layers")
	var result string
	filepath.WalkDir(layersDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == name+".star" && strings.Contains(path, "units") {
			result = path
			return filepath.SkipAll
		}
		return nil
	})
	if result != "" {
		return result
	}
	// Also check external layer paths from the project
	// Walk all directories looking for <name>.star in units/ subdirs
	filepath.WalkDir(projectDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == name+".star" && strings.Contains(path, "units") {
			result = path
			return filepath.SkipAll
		}
		return nil
	})
	return result
}
```

- [ ] **Step 5: Verify it compiles**

Run: `cd /scratch4/yoe/yoe-ng && go build ./internal/tui/...`

- [ ] **Step 6: Commit**

```
git add internal/tui/app.go
git commit -m "tui: add Update with background builds and suspend/resume"
```

---

### Task 5: Integration test — manual verification

**Files:** none (manual testing)

- [ ] **Step 1: Build the binary**

Run: `cd /scratch4/yoe/yoe-ng && source envsetup.sh && yoe_build`

- [ ] **Step 2: Launch the TUI in the e2e-project**

Run: `cd testdata/e2e-project && ../../yoe`

Verify:

- Unit list shows all units with correct statuses (cached/failed/none)
- `j`/`k` navigates the list
- Pressing `b` on a unit starts a background build (status flashes green)
- When build completes, status changes to cached or failed (red)
- Pressing `Enter` shows live log tail
- `Esc` returns to unit list
- `e` opens the unit file in `$EDITOR` and returns
- `l` opens the build log in `$EDITOR` and returns
- `q` exits cleanly

- [ ] **Step 3: Run existing tests**

Run: `cd /scratch4/yoe/yoe-ng && go test ./...`

Expected: all tests pass.

- [ ] **Step 4: Commit any fixes**

If manual testing revealed issues, commit the fixes.

---

### Task 6: Update docs

**Files:**

- Modify: `docs/yoe-tool.md`

- [ ] **Step 1: Update the `yoe` (no args) section in yoe-tool.md**

Replace the existing TUI section (currently shows the old menu mockup) with the
new unit list mockup from the spec. Update key bindings documentation.

- [ ] **Step 2: Commit**

```
git add docs/yoe-tool.md
git commit -m "docs: update TUI section with new unit list interface"
```
