package tui

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	yoe "github.com/yoebuild/yoe/internal"
	"github.com/yoebuild/yoe/internal/build"
	"github.com/yoebuild/yoe/internal/device"
	"github.com/yoebuild/yoe/internal/resolve"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
	"github.com/yoebuild/yoe/internal/tui/query"
)

// Styles
var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e8863a")).Background(lipgloss.Color("#000000"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5fff5f"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cachedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // blue
	failedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	buildingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	waitingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow

	// Query-related styles
	queryDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	queryActiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	queryErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	// Subtle per-class colors for unselected units
	classUnitStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))   // muted blue
	classImageStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))  // muted magenta
	classContainerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))   // muted cyan
)

// Package-level program reference for sending messages from goroutines.
var tuiProgram *tea.Program

// Views
type viewKind int

const (
	viewUnits viewKind = iota
	viewDetail
	viewSetup
	viewFlash
	viewDeploy
)

// Flash view stages
type flashStage int

const (
	flashSelect  flashStage = iota // picking a device
	flashConfirm                   // y/N confirmation
	flashWriting                   // write in progress
	flashDone                      // success
	flashError                     // failed
)

// Deploy view stages
type deployStage int

const (
	deployHostInput deployStage = iota // editing the host field
	deployRunning                      // build + ssh + apk add in progress
	deployDone                         // success
	deployError                        // failed
)

// Unit status
type unitStatus int

const (
	statusNone unitStatus = iota
	statusCached
	statusWaiting  // queued, deps building first
	statusBuilding // actively compiling
	statusFailed
)

// statusKey maps the TUI's enum to the lowercase strings the query
// language exposes via `status:`.
func statusKey(s unitStatus) string {
	switch s {
	case statusCached:
		return "cached"
	case statusBuilding:
		return "building"
	case statusWaiting:
		return "pending"
	case statusFailed:
		return "failed"
	default:
		return ""
	}
}

// Messages
type tickMsg time.Time

type buildDoneMsg struct {
	unit string
	err  error
}

type buildEventMsg struct {
	unit   string
	status string // "cached", "building", "done", "failed"
}

type execDoneMsg struct {
	err error
}

type notifyMsg string

// Flash messages
type flashProgressMsg struct {
	written int64
	total   int64
}

type flashDoneMsg struct {
	err error
}

// Deploy messages
type deployOutputMsg struct {
	line string
}

type deployDoneMsg struct {
	err error
}

// model is the Bubble Tea model for the yoe TUI.
type model struct {
	proj       *yoestar.Project
	projectDir string
	arch       string
	warning      string // persistent warning banner (e.g., binfmt missing)
	notification string // transient global notification (e.g., container rebuild)
	dag        *resolve.DAG
	units      []string
	hashes     map[string]string
	statuses   map[string]unitStatus
	cursor     int
	view       viewKind
	detailUnit   string
	outputLines  []string // executor output (executor.log)
	logLines     []string // build log (build.log)
	detailScroll int      // scroll offset from top in detail view
	autoFollow   bool     // auto-scroll to bottom during builds
	listOffset   int      // first visible row in unit list
	tick       bool // toggles for flashing indicator
	width      int
	height     int
	message    string
	building   map[string]bool
	cancels    map[string]context.CancelFunc // cancel funcs for active builds
	confirm      string // non-empty = waiting for y/n confirmation
	queryEditing  bool            // true while the user is typing in the query bar
	queryInput    string          // text in the query bar; live-parsed every keystroke
	queryError    string          // last parse error; rendered next to the query bar
	inSet         map[string]bool // pre-computed in:X closure for the active query, nil if no in: filter
	visible       []int           // indexes into m.units after applying m.query
	query         query.Query     // active query, applied to m.units to produce visible
	queryRevertTo query.Query     // snapshot taken when the user opens `/`
	savedQuery    string          // canonical form of the last user-saved query (or bootstrap)

	// Detail log search
	detailSearching  bool   // true = detail search input active
	detailSearchText string // current detail search query
	detailMatches    []int  // line indices in allLines that match
	detailMatchIdx   int    // current match cursor (-1 = none)

	// Setup view
	machines    []string // sorted machine names
	setupCursor int      // cursor within setup options
	setupField  string   // "" = top-level, "machine" = picking machine
	machineCursor int    // cursor within machine list

	// Flash view
	flashUnit       string
	flashCandidates []device.Candidate
	flashCursor     int
	flashStage      flashStage
	flashImagePath  string
	flashImageSize  int64
	flashWritten    int64
	flashTotal      int64
	flashErr        error
	flashProgress   progress.Model

	// Deploy view
	deployUnit   string
	deployHost   string // text input buffer
	deployStage  deployStage
	deployOutput []string
	deployErr    error

	// Feed (yoe serve) status — set at startup by startProjectFeed.
	feedStatus string

	// loadOpts is the set of LoadOptions to apply when the TUI reloads the
	// project (e.g. after editing a .star file or switching machines). The
	// caller passes the same options used for the initial load so global
	// flags like --allow-duplicate-provides survive reloads.
	loadOpts []yoestar.LoadOption

	// globalFlagArgs holds the parent yoe invocation's global flags as argv
	// tokens (e.g. ["--allow-duplicate-provides"]). Re-execs of the yoe
	// binary from inside the TUI (currently `yoe run` on image units)
	// prepend these so the child sees the same load behavior.
	globalFlagArgs []string
}

// Config carries the cross-cutting context the TUI needs from the cmd layer:
// LoadOptions to use on every project reload, and the global flag tokens to
// forward when re-execing the yoe binary for image runs.
type Config struct {
	LoadOpts       []yoestar.LoadOption
	GlobalFlagArgs []string
}

