package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yoebuild/yoe/internal/serial"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// serialTestModel is a model whose current machine declares a 115200
// console, with a scratch project dir for local.star.
func serialTestModel(t *testing.T, withSerial bool) model {
	t.Helper()
	mc := &yoestar.Machine{Name: "rpi5", Arch: "arm64"}
	if withSerial {
		mc.Serial = &yoestar.SerialConfig{Baud: 115200}
	}
	m := model{
		projectDir: t.TempDir(),
		width:      120,
		height:     40,
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "rpi5", Image: "dev-image"},
			Machines: map[string]*yoestar.Machine{"rpi5": mc},
		},
	}
	m.serial.follow = true
	return m
}

func serialKey(t *testing.T, m model, s string, fn func(model, tea.KeyMsg) (tea.Model, tea.Cmd)) (model, tea.Cmd) {
	t.Helper()
	var msg tea.KeyMsg
	switch s {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	updated, cmd := fn(m, msg)
	return updated.(model), cmd
}

func TestSetupOptions_HasSerialPort(t *testing.T) {
	if setupOptionIndex("Serial port") == 0 {
		t.Fatalf("Serial port missing from setupOptions: %v", setupOptions)
	}
}

func TestSetupSerial_PickerWritesAndClearsMapping(t *testing.T) {
	m := serialTestModel(t, true)
	m.serial.ports = []serial.Port{
		{ByID: "/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0", Path: "/dev/ttyUSB0", Vendor: "FTDI FT232R USB UART", Serial: "A50285BI"},
		{Path: "/dev/ttyACM0"},
	}
	m.serial.cursor = 0

	m, _ = serialKey(t, m, "enter", model.updateSetupSerial)
	ov, err := yoestar.LoadLocalOverrides(m.projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ov.SerialPorts["rpi5"]; got != "/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0" {
		t.Fatalf("mapping after enter = %q", got)
	}
	if m.setupField != "" {
		t.Errorf("picker should close after select, setupField = %q", m.setupField)
	}
	if !strings.Contains(m.message, "saved to local.star") {
		t.Errorf("message = %q", m.message)
	}
	// No adapter is really present, so the session waits rather than
	// failing; the tab must report that, not "off".
	if m.serialConsole() == nil {
		t.Fatalf("expected an owned session after mapping, why = %q", m.serial.why)
	}
	if st := m.serialState(); !strings.HasPrefix(st, "waiting") {
		t.Errorf("state = %q, want waiting", st)
	}
	m.serialStop()

	// Summary row reflects the mapping.
	if s := m.serialSettingsSummary(); !strings.Contains(s, "115200") || !strings.Contains(s, "A50285BI") {
		t.Errorf("summary = %q", s)
	}

	// Second row (no by-id) stores its /dev/tty* path.
	m.setupField = "serial"
	m.serial.cursor = 1
	m, _ = serialKey(t, m, "enter", model.updateSetupSerial)
	ov, _ = yoestar.LoadLocalOverrides(m.projectDir)
	if got := ov.SerialPorts["rpi5"]; got != "/dev/ttyACM0" {
		t.Fatalf("mapping after second enter = %q", got)
	}
	m.serialStop()

	// d clears.
	m.setupField = "serial"
	m, _ = serialKey(t, m, "d", model.updateSetupSerial)
	ov, _ = yoestar.LoadLocalOverrides(m.projectDir)
	if _, ok := ov.SerialPorts["rpi5"]; ok {
		t.Fatalf("mapping not cleared: %v", ov.SerialPorts)
	}
	if m.serialConsole() != nil {
		t.Error("session should be closed after clearing the mapping")
	}
	if !strings.Contains(m.serial.why, "no serial adapter mapped") {
		t.Errorf("why = %q", m.serial.why)
	}
}

func TestSetupSerial_MachineWithoutSerialBlock(t *testing.T) {
	m := serialTestModel(t, false)
	m.serial.ports = []serial.Port{{Path: "/dev/ttyUSB0"}}
	view := m.viewSetupSerial()
	if !strings.Contains(view, "declares no serial console") {
		t.Errorf("view should explain the missing serial block:\n%s", view)
	}
	m, _ = serialKey(t, m, "enter", model.updateSetupSerial)
	ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
	if len(ov.SerialPorts) != 0 {
		t.Errorf("enter must not map a port for a machine without serial: %v", ov.SerialPorts)
	}
	if s := m.serialSettingsSummary(); !strings.Contains(s, "no serial console") {
		t.Errorf("summary = %q", s)
	}
}

func TestSetupSerial_PickerRows(t *testing.T) {
	m := serialTestModel(t, true)
	m.serial.ports = []serial.Port{
		{ByID: "/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0", Path: "/dev/ttyUSB0", Vendor: "FTDI FT232R USB UART", Serial: "A50285BI", Interface: "if00"},
		{Path: "/dev/ttyACM0"},
	}
	if err := yoestar.WriteLocalOverrides(m.projectDir, yoestar.LocalOverrides{
		SerialPorts: map[string]string{"rpi5": "/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0"},
	}); err != nil {
		t.Fatal(err)
	}
	view := m.viewSetupSerial()
	for _, want := range []string{"FTDI FT232R USB UART", "A50285BI", "/dev/ttyUSB0", "(mapped)", "/dev/ttyACM0", "(no by-id entry)"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker view lacks %q:\n%s", want, view)
		}
	}
}

func TestSerialTab_HeaderStates(t *testing.T) {
	m := serialTestModel(t, true)
	m.activeTab = tabSerial
	m.serial.why = `no serial adapter mapped for "rpi5"`
	view := m.viewSerialTab()
	if !strings.Contains(view, "off — no serial adapter mapped") || !strings.Contains(view, "Setup page") {
		t.Errorf("unmapped view:\n%s", view)
	}

	// Map an adapter that is not present: the session waits.
	if err := yoestar.WriteLocalOverrides(m.projectDir, yoestar.LocalOverrides{
		SerialPorts: map[string]string{"rpi5": "/dev/serial/by-id/usb-Nope_0-if00"},
	}); err != nil {
		t.Fatal(err)
	}
	if cmd := m.serialStart(); cmd == nil {
		t.Fatalf("serialStart returned no pump, why = %q", m.serial.why)
	}
	defer m.serialStop()
	view = m.viewSerialTab()
	if !strings.Contains(view, "115200 baud") || !strings.Contains(view, "waiting") || !strings.Contains(view, "rpi5.log") {
		t.Errorf("waiting view:\n%s", view)
	}
}

func TestSerialTab_LinesFollowAndScroll(t *testing.T) {
	m := serialTestModel(t, true)
	m.activeTab = tabSerial
	m.height = 20
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "[2026-09-19 14:03:22.000] line "+strings.Repeat("x", i%3)+string(rune('A'+i%26)))
	}
	m.serialAppend(lines)
	if !m.serial.follow {
		t.Fatal("follow should start on")
	}
	view := m.viewSerialTab()
	if !strings.Contains(view, lines[49]) || strings.Contains(view, lines[0]) {
		t.Errorf("following view should show the newest line, not the oldest:\n%s", view)
	}
	if !strings.Contains(view, "following") {
		t.Errorf("bottom row should say following:\n%s", view)
	}

	// k pauses following; g jumps to the top; G resumes.
	m, _ = serialKey(t, m, "k", model.updateSerialTab)
	if m.serial.follow {
		t.Error("k should pause following")
	}
	m, _ = serialKey(t, m, "g", model.updateSerialTab)
	view = m.viewSerialTab()
	if !strings.Contains(view, lines[0]) || !strings.Contains(view, "line 1/50") {
		t.Errorf("g view:\n%s", view)
	}
	m, _ = serialKey(t, m, "G", model.updateSerialTab)
	if !m.serial.follow {
		t.Error("G should resume following")
	}

	// c clears the scrollback.
	m, _ = serialKey(t, m, "c", model.updateSerialTab)
	if len(m.serial.lines) != 0 {
		t.Errorf("c left %d lines", len(m.serial.lines))
	}
}

