package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yoebuild/yoe/internal/serial"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// serialScrollback bounds the lines the Serial tab keeps in memory. The
// log file under build/serial/ is the complete record.
const serialScrollback = 10000

// serialModel is the TUI's view of the selected machine's console: the
// capture session (owned here, or attached to another yoe process), the
// scrollback shown on the Serial tab, and the Setup → Serial port picker.
type serialModel struct {
	session *serial.Session // owner session, nil when attached or off
	client  *serial.Client  // attachment to another process's session
	cfg     serial.Config
	// why explains an absent session: no serial block on the machine, no
	// adapter mapped, or an open error. Empty while a session runs.
	why string

	lines     []string
	follow    bool
	scroll    int // first visible line while not following
	linesCh   <-chan string
	cancelSub func()

	// Setup → Serial port picker
	ports    []serial.Port
	portsErr string
	cursor   int
}

// serialLinesMsg carries formatted console lines to the model.
type serialLinesMsg struct{ lines []string }

// serialClosedMsg reports that the line feed ended (session closed).
type serialClosedMsg struct{}

// serialStart (re)opens the console for the current machine. It closes any
// running session first, so it doubles as the machine-change hook. The
// returned command pumps lines into the model; nil when no console.
func (m *model) serialStart() tea.Cmd {
	m.serialStop()
	m.serial.follow = true

	ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
	cfg, err := serial.ResolveConfig(m.proj, ov, "", m.projectDir)
	if err != nil {
		m.serial.why = err.Error()
		return nil
	}
	m.serial.cfg = cfg

	s, err := serial.Open(context.Background(), cfg)
	if err == nil {
		m.serial.session = s
		m.serial.why = ""
		m.serial.linesCh, m.serial.cancelSub = s.SubscribeLines()
		return serialWaitCmd(m.serial.linesCh)
	}
	if !errors.Is(err, serial.ErrSessionExists) {
		m.serial.why = err.Error()
		return nil
	}
	// Another yoe process (a `yoe serial` in a shell) owns the adapter.
	// Attach to it and format its raw stream locally so the tab still
	// shows the console.
	c, err := serial.Attach(context.Background(), serial.SocketPath(cfg.Dir, cfg.Machine))
	if err != nil {
		m.serial.why = err.Error()
		return nil
	}
	m.serial.client = c
	m.serial.why = ""
	m.serial.linesCh, m.serial.cancelSub = clientLines(c)
	return serialWaitCmd(m.serial.linesCh)
}

// serialStop closes the session or attachment, if any.
func (m *model) serialStop() {
	if m.serial.cancelSub != nil {
		m.serial.cancelSub()
		m.serial.cancelSub = nil
	}
	if m.serial.session != nil {
		m.serial.session.Close()
		m.serial.session = nil
	}
	if m.serial.client != nil {
		m.serial.client.Detach()
		m.serial.client = nil
	}
	m.serial.linesCh = nil
}

// serialConsole returns whatever the interactive attach should talk to.
func (m model) serialConsole() serial.Console {
	if m.serial.session != nil {
		return m.serial.session
	}
	if m.serial.client != nil {
		return m.serial.client
	}
	return nil
}

// serialState summarizes the session for the header: connected / waiting /
// attached, with the current path.
func (m model) serialState() string {
	switch {
	case m.serial.session != nil:
		st := m.serial.session.State()
		switch st.Kind {
		case serial.StateConnected:
			return "connected " + st.Path
		case serial.StateWaiting:
			if st.LastErr != "" {
				return "waiting for adapter (" + st.LastErr + ")"
			}
			return "waiting for adapter"
		}
		return st.Kind.String()
	case m.serial.client != nil:
		return "attached to a session owned by another yoe process"
	}
	return "off"
}

// clientLines formats a Client's raw stream into log-style lines, the way
// the owning process does for its log, so the tab reads the same either
// way.
func clientLines(c *serial.Client) (<-chan string, func()) {
	out := make(chan string, 1024)
	raw, cancel := c.Subscribe()
	lw := serial.NewLogWriter(chanWriter{out}, time.Now)
	go func() {
		defer close(out)
		defer lw.Close()
		for chunk := range raw {
			_, _ = lw.Write(chunk)
		}
	}()
	return out, cancel
}

// chanWriter pushes each written line onto a channel, dropping when full.
type chanWriter struct{ ch chan string }