// Run launches the TUI.
func Run(proj *yoestar.Project, projectDir string, cfg Config) error {
	dag, err := resolve.BuildDAG(proj)
	if err != nil {
		return fmt.Errorf("building DAG: %w", err)
	}

	arch := build.Arch()
	if m, ok := proj.Machines[proj.Defaults.Machine]; ok {
		arch = m.Arch
	}
	hashes, err := resolve.ComputeAllHashes(dag, arch, proj.Defaults.Machine)
	if err != nil {
		return fmt.Errorf("computing hashes: %w", err)
	}

	units := allUnits(proj)
	statuses := make(map[string]unitStatus, len(units))
	for _, name := range units {
		hash := hashes[name]
		sd := arch
		if u, ok := proj.Units[name]; ok {
			sd = build.ScopeDir(u, arch, proj.Defaults.Machine)
		}
		if build.IsBuildCached(projectDir, sd, name, hash) {
			statuses[name] = statusCached
		} else if build.IsBuildInProgress(projectDir, sd, name) {
			statuses[name] = statusBuilding
		} else if meta := build.ReadMeta(build.UnitBuildDir(projectDir, sd, name)); meta != nil && meta.Hash == hash && meta.Status == "failed" {
			statuses[name] = statusFailed
		}
	}

	machines := sortedKeys(proj.Machines)

	// Read local.star once for both deploy-host prefill and query bootstrap.
	ov, _ := yoestar.LoadLocalOverrides(projectDir)

	m := model{
		proj:           proj,
		projectDir:     projectDir,
		arch:           arch,
		dag:            dag,
		units:          units,
		hashes:         hashes,
		statuses:       statuses,
		building:       make(map[string]bool),
		cancels:        make(map[string]context.CancelFunc),
		machines:       machines,
		flashProgress:  progress.New(progress.WithDefaultGradient()),
		deployHost:     ov.DeployHost,
		loadOpts:       cfg.LoadOpts,
		globalFlagArgs: cfg.GlobalFlagArgs,
	}

	// Bootstrap query: prefer local.star, fall back to in:<defaults.image>.
	bootstrap := ov.Query
	if bootstrap == "" && proj.Defaults.Image != "" {
		bootstrap = "in:" + proj.Defaults.Image
	}
	if q, err := query.Parse(bootstrap); err == nil {
		m.query = q
		m.queryInput = q.String()
	}
	m.savedQuery = m.query.String()
	m.applyQuery()

	m.checkBinfmtWarning()

	stopFeed, feedStatus := startProjectFeed(proj, projectDir)
	defer stopFeed()
	m.feedStatus = feedStatus

	p := tea.NewProgram(m, tea.WithAltScreen())
	tuiProgram = p

	yoe.OnNotify = func(msg string) {
		if tuiProgram != nil {
			tuiProgram.Send(notifyMsg(msg))
		}
	}
	defer func() { yoe.OnNotify = nil }()

	_, err = p.Run()
	return err
}

func doTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) Init() tea.Cmd {
	return doTick()
}

// ----- Update -----

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		m.tick = !m.tick
		if m.view == viewDetail {
			m.refreshDetail()
		}
		return m, doTick()

	case buildEventMsg:
		switch msg.status {
		case "cached", "done":
			m.statuses[msg.unit] = statusCached
		case "waiting":
			m.statuses[msg.unit] = statusWaiting
		case "building":
			m.statuses[msg.unit] = statusBuilding
		case "failed":
			m.statuses[msg.unit] = statusFailed
		}
		return m, nil

	case buildDoneMsg:
		delete(m.building, msg.unit)
		delete(m.cancels, msg.unit)
		if msg.err != nil {
			if msg.err.Error() == "build cancelled" || strings.Contains(msg.err.Error(), "signal: killed") {
				m.statuses[msg.unit] = statusNone
				m.message = fmt.Sprintf("Build cancelled: %s", msg.unit)
			} else {
				m.statuses[msg.unit] = statusFailed
				m.message = fmt.Sprintf("Build failed: %s", msg.unit)
			}
		} else {
			m.statuses[msg.unit] = statusCached
			m.message = fmt.Sprintf("Build complete: %s", msg.unit)
		}
		return m, nil

	case execDoneMsg:
		if msg.err != nil {
			m.message = fmt.Sprintf("Command error: %v", msg.err)
		}
		return m, nil

	case flashProgressMsg:
		m.flashWritten = msg.written
		m.flashTotal = msg.total
		var ratio float64
		if msg.total > 0 {
			ratio = float64(msg.written) / float64(msg.total)
		}
		cmd := m.flashProgress.SetPercent(ratio)
		return m, cmd

	case progress.FrameMsg:
		var pm tea.Model
		pm, cmd := m.flashProgress.Update(msg)
		m.flashProgress = pm.(progress.Model)
		return m, cmd

	case flashDoneMsg:
		if msg.err != nil {
			m.flashStage = flashError
			m.flashErr = msg.err
		} else {
			m.flashStage = flashDone
		}
		return m, nil

	case deployOutputMsg:
		m.deployOutput = append(m.deployOutput, msg.line)
		return m, nil

	case deployDoneMsg:
		if msg.err != nil {
			m.deployStage = deployError
			m.deployErr = msg.err
		} else {
			m.deployStage = deployDone
			// Persist the host so next time the field is pre-filled.
			ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
			if ov.Machine == "" {
				ov.Machine = m.proj.Defaults.Machine
			}
			ov.DeployHost = strings.TrimSpace(m.deployHost)
			_ = yoestar.WriteLocalOverrides(m.projectDir, ov)
		}
		return m, nil

	case notifyMsg:
		m.notification = string(msg)
		return m, nil

	case tea.KeyMsg:
		// Handle confirmation prompt
		if m.confirm != "" {
			return m.updateConfirm(msg)
		}
		// Handle search input
		if m.detailSearching {
			return m.updateDetailSearch(msg)
		}
		if m.queryEditing {
			return m.updateSearch(msg)
		}
		m.message = ""
		switch m.view {
		case viewUnits:
			return m.updateUnits(msg)
		case viewDetail:
			return m.updateDetail(msg)
		case viewSetup:
			return m.updateSetup(msg)
		case viewFlash:
			return m.updateFlash(msg)
		case viewDeploy:
			return m.updateDeploy(msg)
		}
	}
	return m, nil
}

