// Package httputil holds the HTTP client yoe uses to download things from
// the network: source archives, prebuilt packages, distro feed indexes, and
// yoe's own release binaries.
//
// Everything here fetches whole files as opaque bytes, so the client is
// configured for that one job rather than for general web traffic. Two
// settings differ from Go's default, and both matter for correctness or
// reliability rather than for speed.
//
// # HTTP/1.1 only
//
// Downloading one large file per request gains nothing from HTTP/2
// multiplexing, and sometimes transfers more slowly under it. More
// importantly, some CDN endpoints reset the stream instead of serving the
// file: kernel.org has answered PROTOCOL_ERROR on stream 1 for every attempt
// from an affected network path, while the same URL over HTTP/1.1 downloads
// normally. Retrying cannot recover from that, since each attempt opens a
// fresh connection and negotiates the same broken protocol, so the transport
// avoids the protocol rather than the callers working around it.
//
// This also matches how other build systems fetch sources. BitBake and
// Buildroot both shell out to wget, which speaks only HTTP/1.1, so neither
// has ever negotiated HTTP/2 for a download.
//
// # No transparent decompression
//
// Go's default transport advertises `Accept-Encoding: gzip` and then inflates
// the response before the caller sees it. That corrupts every download yoe
// makes, because yoe wants the bytes the server was asked for:
//
//   - Savannah and some of its mirrors (nongnu.askapache.com does, and
//     mirrors.ocf.berkeley.edu does not, with savannah's redirector choosing
//     between them per request) serve a `.tar.gz` with `Content-Encoding:
//     gzip`. A decoded body leaves a bare tar on disk under a `.tar.gz` name,
//     and extraction later picks gzip by extension and fails with "gzip:
//     invalid header".
//   - Checksums are the more important case: a decoded body hashes to
//     something other than what upstream published, so a unit's sha256 would
//     match on one mirror and fail on the next.
//   - Feed indexes are verified by signature over the compressed bytes, and
//     decompressed explicitly afterward.
//
// Nothing is lost by it. Archives, packages, and feed indexes are already
// compressed, so gzip transfer encoding would not shrink them.
//
// # Stall detection instead of a timeout
//
// A connection that is established and then goes silent would otherwise hang
// a build forever: the read never returns, so the retry logic above it never
// runs. The obvious fix, http.Client.Timeout, is the wrong one — it covers
// reading the body, so it caps total transfer time and would abandon a large
// archive that is downloading normally from a slow mirror.
//
// Instead each stage that should be quick regardless of file size gets its
// own bound (dial, TLS handshake, response headers), and the body is watched
// for progress: the transfer is abandoned only after it delivers no bytes at
// all for StallTimeout. A slow download runs as long as it needs to; a dead
// one fails quickly enough to be retried.
package httputil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Timeouts bounding each stage of a download. None of them cap how long a
// transfer may take in total: source archives run to hundreds of megabytes
// and mirrors are often slow but healthy, so a total deadline would abandon
// good downloads. Each of these bounds a stage that should never take long
// no matter how large the file is.
const (
	// DialTimeout bounds establishing the TCP connection.
	DialTimeout = 30 * time.Second

	// TLSTimeout bounds the TLS handshake.
	TLSTimeout = 15 * time.Second

	// ResponseHeaderTimeout bounds the wait between sending the request and
	// receiving response headers. A mirror that accepts the connection and
	// then never answers is caught here.
	ResponseHeaderTimeout = 60 * time.Second

	// StallTimeout is how long a response body may go without delivering a
	// single byte before the transfer is abandoned. This is deliberately not
	// a limit on the transfer's duration — a download making steady progress
	// runs as long as it needs to.
	StallTimeout = 60 * time.Second
)

// ErrStalled reports a connection that was established and then went silent.
// It is distinct from a timeout on the request as a whole, which yoe does not
// impose.
var ErrStalled = errors.New("connection stalled")

// Client downloads files over HTTP/1.1 without transparent decompression,
// abandoning a transfer that stops making progress. It is safe for concurrent
// use, and callers should share it rather than constructing their own so that
// connection reuse and the settings above apply to every download yoe makes.
var Client = &http.Client{Transport: NewTransport()}

// NewTransport returns a transport configured the way Client's is. Use it
// when a caller needs its own client and still wants yoe's download settings.
//
// Do not set Timeout on a client built from this. http.Client.Timeout covers
// reading the body, so it caps total transfer time and kills large downloads
// that are progressing normally. The stall detection here is what a timeout
// would be reaching for, without that failure mode.
func NewTransport() http.RoundTripper {
	return &stallTransport{base: newBaseTransport(), timeout: StallTimeout}
}

func newBaseTransport() *http.Transport {
	// Left unset, a transport negotiates HTTP/2 over TLS by default.
	var protocols http.Protocols
	protocols.SetHTTP1(true)

	return &http.Transport{
		DisableCompression:    true,
		Protocols:             &protocols,
		DialContext:           (&net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   TLSTimeout,
		ResponseHeaderTimeout: ResponseHeaderTimeout,
		IdleConnTimeout:       90 * time.Second,
	}
}

// stallTransport wraps every response body in a watchdog. Doing this at the
// transport rather than at each call site means a caller cannot forget it,
// and clients built from NewTransport get it without threading a context
// through their own API.
type stallTransport struct {
	base    *http.Transport
	timeout time.Duration
}

func (t *stallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Derive a cancelable context so the watchdog can abort a read that is
	// already blocked in the transport. Canceling the request is the only
	// thing that unblocks it; closing the body would deadlock behind the
	// same read.
	ctx, cancel := context.WithCancel(req.Context())

	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}

	resp.Body = newStallGuard(resp.Body, cancel, t.timeout)
	return resp, nil
}

// stallGuard fails a read when the body delivers nothing for timeout. The
// clock restarts on every byte received, so a slow transfer is fine and only
// a silent one is abandoned.
type stallGuard struct {
	rc      io.ReadCloser
	cancel  context.CancelFunc
	timeout time.Duration
	timer   *time.Timer
	fired   atomic.Bool
}

func newStallGuard(rc io.ReadCloser, cancel context.CancelFunc, timeout time.Duration) *stallGuard {
	g := &stallGuard{rc: rc, cancel: cancel, timeout: timeout}
	g.timer = time.AfterFunc(timeout, func() {
		g.fired.Store(true)
		cancel()
	})
	return g
}

func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.rc.Read(p)
	if n > 0 {
		g.timer.Reset(g.timeout)
	}
	if err != nil && err != io.EOF && g.fired.Load() {
		// The read failed because the watchdog canceled the request. Report
		// that, rather than the "context canceled" the transport surfaces,
		// which reads like yoe gave up for no reason.
		return n, fmt.Errorf("%w: no data received for %s", ErrStalled, g.timeout)
	}
	return n, err
}

func (g *stallGuard) Close() error {
	g.timer.Stop()
	err := g.rc.Close()
	g.cancel()
	return err
}