func TestSerialTab_ScrollbackIsBounded(t *testing.T) {
	m := serialTestModel(t, true)
	batch := make([]string, 3000)
	for i := range batch {
		batch[i] = "l"
	}
	for i := 0; i < 5; i++ {
		m.serialAppend(batch)
	}
	if len(m.serial.lines) != serialScrollback {
		t.Errorf("scrollback = %d, want %d", len(m.serial.lines), serialScrollback)
	}
}

func TestSerialTab_EnterWithoutConsoleExplains(t *testing.T) {
	m := serialTestModel(t, true)
	m.serial.why = "no serial adapter mapped"
	m, cmd := serialKey(t, m, "enter", model.updateSerialTab)
	if cmd != nil {
		t.Error("enter without a console must not exec")
	}
	if !strings.Contains(m.message, "no serial console") {
		t.Errorf("message = %q", m.message)
	}
}

func TestSerialTab_HelpAndTabLabel(t *testing.T) {
	m := serialTestModel(t, true)
	m.view = viewUnits
	m.activeTab = tabSerial
	title, sections := m.helpSections()
	if !strings.HasPrefix(title, "Serial") || len(sections) == 0 {
		t.Errorf("help for the Serial tab: %q %d sections", title, len(sections))
	}
	if tabSerial.String() != "Serial" {
		t.Errorf("tab label = %q", tabSerial.String())
	}
	if int(numHomeTabs) != 4 {
		t.Errorf("numHomeTabs = %d, want 4", numHomeTabs)
	}
}

func TestSerialWaitCmd_BatchesAndCloses(t *testing.T) {
	ch := make(chan string, 8)
	ch <- "a"
	ch <- "b"
	msg := serialWaitCmd(ch)()
	got, ok := msg.(serialLinesMsg)
	if !ok || len(got.lines) != 2 {
		t.Fatalf("msg = %#v", msg)
	}
	close(ch)
	if _, ok := serialWaitCmd(ch)().(serialClosedMsg); !ok {
		t.Error("closed channel should yield serialClosedMsg")
	}
	if serialWaitCmd(nil) != nil {
		t.Error("nil channel should yield no command")
	}
}