func (m model) updateUnits(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		if len(m.cancels) > 0 {
			m.confirm = "quit"
			m.message = "Builds are running. Quit and cancel them? (y/n)"
			return m, nil
		}
		return m, tea.Quit

	case "up", "k":
		m.cursor = m.prevVisible()
		m.adjustListOffset()
		return m, nil

	case "down", "j":
		m.cursor = m.nextVisible()
		m.adjustListOffset()
		return m, nil

	case "pgup", "ctrl+b":
		vis := m.visibleIndices()
		page := m.listViewportHeight()
		cursorPos := 0
		for vi, i := range vis {
			if i == m.cursor {
				cursorPos = vi
				break
			}
		}
		newPos := cursorPos - page
		if newPos < 0 {
			newPos = 0
		}
		if len(vis) > 0 {
			m.cursor = vis[newPos]
		}
		m.adjustListOffset()
		return m, nil

	case "pgdown", "ctrl+f":
		vis := m.visibleIndices()
		page := m.listViewportHeight()
		cursorPos := 0
		for vi, i := range vis {
			if i == m.cursor {
				cursorPos = vi
				break
			}
		}
		newPos := cursorPos + page
		if newPos >= len(vis) {
			newPos = len(vis) - 1
		}
		if len(vis) > 0 {
			m.cursor = vis[newPos]
		}
		m.adjustListOffset()
		return m, nil

	case "enter":
		if m.cursor < len(m.units) {
			m.detailUnit = m.units[m.cursor]
			m.view = viewDetail
			m.autoFollow = true
			m.detailScroll = 0
			m.refreshDetail()
			if m.autoFollow {
				m.scrollToBottom()
			}
		}
		return m, nil

	case "b":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			return m, m.startBuild(name)
		}
		return m, nil

	case "x":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			if _, ok := m.cancels[name]; ok {
				m.confirm = "cancel:" + name
				m.message = fmt.Sprintf("Cancel build of %s? (y/n)", name)
			}
		}
		return m, nil

	case "B":
		var cmds []tea.Cmd
		for _, name := range m.units {
			if m.statuses[name] != statusBuilding {
				if cmd := m.startBuild(name); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return m, tea.Batch(cmds...)

	case "e":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			path := findUnitFile(m.projectDir, name)
			if path != "" {
				return m, m.execEditor(path)
			}
			m.message = fmt.Sprintf("Could not find .star file for %s", name)
		}
		return m, nil

	case "l":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			logPath := filepath.Join(build.UnitBuildDir(m.projectDir, m.unitScopeDir(name), name), "build.log")
			if _, err := os.Stat(logPath); err == nil {
				return m, m.execEditor(logPath)
			}
			m.message = fmt.Sprintf("No build log for %s", name)
		}
		return m, nil

	case "d":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			logPath := filepath.Join(build.UnitBuildDir(m.projectDir, m.unitScopeDir(name), name), "build.log")
			c := exec.Command("claude", fmt.Sprintf("diagnose %s", logPath))
			c.Dir = m.projectDir
			return m, tea.ExecProcess(c, func(err error) tea.Msg {
				return execDoneMsg{err: err}
			})
		}
		return m, nil

	case "a":
		c := exec.Command("claude", "/new-unit")
		c.Dir = m.projectDir
		return m, tea.ExecProcess(c, func(err error) tea.Msg {
			return execDoneMsg{err: err}
		})

	case "r":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			if u, ok := m.proj.Units[name]; ok && u.Class == "image" {
				args := append([]string{}, m.globalFlagArgs...)
				args = append(args, "run", name, "--machine", m.proj.Defaults.Machine)
				c := exec.Command(os.Args[0], args...)
				c.Dir = m.projectDir
				return m, tea.ExecProcess(c, func(err error) tea.Msg {
					return execDoneMsg{err: err}
				})
			}
			m.message = fmt.Sprintf("%s is not an image unit", name)
		}
		return m, nil

	case "f":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			u, ok := m.proj.Units[name]
			if !ok || u.Class != "image" {
				m.message = fmt.Sprintf("%s is not an image unit", name)
				return m, nil
			}
			if m.statuses[name] != statusCached {
				m.message = fmt.Sprintf("Build %s first before flashing", name)
				return m, nil
			}
			cands, err := device.ListCandidates()
			if err != nil {
				m.message = fmt.Sprintf("Listing devices: %v", err)
				return m, nil
			}
			m.flashUnit = name
			m.flashCandidates = cands
			m.flashCursor = 0
			m.flashStage = flashSelect
			m.flashErr = nil
			m.flashWritten = 0
			m.flashTotal = 0
			m.view = viewFlash
		}
		return m, nil

	case "D":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			u, ok := m.proj.Units[name]
			if !ok || u.Class == "image" {
				m.message = fmt.Sprintf("%s is an image unit; use `f` to flash, not deploy", name)
				return m, nil
			}
			m.deployUnit = name
			m.deployStage = deployHostInput
			m.deployOutput = nil
			m.deployErr = nil
			m.view = viewDeploy
		}
		return m, nil

	case "c":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			m.confirm = "clean:" + name
			m.message = fmt.Sprintf("Clean %s? All build artifacts will be removed. (y/n)", name)
		}
		return m, nil

	case "C":
		m.confirm = "clean-all"
		m.message = "Clean ALL build artifacts? (y/n)"
		return m, nil

	case "/":
		m.queryEditing = true
		m.queryRevertTo = m.query
		m.queryInput = m.query.String() // start the bar prefilled with the active query
		return m, nil

	case "s":
		m.view = viewSetup
		m.setupCursor = 0
		m.setupField = ""
		// Set machineCursor to current machine
		m.machineCursor = 0
		for i, name := range m.machines {
			if name == m.proj.Defaults.Machine {
				m.machineCursor = i
				break
			}
		}
		return m, nil
	}
	return m, nil
}

func (m model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Revert to whatever query was active when the bar opened.
		m.queryEditing = false
		m.query = m.queryRevertTo
		m.queryInput = m.query.String()
		m.queryError = ""
		m.applyQuery()
		return m, nil

	case "enter":
		m.queryEditing = false
		// Keep current query active. If parse error, fall back to last
		// valid (already in m.query); clear the error.
		m.queryError = ""
		return m, nil

	case "backspace":
		if len(m.queryInput) > 0 {
			m.queryInput = m.queryInput[:len(m.queryInput)-1]
			m.reparse()
		}
		return m, nil

	default:
		// Single printable character
		key := msg.String()
		if len(key) == 1 && key[0] >= 32 && key[0] <= 126 {
			m.queryInput += key
			m.reparse()
		}
		return m, nil
	}
}

// reparse re-parses m.queryInput; on success updates m.query and
// re-applies. On failure keeps m.query and m.visible at their last-valid
// values and stores the error message for the bar.
func (m *model) reparse() {
	q, err := query.Parse(m.queryInput)
	if err != nil {
		m.queryError = err.Error()
		return
	}
	m.queryError = ""
	m.query = q
	m.applyQuery()
}

// applyQuery refreshes m.inSet and m.visible after m.query changes.
// The cursor is moved to the first visible row when it falls outside
// the new visible set; otherwise it is left alone (so live filtering
// while typing doesn't yank the cursor).
func (m *model) applyQuery() {
	m.inSet = nil
	if root := m.query.InRoot(); root != "" {
		m.inSet = query.BuildInClosure(m.proj, root)
	}
	if cap(m.visible) > 0 {
		m.visible = m.visible[:0]
	} else {
		m.visible = make([]int, 0, len(m.units))
	}
	for i, name := range m.units {
		u := m.proj.Units[name]
		if m.query.Matches(name, u, statusKey(m.statuses[name]), m.inSet) {
			m.visible = append(m.visible, i)
		}
	}
	// Keep cursor on a visible row if at all possible.
	if len(m.visible) > 0 {
		stillVisible := false
		for _, i := range m.visible {
			if i == m.cursor {
				stillVisible = true
				break
			}
		}
		if !stillVisible {
			m.cursor = m.visible[0]
		}
	}
	m.listOffset = 0
	m.adjustListOffset()
}

func (m model) visibleIndices() []int {
	return m.visible
}

func (m model) prevVisible() int {
	vis := m.visibleIndices()
	for i := len(vis) - 1; i >= 0; i-- {
		if vis[i] < m.cursor {
			return vis[i]
		}
	}
	return m.cursor
}