func (w chanWriter) Write(p []byte) (int, error) {
	select {
	case w.ch <- strings.TrimSuffix(string(p), "\n"):
	default:
	}
	return len(p), nil
}

// serialWaitCmd blocks for the next line, then drains what else is queued
// so a burst arrives as one message.
func serialWaitCmd(ch <-chan string) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		first, ok := <-ch
		if !ok {
			return serialClosedMsg{}
		}
		lines := []string{first}
		for len(lines) < 512 {
			select {
			case l, ok := <-ch:
				if !ok {
					return serialLinesMsg{lines: lines}
				}
				lines = append(lines, l)
			default:
				return serialLinesMsg{lines: lines}
			}
		}
		return serialLinesMsg{lines: lines}
	}
}

// serialAppend adds lines to the scrollback, trimming to the bound.
func (m *model) serialAppend(lines []string) {
	m.serial.lines = append(m.serial.lines, lines...)
	if over := len(m.serial.lines) - serialScrollback; over > 0 {
		m.serial.lines = append([]string(nil), m.serial.lines[over:]...)
		m.serial.scroll -= over
		if m.serial.scroll < 0 {
			m.serial.scroll = 0
		}
	}
}

func (m model) serialViewportHeight() int {
	chrome := m.homeHeaderLines()
	chrome++ // status line
	chrome++ // blank after status
	chrome++ // blank before bottom row
	chrome++ // help / message
	return m.paneHeight(chrome, 3)
}

// serialMaxScroll is the largest first-visible index that still fills the
// viewport.
func (m model) serialMaxScroll() int {
	if n := len(m.serial.lines) - m.serialViewportHeight(); n > 0 {
		return n
	}
	return 0
}

// updateSerialTab handles keys on the Serial tab.
func (m model) updateSerialTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		if len(m.cancels) > 0 {
			m.confirm = "quit"
			m.message = "Builds are running. Quit and cancel them? (y/n)"
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		if m.serial.follow {
			m.serial.follow = false
			m.serial.scroll = m.serialMaxScroll()
		}
		if m.serial.scroll > 0 {
			m.serial.scroll--
		}
		return m, nil
	case "down", "j":
		if m.serial.follow {
			return m, nil
		}
		if m.serial.scroll < m.serialMaxScroll() {
			m.serial.scroll++
		} else {
			m.serial.follow = true
		}
		return m, nil
	case "pgup", "ctrl+b":
		if m.serial.follow {
			m.serial.follow = false
			m.serial.scroll = m.serialMaxScroll()
		}
		m.serial.scroll -= m.serialViewportHeight()
		if m.serial.scroll < 0 {
			m.serial.scroll = 0
		}
		return m, nil
	case "pgdown", "ctrl+f":
		if m.serial.follow {
			return m, nil
		}
		m.serial.scroll += m.serialViewportHeight()
		if m.serial.scroll >= m.serialMaxScroll() {
			m.serial.scroll = m.serialMaxScroll()
			m.serial.follow = true
		}
		return m, nil
	case "g":
		m.serial.follow = false
		m.serial.scroll = 0
		return m, nil
	case "G":
		m.serial.follow = true
		return m, nil
	case "c":
		m.serial.lines = nil
		m.serial.scroll = 0
		m.serial.follow = true
		return m, nil
	case "s":
		m.view = viewSetup
		m.setupField = "serial"
		m.setupCursor = setupOptionIndex("Serial port")
		m.serialRefreshPorts()
		return m, nil
	case "enter":
		c := m.serialConsole()
		if c == nil {
			m.message = "no serial console: " + m.serial.why
			return m, nil
		}
		st := m.serialState()
		banner := fmt.Sprintf("[yoe serial] %s console, %s, %d baud — %s. ctrl-t q returns to the TUI, ctrl-t ? lists chords.",
			m.serial.cfg.Machine, m.serial.cfg.Mapped, m.serial.cfg.Baud, st)
		return m, tea.Exec(&serialExec{console: c, banner: banner}, func(err error) tea.Msg {
			return execDoneMsg{err: err}
		})
	}
	return m, nil
}

