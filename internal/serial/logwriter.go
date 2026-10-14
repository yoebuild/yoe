package serial

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"time"
)

// LogWriter turns a raw console stream into the line-oriented text yoe logs:
// one line per console line, each prefixed with a timestamp, with terminal
// escape sequences removed so the file reads cleanly in an editor, in grep,
// or in Claude Code. The timestamp is taken when the line's first byte
// arrives, so it dates what the board printed, not when the newline came.
//
// A partial line (a prompt with no newline) is flushed after idleFlush of
// silence so it still lands in the log.
type LogWriter struct {
	mu   sync.Mutex
	out  io.Writer
	now  func() time.Time
	line bytes.Buffer
	// lineStart is when the pending line's first byte arrived.
	lineStart time.Time
	// crFlushed is set when a bare '\r' just flushed a non-empty line, so
	// the '\n' of a "\r\n" pair does not emit a second, empty line.
	crFlushed bool
	esc       escState
	idle      *time.Timer
	closed    bool
}

type escState int

const (
	escNone escState = iota
	escESC           // saw ESC, deciding what follows
	escCSI           // inside ESC [ … final byte
	escOSC           // inside ESC ] … BEL or ESC \
)

// idleFlush is how long a partial line waits before it is written out.
const idleFlush = 250 * time.Millisecond

// timestampLayout is the prefix on every logged line.
const timestampLayout = "2006-01-02 15:04:05.000"

// NewLogWriter returns a LogWriter writing formatted lines to out. now is
// the clock; pass time.Now outside tests.
func NewLogWriter(out io.Writer, now func() time.Time) *LogWriter {
	return &LogWriter{out: out, now: now}
}

// Write consumes raw console bytes. It never returns a short count: bytes
// that produce no output (escape sequences, control characters) are still
// consumed.
func (l *LogWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range p {
		if err := l.feed(b); err != nil {
			return 0, err
		}
	}
	if l.line.Len() > 0 {
		l.armIdle()
	}
	return len(p), nil
}

func (l *LogWriter) feed(b byte) error {
	switch l.esc {
	case escESC:
		switch b {
		case '[':
			l.esc = escCSI
		case ']':
			l.esc = escOSC
		default:
			// Two-byte sequence (ESC c, ESC =, charset selects …): done.
			l.esc = escNone
		}
		return nil
	case escCSI:
		// Parameter and intermediate bytes are 0x20–0x3F; the final byte
		// is 0x40–0x7E.
		if b >= 0x40 && b <= 0x7e {
			l.esc = escNone
		}
		return nil
	case escOSC:
		if b == 0x07 {
			l.esc = escNone
		} else if b == 0x1b {
			// ESC \ terminates an OSC; the '\' arrives next and is
			// swallowed by the escESC state.
			l.esc = escESC
		}
		return nil
	}

	switch {
	case b == 0x1b:
		l.esc = escESC
	case b == '\n':
		if l.crFlushed {
			l.crFlushed = false
			return nil
		}
		return l.flushLine()
	case b == '\r':
		if l.line.Len() > 0 {
			if err := l.flushLine(); err != nil {
				return err
			}
			l.crFlushed = true
		}
	case b == '\t' || b >= 0x20 && b != 0x7f:
		l.crFlushed = false
		if l.line.Len() == 0 {
			l.lineStart = l.now()
		}
		l.line.WriteByte(b)
	default:
		// Other control bytes (BEL, backspace, …) carry nothing worth
		// logging.
	}
	return nil
}

// flushLine writes the pending line, empty or not, with its timestamp.
func (l *LogWriter) flushLine() error {
	ts := l.lineStart
	if l.line.Len() == 0 {
		ts = l.now()
	}
	_, err := fmt.Fprintf(l.out, "[%s] %s\n", ts.Format(timestampLayout), l.line.String())
	l.line.Reset()
	l.disarmIdle()
	return err
}

func (l *LogWriter) armIdle() {
	if l.closed {
		return
	}
	if l.idle == nil {
		l.idle = time.AfterFunc(idleFlush, l.idleFire)
		return
	}
	l.idle.Reset(idleFlush)
}

func (l *LogWriter) disarmIdle() {
	if l.idle != nil {
		l.idle.Stop()
	}
}

func (l *LogWriter) idleFire() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.line.Len() > 0 {
		_ = l.flushLine()
	}
}

// Flush writes out any partial line now.
func (l *LogWriter) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.line.Len() == 0 {
		return nil
	}
	return l.flushLine()
}

// Marker flushes any partial line and writes a "--- text ---" line so
// session boundaries are visible in the log.
func (l *LogWriter) Marker(text string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.line.Len() > 0 {
		if err := l.flushLine(); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(l.out, "[%s] --- %s ---\n", l.now().Format(timestampLayout), text)
	return err
}

// Close flushes and stops the idle timer. The writer must not be used
// afterwards.
func (l *LogWriter) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.disarmIdle()
	if l.line.Len() > 0 {
		return l.flushLine()
	}
	return nil
}