func (m model) nextVisible() int {
	vis := m.visibleIndices()
	for _, idx := range vis {
		if idx > m.cursor {
			return idx
		}
	}
	return m.cursor
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	action := m.confirm
	m.confirm = ""

	switch msg.String() {
	case "y", "Y":
		if strings.HasPrefix(action, "cancel:") {
			name := strings.TrimPrefix(action, "cancel:")
			if cancel, ok := m.cancels[name]; ok {
				cancel()
				delete(m.cancels, name)
				m.message = fmt.Sprintf("Cancelling build: %s", name)
			}
		} else if strings.HasPrefix(action, "clean:") {
			name := strings.TrimPrefix(action, "clean:")
			buildDir := build.UnitBuildDir(m.projectDir, m.unitScopeDir(name), name)
			if err := os.RemoveAll(buildDir); err != nil {
				m.message = fmt.Sprintf("Clean failed: %v", err)
			} else {
				m.statuses[name] = statusNone
				m.message = fmt.Sprintf("Cleaned %s", name)
			}
		} else if action == "quit" {
			for name, cancel := range m.cancels {
				cancel()
				delete(m.cancels, name)
			}
			return m, tea.Quit
		} else if action == "clean-all" {
			buildDir := filepath.Join(m.projectDir, "build")
			if err := os.RemoveAll(buildDir); err != nil {
				m.message = fmt.Sprintf("Clean failed: %v", err)
			} else {
				for _, name := range m.units {
					m.statuses[name] = statusNone
				}
				m.message = "Cleaned all build artifacts"
			}
		}
	default:
		m.message = ""
	}
	return m, nil
}

// Setup option names — add new options here.
var setupOptions = []string{"Machine"}

func (m model) updateSetup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.setupField == "machine" {
		return m.updateSetupMachine(msg)
	}

	switch msg.String() {
	case "esc", "q":
		m.view = viewUnits
		return m, nil

	case "up", "k":
		if m.setupCursor > 0 {
			m.setupCursor--
		}
		return m, nil

	case "down", "j":
		if m.setupCursor < len(setupOptions)-1 {
			m.setupCursor++
		}
		return m, nil

	case "enter":
		switch setupOptions[m.setupCursor] {
		case "Machine":
			m.setupField = "machine"
		}
		return m, nil
	}
	return m, nil
}

func (m model) updateSetupMachine(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.setupField = ""
		return m, nil

	case "up", "k":
		if m.machineCursor > 0 {
			m.machineCursor--
		}
		return m, nil

	case "down", "j":
		if m.machineCursor < len(m.machines)-1 {
			m.machineCursor++
		}
		return m, nil

	case "enter":
		picked := m.machines[m.machineCursor]
		m.proj.Defaults.Machine = picked
		if mach, ok := m.proj.Machines[picked]; ok {
			m.arch = mach.Arch
		}
		m.recomputeStatuses()
		m.checkBinfmtWarning()
		ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
		ov.Machine = picked
		if err := yoestar.WriteLocalOverrides(m.projectDir, ov); err != nil {
			m.message = fmt.Sprintf("Machine set to %s (warning: failed to save local.star: %v)", picked, err)
		} else {
			m.message = fmt.Sprintf("Machine set to %s (saved to local.star)", picked)
		}
		m.setupField = ""
		m.view = viewUnits
		return m, nil
	}
	return m, nil
}

func (m model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// First esc clears search, second esc goes back to list
		if m.detailSearchText != "" {
			m.detailSearchText = ""
			m.detailMatches = nil
			m.detailMatchIdx = -1
			return m, nil
		}
		m.view = viewUnits
		m.detailUnit = ""
		m.outputLines = nil
		m.logLines = nil
		m.detailScroll = 0
		return m, nil

	case "q", "ctrl+c":
		if len(m.cancels) > 0 {
			m.confirm = "quit"
			m.message = "Builds are running. Quit and cancel them? (y/n)"
			return m, nil
		}
		return m, tea.Quit

	case "up", "k":
		m.autoFollow = false
		if m.detailScroll > 0 {
			m.detailScroll--
		}
		return m, nil

	case "down", "j":
		maxScroll := m.detailMaxScroll()
		if m.detailScroll < maxScroll {
			m.detailScroll++
		}
		if m.detailScroll >= maxScroll {
			m.autoFollow = true
		}
		return m, nil

	case "pgup", "ctrl+b":
		m.autoFollow = false
		page := m.detailViewportHeight()
		m.detailScroll -= page
		if m.detailScroll < 0 {
			m.detailScroll = 0
		}
		return m, nil

	case "pgdown", "ctrl+f":
		page := m.detailViewportHeight()
		maxScroll := m.detailMaxScroll()
		m.detailScroll += page
		if m.detailScroll > maxScroll {
			m.detailScroll = maxScroll
		}
		if m.detailScroll >= maxScroll {
			m.autoFollow = true
		}
		return m, nil

	case "G":
		m.autoFollow = true
		m.scrollToBottom()
		return m, nil

	case "g":
		m.autoFollow = false
		m.detailScroll = 0
		return m, nil

	case "b":
		m.autoFollow = true
		return m, m.startBuild(m.detailUnit)

	case "d":
		logPath := filepath.Join(build.UnitBuildDir(m.projectDir, m.unitScopeDir(m.detailUnit), m.detailUnit), "build.log")
		c := exec.Command("claude", fmt.Sprintf("diagnose %s", logPath))
		c.Dir = m.projectDir
		return m, tea.ExecProcess(c, func(err error) tea.Msg {
			return execDoneMsg{err: err}
		})

	case "D":
		u, ok := m.proj.Units[m.detailUnit]
		if !ok || u.Class == "image" {
			m.message = fmt.Sprintf("%s is an image unit; use `f` to flash, not deploy", m.detailUnit)
			return m, nil
		}
		m.deployUnit = m.detailUnit
		m.deployStage = deployHostInput
		m.deployOutput = nil
		m.deployErr = nil
		m.view = viewDeploy
		return m, nil

	case "r":
		if u, ok := m.proj.Units[m.detailUnit]; ok && u.Class == "image" {
			args := append([]string{}, m.globalFlagArgs...)
			args = append(args, "run", m.detailUnit, "--machine", m.proj.Defaults.Machine)
			c := exec.Command(os.Args[0], args...)
			c.Dir = m.projectDir
			return m, tea.ExecProcess(c, func(err error) tea.Msg {
				return execDoneMsg{err: err}
			})
		}
		m.message = fmt.Sprintf("%s is not an image unit", m.detailUnit)
		return m, nil

	case "l":
		logPath := filepath.Join(build.UnitBuildDir(m.projectDir, m.unitScopeDir(m.detailUnit), m.detailUnit), "build.log")
		if _, err := os.Stat(logPath); err == nil {
			return m, m.execEditor(logPath)
		}
		m.message = fmt.Sprintf("No build log for %s", m.detailUnit)
		return m, nil

	case "/":
		m.detailSearching = true
		m.detailSearchText = ""
		m.detailMatches = nil
		m.detailMatchIdx = -1
		return m, nil

	case "n":
		if len(m.detailMatches) > 0 {
			m.detailMatchIdx = (m.detailMatchIdx + 1) % len(m.detailMatches)
			m.scrollToDetailMatch()
		}
		return m, nil

	case "N":
		if len(m.detailMatches) > 0 {
			m.detailMatchIdx--
			if m.detailMatchIdx < 0 {
				m.detailMatchIdx = len(m.detailMatches) - 1
			}
			m.scrollToDetailMatch()
		}
		return m, nil
	}
	return m, nil
}

