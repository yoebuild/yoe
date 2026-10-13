package serial

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// StateKind is where a Session stands with its adapter.
type StateKind int

const (
	// StateWaiting: the adapter is not present (or was unplugged); the
	// session polls for it.
	StateWaiting StateKind = iota
	// StateConnected: the adapter is open and bytes are flowing.
	StateConnected
	// StateClosed: the session has been closed.
	StateClosed
)

func (k StateKind) String() string {
	switch k {
	case StateWaiting:
		return "waiting"
	case StateConnected:
		return "connected"
	case StateClosed:
		return "closed"
	}
	return "unknown"
}

// State is a snapshot of a Session.
type State struct {
	Kind StateKind
	// Path is the /dev/tty* node in use while connected, else "".
	Path string
	// LastErr is the most recent open or read error, kept while waiting so
	// the UI can say why (permission denied is the common one).
	LastErr string
	// Clients is how many processes are attached through the socket or
	// the TCP listener.
	Clients int
	// Dropped counts output chunks discarded because a subscriber fell
	// behind.
	Dropped int
}

// Config describes the console a Session should capture.
type Config struct {
	Machine string
	// Mapped is the local.star value: a /dev/serial/by-id/ link or a
	// /dev/tty* path.
	Mapped string
	Baud   int
	// Dir is where the log and socket live, normally <project>/build/serial.
	Dir string
	// Listen is an optional TCP address that also serves the raw stream.
	Listen string

	// Test hooks. nil selects the real implementation.
	open         func(path string, baud int) (Transport, error)
	list         func() ([]Port, error)
	pollInterval time.Duration
}

// LogPath is the log file for a machine under dir.
func LogPath(dir, machine string) string {
	return filepath.Join(dir, machine+".log")
}

// SocketPath is the Unix socket for a machine under dir.
func SocketPath(dir, machine string) string {
	return filepath.Join(dir, machine+".sock")
}

// ErrSessionExists is returned by Open when another process already owns
// the machine's socket. Attach to it instead.
var ErrSessionExists = errors.New("serial: another yoe process already owns this console")

// ErrNotConnected is returned by Write and Break while the adapter is
// absent.
var ErrNotConnected = errors.New("serial: adapter not connected")

// Session owns an adapter: it captures the console to the log, fans the
// raw stream out to subscribers, and serves it on a socket for other
// processes. It reconnects on its own when the adapter comes and goes.
type Session struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	logFile *os.File
	log     *LogWriter

	mu        sync.Mutex
	transport Transport
	state     State
	rawSubs   map[int]chan []byte
	lineSubs  map[int]chan string
	nextSub   int
	listeners []net.Listener
	conns     map[net.Conn]struct{}

	wg   sync.WaitGroup
	done chan struct{}
}

// rawSubBuffer and lineSubBuffer bound how far a subscriber may fall
// behind before chunks are dropped rather than stalling the reader.
const (
	rawSubBuffer  = 256
	lineSubBuffer = 1024
)

// Open starts capturing. It returns ErrSessionExists when the socket is
// already served by a live process. The adapter being absent is not an
// error: the session starts in StateWaiting and connects when it appears.
func Open(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Machine == "" || cfg.Mapped == "" || cfg.Baud <= 0 || cfg.Dir == "" {
		return nil, fmt.Errorf("serial: Config needs Machine, Mapped, Baud, and Dir")
	}
	if cfg.open == nil {
		cfg.open = OpenPort
	}
	if cfg.list == nil {
		cfg.list = List
	}
	if cfg.pollInterval == 0 {
		cfg.pollInterval = 2 * time.Second
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}

	sockPath := SocketPath(cfg.Dir, cfg.Machine)
	ln, err := listenUnix(sockPath)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(LogPath(cfg.Dir, cfg.Machine), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("serial: %w", err)
	}

	sctx, cancel := context.WithCancel(ctx)
	s := &Session{
		cfg:      cfg,
		ctx:      sctx,
		cancel:   cancel,
		logFile:  f,
		rawSubs:  map[int]chan []byte{},
		lineSubs: map[int]chan string{},
		conns:    map[net.Conn]struct{}{},
		done:     make(chan struct{}),
	}
	s.log = NewLogWriter(io.MultiWriter(f, lineFanout{s}), time.Now)
	s.listeners = append(s.listeners, ln)

	if cfg.Listen != "" {
		tln, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("serial: listen %s: %w", cfg.Listen, err)
		}
		s.listeners = append(s.listeners, tln)
	}

	for _, l := range s.listeners {
		s.wg.Add(1)
		go s.serve(l)
	}
	s.wg.Add(1)
	go s.run()
	go func() {
		s.wg.Wait()
		close(s.done)
	}()
	return s, nil
}

// listenUnix listens on path. A leftover socket file from a crashed owner
// is removed only after a dial proves nothing answers behind it.
func listenUnix(path string) (net.Listener, error) {
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			c.Close()
			return nil, ErrSessionExists
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("serial: remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("serial: listen %s: %w", path, err)
	}
	return ln, nil
}

// run is the adapter loop: resolve the mapping, open, copy until the read
// fails, then wait for the adapter to come back.
func (s *Session) run() {
	defer s.wg.Done()
	for {
		path, t, err := s.connect()
		if err != nil {
			s.setWaiting(err)
			if !s.sleep() {
				return
			}
			continue
		}
		s.setConnected(path, t)
		_ = s.log.Marker(fmt.Sprintf("serial capture started: %s %s %s %d", s.cfg.Machine, s.cfg.Mapped, path, s.cfg.Baud))
		err = s.copyLoop(t)
		s.dropTransport(t)
		if s.ctx.Err() != nil {
			return
		}
		_ = s.log.Marker(fmt.Sprintf("serial adapter disconnected: %v", err))
		s.setWaiting(err)
		if !s.sleep() {
			return
		}
	}
}