// serialExec is the in-process tea.ExecCommand that hands the terminal to
// the console. bubbletea releases the terminal for its duration; capture
// continues because the session is untouched.
type serialExec struct {
	console serial.Console
	banner  string
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

func (e *serialExec) Run() error {
	in := e.stdin
	if in == nil {
		in = os.Stdin
	}
	out := e.stdout
	if out == nil {
		out = os.Stdout
	}
	err := serial.Interactive(context.Background(), e.console, in, out, serial.InteractiveOptions{Banner: e.banner})
	if errors.Is(err, serial.ErrSessionEnded) {
		// The owning process exited; the tab will show the console as
		// off until a machine change or restart reopens it.
		return nil
	}
	return err
}

func (e *serialExec) SetStdin(r io.Reader)  { e.stdin = r }
func (e *serialExec) SetStdout(w io.Writer) { e.stdout = w }
func (e *serialExec) SetStderr(w io.Writer) { e.stderr = w }

// viewSerialTab renders the Serial tab: a status line and the scrollback.
func (m model) viewSerialTab() string {
	var b strings.Builder
	b.WriteString(m.renderHomeHeader())

	machine := m.proj.Defaults.Machine
	if m.serialConsole() == nil {
		fmt.Fprintf(&b, "  %s%s\n\n",
			fieldLabelStyle.Render("Serial: "),
			dimStyle.Render("off — "+m.serial.why))
	} else {
		fmt.Fprintf(&b, "  %s%s  %s%s  %s%s\n\n",
			fieldLabelStyle.Render("Serial: "),
			fieldValueStyle.Render(fmt.Sprintf("%s %s %d baud", machine, m.serial.cfg.Mapped, m.serial.cfg.Baud)),
			fieldLabelStyle.Render("State: "), fieldValueStyle.Render(m.serialState()),
			fieldLabelStyle.Render("Log: "), dimStyle.Render(serial.LogPath(m.serial.cfg.Dir, m.serial.cfg.Machine)))
	}

	viewH := m.serialViewportHeight()
	start := m.serial.scroll
	if m.serial.follow {
		start = m.serialMaxScroll()
	}
	if start > len(m.serial.lines) {
		start = len(m.serial.lines)
	}
	end := start + viewH
	if end > len(m.serial.lines) {
		end = len(m.serial.lines)
	}
	width := m.width - 2
	if width <= 0 {
		width = 200
	}
	if len(m.serial.lines) == 0 {
		if m.serialConsole() == nil {
			b.WriteString(dimStyle.Render("  Map an adapter on the Setup page (s) to capture this machine's console.\n"))
		} else {
			b.WriteString(dimStyle.Render("  Waiting for console output…\n"))
		}
		for i := 1; i < viewH; i++ {
			b.WriteString("\n")
		}
	} else {
		for _, l := range m.serial.lines[start:end] {
			b.WriteString("  " + clipFixed(l, width) + "\n")
		}
		for i := end - start; i < viewH; i++ {
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	if m.message != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("  " + m.message))
		b.WriteString("\n")
		return b.String()
	}
	follow := "following"
	if !m.serial.follow {
		follow = fmt.Sprintf("line %d/%d (G follows)", start+1, len(m.serial.lines))
	}
	b.WriteString(renderHelp([]helpItem{
		{"enter", "attach"}, {"j/k", "scroll"}, {"g/G", "top/follow"}, {"c", "clear"},
		{"s", "setup"}, {"tab", "next tab"}, {"?", "help"}, {"", follow},
	}))
	return b.String()
}

// ----- Setup → Serial port -----

// setupOptionIndex returns the Setup row for an option name.
func setupOptionIndex(name string) int {
	for i, o := range setupOptions {
		if o == name {
			return i
		}
	}
	return 0
}

// serialRefreshPorts re-reads the host's adapter list for the picker.
func (m *model) serialRefreshPorts() {
	ports, err := serial.List()
	if err != nil {
		m.serial.portsErr = err.Error()
		return
	}
	m.serial.portsErr = ""
	m.serial.ports = ports
	if m.serial.cursor >= len(ports) {
		m.serial.cursor = len(ports) - 1
	}
	if m.serial.cursor < 0 {
		m.serial.cursor = 0
	}
}

// serialMapped returns the local.star mapping for the current machine.
func (m model) serialMapped() string {
	ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
	return ov.SerialPorts[m.proj.Defaults.Machine]
}

// machineSerial returns the current machine's serial block, or nil.
func (m model) machineSerial() *yoestar.SerialConfig {
	if mc, ok := m.proj.Machines[m.proj.Defaults.Machine]; ok {
		return mc.Serial
	}
	return nil
}

func (m model) updateSetupSerial(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.setupField = ""
		m.message = ""
		return m, nil
	case "up", "k":
		if m.serial.cursor > 0 {
			m.serial.cursor--
		}
		return m, nil
	case "down", "j":
		if m.serial.cursor < len(m.serial.ports)-1 {
			m.serial.cursor++
		}
		return m, nil
	case "r":
		m.serialRefreshPorts()
		return m, nil
	case "d", "-":
		machine := m.proj.Defaults.Machine
		if m.serialMapped() == "" {
			return m, nil
		}
		if warn := m.mutateOverrides(func(ov *yoestar.LocalOverrides) {
			delete(ov.SerialPorts, machine)
		}); warn != "" {
			m.message = fmt.Sprintf("Serial port for %s cleared (warning: %s)", machine, warn)
		} else {
			m.message = fmt.Sprintf("Serial port for %s cleared (saved to local.star)", machine)
		}
		return m, m.serialStart()
	case "enter":
		if m.machineSerial() == nil || len(m.serial.ports) == 0 {
			return m, nil
		}
		machine := m.proj.Defaults.Machine
		key := m.serial.ports[m.serial.cursor].Key()
		if warn := m.mutateOverrides(func(ov *yoestar.LocalOverrides) {
			if ov.SerialPorts == nil {
				ov.SerialPorts = map[string]string{}
			}
			ov.SerialPorts[machine] = key
		}); warn != "" {
			m.message = fmt.Sprintf("Serial port for %s set (warning: %s)", machine, warn)
		} else {
			m.message = fmt.Sprintf("Serial port for %s = %s (saved to local.star)", machine, key)
		}
		m.setupField = ""
		return m, m.serialStart()
	}
	return m, nil
}