func (m model) updateDetailSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.detailSearching = false
		m.detailSearchText = ""
		m.detailMatches = nil
		m.detailMatchIdx = -1
		return m, nil

	case "enter":
		m.detailSearching = false
		// Keep matches active for n/N navigation
		return m, nil

	case "backspace":
		if len(m.detailSearchText) > 0 {
			m.detailSearchText = m.detailSearchText[:len(m.detailSearchText)-1]
			m.applyDetailSearch()
		}
		return m, nil

	default:
		key := msg.String()
		if len(key) == 1 && key[0] >= 32 && key[0] <= 126 {
			m.detailSearchText += key
			m.applyDetailSearch()
		}
		return m, nil
	}
}

func (m *model) applyDetailSearch() {
	m.detailMatches = nil
	m.detailMatchIdx = -1
	if m.detailSearchText == "" {
		return
	}
	query := strings.ToLower(m.detailSearchText)
	allLines := m.detailAllLines()
	for i, line := range allLines {
		if strings.Contains(strings.ToLower(line), query) {
			m.detailMatches = append(m.detailMatches, i)
		}
	}
	if len(m.detailMatches) > 0 {
		m.detailMatchIdx = 0
		m.scrollToDetailMatch()
	}
}

func (m *model) scrollToDetailMatch() {
	if m.detailMatchIdx < 0 || m.detailMatchIdx >= len(m.detailMatches) {
		return
	}
	line := m.detailMatches[m.detailMatchIdx]
	viewH := m.detailViewportHeight()
	// Center the match in the viewport
	m.detailScroll = line - viewH/2
	if m.detailScroll < 0 {
		m.detailScroll = 0
	}
	maxScroll := m.detailMaxScroll()
	if m.detailScroll > maxScroll {
		m.detailScroll = maxScroll
	}
	m.autoFollow = false
}

// ----- View -----

func (m model) View() string {
	switch m.view {
	case viewDetail:
		return m.viewDetail()
	case viewSetup:
		return m.viewSetup()
	case viewFlash:
		return m.viewFlash()
	case viewDeploy:
		return m.viewDeploy()
	default:
		return m.viewUnits()
	}
}

func (m model) viewUnits() string {
	var b strings.Builder

	// Header
	machine := m.proj.Defaults.Machine
	image := m.proj.Defaults.Image
	b.WriteString(fmt.Sprintf("  %s  Machine: %s  Image: %s\n",
		titleStyle.Render("[yoe]"),
		headerStyle.Render(machine),
		headerStyle.Render(image)))

	// Warning banner
	if m.warning != "" {
		warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)
		b.WriteString(fmt.Sprintf("  %s\n", warnStyle.Render(m.warning)))
	}
	// Global notification (e.g., container rebuild)
	if m.notification != "" {
		notifyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)
		b.WriteString(fmt.Sprintf("  %s\n", notifyStyle.Render("⏳ "+m.notification)))
	}
	// Feed status (yoe serve)
	if m.feedStatus != "" {
		feedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		b.WriteString(fmt.Sprintf("  %s\n", feedStyle.Render("feed: "+m.feedStatus)))
	}
	b.WriteString("\n")

	// Query header
	qStr := m.query.String()
	qLabel := "Query: "
	qBody := qStr
	if qBody == "" {
		qBody = "(empty — showing all)"
	}
	style := queryDimStyle
	if qStr != m.savedQuery {
		style = queryActiveStyle
	}
	counter := fmt.Sprintf("Units: %d/%d", len(m.visible), len(m.units))
	b.WriteString(fmt.Sprintf("  %s%s    %s\n",
		queryDimStyle.Render(qLabel),
		style.Render(qBody),
		queryDimStyle.Render(counter)))

	// Column header
	b.WriteString(fmt.Sprintf("  %s %s %s\n",
		headerStyle.Render(fmt.Sprintf("%-28s", "NAME")),
		headerStyle.Render(fmt.Sprintf("%-12s", "CLASS")),
		headerStyle.Render("STATUS")))

	// Determine visible units — filtered by current query state
	visible := m.visible

	// Calculate visible window for unit list
	maxRows := m.listViewportHeight()
	end := m.listOffset + maxRows
	if end > len(visible) {
		end = len(visible)
	}

	if m.listOffset > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more", m.listOffset)))
		b.WriteString("\n")
	}

	for _, i := range visible[m.listOffset:end] {
		name := m.units[i]
		cursor := "  "
		nameStyle := dimStyle
		classStyle := dimStyle
		if i == m.cursor {
			cursor = "→ "
			nameStyle = selectedStyle
			classStyle = selectedStyle
		}

		class := ""
		if u, ok := m.proj.Units[name]; ok {
			class = u.Class
			if i != m.cursor {
				switch class {
				case "image":
					nameStyle = classImageStyle
					classStyle = classImageStyle
				case "container":
					nameStyle = classContainerStyle
					classStyle = classContainerStyle
				default:
					nameStyle = classUnitStyle
					classStyle = classUnitStyle
				}
			}
		}

		status := m.renderStatus(name)

		paddedName := fmt.Sprintf("%-28s", name)
		paddedClass := fmt.Sprintf("%-12s", class)
		b.WriteString(fmt.Sprintf("%s%s %s %s\n",
			cursor,
			nameStyle.Render(paddedName),
			classStyle.Render(paddedClass),
			status))
	}

	if end < len(visible) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(visible)-end)))
		b.WriteString("\n")
	}

	// Search bar or help bar
	b.WriteString("\n")
	if m.queryEditing {
		if m.queryError != "" {
			b.WriteString(fmt.Sprintf("  /%s    %s",
				m.queryInput,
				queryErrorStyle.Render(m.queryError)))
		} else {
			b.WriteString(fmt.Sprintf("  /%s▌", m.queryInput))
		}
	} else {
		help := "  b build  D deploy  x cancel  e edit  d diagnose  l log  c clean  s setup  / search  q quit"
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			if u, ok := m.proj.Units[name]; ok && u.Class == "image" {
				if m.statuses[name] == statusCached {
					help = "  b build  x cancel  r run  f flash  e edit  d diagnose  l log  c clean  s setup  / search  q quit"
				} else {
					help = "  b build  x cancel  r run  e edit  d diagnose  l log  c clean  s setup  / search  q quit"
				}
			}
		}
		b.WriteString(helpStyle.Render(help))
	}
	b.WriteString("\n")

	// Status message
	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  "+m.message))
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) viewSetup() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("  %s\n\n", titleStyle.Render("Setup")))

	if m.setupField == "machine" {
		// Machine picker
		b.WriteString(headerStyle.Render("  Select Machine"))
		b.WriteString("\n\n")

		for i, name := range m.machines {
			cursor := "  "
			style := dimStyle
			if i == m.machineCursor {
				cursor = "→ "
				style = selectedStyle
			}
			current := ""
			if name == m.proj.Defaults.Machine {
				current = cachedStyle.Render(" (current)")
			}
			arch := ""
			if mach, ok := m.proj.Machines[name]; ok {
				arch = dimStyle.Render(fmt.Sprintf("  %s", mach.Arch))
			}
			b.WriteString(fmt.Sprintf("%s%s%s%s\n", cursor, style.Render(name), arch, current))
		}

		b.WriteString("\n")
		b.WriteString(helpStyle.Render("  enter select  esc back"))
		b.WriteString("\n")
	} else {
		// Top-level setup menu
		for i, opt := range setupOptions {
			cursor := "  "
			style := dimStyle
			if i == m.setupCursor {
				cursor = "→ "
				style = selectedStyle
			}
			value := ""
			switch opt {
			case "Machine":
				value = headerStyle.Render(m.proj.Defaults.Machine)
			}
			b.WriteString(fmt.Sprintf("%s%s  %s\n", cursor, style.Render(opt), value))
		}

		b.WriteString("\n")
		b.WriteString(helpStyle.Render("  enter select  esc back  q quit"))
		b.WriteString("\n")
	}

	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  "+m.message))
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) detailAllLines() []string {
	var allLines []string

	allLines = append(allLines, headerStyle.Render("  BUILD OUTPUT"))
	if len(m.outputLines) == 0 {
		allLines = append(allLines, dimStyle.Render("  (no output yet)"))
	} else {
		for _, line := range m.outputLines {
			allLines = append(allLines, m.wrapLine("  "+line)...)
		}
	}

	allLines = append(allLines, "")

	allLines = append(allLines, headerStyle.Render("  BUILD LOG"))
	if len(m.logLines) == 0 {
		allLines = append(allLines, dimStyle.Render("  (no build log)"))
	} else {
		for _, line := range m.logLines {
			allLines = append(allLines, m.wrapLine("  "+line)...)
		}
	}

	return allLines
}

