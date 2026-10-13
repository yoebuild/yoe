package serial

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTransport is one end of a net.Pipe; the test holds the other end
// and plays the board.
type fakeTransport struct {
	net.Conn
	breaks int
	mu     sync.Mutex
}

func (f *fakeTransport) Break() error {
	f.mu.Lock()
	f.breaks++
	f.mu.Unlock()
	return nil
}

// fakeAdapter wires Config test hooks so the "adapter" is present when
// present() is true and each open hands out a fresh pipe.
type fakeAdapter struct {
	mu      sync.Mutex
	present bool
	opens   int
	board   net.Conn // the board side of the current pipe
	boardCh chan net.Conn
}

func newFakeAdapter(present bool) *fakeAdapter {
	return &fakeAdapter{present: present, boardCh: make(chan net.Conn, 4)}
}

func (a *fakeAdapter) list() ([]Port, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.present {
		return nil, nil
	}
	return []Port{{ByID: "/dev/serial/by-id/usb-Fake_1-if00", Path: "/dev/ttyFAKE0"}}, nil
}

func (a *fakeAdapter) open(path string, baud int) (Transport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opens++
	yoeSide, boardSide := net.Pipe()
	a.board = boardSide
	a.boardCh <- boardSide
	return &fakeTransport{Conn: yoeSide}, nil
}

func (a *fakeAdapter) setPresent(p bool) {
	a.mu.Lock()
	a.present = p
	a.mu.Unlock()
}

