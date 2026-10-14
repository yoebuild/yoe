package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/yoebuild/yoe/internal/serial"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// cmdSerial is `yoe serial`: attach to a machine's console interactively,
// or with a subcommand list adapters, tail the captured log, or send text.
func cmdSerial(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			cmdSerialList(args[1:])
			return
		case "log":
			cmdSerialLog(args[1:])
			return
		case "send":
			cmdSerialSend(args[1:])
			return
		case "help", "--help", "-h":
			fmt.Print(serialUsage)
			return
		}
	}
	cmdSerialAttach(args)
}

const serialUsage = `Usage:
  yoe serial [--machine <name>] [--listen <addr>]   attach to the console (ctrl-t q detaches)
  yoe serial list                                   list serial adapters on this host
  yoe serial log [--machine <name>] [-f] [-n <N>]   show the captured console log
  yoe serial send [--machine <name>] [--raw] <text> send a line to the console

The console is captured to build/serial/<machine>.log whenever a yoe process
owns it. Map an adapter with 'yoe config set serial-port <path>' or on the
TUI Setup page. See docs/hardware-in-loop.md.
`

// serialConfig resolves the console for the machine (or the project's
// effective machine when machineName is empty).
func serialConfig(machineName string) serial.Config {
	proj, err := loadMachineSettings(machineName)
	if err != nil {
		fail("Error: %v", err)
	}
	root, err := yoestar.FindProjectRoot(projectDir())
	if err != nil {
		fail("Error: %v", err)
	}
	cfg, err := serial.ResolveConfig(proj, readOverrides(), machineName, root)
	if err != nil {
		fail("Error: %v", err)
	}
	return cfg
}