// wrapLine hard-wraps a single logical line into one or more display lines
// that fit within the current terminal width. Continuation lines get an
// extra indent so wrapped content is visually distinct from a fresh line.
func (m model) wrapLine(line string) []string {
	w := m.width
	if w <= 0 || ansi.StringWidth(line) <= w {
		return []string{line}
	}
	const contIndent = "    "
	// First chunk fits the full width.
	first := ansi.Truncate(line, w, "")
	out := []string{first}
	rest := line[len(first):]
	for rest != "" {
		chunk := ansi.Truncate(rest, w-len(contIndent), "")
		if chunk == "" {
			break
		}
		out = append(out, contIndent+chunk)
		rest = rest[len(chunk):]
	}
	return out
}

func (m model) viewDetail() string {
	var b strings.Builder

	status := m.renderStatus(m.detailUnit)
	b.WriteString(fmt.Sprintf("  ← %s %s\n",
		titleStyle.Render(m.detailUnit),
		status))

	// Show build metadata if available
	if u, ok := m.proj.Units[m.detailUnit]; ok {
		sd := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
		buildDir := build.UnitBuildDir(m.projectDir, sd, m.detailUnit)
		currentHash := m.hashes[m.detailUnit]
		if meta := build.ReadMeta(buildDir); meta != nil && meta.Hash == currentHash {
			dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
			info := fmt.Sprintf("  %s", meta.Status)
			if meta.Duration > 0 {
				if meta.Duration < 60 {
					info += fmt.Sprintf("  %.1fs", meta.Duration)
				} else {
					info += fmt.Sprintf("  %dm%ds", int(meta.Duration)/60, int(meta.Duration)%60)
				}
			}
			if meta.DiskBytes > 0 {
				mb := float64(meta.DiskBytes) / (1024 * 1024)
				if mb >= 1024 {
					info += fmt.Sprintf("  build: %.1fGB", mb/1024)
				} else {
					info += fmt.Sprintf("  build: %.0fMB", mb)
				}
			}
			if meta.InstalledBytes > 0 {
				mb := float64(meta.InstalledBytes) / (1024 * 1024)
				if mb >= 1024 {
					info += fmt.Sprintf("  installed: %.1fGB", mb/1024)
				} else if mb >= 1 {
					info += fmt.Sprintf("  installed: %.0fMB", mb)
				} else {
					info += fmt.Sprintf("  installed: %.0fKB", mb*1024)
				}
			}
			b.WriteString(dimStyle.Render(info))
		}
	}
	b.WriteString("\n")

	allLines := m.detailAllLines()

	// Build set of matching line indices for highlight
	matchSet := map[int]bool{}
	for _, idx := range m.detailMatches {
		matchSet[idx] = true
	}
	currentMatchLine := -1
	if m.detailMatchIdx >= 0 && m.detailMatchIdx < len(m.detailMatches) {
		currentMatchLine = m.detailMatches[m.detailMatchIdx]
	}

	// Calculate visible window
	viewH := m.detailViewportHeight()
	start := m.detailScroll
	if start > len(allLines)-viewH {
		start = len(allLines) - viewH
	}
	if start < 0 {
		start = 0
	}
	end := start + viewH
	if end > len(allLines) {
		end = len(allLines)
	}

	matchHighlight := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))       // yellow
	currentHighlight := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true) // bold yellow

	for lineIdx := start; lineIdx < end; lineIdx++ {
		line := allLines[lineIdx]
		if lineIdx == currentMatchLine {
			b.WriteString(currentHighlight.Render(line))
		} else if matchSet[lineIdx] {
			b.WriteString(matchHighlight.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	// Pad remaining lines so help bar stays at bottom
	rendered := end - start
	for i := rendered; i < viewH; i++ {
		b.WriteString("\n")
	}

	// Scroll indicator
	scrollInfo := ""
	if len(allLines) > viewH {
		pct := 100
		if len(allLines)-viewH > 0 {
			pct = start * 100 / (len(allLines) - viewH)
		}
		if m.autoFollow {
			scrollInfo = dimStyle.Render(fmt.Sprintf("  [auto-follow] %d%%", pct))
		} else {
			scrollInfo = dimStyle.Render(fmt.Sprintf("  [%d/%d] %d%%", start+1, len(allLines), pct))
		}
	}
	b.WriteString(scrollInfo)
	b.WriteString("\n")

	// Search bar (shown when actively searching or when matches are active)
	if m.detailSearching {
		matchInfo := ""
		if len(m.detailMatches) > 0 {
			matchInfo = fmt.Sprintf(" [%d/%d]", m.detailMatchIdx+1, len(m.detailMatches))
		} else if m.detailSearchText != "" {
			matchInfo = " [no matches]"
		}
		b.WriteString(fmt.Sprintf("  /%s%s\n", m.detailSearchText, dimStyle.Render(matchInfo)))
	} else if m.detailSearchText != "" && len(m.detailMatches) > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  /%s [%d/%d]  n next  N prev\n",
			m.detailSearchText, m.detailMatchIdx+1, len(m.detailMatches))))
	}

	// Help bar
	help := "  esc back  j/k scroll  g top  G bottom  / search  b build  d diagnose  l log"
	if u, ok := m.proj.Units[m.detailUnit]; ok && u.Class == "image" {
		help = "  esc back  j/k scroll  g top  G bottom  / search  b build  r run  d diagnose  l log"
	}
	b.WriteString(helpStyle.Render(help))
	b.WriteString("\n")

	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  "+m.message))
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) renderStatus(name string) string {
	switch m.statuses[name] {
	case statusCached:
		return cachedStyle.Render("● cached")
	case statusWaiting:
		return waitingStyle.Render("● waiting")
	case statusBuilding:
		if m.tick {
			return buildingStyle.Render("▌building...")
		}
		return "            " // blank when flashing off
	case statusFailed:
		return failedStyle.Render("● failed")
	default:
		return ""
	}
}

