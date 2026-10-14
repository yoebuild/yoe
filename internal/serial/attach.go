package serial

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
)

// ErrSessionEnded is returned by Interactive when the console went away
// underneath it: the owning process exited.
var ErrSessionEnded = errors.New("serial: session ended")

// chordPrefix is the key that introduces a local command, as in tio.
const chordPrefix = 0x14 // ctrl-t

// actionKind is what a parsed keystroke asks for.
type actionKind int

const (
	actSend actionKind = iota
	actQuit
	actBreak
	actToggleTimestamps
	actHelp
)

type action struct {
	kind actionKind
	data []byte
}

// chordParser turns keystrokes into actions. Bytes pass through until
// ctrl-t; the byte after ctrl-t selects a local command, and an unknown
// one is ignored.
type chordParser struct{ prefixed bool }

func (p *chordParser) feed(b []byte) []action {
	var out []action
	var plain []byte
	flush := func() {
		if len(plain) > 0 {
			out = append(out, action{kind: actSend, data: plain})
			plain = nil
		}
	}
	for _, c := range b {
		if !p.prefixed {
			if c == chordPrefix {
				p.prefixed = true
				continue
			}
			plain = append(plain, c)
			continue
		}
		p.prefixed = false
		flush()
		switch c {
		case 'q':
			out = append(out, action{kind: actQuit})
		case 'b':
			out = append(out, action{kind: actBreak})
		case 't':
			out = append(out, action{kind: actToggleTimestamps})
		case '?':
			out = append(out, action{kind: actHelp})
		case chordPrefix:
			plain = append(plain, chordPrefix)
		}
	}
	flush()
	return out
}

const chordHelp = "\r\n[yoe serial] ctrl-t q detach · ctrl-t b break · ctrl-t t timestamps · ctrl-t ctrl-t send ctrl-t · ctrl-t ? help\r\n"

// InteractiveOptions tunes Interactive.
type InteractiveOptions struct {
	// Timestamps starts with a local timestamp prefix on each output line.
	Timestamps bool
	// Now is the clock for timestamps; nil means time.Now.
	Now func() time.Time
	// Banner is printed before the loop starts, e.g. "attached to
	// raspberrypi5 (/dev/ttyUSB0 115200); ctrl-t q to detach".
	Banner string
}

// Interactive gives the terminal to the console: bytes from in go to the
// console and console output goes to out, until ctrl-t q. When in is a
// terminal it is put into raw mode for the duration. Returns nil on
// detach, ErrSessionEnded when the console closed, or the context error.
func Interactive(ctx context.Context, c Console, in io.Reader, out io.Writer, opts InteractiveOptions) error {
	if f, ok := in.(*os.File); ok && term.IsTerminal(f.Fd()) {
		st, err := term.MakeRaw(f.Fd())
		if err != nil {
			return fmt.Errorf("serial: raw mode: %w", err)
		}
		defer term.Restore(f.Fd(), st)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	// A cancelable reader so a session that ends while we block on stdin
	// does not leave a goroutine holding the terminal's input after we
	// return (the TUI takes it back).
	cr, err := cancelreader.NewReader(in)
	if err != nil {
		return fmt.Errorf("serial: input: %w", err)
	}
	defer cr.Cancel()

	if opts.Banner != "" {
		fmt.Fprintf(out, "%s\r\n", opts.Banner)
	}

	output, unsubscribe := c.Subscribe()
	defer unsubscribe()

	// The input goroutine closes actions when stdin ends, so queued
	// actions are always handled before the "input ended" signal.
	actions := make(chan action, 16)
	go func() {
		defer close(actions)
		var p chordParser
		buf := make([]byte, 256)
		for {
			n, err := cr.Read(buf)
			if n > 0 {
				for _, a := range p.feed(append([]byte(nil), buf[:n]...)) {
					actions <- a
					if a.kind == actQuit {
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	ts := opts.Timestamps
	atLineStart := true
	writeOut := func(chunk []byte) error {
		if !ts {
			_, err := out.Write(chunk)
			return err
		}
		var b []byte
		for _, ch := range chunk {
			if atLineStart {
				b = append(b, []byte("["+now().Format("15:04:05.000")+"] ")...)
				atLineStart = false
			}
			b = append(b, ch)
			if ch == '\n' {
				atLineStart = true
			}
		}
		_, err := out.Write(b)
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-output:
			if !ok {
				fmt.Fprint(out, "\r\n[yoe serial] session ended\r\n")
				return ErrSessionEnded
			}
			if err := writeOut(chunk); err != nil {
				return err
			}
		case a, ok := <-actions:
			if !ok {
				// stdin closed without a quit chord (piped input ended).
				return nil
			}
			switch a.kind {
			case actSend:
				if _, err := c.Write(a.data); err != nil {
					fmt.Fprintf(out, "\r\n[yoe serial] %v\r\n", err)
				}
			case actQuit:
				// Show whatever the board already sent before letting go
				// of the terminal, so the last lines are not lost.
				for drained := false; !drained; {
					select {
					case chunk, ok := <-output:
						if !ok {
							drained = true
						} else if err := writeOut(chunk); err != nil {
							return err
						}
					default:
						drained = true
					}
				}
				fmt.Fprint(out, "\r\n[yoe serial] detached\r\n")
				return nil
			case actBreak:
				if err := c.Break(); err != nil {
					fmt.Fprintf(out, "\r\n[yoe serial] %v\r\n", err)
				} else {
					fmt.Fprint(out, "\r\n[yoe serial] break sent\r\n")
				}
			case actToggleTimestamps:
				ts = !ts
				atLineStart = true
				fmt.Fprintf(out, "\r\n[yoe serial] timestamps %s\r\n", map[bool]string{true: "on", false: "off"}[ts])
			case actHelp:
				fmt.Fprint(out, chordHelp)
			}
		}
	}
}
