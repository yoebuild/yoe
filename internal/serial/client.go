package serial

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// Console is what an interactive attachment or a one-shot send talks to:
// the owning Session in the process that opened the adapter, or a Client
// attached to that process's socket.
type Console interface {
	Write(p []byte) (int, error)
	Break() error
	// Subscribe returns the console's output; the channel closes when the
	// console goes away.
	Subscribe() (<-chan []byte, func())
	// Detach releases this attachment. On a Session it is a no-op: the
	// session keeps capturing.
	Detach() error
}

// ErrBreakUnavailable is returned by a Client's Break: the socket carries
// bytes only, and a break is a line condition only the owner can raise.
var ErrBreakUnavailable = errors.New("serial: break unavailable through the socket; run yoe serial where the console is owned")

// Client is an attachment to another process's Session over its socket.
type Client struct {
	conn net.Conn
	mu   sync.Mutex
	subs map[int]chan []byte
	next int
	done chan struct{}
}

// Attach connects to the socket a Session serves.
func Attach(ctx context.Context, sockPath string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, subs: map[int]chan []byte{}, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	defer close(c.done)
	buf := make([]byte, 4096)
	for {
		n, err := c.conn.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			c.mu.Lock()
			for _, ch := range c.subs {
				select {
				case ch <- chunk:
				default:
				}
			}
			c.mu.Unlock()
		}
		if err != nil {
			c.mu.Lock()
			for id, ch := range c.subs {
				delete(c.subs, id)
				close(ch)
			}
			c.mu.Unlock()
			return
		}
	}
}

// Write sends bytes to the adapter through the owner.
func (c *Client) Write(p []byte) (int, error) { return c.conn.Write(p) }

// Break is unavailable on a client.
func (c *Client) Break() error { return ErrBreakUnavailable }

// Subscribe returns the console output relayed by the owner.
func (c *Client) Subscribe() (<-chan []byte, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.next
	c.next++
	ch := make(chan []byte, rawSubBuffer)
	select {
	case <-c.done:
		close(ch)
		return ch, func() {}
	default:
	}
	c.subs[id] = ch
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if s, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(s)
		}
	}
}

// Detach closes the connection.
func (c *Client) Detach() error {
	err := c.conn.Close()
	select {
	case <-c.done:
	case <-time.After(time.Second):
	}
	return err
}

// OpenOrAttach attaches to a running session for cfg.Machine when one
// exists, else opens the adapter and becomes the owner. The bool reports
// which happened, so the caller knows whether Close is its job.
func OpenOrAttach(ctx context.Context, cfg Config) (Console, bool, error) {
	if c, err := Attach(ctx, SocketPath(cfg.Dir, cfg.Machine)); err == nil {
		return c, false, nil
	}
	s, err := Open(ctx, cfg)
	if err != nil {
		if errors.Is(err, ErrSessionExists) {
			// Lost the race with another opener; attach to it.
			c, aerr := Attach(ctx, SocketPath(cfg.Dir, cfg.Machine))
			if aerr == nil {
				return c, false, nil
			}
		}
		return nil, false, err
	}
	return s, true, nil
}