// ----- Helpers -----

func (m *model) startBuild(name string) tea.Cmd {
	if m.statuses[name] == statusBuilding || m.statuses[name] == statusWaiting {
		return nil
	}
	m.statuses[name] = statusWaiting
	m.building[name] = true

	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[name] = cancel

	proj := m.proj
	projectDir := m.projectDir
	arch := m.arch
	machine := m.proj.Defaults.Machine
	unitName := name
	loadOpts := append([]yoestar.LoadOption{}, m.loadOpts...)

	// Write executor output to a log file so detail view can tail it
	sd := arch
	if u, ok := m.proj.Units[name]; ok {
		sd = build.ScopeDir(u, arch, machine)
	}
	outputPath := filepath.Join(build.UnitBuildDir(projectDir, sd, unitName), "executor.log")
	build.EnsureDir(filepath.Dir(outputPath))

	return func() tea.Msg {
		defer cancel()
		f, err := os.Create(outputPath)
		if err != nil {
			return buildDoneMsg{unit: unitName, err: err}
		}
		defer f.Close()

		// Reload project from .star files so we pick up any changes
		// made since the TUI started (e.g., edited build steps).
		freshProj, err := yoestar.LoadProject(projectDir,
			append(loadOpts, yoestar.WithMachine(machine))...)
		if err != nil {
			fmt.Fprintf(f, "warning: could not reload project: %v, using cached config\n", err)
			freshProj = proj
		}

		err = build.BuildUnits(freshProj, []string{unitName}, build.Options{
			Ctx:        ctx,
			Force:      true,
			ProjectDir: projectDir,
			Arch:       arch,
			Machine:    machine,
			OnEvent: func(ev build.BuildEvent) {
				if tuiProgram != nil {
					tuiProgram.Send(buildEventMsg{
						unit:   ev.Unit,
						status: ev.Status,
					})
				}
			},
		}, f)
		return buildDoneMsg{unit: unitName, err: err}
	}
}

func (m model) execEditor(path string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return execDoneMsg{err: err}
	})
}

func (m *model) refreshDetail() {
	unitDir := build.UnitBuildDir(m.projectDir, m.unitScopeDir(m.detailUnit), m.detailUnit)
	outputPath := filepath.Join(unitDir, "executor.log")
	m.outputLines = readFileAll(outputPath)
	logPath := filepath.Join(unitDir, "build.log")
	m.logLines = readFileAll(logPath)
	if m.autoFollow {
		m.scrollToBottom()
	}
}

// detailViewportHeight returns the number of content lines visible in detail view.
// Reserves lines for: header (2) + scroll indicator (1) + help bar (1) + message (2) + padding (1).
func (m model) detailViewportHeight() int {
	h := m.height - 7
	if h < 5 {
		h = 5
	}
	return h
}

// detailTotalLines returns the total number of display lines in the combined
// detail content (after wrapping).
func (m model) detailTotalLines() int {
	return len(m.detailAllLines())
}

// detailMaxScroll returns the maximum scroll offset for the detail view.
func (m model) detailMaxScroll() int {
	max := m.detailTotalLines() - m.detailViewportHeight()
	if max < 0 {
		return 0
	}
	return max
}

// scrollToBottom sets the scroll position to the end of content.
func (m *model) scrollToBottom() {
	m.detailScroll = m.detailMaxScroll()
}

// adjustListOffset ensures the cursor is visible within the unit list viewport.
func (m *model) adjustListOffset() {
	visible := m.visibleIndices()
	maxRows := m.listViewportHeight()

	cursorPos := -1
	for vi, i := range visible {
		if i == m.cursor {
			cursorPos = vi
			break
		}
	}
	if cursorPos < 0 {
		return
	}
	if cursorPos < m.listOffset {
		m.listOffset = cursorPos
	}
	if cursorPos >= m.listOffset+maxRows {
		m.listOffset = cursorPos - maxRows + 1
	}
	if m.listOffset > len(visible)-maxRows {
		m.listOffset = len(visible) - maxRows
	}
	if m.listOffset < 0 {
		m.listOffset = 0
	}
}

// unitScopeDir returns the scope directory for a unit (arch, machine name, or noarch).
func (m model) unitScopeDir(name string) string {
	if u, ok := m.proj.Units[name]; ok {
		return build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	}
	return m.arch
}

// recomputeStatuses reloads the project for the new machine and recomputes
// hashes and cache statuses. Required because image definitions depend on
// MACHINE_CONFIG which changes per machine.
func (m *model) recomputeStatuses() {
	// Reload project with the new machine so MACHINE_CONFIG/PROVIDES/ARCH
	// are correct for image definitions.
	freshProj, err := yoestar.LoadProject(m.projectDir,
		append(m.loadOpts, yoestar.WithMachine(m.proj.Defaults.Machine))...)
	if err == nil {
		m.proj = freshProj
		// Rebuild DAG and unit list from fresh project
		if dag, err := resolve.BuildDAG(freshProj); err == nil {
			m.dag = dag
			m.units = allUnits(freshProj)
			m.cursor = 0
			m.listOffset = 0
		}
	}

	// Rebuild visible list to reflect the new unit set before any early
	// return. m.units may already have been replaced above; without this,
	// m.visible would carry stale indices into the old slice.
	m.applyQuery()

	hashes, err := resolve.ComputeAllHashes(m.dag, m.arch, m.proj.Defaults.Machine)
	if err != nil {
		return
	}
	m.hashes = hashes
	for _, name := range m.units {
		if m.building[name] {
			continue // don't override in-progress builds
		}
		hash := hashes[name]
		sd := m.arch
		if u, ok := m.proj.Units[name]; ok {
			sd = build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
		}
		if build.IsBuildCached(m.projectDir, sd, name, hash) {
			m.statuses[name] = statusCached
		} else if build.IsBuildInProgress(m.projectDir, sd, name) {
			m.statuses[name] = statusBuilding
		} else if meta := build.ReadMeta(build.UnitBuildDir(m.projectDir, sd, name)); meta != nil && meta.Hash == hash && meta.Status == "failed" {
			m.statuses[name] = statusFailed
		} else {
			m.statuses[name] = statusNone
		}
	}
}

// checkBinfmtWarning sets or clears the warning banner based on whether
// binfmt_misc is registered for the current target arch.
func (m *model) checkBinfmtWarning() {
	if err := yoe.CheckBinfmt(m.arch); err != nil {
		m.warning = "⚠ Cross-arch build: run 'yoe container binfmt' to register QEMU emulation for " + m.arch
	} else {
		m.warning = ""
	}
}