// serialSettingsSummary is the value shown next to the "Serial port" row.
func (m model) serialSettingsSummary() string {
	sc := m.machineSerial()
	if sc == nil {
		return "machine declares no serial console"
	}
	mapped := m.serialMapped()
	if mapped == "" {
		return fmt.Sprintf("%d baud · no adapter mapped", sc.Baud)
	}
	return fmt.Sprintf("%d baud · %s", sc.Baud, mapped)
}

func (m model) viewSetupSerial() string {
	var b strings.Builder
	machine := m.proj.Defaults.Machine
	b.WriteString(headerStyle.Render(fmt.Sprintf("  Serial port for %s", machine)))
	b.WriteString("\n\n")

	if m.machineSerial() == nil {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  machine %q declares no serial console; add\n", machine)))
		b.WriteString(dimStyle.Render("  serial = serial_config(baud = ...) to its machine file.\n"))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("  esc back  ? help"))
		b.WriteString("\n")
		return b.String()
	}
	if m.serial.portsErr != "" {
		b.WriteString(dimStyle.Render("  could not list adapters: " + m.serial.portsErr + "\n"))
	} else if len(m.serial.ports) == 0 {
		b.WriteString(dimStyle.Render("  no serial adapters found (looked in /dev/serial/by-id, /dev/ttyUSB*, /dev/ttyACM*)\n"))
	}
	mapped := m.serialMapped()
	for i, p := range m.serial.ports {
		cursor := "  "
		style := dimStyle
		if i == m.serial.cursor {
			cursor = "→ "
			style = selectedStyle
		}
		vendor := p.Vendor
		if vendor == "" {
			vendor = p.Path
		}
		serialNo := p.Serial
		if p.Interface != "" && p.Interface != "if00" {
			serialNo += " " + p.Interface
		}
		path := p.Path
		if p.ByID == "" {
			path = ""
		}
		tag := ""
		if _, ok := serial.Match([]serial.Port{p}, mapped); ok && mapped != "" {
			tag = cachedStyle.Render("  (mapped)")
		}
		if p.ByID == "" {
			tag += dimStyle.Render("  (no by-id entry)")
		}
		b.WriteString(fmt.Sprintf("%s%s  %s  %s%s\n",
			cursor, style.Render(clipFixed(vendor, 36)), clipFixed(serialNo, 14), path, tag))
	}
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  enter select  d clear  r refresh  esc back  ? help"))
	b.WriteString("\n")
	return b.String()
}
