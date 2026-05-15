package tui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	yoe "github.com/yoebuild/yoe/internal"
	"github.com/yoebuild/yoe/internal/build"
	"github.com/yoebuild/yoe/internal/device"
	"github.com/yoebuild/yoe/internal/module"
	"github.com/yoebuild/yoe/internal/resolve"
	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
	"github.com/yoebuild/yoe/internal/tui/query"
)

// Styles
var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e8863a")).Background(lipgloss.Color("#000000"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5fff5f"))
	// Faded variant of selectedStyle for the always-on "cursor unit name"
	// line above the bottom row — same green so the eye links it to the
	// highlighted row, but dimmed and not bold so it doesn't compete.
	cursorNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#5fff5f")).Faint(true)
	dimStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cachedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // blue
	failedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	buildingStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	helpStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	// Amber, matching the [yoe] logo, for the keyboard shortcut letter
	// in each help-bar item — the description stays helpStyle gray so
	// the eye can scan keys at a glance.
	helpKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#e8863a")).Bold(true)
	waitingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow

	// Query-related styles
	queryDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	queryActiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	queryErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	// Subtle per-class colors for unselected units
	classUnitStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))  // muted blue
	classImageStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("13")) // muted magenta
	classContainerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))  // muted cyan

	// matchHighlightStyle draws the matched substring on top of whatever
	// the row's existing color is.
	matchHighlightStyle = lipgloss.NewStyle().Underline(true).Bold(true)

	// SRC column state styles. Each token is short (≤ 9 cells) and
	// colored so the eye can scan a long unit list and spot dev units
	// instantly. Pin == "match the .star" (cyan, like cached). Dev ==
	// "user-controlled but clean" (green). Dev-mod == "user has
	// committed local changes" (yellow). Dev-dirty == "uncommitted
	// edits" (red). Local == "module(local=...)" override (faded).
	srcPinStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // blue/cyan
	srcDevStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	srcDevModStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	srcDevDirtyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	srcLocalStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Faint(true)

	// Tab bar styling: zellij-style "ribbon" segments. The active tab is a
	// solid amber block (matching the [yoe] logo) with black bold text;
	// inactive tabs are dark-gray blocks with light text. Both ends of each
	// ribbon are a powerline wedge rendered ON the bar background so the cell
	// fills full-height and the arrow reads crisply (a foreground-only glyph
	// on the terminal default renders as a small, top-aligned sliver on many
	// powerline fonts).
	tabBarBg       = lipgloss.Color("235")
	tabActiveBg    = lipgloss.Color("#e8863a")
	tabInactiveBg  = lipgloss.Color("238")
	tabActiveSeg   = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(tabActiveBg).Bold(true)
	tabInactiveSeg = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(tabInactiveBg)
	tabBarGap      = lipgloss.NewStyle().Background(tabBarBg).Render(" ")
)

// tabCapL / tabCapR are the powerline wedges forming the left and right
// edges of a ribbon. U+E0B6 points left (the ribbon's left tip), U+E0B4
// points right (its right tip); together they make a rounded banner.
// Requires a powerline-patched font.
const (
	tabCapL = ""
	tabCapR = ""
)

// renderTabBar draws a zellij-style powerline ribbon strip. Each label is a
// space-padded colored block bracketed by a left and right wedge in the
// block's color, all painted on the bar background so every cell is full
// height. Ribbons are separated by a single bar-colored space.
func renderTabBar(labels []string, active int) string {
	var b strings.Builder
	for i, label := range labels {
		seg := tabInactiveSeg
		bg := tabInactiveBg
		if i == active {
			seg = tabActiveSeg
			bg = tabActiveBg
		}
		if i > 0 {
			b.WriteString(tabBarGap)
		}
		cap := lipgloss.NewStyle().Foreground(bg).Background(tabBarBg)
		b.WriteString(cap.Render(tabCapL) + seg.Render(" "+label+" ") + cap.Render(tabCapR))
	}
	return b.String()
}

// Package-level program reference for sending messages from goroutines.
var tuiProgram *tea.Program

// autoFollowIdleThreshold is how long after the most recent keypress the
// units table will auto-follow a newly building unit by moving the cursor
// and scrolling it into view. Within the window the user is presumed to be
// actively navigating, so auto-follow is suppressed to avoid yanking their
// cursor and viewport mid-keystroke.
const autoFollowIdleThreshold = 2 * time.Second

// Sort columns for the units table. Order matches the on-screen column
// order and the indices stored in model.sortColumn. The cycle order
// driven by the `o` key follows this same sequence.
const (
	sortByName = iota
	sortByClass
	sortByModule
	sortByVersion
	sortBySize
	sortByDeps
	sortByStatus
	numSortColumns
)

// Views
type viewKind int

const (
	viewUnits viewKind = iota
	viewDetail
	viewSetup
	viewFlash
	viewDeploy
	viewSourcePrompt
	viewSourceProgress
)

// sourcePromptKind names which dev-mode modal is showing. Each kind has
// its own option set and post-selection action; updateSourcePrompt
// dispatches on this enum to call the right internal/dev.go helper.
type sourcePromptKind int

const (
	promptSSHHTTPS     sourcePromptKind = iota // pin → dev, stage 1: pick remote scheme
	promptHistoryDepth                         // pin → dev, stage 2: pick fetch depth
	promptDiscardDev                           // dev-mod / dev-dirty → pin: confirm discard
)

// sourcePromptOption is one row in the dev-mode modal. `value` is the
// back-channel string passed to the action (e.g. "ssh", "tag"); `label`
// is what the user reads.
type sourcePromptOption struct {
	label    string
	desc     string // secondary line under the label, optional
	value    string
	disabled bool // greyed-out, non-selectable (e.g. "tag" when HEAD has no tag)
}

// sourcePrompt is the in-flight modal state. The TUI parks the previous
// view in `prevView` so cancelling the prompt restores it; the action
// fires on Enter and clears the state.
type sourcePrompt struct {
	kind       sourcePromptKind
	target     string // "unit" | "module"
	targetName string
	header     string
	subheader  string // optional dim line under the title (e.g. "upstream: …")
	options    []sourcePromptOption
	cursor     int
	prevView   viewKind

	// Carried across multi-stage prompts. The pin → dev flow asks for
	// the remote scheme first, then for the fetch depth — chosenSSH
	// remembers stage-1's pick so stage-2's apply has both choices.
	chosenSSH bool
}

// sourceOp tracks an in-flight dev-mode action so the UI can render
// a "working…" view while a goroutine does the (potentially slow)
// git work. The spinner ticks autonomously via spinner.Tick; the
// completion message arrives as sourceOpDoneMsg.
type sourceOp struct {
	target   sourceTarget // unit or module — drives cache invalidation on done
	name     string
	label    string // user-facing description ("fetching upstream history for foo")
	spinner  spinner.Model
	prevView viewKind
}

// sourceOpDoneMsg is the completion signal for a dev-mode action. err
// is nil on success; successMsg is set in that case for the status
// strip ("foo switched to dev mode").
type sourceOpDoneMsg struct {
	target     sourceTarget
	name       string
	err        error
	successMsg string
}

// fileEntry is one row in the Files tab of the detail view: a path
// (relative to the unit's destdir, with leading slash so it reads as
// the on-target path) and the size of the underlying inode. Symlinks
// report the size of the link, not its target.
type fileEntry struct {
	Path string
	Size int64
	Link bool
}

// Tabs that share the home screen. Tab key cycles forward.
type homeTab int

const (
	tabUnits homeTab = iota
	tabModules
	tabDiagnostics
	numHomeTabs
)

// Tabs inside the unit-detail view. Tab key cycles forward, mirroring
// the home tab behavior.
type detailTab int

const (
	detailTabInfo detailTab = iota
	detailTabFiles
	numDetailTabs
)

// Sort columns for the Files tab inside the detail view. Order matches
// the on-screen column order; the cycle order driven by `o` follows
// this sequence.
const (
	filesSortByName = iota
	filesSortBySize
	numFilesSortColumns
)

func (t detailTab) String() string {
	switch t {
	case detailTabInfo:
		return "Info"
	case detailTabFiles:
		return "Files"
	}
	return ""
}

func (t homeTab) String() string {
	switch t {
	case tabUnits:
		return "Units"
	case tabModules:
		return "Modules"
	case tabDiagnostics:
		return "Diagnostics"
	}
	return ""
}

// Flash view stages
type flashStage int

