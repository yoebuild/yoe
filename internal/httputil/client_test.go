package httputil

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Downloads must not negotiate HTTP/2. Some CDN endpoints reset the stream
// rather than serving the file, and retrying cannot recover because every
// attempt opens a fresh connection and negotiates the same protocol.
//
// The invariant lives in the transport's configuration, so assert on it
// directly. An end-to-end fetch cannot check this: supplying a test server's
// TLSClientConfig suppresses HTTP/2 on its own, and the assertion would pass
// even if the production setting were dropped.
func TestTransportDisablesHTTP2(t *testing.T) {
	tr := newBaseTransport()
	if tr.Protocols == nil {
		t.Fatal("Protocols is nil, so the transport negotiates HTTP/2 over TLS by default")
	}
	if !tr.Protocols.HTTP1() {
		t.Error("HTTP/1.1 is not enabled; downloads have no protocol to use")
	}
	if tr.Protocols.HTTP2() {
		t.Error("HTTP/2 is enabled; downloads must stay on HTTP/1.1")
	}
	if tr.Protocols.UnencryptedHTTP2() {
		t.Error("unencrypted HTTP/2 is enabled; downloads must stay on HTTP/1.1")
	}
}

// Every stage that should be quick regardless of file size needs its own
// bound, or a dead connection hangs the build with no retry ever running.
func TestTransportBoundsEachStage(t *testing.T) {
	tr := newBaseTransport()
	if tr.DialContext == nil {
		t.Error("DialContext is nil, so connecting to a black-holed host never times out")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Error("TLSHandshakeTimeout is unset")
	}
	if tr.ResponseHeaderTimeout == 0 {
		t.Error("ResponseHeaderTimeout is unset; a server that accepts and never answers hangs the build")
	}
}

// A total timeout would cap transfer time and abandon large archives that are
// downloading normally, which is the failure mode stall detection exists to
// avoid. Guard against someone "fixing" a hang by reaching for the obvious knob.
func TestClientHasNoTotalTimeout(t *testing.T) {
	if Client.Timeout != 0 {
		t.Errorf("Client.Timeout is %s; a total deadline kills large downloads that are progressing",
			Client.Timeout)
	}
}

// Client is the one every caller shares, so it must carry the download
// settings rather than Go's defaults.
func TestClientUsesStallTransport(t *testing.T) {
	st, ok := Client.Transport.(*stallTransport)
	if !ok {
		t.Fatalf("Client.Transport is %T, want *stallTransport", Client.Transport)
	}
	if st.timeout != StallTimeout {
		t.Errorf("stall timeout = %s, want %s", st.timeout, StallTimeout)
	}
	if !st.base.DisableCompression {
		t.Error("DisableCompression is unset; responses would be inflated before the caller sees them")
	}
}

// The client must ask for the bytes as stored, not a gzip-encoded body it
// would then inflate. A server that compresses only when asked shows which
// one the caller receives.
func TestClientDoesNotRequestGzip(t *testing.T) {
	const body = "the bytes as stored"

	var sawAcceptEncoding string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAcceptEncoding = r.Header.Get("Accept-Encoding")
		io.WriteString(w, body)
	}))
	defer srv.Close()

	resp, err := Client.Get(srv.URL + "/archive.tar.gz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if sawAcceptEncoding != "" {
		t.Errorf("server saw Accept-Encoding %q, want the header absent", sawAcceptEncoding)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
}

// newTestClient builds a client with the production wiring but a stall window
// short enough to hit in a test.
func newTestClient(stall time.Duration) *http.Client {
	return &http.Client{Transport: &stallTransport{base: newBaseTransport(), timeout: stall}}
}

// The bug behind "partial downloads cause the build to hang": a server that
// sends headers and part of a body, then goes silent forever. Without the
// watchdog the read never returns, so the retry loop above never runs.
func TestStalledBodyFailsInsteadOfHanging(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		<-release // go silent, holding the connection open
	}))
	defer func() { close(release); srv.Close() }()

	resp, err := newTestClient(150 * time.Millisecond).Get(srv.URL + "/big.tar.gz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(resp.Body); done <- err }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrStalled) {
			t.Errorf("error = %v, want it to wrap ErrStalled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("read never returned: the stall watchdog did not fire, which is the hang this guards")
	}
}

// A slow transfer is not a stalled one. A body delivering a byte at a time,
// for longer in total than the stall window, must still complete — this is
// what a total http.Client.Timeout would have broken.
func TestSlowButProgressingBodySucceeds(t *testing.T) {
	const chunks = 12
	const gap = 25 * time.Millisecond // total ~300ms, well past the 100ms window

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range chunks {
			io.WriteString(w, "x")
			w.(http.Flusher).Flush()
			time.Sleep(gap)
		}
	}))
	defer srv.Close()

	resp, err := newTestClient(100 * time.Millisecond).Get(srv.URL + "/slow.tar.gz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("slow transfer was abandoned: %v", err)
	}
	if want := strings.Repeat("x", chunks); string(got) != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// A server that accepts the connection and never sends headers is caught
// before any body read, by ResponseHeaderTimeout rather than the watchdog.
func TestNoResponseHeadersFails(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	base := newBaseTransport()
	base.ResponseHeaderTimeout = 150 * time.Millisecond
	client := &http.Client{Transport: &stallTransport{base: base, timeout: StallTimeout}}

	done := make(chan error, 1)
	go func() {
		resp, err := client.Get(srv.URL + "/never.tar.gz")
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("request succeeded, want a timeout waiting for response headers")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("request never returned despite ResponseHeaderTimeout")
	}
}

// Normal completion must not be reported as a stall: EOF arrives as an error
// from Read, and the watchdog must not claim it.
func TestCompletedBodyIsNotAStall(t *testing.T) {
	const body = "complete payload"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer srv.Close()

	resp, err := newTestClient(50 * time.Millisecond).Get(srv.URL + "/small.tar.gz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resp.Body.Close()

	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	// Idle well past the stall window with the body closed; nothing should fire.
	time.Sleep(150 * time.Millisecond)
}
