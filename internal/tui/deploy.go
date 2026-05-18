package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yoebuild/yoe/internal/build"
	"github.com/yoebuild/yoe/internal/device"
	"github.com/yoebuild/yoe/internal/feed"
	"github.com/yoebuild/yoe/internal/resolve"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// updateDeploy handles keys while the deploy view is active.
func (m model) updateDeploy(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.deployStage {
	case deployHostInput:
		switch msg.String() {
		case "esc":
			m.view = viewUnits
			return m, nil
		case "enter":
			if strings.TrimSpace(m.deployHost) == "" {
				return m, nil
			}
			m.deployStage = deployRunning
			m.deployOutput = nil
			m.deployErr = nil
			return m, m.startDeployCmd()
		case "backspace":
			if n := len(m.deployHost); n > 0 {
				m.deployHost = m.deployHost[:n-1]
			}
			return m, nil
		case "ctrl+u":
			m.deployHost = ""
			return m, nil
		default:
			s := msg.String()
			if len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f {
				m.deployHost += s
			}
			return m, nil
		}
	case deployRunning:
		// Ignore keys while running. Ctrl+C still quits via the global
		// handler.
		return m, nil
	case deployDone, deployError:
		switch msg.String() {
		case "esc", "enter", "q":
			m.view = viewUnits
			m.deployStage = deployHostInput
			return m, nil
		}
	}
	return m, nil
}

// startDeployCmd builds the unit, then runs device.Deploy with output
// streamed to the TUI line-by-line.
func (m model) startDeployCmd() tea.Cmd {
	proj := m.proj
	projectDir := m.projectDir
	machine := proj.Defaults.Machine
	unitName := m.deployUnit
	hostSpec := strings.TrimSpace(m.deployHost)
	arch := m.arch

	return func() tea.Msg {
		// Build first.
		emit := func(line string) {
			if tuiProgram != nil {
				tuiProgram.Send(deployOutputMsg{line: line})
			}
		}
		emit("→ build " + unitName)
		buildOut := lineWriter{emit: emit}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Build the runtime closure, not just the leaf unit, so the device's
		// apk add can resolve every dep against the feed.
		closure := resolve.RuntimeClosure(proj, []string{unitName})
		if err := build.BuildUnits(proj, closure, build.Options{
			Ctx:        ctx,
			ProjectDir: projectDir,
			Arch:       arch,
			Machine:    machine,
		}, buildOut); err != nil {
			return deployDoneMsg{err: fmt.Errorf("build %s: %w", unitName, err)}
		}

		// Resolve a feed: reuse mDNS-advertised one (likely the TUI's own
		// auto-started serve) or fall back to constructing one from the
		// project's hostname-derived URL.
		feedURL := resolveFeedURL(proj, projectDir)
		if feedURL == "" {
			return deployDoneMsg{err: fmt.Errorf("no feed available — restart the TUI or run `yoe serve` separately")}
		}
		emit("→ feed " + feedURL)

		// Parse the host spec.
		target, err := device.ParseSSHTarget(hostSpec, "root")
		if err != nil {
			return deployDoneMsg{err: err}
		}

		emit(fmt.Sprintf("→ ssh %s — apk del + apk add %s", target.Host, unitName))
		err = device.Deploy(ctx, device.DeployInput{
			Target:  target,
			Unit:    unitName,
			FeedURL: feedURL,
			Out:     lineWriter{emit: emit},
		})
		return deployDoneMsg{err: err}
	}
}

// resolveFeedURL picks an mDNS feed URL matching the project, or returns
// "" if none is advertised. The TUI auto-starts a feed at startup, so
// under normal circumstances this answers immediately.
func resolveFeedURL(proj *yoestar.Project, projectDir string) string {
	_ = projectDir
	results, _ := feed.BrowseMDNS(500 * time.Millisecond)
	for _, r := range results {
		if r.Project == proj.Name {
			return r.URL()
		}
	}
	return ""
}

// lineWriter is an io.Writer that splits on newlines and emits each line.
type lineWriter struct {
	emit func(string)
	buf  string
}

func (w lineWriter) Write(p []byte) (int, error) {
	r := bufio.NewReader(strings.NewReader(w.buf + string(p)))
	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return len(p), err
		}
		w.emit(strings.TrimRight(line, "\r\n"))
	}
	// The lineWriter is a value receiver, so partial-line buffering doesn't
	// persist between Writes — flush every chunk by emitting any trailing
	// non-newline-terminated text as its own line if non-empty.
	rest, _ := r.ReadString(0)
	if rest != "" {
		w.emit(rest)
	}
	return len(p), nil
}

// viewDeploy renders the deploy view.
func (m model) viewDeploy() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf(" yoe deploy — %s ", m.deployUnit)))
	b.WriteString("\n\n")

	switch m.deployStage {
	case deployHostInput:
		b.WriteString("  Target host (e.g. dev-pi.local, pi@host:22, localhost:2222):\n\n")
		b.WriteString("    > " + m.deployHost + "_")
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("enter: deploy • esc: cancel • ctrl+u: clear"))
	case deployRunning:
		b.WriteString(buildingStyle.Render(fmt.Sprintf("Deploying %s → %s", m.deployUnit, m.deployHost)))
		b.WriteString("\n\n")
		// Show the last N output lines so the view doesn't scroll
		// indefinitely.
		const maxLines = 20
		start := 0
		if len(m.deployOutput) > maxLines {
			start = len(m.deployOutput) - maxLines
		}
		for _, line := range m.deployOutput[start:] {
			b.WriteString("  " + dimStyle.Render(line) + "\n")
		}
	case deployDone:
		b.WriteString(buildingStyle.Render("Deploy complete."))
		b.WriteString("\n\n")
		const maxLines = 10
		start := 0
		if len(m.deployOutput) > maxLines {
			start = len(m.deployOutput) - maxLines
		}
		for _, line := range m.deployOutput[start:] {
			b.WriteString("  " + dimStyle.Render(line) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("press any key to return"))
	case deployError:
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
		b.WriteString(errStyle.Render(fmt.Sprintf("Deploy failed: %v", m.deployErr)))
		b.WriteString("\n\n")
		const maxLines = 15
		start := 0
		if len(m.deployOutput) > maxLines {
			start = len(m.deployOutput) - maxLines
		}
		for _, line := range m.deployOutput[start:] {
			b.WriteString("  " + dimStyle.Render(line) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("press any key to return"))
	}
	return b.String()
}