// listViewportHeight returns the number of unit rows that fit on screen.
func (m model) listViewportHeight() int {
	h := m.height - 8
	if h < 5 {
		h = 5
	}
	return h
}

func findUnitFile(projectDir, name string) string {
	// Collect directories to search for .star files.
	// For the project and parent dirs, search under modules/.
	// For cached modules, search the module root directly (units/, images/, etc.).
	var searchDirs []string

	for _, root := range []string{projectDir} {
		d := filepath.Join(root, "modules")
		if _, err := os.Stat(d); err == nil {
			searchDirs = append(searchDirs, d)
		}
	}
	for _, rel := range []string{"..", filepath.Join("..", "..")} {
		d := filepath.Join(projectDir, rel, "modules")
		if _, err := os.Stat(d); err == nil {
			searchDirs = append(searchDirs, d)
		}
	}

	// Add cached module directories
	cacheDir := os.Getenv("YOE_CACHE")
	if cacheDir == "" {
		cacheDir = "cache"
	}
	cachedModules := filepath.Join(cacheDir, "modules")
	if entries, err := os.ReadDir(cachedModules); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				searchDirs = append(searchDirs, filepath.Join(cachedModules, e.Name()))
			}
		}
	}

	// First pass: look for an exact <name>.star file
	for _, dir := range searchDirs {
		var found string
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if d.Name() == name+".star" {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found
		}
	}

	// Second pass: derived units (e.g., base-files-dev is defined inside
	// another .star file via a function call). Grep for the name string.
	needle := []byte(`"` + name + `"`)
	for _, dir := range searchDirs {
		var found string
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".star") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if bytes.Contains(data, needle) {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found
		}
	}

	return ""
}

func readFileAll(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // handle long lines up to 1MB
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// allUnits returns sorted unit names from the project.
func allUnits(proj *yoestar.Project) []string {
	return sortedKeys(proj.Units)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ----- Flash view -----

func (m model) updateFlash(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.flashStage {
	case flashSelect:
		switch msg.String() {
		case "esc", "q":
			m.view = viewUnits
			return m, nil
		case "up", "k":
			if m.flashCursor > 0 {
				m.flashCursor--
			}
			return m, nil
		case "down", "j":
			if m.flashCursor < len(m.flashCandidates)-1 {
				m.flashCursor++
			}
			return m, nil
		case "enter":
			if len(m.flashCandidates) == 0 {
				return m, nil
			}
			m.flashStage = flashConfirm
			return m, nil
		}
	case flashConfirm:
		switch strings.ToLower(msg.String()) {
		case "y":
			cand := m.flashCandidates[m.flashCursor]
			imgPath, imgSize, err := findImageForFlash(m.proj, m.flashUnit, m.projectDir)
			if err != nil {
				m.flashStage = flashError
				m.flashErr = err
				return m, nil
			}
			m.flashImagePath = imgPath
			m.flashImageSize = imgSize
			m.flashTotal = imgSize
			m.flashWritten = 0
			m.flashStage = flashWriting
			return m, m.flashWriteCmd(imgPath, cand.Path)
		case "esc", "n", "q":
			m.flashStage = flashSelect
			return m, nil
		}
	case flashWriting:
		// no-op; ignore keys while writing
		return m, nil
	case flashDone, flashError:
		switch msg.String() {
		case "esc", "q", "enter":
			m.view = viewUnits
			m.flashStage = flashSelect
			return m, nil
		}
	}
	return m, nil
}

// flashWriteCmd returns a tea.Cmd that writes the image and streams
// progress messages back to the bubbletea program.
func (m model) flashWriteCmd(imagePath, devicePath string) tea.Cmd {
	return func() tea.Msg {
		progressFn := func(written, total int64) {
			if tuiProgram != nil {
				tuiProgram.Send(flashProgressMsg{written: written, total: total})
			}
		}
		err := device.Write(imagePath, devicePath, progressFn)
		return flashDoneMsg{err: err}
	}
}

func (m model) viewFlash() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf(" yoe flash — %s ", m.flashUnit)))
	b.WriteString("\n\n")

	switch m.flashStage {
	case flashSelect:
		if len(m.flashCandidates) == 0 {
			b.WriteString(dimStyle.Render("No removable devices detected."))
			b.WriteString("\n\n")
			b.WriteString(helpStyle.Render("esc: back"))
			return b.String()
		}
		b.WriteString(headerStyle.Render(fmt.Sprintf("%-14s %8s  %-4s %-10s %s", "DEVICE", "SIZE", "BUS", "VENDOR", "MODEL")))
		b.WriteString("\n")
		for i, c := range m.flashCandidates {
			line := fmt.Sprintf("%-14s %8s  %-4s %-10s %s",
				c.Path, device.FormatSize(c.Size), c.Bus, c.Vendor, c.Model)
			if i == m.flashCursor {
				b.WriteString(selectedStyle.Render("> " + line))
			} else {
				b.WriteString("  " + line)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("↑/↓ select • enter confirm • esc back"))
	case flashConfirm:
		c := m.flashCandidates[m.flashCursor]
		b.WriteString(fmt.Sprintf("Flash %s → %s (%s, %s %s)?\n",
			m.flashUnit, c.Path, device.FormatSize(c.Size), c.Vendor, c.Model))
		b.WriteString(failedStyle.Render(fmt.Sprintf("This will erase all data on %s.", c.Path)))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("y to confirm • n/esc to cancel"))
	case flashWriting:
		c := m.flashCandidates[m.flashCursor]
		b.WriteString(fmt.Sprintf("Writing %s → %s\n\n", filepath.Base(m.flashImagePath), c.Path))
		b.WriteString(m.flashProgress.View())
		b.WriteString("\n")
		var rate string
		if m.flashTotal > 0 {
			rate = fmt.Sprintf("%s / %s", device.FormatSize(m.flashWritten), device.FormatSize(m.flashTotal))
		}
		b.WriteString(dimStyle.Render(rate))
	case flashDone:
		b.WriteString(buildingStyle.Render("Flash complete."))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("press any key to return"))
	case flashError:
		b.WriteString(failedStyle.Render(fmt.Sprintf("Flash failed: %v", m.flashErr)))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("press any key to return"))
	}
	return b.String()
}

// findImageForFlash locates the built image for an image unit at the
// project's default machine. Mirrors the resolution done in device.Flash
// without requiring the rest of the orchestration.
func findImageForFlash(proj *yoestar.Project, unitName, projectDir string) (string, int64, error) {
	machine, ok := proj.Machines[proj.Defaults.Machine]
	if !ok {
		return "", 0, fmt.Errorf("default machine %q not found", proj.Defaults.Machine)
	}
	imgPath := filepath.Join(projectDir, "build", unitName+"."+machine.Name, "destdir", unitName+".img")
	info, err := os.Stat(imgPath)
	if err != nil {
		return "", 0, fmt.Errorf("no built image found — run yoe build %s first", unitName)
	}
	return imgPath, info.Size(), nil
}
