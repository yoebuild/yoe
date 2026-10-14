package serial

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedClock() func() time.Time {
	t0 := time.Date(2026, 9, 19, 14, 3, 22, 123_000_000, time.UTC)
	n := 0
	return func() time.Time {
		n++
		return t0.Add(time.Duration(n-1) * time.Second)
	}
}

func TestLogWriter_Lines(t *testing.T) {
	cases := []struct {
		name string
		in   []string // successive Write calls
		want []string // logged lines without the timestamp prefix
	}{
		{"lf", []string{"hello\nworld\n"}, []string{"hello", "world"}},
		{"crlf", []string{"hello\r\nworld\r\n"}, []string{"hello", "world"}},
		{"cr only", []string{"10%\r50%\r100%\r"}, []string{"10%", "50%", "100%"}},
		{"crlf split across writes", []string{"hello\r", "\nworld\n"}, []string{"hello", "world"}},
		{"blank line kept", []string{"a\n\nb\n"}, []string{"a", "", "b"}},
		{"sgr stripped", []string{"\x1b[01;34mdir\x1b[0m  \x1b[32mfile\x1b[0m\n"}, []string{"dir  file"}},
		{"csi split across writes", []string{"\x1b[3", "2mgreen\x1b[0m\n"}, []string{"green"}},
		{"osc title stripped", []string{"\x1b]0;root@host: ~\x07# ls\n"}, []string{"# ls"}},
		{"osc esc-backslash", []string{"\x1b]0;t\x1b\\x\n"}, []string{"x"}},
		{"control bytes dropped, tab kept", []string{"a\x07b\x08c\td\n"}, []string{"abc\td"}},
		{"two-byte esc", []string{"\x1bcreset\n"}, []string{"reset"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewLogWriter(&buf, fixedClock())
			for _, s := range c.in {
				if _, err := w.Write([]byte(s)); err != nil {
					t.Fatal(err)
				}
			}
			w.Close()
			var got []string
			for _, l := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
				if l == "" {
					continue
				}
				i := strings.Index(l, "] ")
				if !strings.HasPrefix(l, "[2026-09-19 14:03:") || i < 0 {
					t.Fatalf("line lacks timestamp prefix: %q", l)
				}
				got = append(got, l[i+2:])
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestLogWriter_TimestampIsLineStart(t *testing.T) {
	var buf bytes.Buffer
	w := NewLogWriter(&buf, fixedClock()) // clock ticks one second per call
	w.Write([]byte("a"))                  // first byte: 14:03:22
	w.Write([]byte("b\n"))                // newline: no clock call
	w.Close()
	if !strings.HasPrefix(buf.String(), "[2026-09-19 14:03:22.123] ab\n") {
		t.Errorf("got %q", buf.String())
	}
}

// syncBuffer is a bytes.Buffer safe to poll from the test while the idle
// timer writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestLogWriter_IdleFlush(t *testing.T) {
	var buf syncBuffer
	w := NewLogWriter(&buf, fixedClock())
	w.Write([]byte("login: "))
	if buf.String() != "" {
		t.Fatalf("partial line flushed too early: %q", buf.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for buf.String() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	w.Close()
	if !strings.HasSuffix(buf.String(), "] login: \n") {
		t.Errorf("idle flush: got %q", buf.String())
	}
}

func TestLogWriter_Marker(t *testing.T) {
	var buf bytes.Buffer
	w := NewLogWriter(&buf, fixedClock())
	w.Write([]byte("partial"))
	w.Marker("serial capture started: rpi5 /dev/ttyUSB0 115200")
	w.Close()
	want := "[2026-09-19 14:03:22.123] partial\n[2026-09-19 14:03:23.123] --- serial capture started: rpi5 /dev/ttyUSB0 115200 ---\n"
	if buf.String() != want {
		t.Errorf("got %q\nwant %q", buf.String(), want)
	}
}
