package httputil

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
	tr := NewTransport()
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

// Client is the one every caller shares, so it must carry the settings
// NewTransport describes rather than Go's defaults.
func TestClientUsesTransport(t *testing.T) {
	tr, ok := Client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Client.Transport is %T, want *http.Transport", Client.Transport)
	}
	if !tr.DisableCompression {
		t.Error("DisableCompression is unset; responses would be inflated before the caller sees them")
	}
	if tr.Protocols == nil || tr.Protocols.HTTP2() {
		t.Error("Client does not carry the HTTP/1.1-only protocol set")
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