func cmdSerialAttach(args []string) {
	fs := flag.NewFlagSet("serial", flag.ExitOnError)
	machineName := fs.String("machine", "", "machine whose console to attach (default: the project's current machine)")
	listen := fs.String("listen", "", "also serve the raw console on this TCP address (e.g. 127.0.0.1:7001)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, serialUsage) }
	fs.Parse(args)
	if fs.NArg() > 0 {
		fail("yoe serial: unknown subcommand %q\n\n%s", fs.Arg(0), serialUsage)
	}

	cfg := serialConfig(*machineName)
	if *listen != "" {
		cfg.Listen = *listen
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for {
		c, owner, err := serial.OpenOrAttach(ctx, cfg)
		if err != nil {
			fail("Error: %v", err)
		}
		role := "attached to the session owned by another yoe process"
		if owner {
			role = "owning the adapter; log at " + serial.LogPath(cfg.Dir, cfg.Machine)
		}
		banner := fmt.Sprintf("[yoe serial] %s console, %s, %d baud — %s. ctrl-t q detaches, ctrl-t ? lists chords.",
			cfg.Machine, cfg.Mapped, cfg.Baud, role)
		err = serial.Interactive(ctx, c, os.Stdin, os.Stdout, serial.InteractiveOptions{Banner: banner})
		if owner {
			c.(*serial.Session).Close()
		} else {
			c.Detach()
		}
		if errors.Is(err, serial.ErrSessionEnded) {
			// The owner exited under us. Take the adapter over so the
			// console the user is looking at does not go dark.
			fmt.Println("[yoe serial] session ended; taking over the adapter")
			continue
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			fail("Error: %v", err)
		}
		return
	}
}

func cmdSerialList(args []string) {
	fs := flag.NewFlagSet("serial list", flag.ExitOnError)
	fs.Parse(args)

	ports, err := serial.List()
	if err != nil {
		fail("Error: %v", err)
	}
	if len(ports) == 0 {
		fmt.Println("no serial adapters found (looked in /dev/serial/by-id, /dev/ttyUSB*, /dev/ttyACM*)")
		return
	}
	// Mark the adapter mapped to the current machine, when a project is
	// loadable from here; outside a project the listing still works.
	mapped := ""
	machine := ""
	if proj, err := loadMachineSettings(""); err == nil {
		machine = proj.Defaults.Machine
		mapped = readOverrides().SerialPorts[machine]
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "VENDOR\tSERIAL\tPATH\tBY-ID")
	for _, p := range ports {
		byID := p.ByID
		if byID == "" {
			byID = "(no by-id entry)"
		}
		mark := ""
		if _, ok := serial.Match([]serial.Port{p}, mapped); ok && mapped != "" {
			mark = "  (mapped: " + machine + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s%s\n", p.Vendor, p.Serial, p.Path, byID, mark)
	}
	tw.Flush()
}

func cmdSerialLog(args []string) {
	fs := flag.NewFlagSet("serial log", flag.ExitOnError)
	machineName := fs.String("machine", "", "machine whose log to show")
	follow := fs.Bool("f", false, "follow the log as it grows")
	lines := fs.Int("n", 0, "show only the last N lines (0 = whole file)")
	fs.Parse(args)

	// Only the machine and project root are needed; the adapter need not
	// be mapped or present to read what was captured earlier.
	proj, err := loadMachineSettings(*machineName)
	if err != nil {
		fail("Error: %v", err)
	}
	root, err := yoestar.FindProjectRoot(projectDir())
	if err != nil {
		fail("Error: %v", err)
	}
	machine := *machineName
	if machine == "" {
		machine = proj.Defaults.Machine
	}
	path := serial.LogPath(serial.DirFor(root), machine)

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fail("no serial log for %s yet (%s); the log is written while the TUI or 'yoe serial' owns the console", machine, path)
		}
		fail("Error: %v", err)
	}
	if *lines > 0 {
		data = lastLines(data, *lines)
	}
	os.Stdout.Write(data)
	if !*follow {
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := followFile(ctx, path, int64(len(data)), os.Stdout); err != nil {
		fail("Error: %v", err)
	}
}

// lastLines returns the final n lines of data.
func lastLines(data []byte, n int) []byte {
	trimmed := strings.TrimSuffix(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	all := strings.Split(trimmed, "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return []byte(strings.Join(all, "\n") + "\n")
}

// followFile copies bytes appended to path after offset to w until ctx
// ends, polling rather than depending on inotify. A file that shrinks
// (truncated by yoe clean) restarts from the beginning.
func followFile(ctx context.Context, path string, offset int64, w io.Writer) error {
	// offset came from a ReadFile of the whole file; re-anchor to the
	// file's current size so a tail-only print does not replay the head.
	if st, err := os.Stat(path); err == nil {
		offset = st.Size()
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
		st, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				offset = 0
				continue
			}
			return err
		}
		if st.Size() < offset {
			offset = 0
		}
		if st.Size() == offset {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		n, err := io.Copy(w, f)
		f.Close()
		if err != nil {
			return err
		}
		offset += n
	}
}

func cmdSerialSend(args []string) {
	fs := flag.NewFlagSet("serial send", flag.ExitOnError)
	machineName := fs.String("machine", "", "machine whose console to write to")
	raw := fs.Bool("raw", false, "send the text exactly as given, without appending a newline")
	fs.Parse(args)
	if fs.NArg() == 0 {
		fail("Usage: yoe serial send [--machine <name>] [--raw] <text>")
	}
	text := strings.Join(fs.Args(), " ")
	if !*raw {
		text += "\n"
	}

	cfg := serialConfig(*machineName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, owner, err := serial.OpenOrAttach(ctx, cfg)
	if err != nil {
		fail("Error: %v", err)
	}
	if owner {
		// Nobody else holds the console, so this process opened it for
		// the one write. Give the adapter a moment to connect, then let
		// go so the next command (or the TUI) can take it.
		s := c.(*serial.Session)
		defer s.Close()
		deadline := time.Now().Add(3 * time.Second)
		for s.State().Kind != serial.StateConnected && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if st := s.State(); st.Kind != serial.StateConnected {
			fail("Error: adapter %s not connected: %s", cfg.Mapped, st.LastErr)
		}
	} else {
		defer c.Detach()
	}
	if _, err := c.Write([]byte(text)); err != nil {
		fail("Error: %v", err)
	}
	if owner {
		// Let the reply land in the log before the session closes.
		time.Sleep(500 * time.Millisecond)
	}
}