func testConfig(t *testing.T, a *fakeAdapter) Config {
	t.Helper()
	return Config{
		Machine:      "rpi5",
		Mapped:       "/dev/serial/by-id/usb-Fake_1-if00",
		Baud:         115200,
		Dir:          t.TempDir(),
		open:         a.open,
		list:         a.list,
		pollInterval: 20 * time.Millisecond,
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readChunk(t *testing.T, ch <-chan []byte) string {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return string(c)
	case <-time.After(2 * time.Second):
		t.Fatal("no chunk")
	}
	return ""
}

func TestSession_CapturesToLogAndSubscribers(t *testing.T) {
	a := newFakeAdapter(true)
	cfg := testConfig(t, a)
	s, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	board := <-a.boardCh
	waitFor(t, "connected", func() bool { return s.State().Kind == StateConnected })
	if st := s.State(); st.Path != "/dev/ttyFAKE0" {
		t.Errorf("state path = %q", st.Path)
	}

	raw1, cancel1 := s.Subscribe()
	defer cancel1()
	raw2, cancel2 := s.Subscribe()
	defer cancel2()
	lines, cancelL := s.SubscribeLines()
	defer cancelL()

	go board.Write([]byte("\x1b[32mlogin:\x1b[0m root\r\n"))
	if got := readChunk(t, raw1); !strings.Contains(got, "login:") {
		t.Errorf("raw1 got %q", got)
	}
	if got := readChunk(t, raw2); !strings.Contains(got, "login:") {
		t.Errorf("raw2 got %q", got)
	}
	// The start marker may still be in flight; skip marker lines.
	for {
		select {
		case l := <-lines:
			if strings.Contains(l, "] --- ") {
				continue
			}
			if !strings.HasSuffix(l, "] login: root") {
				t.Errorf("line = %q", l)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no line")
		}
		break
	}

	// Input reaches the board.
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := board.Read(buf)
		got <- string(buf[:n])
	}()
	if _, err := s.Write([]byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	if g := <-got; g != "ls\n" {
		t.Errorf("board received %q", g)
	}

	s.Close()
	data, _ := os.ReadFile(LogPath(cfg.Dir, "rpi5"))
	log := string(data)
	if !strings.Contains(log, "--- serial capture started: rpi5 /dev/serial/by-id/usb-Fake_1-if00 /dev/ttyFAKE0 115200 ---") {
		t.Errorf("log lacks start marker: %q", log)
	}
	if !strings.Contains(log, "] login: root\n") {
		t.Errorf("log lacks stripped line: %q", log)
	}
	if _, err := os.Stat(SocketPath(cfg.Dir, "rpi5")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket not removed on close: %v", err)
	}
	if s.State().Kind != StateClosed {
		t.Errorf("state after close = %v", s.State().Kind)
	}
}

func TestSession_WaitsThenReconnects(t *testing.T) {
	a := newFakeAdapter(false)
	cfg := testConfig(t, a)
	s, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	waitFor(t, "waiting state", func() bool {
		st := s.State()
		return st.Kind == StateWaiting && strings.Contains(st.LastErr, "not present")
	})
	if _, err := s.Write([]byte("x")); !errors.Is(err, ErrNotConnected) {
		t.Errorf("write while waiting: %v", err)
	}

	a.setPresent(true)
	board := <-a.boardCh
	waitFor(t, "connected", func() bool { return s.State().Kind == StateConnected })

	// Unplug: the board side closes, the read fails, the session waits.
	a.setPresent(false)
	board.Close()
	waitFor(t, "waiting after unplug", func() bool { return s.State().Kind == StateWaiting })

	// Replug.
	a.setPresent(true)
	<-a.boardCh
	waitFor(t, "reconnected", func() bool { return s.State().Kind == StateConnected })
	if a.opens != 2 {
		t.Errorf("opens = %d, want 2", a.opens)
	}

	s.Close()
	data, _ := os.ReadFile(LogPath(cfg.Dir, "rpi5"))
	if c := strings.Count(string(data), "--- serial capture started"); c != 2 {
		t.Errorf("start markers = %d, want 2:\n%s", c, data)
	}
	if !strings.Contains(string(data), "--- serial adapter disconnected") {
		t.Errorf("no disconnect marker:\n%s", data)
	}
}

func TestSession_SocketOwnerAndClient(t *testing.T) {
	a := newFakeAdapter(true)
	cfg := testConfig(t, a)
	ctx := context.Background()

	owner, isOwner, err := OpenOrAttach(ctx, cfg)
	if err != nil || !isOwner {
		t.Fatalf("first OpenOrAttach: owner=%v err=%v", isOwner, err)
	}
	s := owner.(*Session)
	defer s.Close()
	board := <-a.boardCh
	waitFor(t, "connected", func() bool { return s.State().Kind == StateConnected })

	// A second Open on the same socket is refused; OpenOrAttach attaches.
	if _, err := Open(ctx, cfg); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("second Open err = %v, want ErrSessionExists", err)
	}
	client, isOwner, err := OpenOrAttach(ctx, cfg)
	if err != nil || isOwner {
		t.Fatalf("second OpenOrAttach: owner=%v err=%v", isOwner, err)
	}
	waitFor(t, "client counted", func() bool { return s.State().Clients == 1 })

	out, cancel := client.Subscribe()
	defer cancel()
	go board.Write([]byte("hello from board\n"))
	if got := readChunk(t, out); got != "hello from board\n" {
		t.Errorf("client got %q", got)
	}

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := board.Read(buf)
		got <- string(buf[:n])
	}()
	if _, err := client.Write([]byte("uname\n")); err != nil {
		t.Fatal(err)
	}
	if g := <-got; g != "uname\n" {
		t.Errorf("board received %q from client", g)
	}
	if err := client.Break(); !errors.Is(err, ErrBreakUnavailable) {
		t.Errorf("client Break err = %v", err)
	}
	if err := s.Break(); err != nil {
		t.Errorf("owner Break err = %v", err)
	}

	// Owner goes away: the client's output channel closes.
	s.Close()
	select {
	case _, ok := <-out:
		if ok {
			t.Error("expected closed channel after owner exit")
		}
	case <-time.After(2 * time.Second):
		t.Error("client channel did not close after owner exit")
	}
	client.Detach()
}

func TestSession_StaleSocketIsReplaced(t *testing.T) {
	a := newFakeAdapter(true)
	cfg := testConfig(t, a)
	stale := SocketPath(cfg.Dir, "rpi5")
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A socket file nobody listens on, as a crashed owner leaves behind.
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if _, err := os.Stat(stale); err == nil {
		// Go removes the file on Close; recreate a plain stand-in.
		t.Fatal("expected Close to unlink; adjust test")
	}
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open over stale socket: %v", err)
	}
	<-a.boardCh
	s.Close()
	if _, err := os.Stat(filepath.Dir(stale)); err != nil {
		t.Fatal(err)
	}
}