const (
	flashSelect     flashStage = iota // picking a device
	flashConfirm                      // y/N confirmation
	flashWriting                      // write in progress
	flashPermPrompt                   // permission denied; offer sudo chown
	flashDone                         // success
	flashError                        // failed
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

type flashChownDoneMsg struct {
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
	proj              *yoestar.Project
	projectDir        string
	arch              string
	warning           string // persistent warning banner (e.g., binfmt missing)
	notification      string // transient global notification (e.g., container rebuild)
	dag               *resolve.DAG
	units             []string
	hashes            map[string]string
	statuses          map[string]unitStatus
	cursor            int
	view              viewKind
	helpShowing       bool              // `?`-toggled keybinding overlay for the current page
	helpScroll        int               // scroll offset within the help overlay when it overflows
	activeTab         homeTab           // which tab is showing on the home screen
	modulesCursor     int               // cursor row in the Modules tab
	diagnosticsCursor int               // cursor row in the Diagnostics tab
	moduleStatus      map[string]string // module name -> "clean" / "dirty (N)" / "missing" / err; populated lazily
	detailUnit        string
	outputLines       []string // executor output (executor.log)
	logLines          []string // build log (build.log)
	detailScroll      int      // scroll offset from top in detail view
	autoFollow        bool     // auto-scroll to bottom during builds
	listOffset        int      // first visible row in unit list
	tick              bool     // toggles for flashing indicator
	width             int
	height            int
	message           string
	building          map[string]bool
	cancels           map[string]context.CancelFunc // cancel funcs for active builds
	confirm           string                        // non-empty = waiting for y/n confirmation
	queryEditing      bool                          // true while the user is typing in the query bar
	queryInput        string                        // text in the query bar; live-parsed every keystroke
	queryError        string                        // last parse error; rendered next to the query bar
	queryCompletions  []string                      // tab-completion candidates, rendered under the bar
	inSet             map[string]bool               // pre-computed in:X closure for the active query, nil if no in: filter
	visible           []int                         // indexes into m.units after applying m.query
	query             query.Query                   // active query, applied to m.units to produce visible
	queryRevertTo     query.Query                   // snapshot taken when the user opens `/`
	savedQuery        string                        // canonical form of the last user-saved query (or bootstrap)

	// Detail log search
	detailSearching  bool   // true = detail search input active
	detailSearchText string // current detail search query
	detailMatches    []int  // line indices in allLines that match
	detailMatchIdx   int    // current match cursor (-1 = none)

	// Detail-view tabs (Info/Files). Files tab lists everything the
	// unit installed (its destdir contents) sortable by name/size.
	detailTab           detailTab
	detailFiles         []fileEntry
	detailFilesScroll   int
	detailFilesSortCol  int
	detailFilesSortDesc bool

	// Setup view
	machines       []string // sorted machine names
	setupCursor    int      // cursor within setup options
	setupField     string   // "" = top-level, "machine" / "image" = picker active
	machineCursor  int      // cursor within machine list
	imageCursor    int      // cursor within image list
	parallelBuilds int      // effective `yoe build` concurrency (adjusted on the Setup page)

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

	// Build progress — replaces the feed line at the top of the screen
	// while at least one build is in flight. buildPending is the set of
	// units that emitted "waiting" but haven't yet emitted "done"/"failed",
	// used to dedup events when the user kicks off overlapping builds.
	// buildTotal is the count of non-cached units across the active
	// session and buildDone is how many have completed.
	buildProgress progress.Model
	buildTotal    int
	buildDone     int
	buildPending  map[string]bool

	// Per-unit display metrics — recomputed on project reload and after
	// builds so the unit table reflects fresh on-disk state.
	unitSize map[string]int64 // installed bytes (image units: .img file size)
	unitDeps map[string]int   // runtime closure size, excluding the unit itself

	// Source-state cache for the SRC column. Populated lazily from
	// BuildMeta.SourceState (units) or module.ReadState (modules) on
	// first render, then refreshed by U9's fsnotify watcher. Cleared
	// on project reload so stale entries don't survive a restart.
	unitSrcStates   map[string]source.State
	moduleSrcStates map[string]source.State

	// Active dev-mode modal (SSH/HTTPS picker, promote-kind picker,
	// discard-confirm). Nil when no prompt is showing.
	sourcePrompt *sourcePrompt

	// In-flight dev-mode operation (e.g. DevToUpstream's
	// `git fetch --unshallow` against a 100MB repo). The TUI parks
	// here while the goroutine works so the UI doesn't freeze and the
	// user has visible feedback. Nil when no operation is active.
	sourceOp *sourceOp

	// Background watcher that polls the on-disk state of every dev*
	// unit/module and pushes a sourceStateChangedMsg when DetectState
	// returns something new. Invariant: watcher membership matches
	// the set of unitSrcStates / moduleSrcStates whose value is in
	// the dev* family. Nil in tests that build a model directly.
	srcWatcher *sourceWatcher

	// Column sort state. sortColumn picks the comparator (sortByName etc.).
	// sortDesc inverts it. Click a header to switch column or toggle direction.
	sortColumn int
	sortDesc   bool

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

	// lastKeypress is the time the user last pressed a key. The auto-follow
	// path that scrolls a newly building unit into view checks this against
	// autoFollowIdleThreshold so it doesn't yank the cursor and viewport
	// while the user is actively navigating.
	lastKeypress time.Time
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
	hashes, err := resolve.ComputeAllHashes(dag, arch, proj.Defaults.Machine, build.SrcInputsFn(projectDir, arch, proj.Defaults.Machine))
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

	parallelBuilds := yoestar.DefaultParallelBuilds
	if ov.ParallelBuilds > 0 {
		parallelBuilds = ov.ParallelBuilds
	}

	m := model{
		proj:            proj,
		projectDir:      projectDir,
		arch:            arch,
		dag:             dag,
		units:           units,
		hashes:          hashes,
		statuses:        statuses,
		building:        make(map[string]bool),
		cancels:         make(map[string]context.CancelFunc),
		unitSrcStates:   make(map[string]source.State),
		moduleSrcStates: make(map[string]source.State),
		srcWatcher:      newSourceWatcher(),
		machines:        machines,
		flashProgress:   progress.New(progress.WithDefaultGradient()),
		buildProgress:   progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage()),
		buildPending:    make(map[string]bool),
		deployHost:      ov.DeployHost,
		parallelBuilds:  parallelBuilds,
		loadOpts:        cfg.LoadOpts,
		globalFlagArgs:  cfg.GlobalFlagArgs,
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
	m.recomputeMetrics()
	// Park the cursor on the default image so the table opens centered
	// on the artifact most users care about. scrollUnitIntoView is a
	// no-op when the active query filters that image out.
	if proj.Defaults.Image != "" {
		m.scrollUnitIntoView(proj.Defaults.Image)
	}

	m.checkBinfmtWarning()

	stopFeed, feedStatus := startProjectFeed(proj, projectDir)
	defer stopFeed()
	m.feedStatus = feedStatus

	p := tea.NewProgram(m, tea.WithAltScreen())
	tuiProgram = p

	// Arm the source watcher for any unit/module that's already in
	// dev* state at startup (e.g. user toggled it via the CLI before
	// launching the TUI). Polling fires immediately afterwards.
	m.armWatcherFromInitialStates()
	m.srcWatcher.Start(func(msg tea.Msg) {
		if tuiProgram != nil {
			tuiProgram.Send(msg)
		}
	})
	defer m.srcWatcher.Stop()

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

	case spinner.TickMsg:
		// Forward to the in-flight op's spinner so its frame advances.
		// Ignored when no op is pending — a stale tick can arrive after
		// the op has already completed and shouldn't crash us.
		if m.sourceOp != nil {
			var cmd tea.Cmd
			m.sourceOp.spinner, cmd = m.sourceOp.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case sourceOpDoneMsg:
		// Completion from a backgrounded dev-mode action. Pop the
		// progress view, surface success/error, and reconcile the
		// source-state cache so the next render shows the new token.
		op := m.sourceOp
		m.sourceOp = nil
		prev := viewUnits
		if op != nil {
			prev = op.prevView
		}
		m.view = prev
		if msg.err != nil {
			m.message = fmt.Sprintf("%s", msg.err)
			return m, nil
		}
		m.message = msg.successMsg
		switch msg.target {
		case targetUnit:
			m.invalidateUnitState(msg.name)
			// The previous build status reflected a different source
			// state; toggling pin↔dev (or P-pinning) invalidates that
			// — clear the cached/built status so the row doesn't
			// misleadingly show a green check for the old build.
			delete(m.statuses, msg.name)
		case targetModule:
			m.invalidateModuleState(msg.name)
		}
		return m, nil

	case sourceStateChangedMsg:
		// Watcher saw a state transition (e.g. user committed in a
		// dev clone outside the TUI). Update the cache directly so
		// the next render shows the new token without forcing a full
		// project reload.
		switch msg.target {
		case targetUnit:
			if m.unitSrcStates != nil {
				m.unitSrcStates[msg.name] = msg.state
			}
			// A state transition (dev → dev-mod, dev → dev-dirty,
			// etc.) means the working tree no longer matches what
			// the last build produced. Drop the cached build status
			// so the row doesn't keep showing a stale green check.
			// The hash itself already invalidates (SrcHashInputs
			// folds HEAD sha + diff sha for dev-dirty), so the next
			// build correctly cache-misses; this just makes the row
			// reflect that immediately.
			delete(m.statuses, msg.name)
		case targetModule:
			if m.moduleSrcStates != nil {
				m.moduleSrcStates[msg.name] = msg.state
			}
		}
		return m, nil

	case buildEventMsg:
		switch msg.status {
		case "cached":
			m.statuses[msg.unit] = statusCached
		case "done":
			m.statuses[msg.unit] = statusCached
			// Build just finished writing this unit's build.json (or
			// destdir/<name>.img for images), so refresh the SIZE column
			// now instead of waiting for the parent build's final
			// recomputeMetrics — otherwise sizes for transitive deps stay
			// blank for the entire duration of a multi-unit image build.
			m.refreshUnitSize(msg.unit)
			// Drop any cached source state — the build just wrote a
			// fresh SourceState (pin or dev) to BuildMeta, and the
			// renderer's lazy cache might have a stale empty value from
			// a mid-build render. The next paint re-reads BuildMeta.
			m.invalidateUnitState(msg.unit)
			cmd := m.markBuildUnitFinished(msg.unit)
			return m, cmd
		case "waiting":
			m.statuses[msg.unit] = statusWaiting
			m.markBuildUnitWaiting(msg.unit)
		case "building":
			m.statuses[msg.unit] = statusBuilding
			// When a build starts (often a transitive dep of the unit
			// the user invoked), scroll its row into view so the user
			// can see what's happening. Suppressed while the user is
			// actively navigating — otherwise transitive build events
			// would yank the cursor and viewport out from under them.
			if time.Since(m.lastKeypress) >= autoFollowIdleThreshold {
				m.scrollUnitIntoView(msg.unit)
			}
		case "failed":
			m.statuses[msg.unit] = statusFailed
			cmd := m.markBuildUnitFinished(msg.unit)
			return m, cmd
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
			m.recomputeMetrics()
			// Re-sort so newly-known size/deps land in the right place
			// when the user is sorted by one of those columns.
			if m.sortColumn == sortBySize || m.sortColumn == sortByDeps {
				m.sortVisible()
			}
		}
		// All goroutines are done — drop the progress bar and let the
		// feed banner come back. A fresh "waiting" event from a later
		// build will start a new session from zero.
		if len(m.building) == 0 {
			m.resetBuildProgress()
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
		// Both the flash and the build bars use progress.Model and
		// route by id internally, so it's safe to forward the same
		// frame to each — whichever one owns the id will animate.
		fp, fcmd := m.flashProgress.Update(msg)
		m.flashProgress = fp.(progress.Model)
		bp, bcmd := m.buildProgress.Update(msg)
		m.buildProgress = bp.(progress.Model)
		return m, tea.Batch(fcmd, bcmd)

	case flashDoneMsg:
		if errors.Is(msg.err, device.ErrPermission) {
			m.flashStage = flashPermPrompt
			m.flashErr = msg.err
			return m, nil
		}
		if msg.err != nil {
			m.flashStage = flashError
			m.flashErr = msg.err
		} else {
			m.flashStage = flashDone
		}
		return m, nil

	case flashChownDoneMsg:
		if msg.err != nil {
			m.flashStage = flashError
			m.flashErr = fmt.Errorf("sudo chown failed: %w", msg.err)
			return m, nil
		}
		cand := m.flashCandidates[m.flashCursor]
		m.flashStage = flashWriting
		m.flashWritten = 0
		m.flashErr = nil
		return m, m.flashWriteCmd(m.flashImagePath, cand.Path)

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
		// Record the keypress timestamp before any sub-handler runs so the
		// auto-follow gate sees a fresh time even when the key is consumed
		// by a modal handler (confirm prompt, search, etc.).
		m.lastKeypress = time.Now()
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
		// Help overlay: `?` opens a centered keybinding reference for the
		// current page. While it's up, scroll keys move the (possibly
		// taller-than-terminal) list and any other key dismisses it.
		// Text-entry modes that legitimately take a literal `?` (confirm,
		// detail search, query bar) are already consumed above; the deploy
		// host field is the only other one, so the toggle is suppressed
		// there.
		if m.helpShowing {
			switch msg.String() {
			case "up", "k":
				m.helpScroll--
			case "down", "j":
				m.helpScroll++
			case "pgup", "ctrl+b":
				m.helpScroll -= 10
			case "pgdown", "ctrl+f":
				m.helpScroll += 10
			case "g":
				m.helpScroll = 0
			case "G":
				m.helpScroll = m.helpMaxScroll()
			default:
				m.helpShowing = false
				return m, nil
			}
			if m.helpScroll < 0 {
				m.helpScroll = 0
			}
			if max := m.helpMaxScroll(); m.helpScroll > max {
				m.helpScroll = max
			}
			return m, nil
		}
		if msg.String() == "?" && !(m.view == viewDeploy && m.deployStage == deployHostInput) {
			m.helpShowing = true
			m.helpScroll = 0
			return m, nil
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
		case viewSourcePrompt:
			return m.updateSourcePrompt(msg)
		case viewSourceProgress:
			// Keys are inert while a dev-mode op is in flight — the
			// goroutine will finish on its own schedule, and a stray
			// `q` press shouldn't kill the TUI mid-fetch and leave the
			// clone half-rewritten.
			return m, nil
		}
	}
	return m, nil
}

func (m model) updateUnits(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Tab cycles between home-screen tabs regardless of which one is
	// active. Quit is also valid on every tab.
	switch msg.String() {
	case "tab":
		m.activeTab = (m.activeTab + 1) % numHomeTabs
		if m.activeTab == tabModules {
			m.refreshModuleStatus()
		}
		m.message = ""
		return m, nil
	case "shift+tab":
		m.activeTab = (m.activeTab + numHomeTabs - 1) % numHomeTabs
		if m.activeTab == tabModules {
			m.refreshModuleStatus()
		}
		m.message = ""
		return m, nil
	}

	switch m.activeTab {
	case tabModules:
		return m.updateModulesTab(msg)
	case tabDiagnostics:
		return m.updateDiagnosticsTab(msg)
	}

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

	case "g":
		if vis := m.visibleIndices(); len(vis) > 0 {
			m.cursor = vis[0]
			m.adjustListOffset()
		}
		return m, nil

	case "G":
		if vis := m.visibleIndices(); len(vis) > 0 {
			m.cursor = vis[len(vis)-1]
			m.adjustListOffset()
		}
		return m, nil

	case "enter":
		if m.cursor < len(m.units) {
			m.detailUnit = m.units[m.cursor]
			m.view = viewDetail
			m.detailTab = detailTabInfo
			m.detailFiles = nil
			m.detailFilesScroll = 0
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
			// Build is an action, not navigation: clear the idle timer
			// so the upcoming cascade of "building" events can auto-
			// follow the actively compiling unit into view.
			m.lastKeypress = time.Time{}
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

	case "u":
		if m.cursor < len(m.units) {
			return m.openSourcePromptForUnit(m.units[m.cursor])
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
		// Build is an action, not navigation: clear the idle timer
		// so the upcoming cascade of "building" events can auto-
		// follow the actively compiling unit into view.
		m.lastKeypress = time.Time{}
		return m, tea.Batch(cmds...)

	case "e":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			// Use the resolved unit's DefinedIn (= the winning module's
			// .star directory after prefer_modules + last-module-wins
			// shadowing) so editing opens the file yoe actually uses,
			// not just the first `<name>.star` the filesystem walk hits.
			if path := unitStarPath(m.proj.Units[name]); path != "" {
				return m, m.execEditor(path)
			}
			// Fallback for derived units (e.g., base-files-dev defined
			// via a helper inside another .star): scan the project for
			// any file mentioning the name.
			if path := findUnitFile(m.projectDir, name); path != "" {
				return m, m.execEditor(path)
			}
			m.message = fmt.Sprintf("Could not find .star file for %s", name)
		}
		return m, nil

	case "$":
		if m.cursor < len(m.units) {
			name := m.units[m.cursor]
			srcDir := m.unitSrcDir(name)
			if srcDir == "" {
				m.message = fmt.Sprintf("No source for %s — build it first (b) or it has no source", name)
				return m, nil
			}
			return m, m.execShell(srcDir)
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
			// Pre-position the cursor on the device the user picked last
			// time (saved as `flash_device` in local.star), so reflashing
			// the same SD card / USB stick is one keypress (`f`, `Enter`)
			// instead of a fresh hunt through the candidate list.
			if ov, err := yoestar.LoadLocalOverrides(m.projectDir); err == nil && ov.FlashDevice != "" {
				for i, c := range cands {
					if c.Path == ov.FlashDevice {
						m.flashCursor = i
						break
					}
				}
			}
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
		if m.queryInput != "" {
			m.queryInput += " " // ready for the user to append a new term
		}
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

	case "\\":
		// Snap-back: revert active query to whatever is saved as the default.
		bootstrap, _ := query.Parse(m.savedQuery) // savedQuery is canonical, parse must succeed
		m.query = bootstrap
		m.queryInput = m.query.String()
		m.queryError = ""
		m.applyQuery()
		return m, nil

	case "S":
		// Save the current active query to local.star as the new default.
		ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
		ov.Query = m.query.String()
		if err := yoestar.WriteLocalOverrides(m.projectDir, ov); err != nil {
			m.message = fmt.Sprintf("save query failed: %v", err)
			return m, nil
		}
		m.savedQuery = m.query.String()
		if m.query.IsEmpty() {
			m.message = "saved empty query (next session will follow project defaults)"
		} else {
			m.message = fmt.Sprintf("saved query: %s", m.query.String())
		}
		return m, nil

	case "o":
		// Cycle to the next sort column with its default direction.
		next := (m.sortColumn + 1) % numSortColumns
		m.sortColumn = next
		m.sortDesc = next == sortBySize || next == sortByDeps
		m.applySortReset()
		return m, nil

	case "O":
		// Toggle direction of the current sort column.
		m.sortDesc = !m.sortDesc
		m.applySortReset()
		return m, nil
	}
	return m, nil
}

// updateModulesTab handles keys when the Modules tab is active. It only
// consumes navigation and tab/quit keys — most unit-tab actions don't
// apply when looking at modules.
func (m model) updateModulesTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	maxRow := len(m.proj.ResolvedModules) - 1
	if maxRow < 0 {
		maxRow = 0
	}
	switch msg.String() {
	case "q", "ctrl+c":
		if len(m.cancels) > 0 {
			m.confirm = "quit"
			m.message = "Builds are running. Quit and cancel them? (y/n)"
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		if m.modulesCursor > 0 {
			m.modulesCursor--
		}
		return m, nil
	case "down", "j":
		if m.modulesCursor < maxRow {
			m.modulesCursor++
		}
		return m, nil
	case "pgup", "ctrl+b":
		page := m.modulesViewportHeight()
		m.modulesCursor -= page
		if m.modulesCursor < 0 {
			m.modulesCursor = 0
		}
		return m, nil
	case "pgdown", "ctrl+f":
		page := m.modulesViewportHeight()
		m.modulesCursor += page
		if m.modulesCursor > maxRow {
			m.modulesCursor = maxRow
		}
		return m, nil
	case "g":
		m.modulesCursor = 0
		return m, nil
	case "G":
		m.modulesCursor = maxRow
		return m, nil
	case "r":
		m.refreshModuleStatus()
		m.message = "Module status refreshed"
		return m, nil
	case "$":
		mods := m.proj.ResolvedModules
		if m.modulesCursor < 0 || m.modulesCursor >= len(mods) {
			return m, nil
		}
		rm := mods[m.modulesCursor]
		if rm.CloneDir == "" {
			m.message = fmt.Sprintf("Module %s is not synced — run `yoe sync`", rm.Name)
			return m, nil
		}
		return m, m.execShell(rm.CloneDir)
	case "u":
		mods := m.proj.ResolvedModules
		if m.modulesCursor < 0 || m.modulesCursor >= len(mods) {
			return m, nil
		}
		return m.openSourcePromptForModule(mods[m.modulesCursor].Name)
	}
	return m, nil
}

// modulesViewportHeight returns the number of module rows that fit on
// screen. Subtracts the home header, summary line, column header, and
// a help/message row.
func (m model) modulesViewportHeight() int {
	chrome := m.homeHeaderLines()
	chrome++ // summary line
	chrome++ // column header
	chrome++ // ↑ more
	chrome++ // ↓ more
	chrome++ // detail strip "dir: ..."
	chrome++ // detail strip "url: ..."
	chrome++ // blank before bottom row
	chrome++ // help / message
	h := m.height - chrome
	if h < 3 {
		h = 3
	}
	return h
}

// diagnosticsViewportHeight returns the number of content lines that
// fit in the Diagnostics tab body. Mirrors the layout of viewUnitsTab
// so a tab switch keeps the body in the same screen region.
func (m model) diagnosticsViewportHeight() int {
	chrome := m.homeHeaderLines()
	chrome++ // summary line
	chrome++ // ↑ more (always reserved)
	chrome++ // ↓ more (always reserved)
	chrome++ // blank before bottom row
	chrome++ // help / message
	h := m.height - chrome
	if h < 3 {
		h = 3
	}
	return h
}

// updateDiagnosticsTab handles keys when the Diagnostics tab is active.
// Read-only navigation: j/k/up/down step rows, pgup/pgdown jump a page.
func (m model) updateDiagnosticsTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.diagnosticsRowCount()
	maxRow := rows - 1
	if maxRow < 0 {
		maxRow = 0
	}
	switch msg.String() {
	case "q", "ctrl+c":
		if len(m.cancels) > 0 {
			m.confirm = "quit"
			m.message = "Builds are running. Quit and cancel them? (y/n)"
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		if m.diagnosticsCursor > 0 {
			m.diagnosticsCursor--
		}
		return m, nil
	case "down", "j":
		if m.diagnosticsCursor < maxRow {
			m.diagnosticsCursor++
		}
		return m, nil
	case "pgup", "ctrl+b":
		page := m.diagnosticsViewportHeight()
		m.diagnosticsCursor -= page
		if m.diagnosticsCursor < 0 {
			m.diagnosticsCursor = 0
		}
		return m, nil
	case "pgdown", "ctrl+f":
		page := m.diagnosticsViewportHeight()
		m.diagnosticsCursor += page
		if m.diagnosticsCursor > maxRow {
			m.diagnosticsCursor = maxRow
		}
		return m, nil
	case "g":
		m.diagnosticsCursor = 0
		return m, nil
	case "G":
		m.diagnosticsCursor = maxRow
		return m, nil
	}
	return m, nil
}

// diagnosticsRowCount returns the total number of rows shown in the
// Diagnostics tab — sum of shadow rows and duplicate-provides rows.
func (m model) diagnosticsRowCount() int {
	return len(m.proj.Diagnostics.Shadows) + len(m.proj.Diagnostics.DuplicateProvides)
}

// refreshModuleStatus runs `git status --porcelain` in each resolved
// module's directory and stores a summary string in m.moduleStatus.
// Cheap enough to call on every tab switch — the worst case is a few
// dozen short git invocations.
func (m *model) refreshModuleStatus() {
	if m.moduleStatus == nil {
		m.moduleStatus = make(map[string]string)
	}
	for _, rm := range m.proj.ResolvedModules {
		if !rm.Available {
			m.moduleStatus[rm.Name] = "missing"
			continue
		}
		out, err := exec.Command("git", "-C", rm.CloneDir, "status", "--porcelain").Output()
		if err != nil {
			m.moduleStatus[rm.Name] = "no-git"
			continue
		}
		s := strings.TrimSpace(string(out))
		if s == "" {
			m.moduleStatus[rm.Name] = "clean"
			continue
		}
		n := strings.Count(s, "\n") + 1
		m.moduleStatus[rm.Name] = fmt.Sprintf("dirty (%d)", n)
	}
}

// applySortReset re-applies the query (which re-sorts m.visible), then
// snaps the cursor and viewport to the top so the freshly-ranked rows
// are actually visible. Without the snap, adjustListOffset would scroll
// back to wherever the cursor was sitting in the new order.
func (m *model) applySortReset() {
	m.applyQuery()
	if len(m.visible) > 0 {
		m.cursor = m.visible[0]
	}
	m.listOffset = 0
	m.adjustListOffset()
}

func (m model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Any keystroke other than Tab clears a leftover tab-completion
	// list. Tab manages its own state below.
	if msg.String() != "tab" {
		m.message = ""
		m.queryCompletions = nil
	}
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

	case "ctrl+u":
		// Readline-style kill-line: clear the entire input back to a
		// blank bar in one keystroke. Live-applied like a backspace —
		// the unit list updates immediately to "showing all".
		m.queryInput = ""
		m.queryCompletions = nil
		m.reparse()
		return m, nil

	case "tab":
		ctx := query.Context{
			Modules: m.moduleNames(),
			Units:   m.units, // already sorted
		}
		start, end, cands := query.Complete(m.queryInput, len(m.queryInput), ctx)
		switch len(cands) {
		case 0:
			m.message = "no completions"
			m.queryCompletions = nil
		case 1:
			// splice in the single candidate, preserving field: prefix when present
			m.queryInput = spliceCompletion(m.queryInput, start, end, cands[0])
			m.reparse()
			m.message = ""
			m.queryCompletions = nil
		default:
			// longest common prefix
			lcp := longestCommonPrefix(cands)
			cur := m.queryInput[start:end]
			// strip field: prefix from cur for comparison with value-only lcp
			if i := strings.IndexByte(cur, ':'); i >= 0 {
				cur = cur[i+1:]
			}
			if lcp != "" && lcp != cur {
				m.queryInput = spliceCompletion(m.queryInput, start, end, lcp)
				m.reparse()
				m.message = ""
				m.queryCompletions = nil
			} else {
				// Can't advance the input — surface the options as a
				// vertical list under the search bar so the user can
				// pick the next character to type. Without this branch
				// tab looks like a no-op when the user just opened the
				// bar (cands = all top-level keywords) or typed an
				// ambiguous single letter. Cleared on the next keystroke.
				m.queryCompletions = cands
				m.message = ""
			}
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
	m.sortVisible()
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

// moduleNames returns the sorted set of module names in the project,
// plus the synthetic "project" name used for project-root units.
func (m model) moduleNames() []string {
	set := map[string]bool{"project": true}
	for _, u := range m.proj.Units {
		if u.Module != "" {
			set[u.Module] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func longestCommonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			if p == "" {
				return ""
			}
			p = p[:len(p)-1]
		}
	}
	return p
}

// spliceCompletion inserts cand into input, replacing input[start:end].
// When input[start:end] contains a colon (field:value token), the
// field: prefix is preserved and only the value portion is replaced.
func spliceCompletion(input string, start, end int, cand string) string {
	tok := input[start:end]
	if i := strings.IndexByte(tok, ':'); i >= 0 {
		return input[:start] + tok[:i+1] + cand + input[end:]
	}
	return input[:start] + cand + input[end:]
}

func (m model) visibleIndices() []int {
	return m.visible
}

// prevVisible returns the unit-index of the row immediately before the
// cursor in the current display order. Walks m.visible directly rather
// than comparing numeric indices so it works under any sort.
func (m model) prevVisible() int {
	vis := m.visibleIndices()
	for i, idx := range vis {
		if idx == m.cursor {
			if i > 0 {
				return vis[i-1]
			}
			return m.cursor
		}
	}
	if len(vis) > 0 {
		return vis[0]
	}
	return m.cursor
}

func (m model) nextVisible() int {
	vis := m.visibleIndices()
	for i, idx := range vis {
		if idx == m.cursor {
			if i+1 < len(vis) {
				return vis[i+1]
			}
			return m.cursor
		}
	}
	if len(vis) > 0 {
		return vis[0]
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
				// Drop the cached source state and disarm the watcher
				// — BuildMeta.SourceState is gone, the next render
				// shows the SRC column as blank (empty state).
				m.invalidateUnitState(name)
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
					m.invalidateUnitState(name)
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
var setupOptions = []string{"Machine", "Image", "Parallel builds"}

func (m model) updateSetup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.setupField {
	case "machine":
		return m.updateSetupMachine(msg)
	case "image":
		return m.updateSetupImage(msg)
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

	case "left", "h", "right", "l":
		if setupOptions[m.setupCursor] != "Parallel builds" {
			return m, nil
		}
		if msg.String() == "left" || msg.String() == "h" {
			if m.parallelBuilds > 1 {
				m.parallelBuilds--
			}
		} else {
			m.parallelBuilds++
		}
		ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
		ov.ParallelBuilds = m.parallelBuilds
		if err := yoestar.WriteLocalOverrides(m.projectDir, ov); err != nil {
			m.message = fmt.Sprintf("Parallel builds set to %d (warning: failed to save local.star: %v)", m.parallelBuilds, err)
		} else {
			m.message = fmt.Sprintf("Parallel builds set to %d (saved to local.star)", m.parallelBuilds)
		}
		return m, nil

	case "enter":
		switch setupOptions[m.setupCursor] {
		case "Machine":
			m.setupField = "machine"
			// Land the picker cursor on the active machine.
			for i, name := range m.machines {
				if name == m.proj.Defaults.Machine {
					m.machineCursor = i
					break
				}
			}
		case "Image":
			m.setupField = "image"
			m.imageCursor = 0
			imgs := m.imageUnits()
			for i, name := range imgs {
				if name == m.proj.Defaults.Image {
					m.imageCursor = i
					break
				}
			}
		}
		return m, nil
	}
	return m, nil
}

// imageUnits returns the sorted names of all image-class units in the project.
func (m model) imageUnits() []string {
	var out []string
	for name, u := range m.proj.Units {
		if u.Class == "image" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (m model) updateSetupImage(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	imgs := m.imageUnits()
	switch msg.String() {
	case "esc":
		m.setupField = ""
		return m, nil

	case "up", "k":
		if m.imageCursor > 0 {
			m.imageCursor--
		}
		return m, nil

	case "down", "j":
		if m.imageCursor < len(imgs)-1 {
			m.imageCursor++
		}
		return m, nil

	case "enter":
		if len(imgs) == 0 {
			return m, nil
		}
		picked := imgs[m.imageCursor]
		m.proj.Defaults.Image = picked
		// Re-anchor the search to the new image's closure: both the
		// active query and the saved default switch to in:<picked> so
		// the table filters to what the new image actually pulls in.
		newQ := "in:" + picked
		if q, err := query.Parse(newQ); err == nil {
			m.query = q
			m.queryInput = q.String()
			m.queryError = ""
			m.savedQuery = m.query.String()
		}
		ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
		ov.Image = picked
		ov.Query = newQ
		if err := yoestar.WriteLocalOverrides(m.projectDir, ov); err != nil {
			m.message = fmt.Sprintf("Image set to %s (warning: failed to save local.star: %v)", picked, err)
		} else {
			m.message = fmt.Sprintf("Image set to %s (saved to local.star)", picked)
		}
		m.applyQuery()
		m.setupField = ""
		m.view = viewUnits
		m.scrollUnitIntoView(picked)
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
	// Tab cycles between detail tabs (Info / Files), mirroring the home
	// screen. Handled before the per-tab dispatch so it works regardless
	// of which tab is active.
	switch msg.String() {
	case "tab":
		m.detailTab = (m.detailTab + 1) % numDetailTabs
		if m.detailTab == detailTabFiles {
			m.refreshDetailFiles()
		}
		m.message = ""
		return m, nil
	case "shift+tab":
		m.detailTab = (m.detailTab + numDetailTabs - 1) % numDetailTabs
		if m.detailTab == detailTabFiles {
			m.refreshDetailFiles()
		}
		m.message = ""
		return m, nil
	}

	if m.detailTab == detailTabFiles {
		return m.updateDetailFiles(msg)
	}

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
		m.detailFiles = nil
		m.detailFilesScroll = 0
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

	case "$":
		srcDir := m.unitSrcDir(m.detailUnit)
		if srcDir == "" {
			m.message = fmt.Sprintf("No source for %s — build it first (b) or it has no source", m.detailUnit)
			return m, nil
		}
		return m, m.execShell(srcDir)

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

	case "u":
		return m.openSourcePromptForUnit(m.detailUnit)

	case "P":
		return m.openPromotePrompt(m.detailUnit)

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
	if m.helpShowing {
		return m.viewHelp()
	}
	switch m.view {
	case viewDetail:
		return m.viewDetail()
	case viewSetup:
		return m.viewSetup()
	case viewFlash:
		return m.viewFlash()
	case viewDeploy:
		return m.viewDeploy()
	case viewSourcePrompt:
		return m.viewSourcePrompt()
	case viewSourceProgress:
		return m.viewSourceProgress()
	default:
		return m.viewUnits()
	}
}

// renderHomeHeader renders the title + warning + notification + feed
// banners + the tab bar. Shared by all home-screen tabs so the chrome
// is identical across them.
func (m model) renderHomeHeader() string {
	var b strings.Builder
	machine := m.proj.Defaults.Machine
	image := m.proj.Defaults.Image

	// Tab bar with diagnostics-count badge so the user notices when
	// shadows or duplicate provides exist without leaving the Units tab.
	var labels []string
	for t := homeTab(0); t < numHomeTabs; t++ {
		label := t.String()
		if t == tabDiagnostics {
			n := m.diagnosticsRowCount()
			if n > 0 {
				label = fmt.Sprintf("%s (%d)", label, n)
			}
		}
		labels = append(labels, label)
	}
	fmt.Fprintf(&b, "  %s    %s    %s%s\n",
		titleStyle.Render("[yoe]"),
		renderTabBar(labels, int(m.activeTab)),
		helpKeyStyle.Render("tab"),
		helpStyle.Render(": switch"))
	fmt.Fprintf(&b, "  Machine: %s  Image: %s\n",
		headerStyle.Render(machine),
		headerStyle.Render(image))

	if m.warning != "" {
		warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)
		fmt.Fprintf(&b, "  %s\n", warnStyle.Render(m.warning))
	}
	if m.notification != "" {
		notifyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)
		fmt.Fprintf(&b, "  %s\n", notifyStyle.Render("⏳ "+m.notification))
	}
	if m.buildSessionActive() {
		fmt.Fprintf(&b, "  %s\n", m.renderBuildProgress())
	} else if m.feedStatus != "" {
		feedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		fmt.Fprintf(&b, "  %s\n", feedStyle.Render("feed: "+m.feedStatus))
	}
	return b.String()
}

// renderQueryCompletions formats the tab-completion candidate list as
// a vertical column under the query bar. Truncates with a "(N more)"
// hint past a threshold so a one-letter ambiguous prefix can't push
// the unit list off the bottom of the screen.
func (m model) renderQueryCompletions() string {
	const maxRows = 8
	cands := m.queryCompletions
	if len(cands) == 0 {
		return ""
	}
	var b strings.Builder
	shown := cands
	if len(shown) > maxRows {
		shown = shown[:maxRows]
	}
	for _, c := range shown {
		b.WriteString("    ")
		b.WriteString(queryActiveStyle.Render(c))
		b.WriteString("\n")
	}
	if len(cands) > len(shown) {
		fmt.Fprintf(&b, "    %s\n",
			queryDimStyle.Render(fmt.Sprintf("(%d more — type a letter to narrow)", len(cands)-len(shown))))
	}
	return b.String()
}

func (m model) viewUnits() string {
	switch m.activeTab {
	case tabModules:
		return m.viewModulesTab()
	case tabDiagnostics:
		return m.viewDiagnosticsTab()
	}
	return m.viewUnitsTab()
}

func (m model) viewUnitsTab() string {
	var b strings.Builder
	b.WriteString(m.renderHomeHeader())

	// Query header — when the user presses `/`, the input editor replaces
	// the query body in place rather than opening a separate input row at
	// the bottom of the screen, so the eye stays on one Query: ... line.
	counter := fmt.Sprintf("Units: %d/%d", len(m.visible), len(m.units))
	if m.queryEditing {
		var body string
		if m.queryError != "" {
			body = fmt.Sprintf("/%s    %s",
				m.queryInput,
				queryErrorStyle.Render(m.queryError))
		} else {
			body = fmt.Sprintf("/%s▌", m.queryInput)
		}
		b.WriteString(fmt.Sprintf("  %s%s    %s\n",
			queryDimStyle.Render("Query: "),
			body,
			queryDimStyle.Render(counter)))
		// Tab-completion candidates: when the input can't be advanced
		// further (multiple equally-good matches), drop the list right
		// under the bar — closer to the eye than the bottom help row.
		if len(m.queryCompletions) > 0 {
			b.WriteString(m.renderQueryCompletions())
		}
	} else {
		qStr := m.query.String()
		qBody := qStr
		if qBody == "" {
			qBody = "(empty — showing all)"
		}
		style := queryDimStyle
		if qStr != m.savedQuery {
			style = queryActiveStyle
		}
		b.WriteString(fmt.Sprintf("  %s%s    %s\n",
			queryDimStyle.Render("Query: "),
			style.Render(qBody),
			queryDimStyle.Render(counter)))
	}

	// Column header — pad widths match the rows below; clicking a label
	// switches the sort. A trailing arrow marks the active column. Padding
	// is by display cells (not bytes) since the arrow runes are multi-byte
	// — fmt %*s would over-pad if we let it count bytes.
	headerLabel := func(col int, label string, w int, rightAlign bool) string {
		arrow := ""
		if m.sortColumn == col {
			if m.sortDesc {
				arrow = "↓"
			} else {
				arrow = "↑"
			}
		}
		displayW := len(label)
		if arrow != "" {
			displayW++
		}
		if displayW >= w {
			return label + arrow
		}
		pad := strings.Repeat(" ", w-displayW)
		if rightAlign {
			return pad + label + arrow
		}
		return label + arrow + pad
	}
	b.WriteString(fmt.Sprintf("  %s %s %s %s %s %s %s %s\n",
		headerStyle.Render(headerLabel(sortByName, "NAME", 28, false)),
		headerStyle.Render(headerLabel(sortByClass, "CLASS", 9, false)),
		headerStyle.Render(headerLabel(sortByModule, "MODULE", 14, false)),
		headerStyle.Render(headerLabel(sortByVersion, "VERSION", 11, false)),
		headerStyle.Render(fmt.Sprintf("%-9s", "SRC")),
		headerStyle.Render(headerLabel(sortBySize, "SIZE", 6, true)),
		headerStyle.Render(headerLabel(sortByDeps, "DEPS", 5, true)),
		headerStyle.Render(headerLabel(sortByStatus, "STATUS", 10, false))))

	// Determine visible units — filtered by current query state
	visible := m.visible

	// Calculate visible window for unit list
	maxRows := m.listViewportHeight()
	end := m.listOffset + maxRows
	if end > len(visible) {
		end = len(visible)
	}

	// Always emit a row here — either the "↑ N more" indicator when
	// scrolled, or a blank line — so the bottom of the screen doesn't
	// shift up by one when the user is at the top of the list.
	if m.listOffset > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more", m.listOffset)))
	}
	b.WriteString("\n")

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
		module := ""
		version := ""
		if u, ok := m.proj.Units[name]; ok {
			class = u.Class
			module = u.Module
			version = u.Version
			if module == "" {
				module = "(local)"
			}
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
		size := formatSize(m.unitSize[name])
		depsStr := ""
		if d, ok := m.unitDeps[name]; ok && d > 0 {
			depsStr = fmt.Sprintf("%d", d)
		}

		paddedName := clipFixed(name, 28)
		paddedClass := clipFixed(class, 9)
		paddedModule := clipFixed(module, 14)
		paddedVersion := clipFixed(version, 11)
		paddedSize := fmt.Sprintf("%6s", size)
		paddedDeps := fmt.Sprintf("%5s", depsStr)
		srcCell := m.renderSrcCell(name)
		b.WriteString(fmt.Sprintf("%s%s %s %s %s %s %s %s %s\n",
			cursor,
			m.renderName(paddedName, nameStyle),
			classStyle.Render(paddedClass),
			classStyle.Render(paddedModule),
			classStyle.Render(paddedVersion),
			srcCell,
			classStyle.Render(paddedSize),
			classStyle.Render(paddedDeps),
			status))
	}

	// Pad with blanks when the visible slice is shorter than the
	// viewport so the help bar stays pinned to the bottom of the screen.
	// Matches the pattern used by viewModulesTab and viewDiagnosticsTab.
	for i := end - m.listOffset; i < maxRows; i++ {
		b.WriteString("\n")
	}

	// Same treatment as ↑ above — always one line, blank when there
	// isn't more below — so the bottom row's position is independent
	// of the scroll state.
	if end < len(visible) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(visible)-end)))
	}
	b.WriteString("\n")

	if len(m.visible) == 0 {
		b.WriteString(dimStyle.Render("  no units match\n"))
	}

	// Spare line above the bottom row — always shows the full name of
	// the cursor's unit in a faded version of the cursor green. Useful
	// when the name is longer than the NAME column, and a quiet
	// confirmation of which unit the cursor is on the rest of the time.
	if m.cursor < len(m.units) {
		b.WriteString(cursorNameStyle.Render("  " + m.units[m.cursor]))
	}
	b.WriteString("\n")
	if m.message != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  " + m.message))
	} else {
		// While the query input is focused, only the keys updateSearch
		// actually handles are reachable — printable chars, tab to
		// complete, backspace, enter to commit, esc to revert. Showing
		// the navigation help bar there is a lie, so swap in a help
		// row that matches the active mode.
		var items []helpItem
		switch {
		case m.queryEditing:
			items = searchEditHelpItems
		default:
			items = defaultHelpItems
			if m.cursor < len(m.units) {
				name := m.units[m.cursor]
				if u, ok := m.proj.Units[name]; ok && u.Class == "image" {
					if m.statuses[name] == statusCached {
						items = imageCachedHelpItems
					} else {
						items = imageHelpItems
					}
				}
			}
		}
		b.WriteString(renderHelp(items))
	}

	return b.String()
}

// helpItem is one keyboard shortcut + its label, rendered as
// "<amber key> <gray label>" in the bottom help bar.
type helpItem struct {
	key   string
	label string
}

var (
	defaultHelpItems = []helpItem{
		{"b", "build"}, {"D", "deploy"}, {"x", "cancel"}, {"e", "edit"},
		{"$", "shell"}, {"l", "log"}, {"s", "setup"}, {"/", "search"},
		{`\`, "home"}, {"S", "save"}, {"o", "sort"}, {"?", "help"}, {"q", "quit"},
	}
	imageHelpItems = []helpItem{
		{"b", "build"}, {"x", "cancel"}, {"r", "run"}, {"e", "edit"},
		{"$", "shell"}, {"l", "log"}, {"s", "setup"}, {"/", "search"},
		{`\`, "home"}, {"S", "save"}, {"?", "help"}, {"q", "quit"},
	}
	imageCachedHelpItems = []helpItem{
		{"b", "build"}, {"x", "cancel"}, {"r", "run"}, {"f", "flash"},
		{"e", "edit"}, {"$", "shell"}, {"l", "log"}, {"s", "setup"},
		{"/", "search"}, {`\`, "home"}, {"S", "save"}, {"?", "help"}, {"q", "quit"},
	}
	// Shown while the query input is focused — these are the only keys
	// updateSearch actually handles; navigation/build shortcuts are
	// inert until the user commits or escapes the search bar.
	searchEditHelpItems = []helpItem{
		{"type", "filter"}, {"tab", "complete"}, {"⌫", "delete"},
		{"^U", "clear"}, {"enter", "apply"}, {"esc", "cancel"},
	}
	detailHelpItems = []helpItem{
		{"esc", "back"}, {"j/k", "scroll"}, {"g", "top"}, {"G", "bottom"},
		{"/", "search"}, {"b", "build"}, {"$", "shell"}, {"u", "src"},
		{"P", "pin"}, {"d", "diagnose"}, {"l", "log"}, {"?", "help"},
	}
	detailImageHelpItems = []helpItem{
		{"esc", "back"}, {"j/k", "scroll"}, {"g", "top"}, {"G", "bottom"},
		{"/", "search"}, {"b", "build"}, {"r", "run"}, {"$", "shell"},
		{"d", "diagnose"}, {"l", "log"}, {"?", "help"},
	}
	detailFilesHelpItems = []helpItem{
		{"esc", "back"}, {"j/k", "scroll"}, {"g", "top"}, {"G", "bottom"},
		{"o", "sort"}, {"O", "reverse"}, {"?", "help"},
	}
)

// renderHelp formats a list of shortcuts as "  k1 label1  k2 label2 …"
// with the shortcut key in amber and the label in dim gray.
func renderHelp(items []helpItem) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString("  ")
		b.WriteString(helpKeyStyle.Render(it.key))
		b.WriteString(" ")
		b.WriteString(helpStyle.Render(it.label))
	}
	return b.String()
}

// helpEntry is one row in the `?` overlay: a key (or key group) and what it
// does. helpSection groups related entries under a heading.
type helpEntry struct{ keys, desc string }
type helpSection struct {
	title   string
	entries []helpEntry
}

var (
	helpBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#e8863a")).
			Padding(1, 3)
	helpSectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
)

// helpGeneral is appended to every page so the dismiss/quit keys are always
// documented in the same place.
func helpGeneral(quit bool) helpSection {
	s := helpSection{title: "General", entries: []helpEntry{
		{"?", "open / close this help"},
	}}
	if quit {
		s.entries = append(s.entries, helpEntry{"q  ·  Ctrl+C", "quit yoe"})
	}
	return s
}

// helpSections returns the page title and keybinding sections for whatever
// page is currently active. It mirrors the dispatch in Update so the overlay
// always documents exactly the keys that page handles.
func (m model) helpSections() (string, []helpSection) {
	nav := helpSection{title: "Navigation", entries: []helpEntry{
		{"↑  ·  k", "move up"},
		{"↓  ·  j", "move down"},
		{"PgUp  ·  Ctrl+B", "page up"},
		{"PgDn  ·  Ctrl+F", "page down"},
		{"g  ·  G", "jump to top / bottom"},
	}}

	switch m.view {
	case viewDetail:
		scroll := helpSection{title: "Scroll", entries: []helpEntry{
			{"↑  ·  k", "scroll up one line"},
			{"↓  ·  j", "scroll down one line"},
			{"PgUp  ·  Ctrl+B", "scroll page up"},
			{"PgDn  ·  Ctrl+F", "scroll page down"},
			{"g  ·  G", "jump to top / bottom"},
		}}
		tabs := helpSection{title: "Tabs", entries: []helpEntry{
			{"Tab  ·  Shift+Tab", "switch Info / Files tab"},
			{"Esc", "back to the unit list"},
		}}
		if m.detailTab == detailTabFiles {
			return "Detail · Files — installed files in the unit's .apk", []helpSection{
				tabs, scroll,
				{title: "Sort", entries: []helpEntry{
					{"o", "cycle sort column (name / size)"},
					{"O", "reverse sort direction"},
				}},
				helpGeneral(true),
			}
		}
		return "Detail · Info — dependency graph and build streams", []helpSection{
			tabs, scroll,
			{title: "Search", entries: []helpEntry{
				{"/", "search the build log"},
				{"n  ·  N", "next / previous match"},
				{"Esc", "clear search (first press), then back"},
			}},
			{title: "Actions", entries: []helpEntry{
				{"b", "build this unit"},
				{"r", "run the image in QEMU (image units)"},
				{"D", "deploy to a host over SSH (non-image)"},
				{"$", "open a shell in the source dir"},
				{"u", "toggle source between pin and dev mode"},
				{"P", "pin the current HEAD into the .star tag"},
				{"d", "launch claude /diagnose on the build log"},
				{"l", "open the build log in $EDITOR"},
			}},
			helpGeneral(true),
		}

	case viewSetup:
		return "Setup — machine, default image, parallel builds", []helpSection{
			{title: "Navigate", entries: []helpEntry{
				{"↑  ·  k", "move up (Machine / Image / Parallel builds)"},
				{"↓  ·  j", "move down"},
				{"Enter", "open the picker for the selected option"},
				{"←/→  ·  h/l", "adjust Parallel builds"},
				{"Esc  ·  q", "back to the unit list"},
			}},
			{title: "In a picker", entries: []helpEntry{
				{"↑  ·  ↓", "choose a machine / image"},
				{"Enter", "select and save to local.star"},
				{"Esc", "close the picker without changing"},
			}},
			helpGeneral(false),
		}

	case viewFlash:
		return "Flash — write a built image to a device", []helpSection{
			{title: "Select device", entries: []helpEntry{
				{"↑  ·  ↓", "choose a removable device"},
				{"Enter", "continue to the confirm step"},
				{"Esc  ·  q", "back to the unit list"},
			}},
			{title: "Confirm / permissions", entries: []helpEntry{
				{"y", "write the image (or run sudo chown)"},
				{"n  ·  Esc", "cancel and go back"},
			}},
			{title: "When finished", entries: []helpEntry{
				{"Esc  ·  Enter  ·  q", "return to the unit list"},
			}},
			helpGeneral(false),
		}

	case viewDeploy:
		return "Deploy — build and push a unit to a host over SSH", []helpSection{
			{title: "Enter host", entries: []helpEntry{
				{"type", "edit the target host (user@host)"},
				{"Enter", "start the build + deploy"},
				{"Ctrl+U", "clear the host field"},
				{"Esc", "cancel and go back"},
			}},
			{title: "When finished", entries: []helpEntry{
				{"Esc  ·  Enter  ·  q", "return to the unit list"},
			}},
			{title: "Note", entries: []helpEntry{
				{"?", "available except while typing the host"},
			}},
			helpGeneral(false),
		}

	case viewSourcePrompt:
		return "Source mode — pin ↔ dev for this unit / module", []helpSection{
			{title: "Choose", entries: []helpEntry{
				{"↑  ·  k", "move up (skips disabled options)"},
				{"↓  ·  j", "move down"},
				{"Enter", "apply the selected option"},
				{"Esc  ·  q", "cancel, keep the current mode"},
			}},
			helpGeneral(false),
		}

	default: // viewUnits — Units / Modules / Diagnostics tabs
		switch m.activeTab {
		case tabModules:
			return "Modules — external git modules and dev mode", []helpSection{
				{title: "Navigation", entries: append(nav.entries,
					helpEntry{"Tab  ·  Shift+Tab", "switch Units / Modules / Diagnostics"})},
				{title: "Actions", entries: []helpEntry{
					{"$", "open a shell in the module's clone dir"},
					{"u", "toggle source between pin and dev mode"},
					{"r", "refresh the module's git status"},
				}},
				helpGeneral(true),
			}
		case tabDiagnostics:
			return "Diagnostics — shadowed units and duplicate provides", []helpSection{
				{title: "Navigation", entries: append(nav.entries,
					helpEntry{"Tab  ·  Shift+Tab", "switch Units / Modules / Diagnostics"})},
				helpGeneral(true),
			}
		default:
			return "Units — the build target list", []helpSection{
				{title: "Navigation", entries: append(nav.entries,
					helpEntry{"Tab  ·  Shift+Tab", "switch Units / Modules / Diagnostics"},
					helpEntry{"Enter", "open the unit detail view"})},
				{title: "Build", entries: []helpEntry{
					{"b", "build the selected unit"},
					{"B", "build all visible units"},
					{"x", "cancel the selected unit's build"},
					{"c", "clean the selected unit's artifacts"},
					{"C", "clean all build artifacts"},
				}},
				{title: "Inspect", entries: []helpEntry{
					{"e", "edit the unit's .star file in $EDITOR"},
					{"$", "open a shell in the unit's source dir"},
					{"u", "toggle source between pin and dev mode"},
					{"l", "open the unit's build log"},
					{"d", "launch claude /diagnose on the build log"},
					{"a", "launch claude /new-unit"},
					{"r", "run an image unit in QEMU"},
					{"f", "flash a built image to a device"},
					{"D", "deploy a non-image unit to a host"},
				}},
				{title: "Filter & sort", entries: []helpEntry{
					{"/", "edit the filter query"},
					{`\`, "reset the query to the saved default"},
					{"S", "save the current query as the default"},
					{"o  ·  O", "cycle sort column / reverse direction"},
					{"s", "open Setup (machine / image)"},
				}},
				helpGeneral(true),
			}
		}
	}
}

// helpBodyLines formats the sections (the scrollable region between the
// pinned title and footer) into one display line per row, with the key
// column padded to a common width. Used by both viewHelp and helpMaxScroll
// so the rendered window and the scroll clamp agree on the line count.
func (m model) helpBodyLines() []string {
	_, sections := m.helpSections()
	keyWidth := 0
	for _, s := range sections {
		for _, e := range s.entries {
			if w := lipgloss.Width(e.keys); w > keyWidth {
				keyWidth = w
			}
		}
	}
	var lines []string
	for i, s := range sections {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, helpSectionStyle.Render(s.title))
		for _, e := range s.entries {
			pad := strings.Repeat(" ", keyWidth-lipgloss.Width(e.keys))
			lines = append(lines, "  "+helpKeyStyle.Render(e.keys)+pad+"   "+helpStyle.Render(e.desc))
		}
	}
	return lines
}

// helpViewportHeight is how many body lines fit between the pinned title
// and footer inside the box, given the terminal height. 8 accounts for the
// box border (2) + vertical padding (2) + title line + blank (2) + blank +
// footer (2). Returns 0 when the size is unknown (render everything).
func (m model) helpViewportHeight() int {
	if m.height <= 0 {
		return 0
	}
	vp := m.height - 8
	if vp < 1 {
		vp = 1
	}
	return vp
}

// helpMaxScroll is the largest valid scroll offset for the current page and
// terminal size. Update clamps m.helpScroll to this so `up` after `G` still
// moves and the window never scrolls past the last line.
func (m model) helpMaxScroll() int {
	vp := m.helpViewportHeight()
	if vp == 0 {
		return 0
	}
	body := m.helpBodyLines()
	if len(body) <= vp {
		return 0
	}
	return len(body) - vp
}

// viewHelp renders the per-page keybinding reference as a rounded amber box
// centered over the screen, with a pinned title and footer and a scrollable
// body when the list is taller than the terminal. Scroll keys are handled in
// Update; any other key dismisses it.
func (m model) viewHelp() string {
	title, _ := m.helpSections()
	body := m.helpBodyLines()
	vp := m.helpViewportHeight()

	scrollable := vp > 0 && len(body) > vp
	window := body
	footer := dimStyle.Render("press any key to close")
	if scrollable {
		scroll := m.helpScroll
		if max := len(body) - vp; scroll > max {
			scroll = max
		}
		if scroll < 0 {
			scroll = 0
		}
		window = append([]string{}, body[scroll:scroll+vp]...)
		footer = dimStyle.Render(fmt.Sprintf(
			"↑/↓ scroll · g/G ends · any other key closes      lines %d–%d of %d",
			scroll+1, scroll+vp, len(body)))
	}

	inner := titleStyle.Render(title) + "\n\n" +
		strings.Join(window, "\n") + "\n\n" + footer

	box := helpBoxStyle.Render(inner)
	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

// viewModulesTab renders the Modules tab — one row per resolved module
// with declared metadata and live git status.
func (m model) viewModulesTab() string {
	var b strings.Builder
	b.WriteString(m.renderHomeHeader())

	mods := m.proj.ResolvedModules
	fmt.Fprintf(&b, "  %s%s\n",
		queryDimStyle.Render("Modules: "),
		queryDimStyle.Render(fmt.Sprintf("%d declared", len(mods))))
	fmt.Fprintf(&b, "  %s\n", headerStyle.Render(fmt.Sprintf("%-22s %-10s %-9s %-32s %s",
		"NAME", "REF", "SRC", "PATH", "STATUS")))

	viewH := m.modulesViewportHeight()

	// Render each module row into a flat list, then scroll-window it
	// the same way viewUnitsTab handles its visible slice.
	rendered := make([]string, 0, len(mods))
	for i, rm := range mods {
		cursorMark := "  "
		nameStyle := classUnitStyle
		if i == m.modulesCursor {
			cursorMark = "→ "
			nameStyle = selectedStyle
		}
		ref := rm.Ref
		if ref == "" {
			ref = "-"
		}
		path := rm.Path
		if rm.Local != "" {
			path = "local:" + rm.Local
			if rm.Path != "" {
				path += "/" + rm.Path
			}
		}
		if path == "" {
			path = "(root)"
		}
		status := m.moduleStatus[rm.Name]
		if status == "" {
			status = "?"
		}
		statusStyle := dimStyle
		switch status {
		case "clean":
			statusStyle = cachedStyle
		case "missing":
			statusStyle = failedStyle
		case "no-git":
			statusStyle = dimStyle
		default:
			if strings.HasPrefix(status, "dirty") {
				statusStyle = waitingStyle
			}
		}
		modState := m.moduleSourceState(rm)
		srcCell := srcStateStyle(modState).Render(clipFixed(srcStateToken(modState), 9))
		rendered = append(rendered, fmt.Sprintf("%s%s %s %s %s %s",
			cursorMark,
			nameStyle.Render(clipFixed(rm.Name, 22)),
			classUnitStyle.Render(clipFixed(ref, 10)),
			srcCell,
			dimStyle.Render(clipFixed(path, 32)),
			statusStyle.Render(status)))
	}

	// Cursor-driven scroll offset: smallest offset that keeps the
	// cursor row in the viewport.
	offset := 0
	if m.modulesCursor >= viewH {
		offset = m.modulesCursor - viewH + 1
	}
	end := offset + viewH
	if end > len(rendered) {
		end = len(rendered)
	}

	// Top "more" indicator (always one line).
	if offset > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more", offset)))
	}
	b.WriteString("\n")

	for i := offset; i < end; i++ {
		b.WriteString(rendered[i])
		b.WriteString("\n")
	}
	for i := end - offset; i < viewH; i++ {
		b.WriteString("\n")
	}

	// Bottom "more" indicator.
	if end < len(rendered) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(rendered)-end)))
	}
	b.WriteString("\n")

	if len(mods) == 0 {
		b.WriteString(dimStyle.Render("  (no modules declared in PROJECT.star)\n"))
	}

	// Detail strip for the cursor row — useful when fields are clipped.
	if m.modulesCursor >= 0 && m.modulesCursor < len(mods) {
		rm := mods[m.modulesCursor]
		dir := rm.Dir
		if dir == "" {
			dir = "(not synced)"
		}
		fmt.Fprintf(&b, "  %s %s\n", dimStyle.Render("dir:"), dim(dir))
		if rm.URL != "" {
			fmt.Fprintf(&b, "  %s %s\n", dimStyle.Render("url:"), dim(rm.URL))
		}
	} else {
		b.WriteString("\n\n")
	}

	b.WriteString("\n") // blank before help/message
	if m.message != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  " + m.message))
	} else {
		b.WriteString(renderHelp([]helpItem{
			{"tab", "next tab"}, {"j/k", "move"}, {"g/G", "top/bottom"},
			{"$", "shell"}, {"u", "src"}, {"r", "refresh"}, {"?", "help"}, {"q", "quit"},
		}))
	}
	return b.String()
}

// viewDiagnosticsTab renders the Diagnostics tab: shadowed units and
// duplicate provides surfaced from Project.Diagnostics. The content
// scrolls when it exceeds the available height; section/column headers
// scroll with the rows so the user can see what they belong to.
func (m model) viewDiagnosticsTab() string {
	var b strings.Builder
	b.WriteString(m.renderHomeHeader())

	diag := m.proj.Diagnostics
	fmt.Fprintf(&b, "  %s%s\n",
		queryDimStyle.Render("Diagnostics: "),
		queryDimStyle.Render(fmt.Sprintf("%d shadowed, %d duplicate provides",
			len(diag.Shadows), len(diag.DuplicateProvides))))

	viewH := m.diagnosticsViewportHeight()

	if len(diag.Shadows) == 0 && len(diag.DuplicateProvides) == 0 {
		b.WriteString("\n") // ↑ more (reserved)
		b.WriteString(dimStyle.Render("  no diagnostic issues — every unit name and `provides` claim is unique.\n"))
		for i := 1; i < viewH; i++ {
			b.WriteString("\n")
		}
		b.WriteString("\n") // ↓ more (reserved)
		b.WriteString("\n") // blank
		b.WriteString(renderHelp([]helpItem{
			{"tab", "next tab"}, {"q", "quit"},
		}))
		return b.String()
	}

	// Build content as a flat list of pre-rendered lines plus a parallel
	// mapping from row index to line index so we can scroll the cursor
	// into view.
	var lines []string
	rowLine := make([]int, 0, m.diagnosticsRowCount())
	row := 0

	if len(diag.Shadows) > 0 {
		lines = append(lines, "  "+headerStyle.Render(fmt.Sprintf("SHADOWED UNITS (%d)", len(diag.Shadows))))
		lines = append(lines, "  "+dimStyle.Render(fmt.Sprintf("%-28s %-22s %s", "UNIT", "WINNER", "SHADOWED FROM")))
		for _, s := range diag.Shadows {
			cursor := "  "
			style := classUnitStyle
			if row == m.diagnosticsCursor {
				cursor = "→ "
				style = selectedStyle
			}
			rowLine = append(rowLine, len(lines))
			lines = append(lines, fmt.Sprintf("%s%s %s %s",
				cursor,
				style.Render(clipFixed(s.Unit, 28)),
				dimStyle.Render(clipFixed(displayModule(s.WinnerModule), 22)),
				dimStyle.Render(displayModule(s.LoserModule))))
			row++
		}
	}
	if len(diag.DuplicateProvides) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "  "+headerStyle.Render(fmt.Sprintf("DUPLICATE PROVIDES (%d)", len(diag.DuplicateProvides))))
		lines = append(lines, "  "+dimStyle.Render(fmt.Sprintf("%-22s %-28s %s", "VIRTUAL", "ACTIVE", "ALSO PROVIDED BY")))
		for _, p := range diag.DuplicateProvides {
			cursor := "  "
			style := classUnitStyle
			if row == m.diagnosticsCursor {
				cursor = "→ "
				style = selectedStyle
			}
			others := strings.Join(p.Others, ", ")
			rowLine = append(rowLine, len(lines))
			lines = append(lines, fmt.Sprintf("%s%s %s %s",
				cursor,
				style.Render(clipFixed(p.Virtual, 22)),
				cachedStyle.Render(clipFixed(p.Active, 28)),
				dimStyle.Render(others)))
			row++
		}
	}

	// Derive the scroll offset from the cursor: pick the smallest
	// offset that keeps the cursor row visible.
	cursorLine := -1
	if m.diagnosticsCursor >= 0 && m.diagnosticsCursor < len(rowLine) {
		cursorLine = rowLine[m.diagnosticsCursor]
	}
	offset := 0
	if cursorLine >= viewH {
		offset = cursorLine - viewH + 1
	}
	if offset > len(lines)-viewH {
		offset = len(lines) - viewH
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + viewH
	if end > len(lines) {
		end = len(lines)
	}

	// Top "more" indicator (always one line, blank when at top).
	if offset > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more", offset)))
	}
	b.WriteString("\n")

	for i := offset; i < end; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}
	for i := end - offset; i < viewH; i++ {
		b.WriteString("\n")
	}

	// Bottom "more" indicator.
	if end < len(lines) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(lines)-end)))
	}
	b.WriteString("\n")
	b.WriteString("\n") // blank before help/message

	if m.message != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  " + m.message))
	} else {
		b.WriteString(renderHelp([]helpItem{
			{"tab", "next tab"}, {"j/k", "move"}, {"g/G", "top/bottom"}, {"?", "help"}, {"q", "quit"},
		}))
	}
	return b.String()
}

// displayModule formats a module name for display ("project root" for
// the empty string, the literal name otherwise).
func displayModule(name string) string {
	if name == "" {
		return "project root"
	}
	return name
}

// dim renders a string in the dim style, used inline within format strings.
func dim(s string) string { return dimStyle.Render(s) }

func (m model) viewSetup() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("  %s\n\n", titleStyle.Render("Setup")))

	switch m.setupField {
	case "machine":
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
		b.WriteString(helpStyle.Render("  enter select  esc back  ? help"))
		b.WriteString("\n")

	case "image":
		// Image picker
		b.WriteString(headerStyle.Render("  Select Default Image"))
		b.WriteString("\n\n")

		imgs := m.imageUnits()
		if len(imgs) == 0 {
			b.WriteString(dimStyle.Render("  no image() units defined in this project\n"))
		}
		for i, name := range imgs {
			cursor := "  "
			style := dimStyle
			if i == m.imageCursor {
				cursor = "→ "
				style = selectedStyle
			}
			current := ""
			if name == m.proj.Defaults.Image {
				current = cachedStyle.Render(" (current)")
			}
			b.WriteString(fmt.Sprintf("%s%s%s\n", cursor, style.Render(name), current))
		}

		b.WriteString("\n")
		b.WriteString(helpStyle.Render("  enter select  esc back  ? help"))
		b.WriteString("\n")

	default:
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
			case "Image":
				value = headerStyle.Render(m.proj.Defaults.Image)
			case "Parallel builds":
				value = headerStyle.Render(fmt.Sprintf("%d", m.parallelBuilds))
			}
			b.WriteString(fmt.Sprintf("%s%s  %s\n", cursor, style.Render(opt), value))
		}

		b.WriteString("\n")
		b.WriteString(helpStyle.Render("  enter select  ←/→ adjust  esc back  ? help  q quit"))
		b.WriteString("\n")
	}

	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  " + m.message))
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) detailAllLines() []string {
	var allLines []string

	// Dependency context for the unit, above the build output: whether
	// the current default image pulls it in (upstream) and what this
	// unit transitively pulls in itself (downstream).
	allLines = append(allLines, headerStyle.Render("  USED BY (upstream)"))
	allLines = append(allLines, m.upstreamLines()...)
	allLines = append(allLines, "")
	allLines = append(allLines, headerStyle.Render("  PULLS IN (downstream)"))
	allLines = append(allLines, m.renderUnitTree(m.detailUnit, m.downstreamChildren, 4)...)
	allLines = append(allLines, "")

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

// upstreamLines surfaces *why* the current default image ships
// m.detailUnit, by walking back through runtime_deps to the explicit
// pick(s) the user wrote in image(). Three states:
//
//   - The unit itself is in the explicit list: "└── <image> (explicit)"
//   - It's pulled in transitively: list each explicit pick with the
//     runtime-dep chain that bridges them, e.g.
//     └── dev-image (image)
//     ├── x11 → libx11 → cairo
//     └── yazi → libpango → cairo
//   - Nothing in the explicit list reaches it: "(not in <image>)"
func (m model) upstreamLines() []string {
	imgName := m.proj.Defaults.Image
	if imgName == "" {
		return []string{dimStyle.Render("    (no default image set)")}
	}
	img, ok := m.proj.Units[imgName]
	if !ok || img.Class != "image" {
		return []string{dimStyle.Render("    (default image " + imgName + " not found)")}
	}
	if len(img.ArtifactsExplicit) == 0 {
		// Older image() output without artifacts_explicit. We can't
		// distinguish explicit from transitive — report rootfs
		// membership only, without claiming "explicit".
		for _, a := range img.Artifacts {
			if a == m.detailUnit {
				return []string{"  └── " + imgName + dimStyle.Render(" (image)")}
			}
		}
		return []string{dimStyle.Render("    (not in " + imgName + ")")}
	}

	// Direct: unit is itself an explicit pick.
	for _, a := range img.ArtifactsExplicit {
		if a == m.detailUnit {
			return []string{"  └── " + imgName + dimStyle.Render(" (image, explicit)")}
		}
	}

	// Indirect: find a runtime-dep path from each explicit pick to
	// the unit. Picks that don't reach it are skipped.
	type chain struct {
		pick string
		path []string
	}
	var chains []chain
	for _, pick := range img.ArtifactsExplicit {
		if path := m.findRuntimePath(pick, m.detailUnit); path != nil {
			chains = append(chains, chain{pick, path})
		}
	}
	if len(chains) == 0 {
		return []string{dimStyle.Render("    (not in " + imgName + ")")}
	}
	sort.Slice(chains, func(i, j int) bool { return chains[i].pick < chains[j].pick })

	out := []string{"  └── " + imgName + dimStyle.Render(" (image)")}
	for i, c := range chains {
		connector := "├── "
		if i == len(chains)-1 {
			connector = "└── "
		}
		out = append(out, "      "+connector+strings.Join(c.path, " → "))
	}
	return out
}

// findRuntimePath returns the shortest chain from->...->to via
// runtime_deps (with provides routing), or nil if `to` isn't
// reachable from `from`. The returned slice includes both endpoints.
func (m model) findRuntimePath(from, to string) []string {
	if from == to {
		return []string{from}
	}
	type state struct {
		name string
		path []string
	}
	visited := map[string]bool{from: true}
	queue := []state{{from, []string{from}}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		u, ok := m.proj.Units[cur.name]
		if !ok {
			continue
		}
		for _, dep := range u.RuntimeDeps {
			if real, ok := m.proj.Provides[dep]; ok {
				dep = real
			}
			if visited[dep] {
				continue
			}
			next := append(append([]string{}, cur.path...), dep)
			if dep == to {
				return next
			}
			visited[dep] = true
			queue = append(queue, state{dep, next})
		}
	}
	return nil
}

// downstreamChildren returns the units that `name` pulls in at runtime
// — runtime_deps with virtual provides routed through proj.Provides,
// so the tree shows the concrete unit that wins override resolution.
// For image units the user's explicit artifact list (pre-closure
// expansion) takes the place of runtime_deps, so the top of the tree
// matches what the user actually wrote in image() rather than the
// fully flattened runtime closure.
func (m model) downstreamChildren(name string) []string {
	u, ok := m.proj.Units[name]
	if !ok {
		return nil
	}
	deps := u.RuntimeDeps
	if u.Class == "image" {
		deps = u.ArtifactsExplicit
		if len(deps) == 0 {
			// Older image() output without artifacts_explicit — fall
			// back to the resolved artifact list so the tree still
			// renders something.
			deps = u.Artifacts
		}
	}
	var out []string
	for _, dep := range deps {
		if real, ok := m.proj.Provides[dep]; ok {
			dep = real
		}
		if _, ok := m.proj.Units[dep]; ok {
			out = append(out, dep)
		}
	}
	return out
}

// renderUnitTree walks `getChildren` from root and returns the tree
// formatted with ├── / └── connectors. Repeated nodes are listed but
// not re-expanded so trees with shared subtrees (musl is everywhere)
// stay readable. Walking stops at maxDepth and at image-class leaves.
func (m model) renderUnitTree(root string, getChildren func(string) []string, maxDepth int) []string {
	children := getChildren(root)
	if len(children) == 0 {
		return []string{dimStyle.Render("    (none)")}
	}
	seen := map[string]bool{}
	var out []string
	var walk func(name, prefix string, isLast bool, depth int)
	walk = func(name, prefix string, isLast bool, depth int) {
		connector := "├── "
		if isLast {
			connector = "└── "
		}
		label := name
		if u, ok := m.proj.Units[name]; ok {
			switch u.Class {
			case "image":
				label += dimStyle.Render(" (image)")
			case "container":
				label += dimStyle.Render(" (container)")
			}
		}
		dup := ""
		if seen[name] {
			dup = dimStyle.Render(" …")
		}
		out = append(out, "  "+prefix+connector+label+dup)
		if seen[name] || depth >= maxDepth {
			return
		}
		seen[name] = true
		grandkids := getChildren(name)
		childPrefix := prefix + "    "
		if !isLast {
			childPrefix = prefix + "│   "
		}
		for i, c := range grandkids {
			walk(c, childPrefix, i == len(grandkids)-1, depth+1)
		}
	}
	for i, c := range children {
		walk(c, "", i == len(children)-1, 1)
	}
	return out
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
	titleVersion := ""
	if u, ok := m.proj.Units[m.detailUnit]; ok && u.Version != "" {
		titleVersion = " " + lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(u.Version)
	}
	b.WriteString(fmt.Sprintf("  ← %s%s %s\n",
		titleStyle.Render(m.detailUnit),
		titleVersion,
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

	// SOURCE line — colored state token + remote URL + git describe.
	// Skip for image/container units (no source dir to track).
	if line := m.detailSourceLine(); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}

	// Tab bar (Info / Files), styled like the home tab bar so the two
	// strips read as the same UI element.
	var labels []string
	for t := detailTab(0); t < numDetailTabs; t++ {
		labels = append(labels, t.String())
	}
	b.WriteString(fmt.Sprintf("      %s    %s%s\n",
		renderTabBar(labels, int(m.detailTab)),
		helpKeyStyle.Render("tab"),
		helpStyle.Render(": switch")))

	if m.detailTab == detailTabFiles {
		b.WriteString(m.viewDetailFilesBody())
		return b.String()
	}

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

	matchHighlight := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))              // yellow
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
		// \n is appended OUTSIDE the lipgloss Render — when the
		// newline is inside the styled string, the trailing reset
		// escape lands on the next line and pushes the help bar to
		// the right by a few cells in some terminals.
		b.WriteString(dimStyle.Render(fmt.Sprintf("  /%s [%d/%d]  n next  N prev",
			m.detailSearchText, m.detailMatchIdx+1, len(m.detailMatches))))
		b.WriteString("\n")
	}

	// Bottom row: status message replaces the help bar, same pattern as
	// the units view, so the detail page never trails extra blank lines.
	if m.message != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  " + m.message))
	} else {
		items := detailHelpItems
		if u, ok := m.proj.Units[m.detailUnit]; ok && u.Class == "image" {
			items = detailImageHelpItems
		}
		b.WriteString(renderHelp(items))
	}

	return b.String()
}

// viewDetailFilesBody renders everything below the tab bar for the Files
// tab: column header (sortable), file rows, scroll indicators, and the
// bottom help row. Mirrors the units-tab table layout — same arrow on
// the active sort column, same `↑ N more` / `↓ N more` cues.
func (m model) viewDetailFilesBody() string {
	var b strings.Builder

	const sizeW = 10
	pathW := m.width - 2 - sizeW - 2 // leading "  " + gap before SIZE
	if pathW < 20 {
		pathW = 20
	}

	headerLabel := func(col int, label string, w int, rightAlign bool) string {
		arrow := ""
		if m.detailFilesSortCol == col {
			if m.detailFilesSortDesc {
				arrow = "↓"
			} else {
				arrow = "↑"
			}
		}
		displayW := len(label)
		if arrow != "" {
			displayW++
		}
		if displayW >= w {
			return label + arrow
		}
		pad := strings.Repeat(" ", w-displayW)
		if rightAlign {
			return pad + label + arrow
		}
		return label + arrow + pad
	}
	b.WriteString(fmt.Sprintf("  %s  %s\n",
		headerStyle.Render(headerLabel(filesSortByName, "PATH", pathW, false)),
		headerStyle.Render(headerLabel(filesSortBySize, "SIZE", sizeW, true))))

	viewH := m.detailFilesViewportHeight()
	start := m.detailFilesScroll
	if start > len(m.detailFiles)-viewH {
		start = len(m.detailFiles) - viewH
	}
	if start < 0 {
		start = 0
	}
	end := start + viewH
	if end > len(m.detailFiles) {
		end = len(m.detailFiles)
	}

	// Always emit the ↑ row — blank when not scrolled — so the row
	// directly under the column header doesn't shift up by one when
	// the user scrolls down to a multi-page state.
	if start > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
	}
	b.WriteString("\n")

	if len(m.detailFiles) == 0 {
		b.WriteString(dimStyle.Render("  (no files installed — build the unit first)"))
		b.WriteString("\n")
		for i := 1; i < viewH; i++ {
			b.WriteString("\n")
		}
	} else {
		for _, f := range m.detailFiles[start:end] {
			pathStyle := lipgloss.NewStyle()
			if f.Link {
				pathStyle = dimStyle
			}
			b.WriteString(fmt.Sprintf("  %s  %s\n",
				pathStyle.Render(clipFixed(f.Path, pathW)),
				dimStyle.Render(fmt.Sprintf("%*s", sizeW, formatSize(f.Size)))))
		}
		rendered := end - start
		for i := rendered; i < viewH; i++ {
			b.WriteString("\n")
		}
	}

	if end < len(m.detailFiles) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(m.detailFiles)-end)))
	}
	b.WriteString("\n")

	if m.message != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  " + m.message))
	} else {
		b.WriteString(renderHelp(detailFilesHelpItems))
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

// renderName styles `padded` with `base` and underlines/bolds any
// substring that matches a bare term in the active query. When multiple
// terms could match at different positions, the leftmost match wins —
// the algorithm scans every term at each position and picks the one
// with the smallest start index.
func (m model) renderName(padded string, base lipgloss.Style) string {
	terms := m.query.BareTerms()
	if len(terms) == 0 {
		return base.Render(padded)
	}
	lower := strings.ToLower(padded)
	var b strings.Builder
	i := 0
	for i < len(padded) {
		bestStart, bestEnd := -1, -1
		for _, t := range terms {
			if t == "" {
				continue
			}
			idx := strings.Index(lower[i:], t)
			if idx < 0 {
				continue
			}
			start := i + idx
			if bestStart == -1 || start < bestStart {
				bestStart = start
				bestEnd = start + len(t)
			}
		}
		if bestStart == -1 {
			b.WriteString(base.Render(padded[i:]))
			break
		}
		b.WriteString(base.Render(padded[i:bestStart]))
		b.WriteString(base.Inherit(matchHighlightStyle).Render(padded[bestStart:bestEnd]))
		i = bestEnd
	}
	return b.String()
}

// ----- Helpers -----

// buildSessionActive reports whether the home-screen header should
// show the build progress bar. We key off buildTotal rather than
// len(m.building) so the bar can complete its animation to 100% even
// after the last goroutine returned, before resetBuildProgress clears
// the state on the next idle tick.
func (m model) buildSessionActive() bool {
	return m.buildTotal > 0
}

// markBuildUnitWaiting records a "waiting" event from the executor's
// pre-scan. Each unit only counts once per session even if multiple
// concurrent BuildUnits invocations both schedule it.
func (m *model) markBuildUnitWaiting(unit string) {
	if m.buildPending == nil {
		m.buildPending = make(map[string]bool)
	}
	if m.buildPending[unit] {
		return
	}
	m.buildPending[unit] = true
	m.buildTotal++
}

// markBuildUnitFinished records a "done" or "failed" event and returns
// a command that animates the progress bar to its new percentage. Only
// units that were previously announced as "waiting" count — a "done"
// for a cached unit is a no-op as far as the bar is concerned.
func (m *model) markBuildUnitFinished(unit string) tea.Cmd {
	if !m.buildPending[unit] {
		return nil
	}
	delete(m.buildPending, unit)
	m.buildDone++
	return m.buildProgress.SetPercent(m.buildPercent())
}

// buildPercent returns the current build progress as a 0..1 fraction,
// or 0 when no session is active.
func (m model) buildPercent() float64 {
	if m.buildTotal == 0 {
		return 0
	}
	return float64(m.buildDone) / float64(m.buildTotal)
}

// resetBuildProgress drops the active session — called once every
// running goroutine has reported back via buildDoneMsg. The bar
// disappears on the next render and the feed banner returns.
func (m *model) resetBuildProgress() {
	m.buildTotal = 0
	m.buildDone = 0
	m.buildPending = make(map[string]bool)
	m.buildProgress.SetPercent(0)
}

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

// execShell suspends the TUI and drops the user into $SHELL (or sh) in
// dir. Used by the `$` shortcut to inspect or hack on a unit's checked-
// out source or a module's clone. The caller is responsible for
// ensuring dir exists; an empty path or missing directory falls back to
// $HOME so the shell is at least usable.
func (m model) execShell(dir string) tea.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	c := exec.Command(shell)
	c.Dir = dir
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return execDoneMsg{err: err}
	})
}

// unitSrcDir returns the per-unit checked-out source directory under
// build/, or "" if the unit hasn't been fetched yet (so the caller can
// surface a helpful message instead of dropping into a phantom path).
func (m model) unitSrcDir(name string) string {
	if _, ok := m.proj.Units[name]; !ok {
		return ""
	}
	srcDir := filepath.Join(build.UnitBuildDir(m.projectDir, m.unitScopeDir(name), name), "src")
	if _, err := os.Stat(srcDir); err != nil {
		return ""
	}
	return srcDir
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

// refreshDetailFiles populates m.detailFiles by walking the unit's
// destdir — what `apk` would later pack into the .apk. Directories
// are skipped (only files and symlinks are listed) since the user is
// looking at "what got installed". Walked once on tab activation; no
// background polling, since destdir contents only change on a rebuild
// and that already drives a `refreshDetail` for the log panes.
func (m *model) refreshDetailFiles() {
	m.detailFiles = nil
	m.detailFilesScroll = 0
	destDir := filepath.Join(build.UnitBuildDir(m.projectDir, m.unitScopeDir(m.detailUnit), m.detailUnit), "destdir")
	filepath.Walk(destDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == destDir {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(destDir, path)
		if relErr != nil {
			return nil
		}
		m.detailFiles = append(m.detailFiles, fileEntry{
			Path: "/" + filepath.ToSlash(rel),
			Size: info.Size(),
			Link: info.Mode()&os.ModeSymlink != 0,
		})
		return nil
	})
	m.sortDetailFiles()
}

func (m *model) sortDetailFiles() {
	desc := m.detailFilesSortDesc
	col := m.detailFilesSortCol
	sort.SliceStable(m.detailFiles, func(p, q int) bool {
		a, b := m.detailFiles[p], m.detailFiles[q]
		var c int
		switch col {
		case filesSortBySize:
			switch {
			case a.Size < b.Size:
				c = -1
			case a.Size > b.Size:
				c = 1
			}
		default: // filesSortByName
			switch {
			case a.Path < b.Path:
				c = -1
			case a.Path > b.Path:
				c = 1
			}
		}
		if c == 0 {
			switch {
			case a.Path < b.Path:
				c = -1
			case a.Path > b.Path:
				c = 1
			}
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
}

// updateDetailFiles handles keys when the Files tab is active inside
// the detail view. Navigation only — actions like build/run/log live
// on the Info tab so the Files tab stays a pure viewer.
func (m model) updateDetailFiles(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	maxScroll := m.detailFilesMaxScroll()
	switch msg.String() {
	case "esc":
		m.view = viewUnits
		m.detailUnit = ""
		m.outputLines = nil
		m.logLines = nil
		m.detailScroll = 0
		m.detailFiles = nil
		m.detailFilesScroll = 0
		return m, nil

	case "q", "ctrl+c":
		if len(m.cancels) > 0 {
			m.confirm = "quit"
			m.message = "Builds are running. Quit and cancel them? (y/n)"
			return m, nil
		}
		return m, tea.Quit

	case "up", "k":
		if m.detailFilesScroll > 0 {
			m.detailFilesScroll--
		}
		return m, nil

	case "down", "j":
		if m.detailFilesScroll < maxScroll {
			m.detailFilesScroll++
		}
		return m, nil

	case "pgup", "ctrl+b":
		page := m.detailFilesViewportHeight()
		m.detailFilesScroll -= page
		if m.detailFilesScroll < 0 {
			m.detailFilesScroll = 0
		}
		return m, nil

	case "pgdown", "ctrl+f":
		page := m.detailFilesViewportHeight()
		m.detailFilesScroll += page
		if m.detailFilesScroll > maxScroll {
			m.detailFilesScroll = maxScroll
		}
		return m, nil

	case "g":
		m.detailFilesScroll = 0
		return m, nil

	case "G":
		m.detailFilesScroll = maxScroll
		return m, nil

	case "o":
		next := (m.detailFilesSortCol + 1) % numFilesSortColumns
		m.detailFilesSortCol = next
		// SIZE defaults to descending — biggest files first is what
		// the user almost always wants when they switch to that sort.
		m.detailFilesSortDesc = next == filesSortBySize
		m.sortDetailFiles()
		m.detailFilesScroll = 0
		return m, nil

	case "O":
		m.detailFilesSortDesc = !m.detailFilesSortDesc
		m.sortDetailFiles()
		m.detailFilesScroll = 0
		return m, nil
	}
	return m, nil
}

// detailViewportHeight returns the number of content lines visible in
// detail view. Chrome is counted exactly so the page doesn't trail
// extra blank lines below the help bar: title + metadata + tab bar +
// scroll indicator + (search bar) + bottom row (help OR status message).
func (m model) detailViewportHeight() int {
	chrome := 2 // title + metadata/blank
	chrome++    // tab bar (Info / Files)
	chrome++    // scroll indicator (always one line, blank when not scrolled)
	if m.detailSearching || (m.detailSearchText != "" && len(m.detailMatches) > 0) {
		chrome++ // search bar
	}
	chrome++ // bottom row (help or message, single line)
	h := m.height - chrome
	if h < 5 {
		h = 5
	}
	return h
}

// detailFilesViewportHeight returns the number of file rows visible in
// the Files tab. Chrome: title + metadata + tab bar + column header +
// ↑/↓ indicators + bottom help row.
func (m model) detailFilesViewportHeight() int {
	chrome := 2 // title + metadata/blank
	chrome++    // tab bar
	chrome++    // column header
	chrome++    // ↑ more (always reserved)
	chrome++    // ↓ more (always reserved)
	chrome++    // bottom row
	h := m.height - chrome
	if h < 3 {
		h = 3
	}
	return h
}

// detailFilesMaxScroll returns the maximum scroll offset for the Files
// tab so the cursor can't run past the last row.
func (m model) detailFilesMaxScroll() int {
	max := len(m.detailFiles) - m.detailFilesViewportHeight()
	if max < 0 {
		return 0
	}
	return max
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

// scrollUnitIntoView moves the cursor to `name` and scrolls the units
// list so that row is visible. Used when a build starts on a unit the
// user isn't looking at — cursor follows so `enter` opens the detail
// view of whatever is actively building, and j/k continues from there.
// No-op if the unit isn't in m.visible (current query filtered it out).
func (m *model) scrollUnitIntoView(name string) {
	visible := m.visibleIndices()
	idx := -1
	for _, i := range visible {
		if m.units[i] == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	m.cursor = idx
	m.adjustListOffset()
}

// sortVisible reorders m.visible according to the active sort column and
// direction. Empty/zero values for size and deps sort last in ascending
// mode, first in descending mode — i.e., always to the bottom — so unbuilt
// units never push real numbers off-screen. The unit name is the
// tiebreaker so the order is deterministic.
func (m *model) sortVisible() {
	if len(m.visible) <= 1 {
		return
	}
	col := m.sortColumn
	desc := m.sortDesc

	keyName := func(i int) string { return m.units[i] }
	keyClass := func(i int) string {
		if u, ok := m.proj.Units[m.units[i]]; ok {
			return u.Class
		}
		return ""
	}
	keyModule := func(i int) string {
		if u, ok := m.proj.Units[m.units[i]]; ok {
			if u.Module == "" {
				return "(local)"
			}
			return u.Module
		}
		return ""
	}
	keyVersion := func(i int) string {
		if u, ok := m.proj.Units[m.units[i]]; ok {
			return u.Version
		}
		return ""
	}
	keyStatus := func(i int) string { return statusKey(m.statuses[m.units[i]]) }

	cmpString := func(a, b string) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}
	cmpInt64 := func(a, b int64) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}

	sort.SliceStable(m.visible, func(p, q int) bool {
		i, j := m.visible[p], m.visible[q]
		var c int
		switch col {
		case sortByClass:
			c = cmpString(keyClass(i), keyClass(j))
		case sortByModule:
			c = cmpString(keyModule(i), keyModule(j))
		case sortByVersion:
			a, b := keyVersion(i), keyVersion(j)
			// Empty versions (rare; mostly image units) sort to the bottom
			// in both directions, mirroring how SIZE/DEPS treat zero values.
			switch {
			case a == "" && b == "":
				c = 0
			case a == "":
				return false
			case b == "":
				return true
			default:
				c = cmpString(a, b)
			}
		case sortBySize:
			a, b := m.unitSize[m.units[i]], m.unitSize[m.units[j]]
			// Push zero (unbuilt) values to the bottom in both directions
			// so the column always reads "real numbers, then blanks".
			switch {
			case a == 0 && b == 0:
				c = 0
			case a == 0:
				return false
			case b == 0:
				return true
			default:
				c = cmpInt64(a, b)
			}
		case sortByDeps:
			a, b := m.unitDeps[m.units[i]], m.unitDeps[m.units[j]]
			switch {
			case a == 0 && b == 0:
				c = 0
			case a == 0:
				return false
			case b == 0:
				return true
			default:
				c = cmpInt64(int64(a), int64(b))
			}
		case sortByStatus:
			c = cmpString(keyStatus(i), keyStatus(j))
		default: // sortByName
			c = cmpString(keyName(i), keyName(j))
		}
		if c == 0 {
			c = cmpString(keyName(i), keyName(j))
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
}

// recomputeMetrics rebuilds m.unitSize and m.unitDeps from the current
// project + arch + machine. Cheap enough to run on every project reload
// or build completion: each unit walks its own runtime closure once and
// stats one or two files.
func (m *model) recomputeMetrics() {
	size := make(map[string]int64, len(m.units))
	deps := make(map[string]int, len(m.units))
	for _, name := range m.units {
		u := m.proj.Units[name]
		if u == nil {
			continue
		}
		sd := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
		buildDir := build.UnitBuildDir(m.projectDir, sd, name)
		size[name] = installedSize(buildDir)

		// Runtime closure of what the unit pulls in. For image units the
		// package list lives in Artifacts (image.RuntimeDeps is empty
		// by convention), so walk from the artifact roots instead — the
		// number then reflects what actually ships on the device.
		var roots []string
		if u.Class == "image" {
			roots = u.Artifacts
		} else {
			roots = []string{name}
		}
		closure := resolve.RuntimeClosure(m.proj, roots)
		count := len(closure)
		if u.Class != "image" && count > 0 {
			count-- // don't count the unit itself for non-image units
		}
		deps[name] = count
	}
	m.unitSize = size
	m.unitDeps = deps
	// Note: the source-state cache deliberately survives this call.
	// recomputeMetrics is invoked after every build completion, but
	// BuildMeta.SourceState only records the persistent toggle state
	// ("dev"); the live dev-mod / dev-dirty refinement comes from the
	// watcher's polling and lives only in unitSrcStates. Wiping it
	// here would briefly flip a dev-dirty unit back to "dev" until
	// the next 2-second poll picked the dirt up again — exactly the
	// glitch users hit after `yoe build` on a unit with uncommitted
	// edits. Project reloads (recomputeStatuses) wipe the cache
	// explicitly because the DAG and arch may have changed.
}

// refreshUnitSize re-reads a single unit's installed size from disk.
// Called when a unit's build just finished, so the SIZE column updates
// incrementally during a multi-unit build instead of waiting for the
// final recomputeMetrics. Skips the runtime-closure walk because deps
// don't change just because the unit got built.
func (m *model) refreshUnitSize(name string) {
	u, ok := m.proj.Units[name]
	if !ok {
		return
	}
	sd := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	buildDir := build.UnitBuildDir(m.projectDir, sd, name)
	if m.unitSize == nil {
		m.unitSize = make(map[string]int64)
	}
	m.unitSize[name] = installedSize(buildDir)
}

// installedSize returns the on-disk size for a built unit. For image
// units this is the rootfs content (what was actually installed); for
// other units it is the destdir size (what goes into the .apk). The
// executor records this in BuildMeta.InstalledBytes — see executor.go.
// Returns 0 when the unit has not been built yet.
func installedSize(buildDir string) int64 {
	if meta := build.ReadMeta(buildDir); meta != nil {
		return meta.InstalledBytes
	}
	return 0
}

// clipFixed renders s in exactly w display cells: right-padded with
// spaces when shorter, ellipsis-clipped when longer, so a long unit name
// (e.g. abseil-cpp-atomic-hook-test-helper) can't push the rest of the
// row off-screen. Assumes ASCII input — fine for unit/class/module
// names today.
func clipFixed(s string, w int) string {
	if len(s) > w {
		return s[:w-1] + "…"
	}
	return fmt.Sprintf("%-*s", w, s)
}

// detailSourceLine renders the SOURCE strip on the unit detail page:
// colored state token + remote URL + git describe (e.g.
// "v3.4.1-3-gabc1234-dirty"). Returns "" when the unit has no source
// dir (image/container) so the caller can skip the line entirely.
func (m model) detailSourceLine() string {
	u, ok := m.proj.Units[m.detailUnit]
	if !ok || u.Class == "image" || u.Class == "container" {
		return ""
	}
	state := m.unitSourceState(m.detailUnit)
	tok := srcStateToken(state)
	if tok == "" {
		tok = "(unbuilt)"
	}
	parts := []string{
		dimStyle.Render("  SOURCE"),
		srcStateStyle(state).Render(tok),
	}
	// Remote URL: prefer the .star-declared source string so the row is
	// meaningful even before a build has run; if a dev clone has
	// rewritten origin (HTTPS↔SSH), surface the live remote instead.
	url := u.Source
	sd := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
	buildDir := build.UnitBuildDir(m.projectDir, sd, m.detailUnit)
	srcDir := filepath.Join(buildDir, "src")
	if source.IsDev(state) {
		if live := remoteOriginURL(srcDir); live != "" {
			url = live
		}
	}
	if url != "" {
		parts = append(parts, dimStyle.Render(url))
	}
	// Pin: surface the declared tag so the line reads e.g. "SOURCE pin
	// <url> (pinned at v1.36.1)". Dev: rely on the live SourceDescribe
	// below (e.g. v3.4.1-3-gabc1234-dirty) — it's more informative than
	// the static .star pin once the user is hacking on the source.
	if state == source.StatePin && u.Tag != "" {
		parts = append(parts, dimStyle.Render("(pinned at "+u.Tag+")"))
	}
	// Branch-tracking dev unit: surface "tracking origin/<branch>" plus,
	// when the working tree has moved past the pin tag, "(N commits past
	// <tag>)". The count comes from `git rev-list <tag>..HEAD` — a one-off
	// invocation per detail-page render, fast enough not to warrant
	// caching alongside the state cache.
	if source.IsDev(state) && u.Branch != "" {
		hint := "tracking origin/" + u.Branch
		if u.Tag != "" {
			if n := commitsPast(srcDir, u.Tag); n > 0 {
				hint += fmt.Sprintf(" (%d commits past %s)", n, u.Tag)
			}
		}
		parts = append(parts, dimStyle.Render(hint))
	}
	if meta := build.ReadMeta(buildDir); meta != nil && meta.SourceDescribe != "" {
		parts = append(parts, dimStyle.Render(meta.SourceDescribe))
	}
	return strings.Join(parts, "  ")
}

// commitsPast returns how many commits HEAD is past the given ref in
// srcDir. Returns 0 on any error (ref unknown, not a git dir, etc.) —
// the hint is purely informational, so silently dropping it is the
// right failure mode.
func commitsPast(srcDir, ref string) int {
	cmd := exec.Command("git", "rev-list", "--count", ref+"..HEAD")
	cmd.Dir = srcDir
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return n
}

// remoteOriginURL returns `git remote get-url origin` for srcDir, or
// "" when not a git repo. Used by detailSourceLine to show the live
// remote of a dev clone (which may differ from the .star-declared
// source if the user rewrote it to SSH).
func remoteOriginURL(srcDir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = srcDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// srcStateToken is the short label rendered in the SRC column for a
// given source state. Returned width is 9 cells max (matches the
// column width in renderUnitsBody / viewModulesTab); blank for an
// unbuilt or non-source unit so the column reads as cleanly empty.
func srcStateToken(s source.State) string {
	switch s {
	case source.StatePin:
		return "pin"
	case source.StateDev:
		return "dev"
	case source.StateDevMod:
		return "dev-mod"
	case source.StateDevDirty:
		return "dev-dirty"
	case source.StateLocal:
		return "local"
	default:
		return ""
	}
}

// srcStateStyle picks the lipgloss style for a source-state token.
// Pin: blue/cyan (matches cachedStyle's "stable, matches the
// declared ref" semantic). Dev: green. Dev-mod: yellow. Dev-dirty:
// red. Local: faded. Empty/unknown: dim.
func srcStateStyle(s source.State) lipgloss.Style {
	switch s {
	case source.StatePin:
		return srcPinStyle
	case source.StateDev:
		return srcDevStyle
	case source.StateDevMod:
		return srcDevModStyle
	case source.StateDevDirty:
		return srcDevDirtyStyle
	case source.StateLocal:
		return srcLocalStyle
	default:
		return dimStyle
	}
}

// renderSrcCell returns the styled SRC column cell for a unit, padded
// to width 9. Image and container units return blank (no source dir
// to track). The state value is taken from m.unitSrcStates if cached,
// otherwise from BuildMeta.SourceState; if neither is set, the unit
// is assumed pin (it has never been toggled to dev).
func (m model) renderSrcCell(name string) string {
	const w = 9
	u, ok := m.proj.Units[name]
	if !ok || u.Class == "image" || u.Class == "container" {
		return clipFixed("", w)
	}
	state := m.unitSourceState(name)
	tok := srcStateToken(state)
	return srcStateStyle(state).Render(clipFixed(tok, w))
}

// unitSourceState returns the cached source state for a unit, falling
// back to BuildMeta.SourceState on the disk and finally to "empty"
// (which the renderer displays as blank). Caches the answer so a
// long unit list doesn't trigger N disk reads per render.
func (m model) unitSourceState(name string) source.State {
	if m.unitSrcStates == nil {
		// In production newModel always initialises this, but tests
		// build models directly via &model{} — bail rather than panic.
		return source.StateEmpty
	}
	if s, ok := m.unitSrcStates[name]; ok {
		return s
	}
	state := source.StateEmpty
	if u, ok := m.proj.Units[name]; ok {
		sd := build.ScopeDir(u, m.arch, m.proj.Defaults.Machine)
		buildDir := build.UnitBuildDir(m.projectDir, sd, name)
		if meta := build.ReadMeta(buildDir); meta != nil {
			state = source.State(meta.SourceState)
		}
	}
	m.unitSrcStates[name] = state
	return state
}

// moduleSourceState returns the cached source state for a module.
// Modules carry their state in a sibling .yoe-state.json (see
// internal/module/state.go); module(local=…) overrides always
// report "local" without touching disk.
func (m model) moduleSourceState(rm yoestar.ResolvedModule) source.State {
	if rm.Local != "" {
		return source.StateLocal
	}
	if m.moduleSrcStates == nil {
		return source.StateEmpty
	}
	if s, ok := m.moduleSrcStates[rm.Name]; ok {
		return s
	}
	state := source.StateEmpty
	if rm.CloneDir != "" {
		state = module.ReadState(rm.CloneDir)
	}
	m.moduleSrcStates[rm.Name] = state
	return state
}

// formatSize renders a byte count in a fixed-width, human-readable form
// using KiB/MiB/GiB. Empty string for unbuilt units (size == 0) so the
// column reads as cleanly absent rather than misleading "0 B". Uses one
// decimal below 10 of each unit ("9.9K") and drops the decimal at 10 and
// above ("1003K") so the rendered width stays bounded — max 5 chars.
func formatSize(b int64) string {
	if b <= 0 {
		return ""
	}
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	format := func(v float64, unit string) string {
		if v < 10 {
			return fmt.Sprintf("%.1f%s", v, unit)
		}
		return fmt.Sprintf("%d%s", int64(v), unit)
	}
	switch {
	case b >= gib:
		return format(float64(b)/float64(gib), "G")
	case b >= mib:
		return format(float64(b)/float64(mib), "M")
	case b >= kib:
		return format(float64(b)/float64(kib), "K")
	default:
		return fmt.Sprintf("%dB", b)
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

	hashes, err := resolve.ComputeAllHashes(m.dag, m.arch, m.proj.Defaults.Machine, build.SrcInputsFn(m.projectDir, m.arch, m.proj.Defaults.Machine))
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
	m.recomputeMetrics()
	// Project actually reloaded — DAG, arch, machine may all be
	// different — so the runtime source-state cache is genuinely
	// stale and should be re-derived from scratch on the next render.
	m.unitSrcStates = make(map[string]source.State)
	m.moduleSrcStates = make(map[string]source.State)
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
// Chrome is counted exactly so the unit list never pushes the title and
// banner lines off the top: when the rendered output exceeds the
// terminal height, the terminal scrolls and the topmost lines disappear.
//
// The ↑/↓ "more" indicators are always reserved even when not currently
// rendered (single-page state) so the row count doesn't jump by 1 the
// moment the user crosses into multi-page territory.
func (m model) listViewportHeight() int {
	chrome := m.homeHeaderLines()
	chrome++ // query header
	chrome++ // column header
	chrome++ // ↑ more (always reserved)
	chrome++ // ↓ more (always reserved)
	chrome++ // blank line before bottom row
	chrome++ // bottom row: help / search / message — always one line
	chrome += m.queryCompletionsLines()
	h := m.height - chrome
	if h < 3 {
		h = 3
	}
	return h
}

// queryCompletionsLines returns how many lines the tab-completion
// candidate list adds under the query bar. Zero when the list is
// empty. Capped at the same maxRows the renderer uses, plus one for
// the "(N more)" hint when truncated. listViewportHeight subtracts
// this so a long candidate list doesn't push the home header off
// the top of the screen.
func (m model) queryCompletionsLines() int {
	const maxRows = 8
	n := len(m.queryCompletions)
	if n == 0 {
		return 0
	}
	if n > maxRows {
		return maxRows + 1 // truncation hint
	}
	return n
}

// homeHeaderLines returns the number of lines renderHomeHeader outputs.
// Counted exactly so each tab can subtract chrome from m.height when
// sizing its own scrollable region.
func (m model) homeHeaderLines() int {
	n := 1 // title
	if m.warning != "" {
		n++
	}
	if m.notification != "" {
		n++
	}
	if m.buildSessionActive() || m.feedStatus != "" {
		n++
	}
	n++ // tab bar
	return n
}

// renderBuildProgress draws the progress bar that replaces the feed
// status line during builds. The text label sits to the right of the
// bar so the percentage, total, and remaining counts stay visible
// even on narrow terminals where the bar shrinks.
func (m model) renderBuildProgress() string {
	left := m.buildTotal - m.buildDone
	pct := int(m.buildPercent()*100 + 0.5)
	label := fmt.Sprintf(" %d%% · %d/%d done · %d left",
		pct, m.buildDone, m.buildTotal, left)

	// Indent (2) + label width is the chrome around the bar.
	bar := m.buildProgress
	avail := m.width - 2 - lipgloss.Width(label)
	switch {
	case avail < 10:
		bar.Width = 10
	case avail > 60:
		bar.Width = 60
	default:
		bar.Width = avail
	}
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	return bar.View() + labelStyle.Render(label)
}

// unitStarPath returns the .star file that defines the given resolved
// unit, using its DefinedIn directory (the winning module's path after
// priority resolution). Tries the conventional `<name>.star` first,
// then falls back to scanning DefinedIn for any .star file whose body
// names this unit — handles helpers that emit a unit with a different
// filename from the function definition.
//
// Returns "" if the unit is nil, has no DefinedIn (unit constructed
// programmatically), or no .star in DefinedIn references it.
func unitStarPath(u *yoestar.Unit) string {
	if u == nil || u.DefinedIn == "" {
		return ""
	}
	candidate := filepath.Join(u.DefinedIn, u.Name+".star")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	needle := []byte(`"` + u.Name + `"`)
	entries, err := os.ReadDir(u.DefinedIn)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".star") {
			continue
		}
		path := filepath.Join(u.DefinedIn, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if bytes.Contains(data, needle) {
			return path
		}
	}
	return ""
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
			// Remember this device for next time. Best-effort: a write
			// failure doesn't block the flash (the user is mid-action).
			if ov, lerr := yoestar.LoadLocalOverrides(m.projectDir); lerr == nil {
				ov.FlashDevice = cand.Path
				_ = yoestar.WriteLocalOverrides(m.projectDir, ov)
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
	case flashPermPrompt:
		switch strings.ToLower(msg.String()) {
		case "y":
			cand := m.flashCandidates[m.flashCursor]
			username := flashChownUser()
			c := exec.Command("sudo", "chown", username, cand.Path)
			return m, tea.ExecProcess(c, func(err error) tea.Msg {
				return flashChownDoneMsg{err: err}
			})
		case "esc", "n", "q":
			cand := m.flashCandidates[m.flashCursor]
			m.flashStage = flashError
			m.flashErr = fmt.Errorf("permission denied — run: sudo chown %s %s", flashChownUser(), cand.Path)
			return m, nil
		}
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
		b.WriteString(helpStyle.Render("↑/↓ select • enter confirm • esc back • ? help"))
	case flashConfirm:
		c := m.flashCandidates[m.flashCursor]
		b.WriteString(fmt.Sprintf("Flash %s → %s (%s, %s %s)?\n",
			m.flashUnit, c.Path, device.FormatSize(c.Size), c.Vendor, c.Model))
		b.WriteString(failedStyle.Render(fmt.Sprintf("This will erase all data on %s.", c.Path)))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("y to confirm • n/esc to cancel • ? help"))
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
	case flashPermPrompt:
		cand := m.flashCandidates[m.flashCursor]
		username := flashChownUser()
		b.WriteString(failedStyle.Render(fmt.Sprintf("Permission denied writing %s.", cand.Path)))
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf("Run sudo chown %s %s?", username, cand.Path))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("y to run sudo chown • n/esc to cancel • ? help"))
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

// flashChownUser returns the username yoe should pass to `sudo chown`.
// Resolved in the yoe process so the value isn't subject to sudo's
// environment scrubbing.
func flashChownUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
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