func (s *Session) sleep() bool {
	select {
	case <-s.ctx.Done():
		return false
	case <-time.After(s.cfg.pollInterval):
		return true
	}
}

// connect resolves the mapping to a current path and opens it.
func (s *Session) connect() (string, Transport, error) {
	ports, err := s.cfg.list()
	if err != nil {
		return "", nil, err
	}
	p, ok := Match(ports, s.cfg.Mapped)
	if !ok {
		return "", nil, fmt.Errorf("adapter %s not present", s.cfg.Mapped)
	}
	t, err := s.cfg.open(p.Path, s.cfg.Baud)
	if err != nil {
		return "", nil, err
	}
	return p.Path, t, nil
}

func (s *Session) copyLoop(t Transport) error {
	buf := make([]byte, 4096)
	for {
		n, err := t.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			_, _ = s.log.Write(chunk)
			s.fanoutRaw(chunk)
		}
		if err != nil {
			return err
		}
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
	}
}

func (s *Session) setConnected(path string, t Transport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transport = t
	s.state.Kind = StateConnected
	s.state.Path = path
	s.state.LastErr = ""
}

func (s *Session) setWaiting(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Kind == StateClosed {
		return
	}
	s.state.Kind = StateWaiting
	s.state.Path = ""
	if err != nil {
		s.state.LastErr = err.Error()
	}
}

func (s *Session) dropTransport(t Transport) {
	s.mu.Lock()
	if s.transport == t {
		s.transport = nil
	}
	s.mu.Unlock()
	t.Close()
}

func (s *Session) fanoutRaw(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.rawSubs {
		select {
		case ch <- chunk:
		default:
			s.state.Dropped++
		}
	}
}

// lineFanout delivers each formatted log line to the line subscribers.
type lineFanout struct{ s *Session }

func (f lineFanout) Write(p []byte) (int, error) {
	line := strings.TrimSuffix(string(p), "\n")
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	for _, ch := range f.s.lineSubs {
		select {
		case ch <- line:
		default:
			f.s.state.Dropped++
		}
	}
	return len(p), nil
}

// State returns a snapshot.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Config returns the configuration the session was opened with.
func (s *Session) Config() Config { return s.cfg }

// Subscribe returns a channel of raw output chunks. Call cancel to stop;
// the channel closes when the session closes.
func (s *Session) Subscribe() (<-chan []byte, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSub
	s.nextSub++
	ch := make(chan []byte, rawSubBuffer)
	if s.state.Kind == StateClosed {
		close(ch)
		return ch, func() {}
	}
	s.rawSubs[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.rawSubs[id]; ok {
			delete(s.rawSubs, id)
			close(c)
		}
	}
}

// SubscribeLines returns a channel of the formatted lines written to the
// log (timestamped, escape-stripped): what the TUI's Serial tab shows.
func (s *Session) SubscribeLines() (<-chan string, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSub
	s.nextSub++
	ch := make(chan string, lineSubBuffer)
	if s.state.Kind == StateClosed {
		close(ch)
		return ch, func() {}
	}
	s.lineSubs[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.lineSubs[id]; ok {
			delete(s.lineSubs, id)
			close(c)
		}
	}
}

// Write sends bytes to the adapter.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	t := s.transport
	s.mu.Unlock()
	if t == nil {
		return 0, ErrNotConnected
	}
	return t.Write(p)
}

// Break sends a break on the adapter.
func (s *Session) Break() error {
	s.mu.Lock()
	t := s.transport
	s.mu.Unlock()
	if t == nil {
		return ErrNotConnected
	}
	return t.Break()
}

// Detach is a no-op on the owner: the session outlives an interactive
// attachment. It exists so a Session satisfies Console.
func (s *Session) Detach() error { return nil }

// serve accepts socket or TCP clients. Each client subscribes to the raw
// stream and its input goes to the adapter.
func (s *Session) serve(ln net.Listener) {
	defer s.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		// Subscribe before the client is counted, so a caller that waits
		// on State().Clients cannot write a chunk this client would miss.
		out, cancel := s.Subscribe()
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.state.Clients++
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serveConn(c, out, cancel)
	}
}

func (s *Session) serveConn(c net.Conn, out <-chan []byte, cancel func()) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.state.Clients--
		s.mu.Unlock()
		c.Close()
	}()
	defer cancel()

	readErr := make(chan struct{})
	go func() {
		defer close(readErr)
		buf := make([]byte, 1024)
		for {
			n, err := c.Read(buf)
			if n > 0 {
				// A write while the adapter is absent is dropped; the
				// client sees the state through the log markers.
				_, _ = s.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case chunk, ok := <-out:
			if !ok {
				return
			}
			if _, err := c.Write(chunk); err != nil {
				return
			}
		case <-readErr:
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// Close stops capture, closes clients and listeners, flushes the log, and
// removes the socket file.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.state.Kind == StateClosed {
		s.mu.Unlock()
		return nil
	}
	s.state.Kind = StateClosed
	s.state.Path = ""
	t := s.transport
	s.transport = nil
	for _, l := range s.listeners {
		l.Close()
	}
	for c := range s.conns {
		c.Close()
	}
	for id, ch := range s.rawSubs {
		delete(s.rawSubs, id)
		close(ch)
	}
	for id, ch := range s.lineSubs {
		delete(s.lineSubs, id)
		close(ch)
	}
	s.mu.Unlock()

	s.cancel()
	if t != nil {
		t.Close() // unblocks the reader
	}
	<-s.done
	err := s.log.Close()
	if cerr := s.logFile.Close(); err == nil {
		err = cerr
	}
	os.Remove(SocketPath(s.cfg.Dir, s.cfg.Machine))
	return err
}
