package serial

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func kinds(as []action) string {
	var parts []string
	for _, a := range as {
		switch a.kind {
		case actSend:
			parts = append(parts, "send:"+string(a.data))
		case actQuit:
			parts = append(parts, "quit")
		case actBreak:
			parts = append(parts, "break")
		case actToggleTimestamps:
			parts = append(parts, "ts")
		case actHelp:
			parts = append(parts, "help")
		}
	}
	return strings.Join(parts, " ")
}

func TestChordParser(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"ls\r"}, "send:ls\r"},
		{[]string{"\x14q"}, "quit"},
		{[]string{"ab\x14bcd"}, "send:ab break send:cd"},
		{[]string{"\x14", "t"}, "ts"},       // prefix and command in separate reads
		{[]string{"\x14\x14"}, "send:\x14"}, // literal ctrl-t
		{[]string{"\x14?x"}, "help send:x"},
		{[]string{"\x14zq"}, "send:q"}, // unknown chord ignored, next byte plain
	}
	for _, c := range cases {
		var p chordParser
		var all []action
		for _, s := range c.in {
			all = append(all, p.feed([]byte(s))...)
		}
		if got := kinds(all); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

// memConsole is a Console backed by buffers for exercising Interactive.
type memConsole struct {
	written bytes.Buffer
	out     chan []byte
	breaks  int
}

func (m *memConsole) Write(p []byte) (int, error) { return m.written.Write(p) }
func (m *memConsole) Break() error                { m.breaks++; return nil }
func (m *memConsole) Subscribe() (<-chan []byte, func()) {
	return m.out, func() {}
}
func (m *memConsole) Detach() error { return nil }

func TestInteractive_DetachAndPassthrough(t *testing.T) {
	c := &memConsole{out: make(chan []byte, 4)}
	c.out <- []byte("U-Boot 2026.01\r\n")
	in := strings.NewReader("help\r\x14b\x14q")
	var out bytes.Buffer
	err := Interactive(context.Background(), c, in, &out, InteractiveOptions{})
	if err != nil {
		t.Fatalf("Interactive: %v", err)
	}
	if got := c.written.String(); got != "help\r" {
		t.Errorf("console received %q", got)
	}
	if c.breaks != 1 {
		t.Errorf("breaks = %d", c.breaks)
	}
	if !strings.Contains(out.String(), "U-Boot 2026.01") || !strings.Contains(out.String(), "detached") {
		t.Errorf("terminal saw %q", out.String())
	}
}

func TestInteractive_SessionEnded(t *testing.T) {
	c := &memConsole{out: make(chan []byte)}
	pr, pw := io.Pipe() // stdin that never delivers a quit chord
	defer pw.Close()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Interactive(context.Background(), c, pr, &out, InteractiveOptions{}) }()
	close(c.out)
	select {
	case err := <-done:
		if err != ErrSessionEnded {
			t.Fatalf("err = %v, want ErrSessionEnded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Interactive did not return when the console closed")
	}
}

func TestInteractive_Timestamps(t *testing.T) {
	c := &memConsole{out: make(chan []byte, 4)}
	c.out <- []byte("a\nb")
	now := func() time.Time { return time.Date(2026, 9, 19, 1, 2, 3, 0, time.UTC) }
	var out bytes.Buffer
	in := strings.NewReader("\x14q")
	// Give the output a head start so both chunks are drawn before quit.
	err := Interactive(context.Background(), c, in, &out, InteractiveOptions{Timestamps: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[01:02:03.000] a\n[01:02:03.000] b") {
		t.Errorf("got %q", out.String())
	}
}
