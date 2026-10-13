package serial

import (
	"fmt"
	"io"
	"time"

	bugst "go.bug.st/serial"
)

// Transport is an open serial adapter. The Session owns one; everything
// else reaches the adapter through the Session.
type Transport interface {
	io.ReadWriteCloser
	// Break holds the line in break state for a moment, the way a terminal
	// program's "send break" does.
	Break() error
}

// breakDuration is how long Break asserts the line. 250 ms is what tio and
// most terminal programs use; it is long enough for every UART to notice.
const breakDuration = 250 * time.Millisecond

// OpenPort opens the adapter at path at the given speed, 8N1 with no flow
// control, which is what every board console yoe knows of expects.
func OpenPort(path string, baud int) (Transport, error) {
	if baud <= 0 {
		return nil, fmt.Errorf("serial: baud must be positive, got %d", baud)
	}
	p, err := bugst.Open(path, &bugst.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   bugst.NoParity,
		StopBits: bugst.OneStopBit,
	})
	if err != nil {
		return nil, fmt.Errorf("serial: open %s: %w", path, err)
	}
	return &port{p}, nil
}

type port struct{ bugst.Port }

func (p *port) Break() error { return p.Port.Break(breakDuration) }
