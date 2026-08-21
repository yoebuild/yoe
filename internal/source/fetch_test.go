package source

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// payload is a stand-in source archive. Its bytes are what every mirror in
// these tests serves, so a fetch that lands anywhere still verifies.
var payload = []byte("fake source tarball contents\n")

const payloadSHA = "8ced396f790d62b89ec283073a1689c8b56e0cd3a7b649c5e63a942bc9be3c0b"

// fastRetries shrinks the backoff so tests exercise the retry path without
// sleeping the real 2s/4s/6s.
func fastRetries(t *testing.T) {
	t.Helper()
	orig := retryDelay
	retryDelay = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { retryDelay = orig })
}

// cacheIn points the source cache at a temp dir for the duration of a test.
func cacheIn(t *testing.T) {
	t.Helper()
	t.Setenv("YOE_CACHE", t.TempDir())
}

// serveAfter returns a server that fails with status `code` for the first
// `fail` requests and then serves the payload, plus a counter of requests.
func serveAfter(fail int, code int) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(n.Add(1)) <= fail {
			w.WriteHeader(code)
			return
		}
		w.Write(payload)
	}))
	return srv, &n
}

func fetchURL(t *testing.T, src string, mirrors ...string) (string, string, error) {
	t.Helper()
	var log strings.Builder
	p, err := Fetch(&yoestar.Unit{Name: "t", Source: src, Mirrors: mirrors}, &log)
	return p, log.String(), err
}

// A mirror that is briefly unavailable must not fail the build: the fetch
// retries and succeeds once the host recovers.
func TestFetchRetriesTransientServerError(t *testing.T) {
	fastRetries(t)
	cacheIn(t)
	srv, n := serveAfter(2, http.StatusBadGateway)
	defer srv.Close()

	p, log, err := fetchURL(t, srv.URL+"/attr-1.0.tar.gz")
	if err != nil {
		t.Fatalf("fetch: %v\nlog:\n%s", err, log)
	}
	if got := n.Load(); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
	if b, _ := os.ReadFile(p); string(b) != string(payload) {
		t.Errorf("cached content = %q, want %q", b, payload)
	}
}

// Retries are bounded — a permanently broken mirror gives up rather than
// stalling the build indefinitely, and the error names the attempt count.
func TestFetchGivesUpAfterRetryBudget(t *testing.T) {
	fastRetries(t)
	cacheIn(t)
	srv, n := serveAfter(1000, http.StatusBadGateway)
	defer srv.Close()

	_, _, err := fetchURL(t, srv.URL+"/x.tar.gz")
	if err == nil {
		t.Fatal("expected failure")
	}
	if got := n.Load(); int(got) != downloadRetries {
		t.Errorf("requests = %d, want %d", got, downloadRetries)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("after %d attempts", downloadRetries)) {
		t.Errorf("error should report the attempt count, got: %v", err)
	}
}

// A 404 means the URL is wrong, not that the host is struggling. Retrying
// only delays a failure the user has to fix by editing the unit.
func TestFetchDoesNotRetryNotFound(t *testing.T) {
	fastRetries(t)
	cacheIn(t)
	srv, n := serveAfter(1000, http.StatusNotFound)
	defer srv.Close()

	_, _, err := fetchURL(t, srv.URL+"/missing.tar.gz")
	if err == nil {
		t.Fatal("expected failure")
	}
	if got := n.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (no retry on 404)", got)
	}
}

// When the primary URL stays down through its whole retry budget, the fetch
// falls back to a mirror rather than failing the build.
func TestFetchFallsBackToMirror(t *testing.T) {
	fastRetries(t)
	cacheIn(t)
	down, downN := serveAfter(1000, http.StatusBadGateway)
	defer down.Close()
	up, upN := serveAfter(0, 0)
	defer up.Close()

	p, log, err := fetchURL(t, down.URL+"/a.tar.gz", up.URL+"/a.tar.gz")
	if err != nil {
		t.Fatalf("fetch: %v\nlog:\n%s", err, log)
	}
	if got := downN.Load(); int(got) != downloadRetries {
		t.Errorf("primary requests = %d, want %d", got, downloadRetries)
	}
	if got := upN.Load(); got != 1 {
		t.Errorf("mirror requests = %d, want 1", got)
	}
	if b, _ := os.ReadFile(p); string(b) != string(payload) {
		t.Errorf("cached content = %q, want %q", b, payload)
	}
}

// A mirror serving the declared bytes verifies and is accepted, so falling
// back is a real recovery rather than a path that only works unverified.
func TestFetchAcceptsVerifiedMirror(t *testing.T) {
	fastRetries(t)
	cacheIn(t)
	down, _ := serveAfter(1000, http.StatusBadGateway)
	defer down.Close()
	up, _ := serveAfter(0, 0)
	defer up.Close()

	var log strings.Builder
	p, err := Fetch(&yoestar.Unit{
		Name:    "t",
		Source:  down.URL + "/a.tar.gz",
		Mirrors: []string{up.URL + "/a.tar.gz"},
		SHA256:  payloadSHA,
	}, &log)
	if err != nil {
		t.Fatalf("fetch: %v\nlog:\n%s", err, log.String())
	}
	if b, _ := os.ReadFile(p); string(b) != string(payload) {
		t.Errorf("cached content = %q, want %q", b, payload)
	}
}

// A mirror serving different bytes than the unit declares must be rejected —
// falling back to another host cannot become a way to accept unverified
// content.
func TestFetchVerifiesSHA256FromMirror(t *testing.T) {
	fastRetries(t)
	cacheIn(t)
	down, _ := serveAfter(1000, http.StatusBadGateway)
	defer down.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered\n"))
	}))
	defer bad.Close()

	var log strings.Builder
	_, err := Fetch(&yoestar.Unit{
		Name:    "t",
		Source:  down.URL + "/a.tar.gz",
		Mirrors: []string{bad.URL + "/a.tar.gz"},
		SHA256:  payloadSHA,
	}, &log)
	if err == nil {
		t.Fatal("expected SHA256 mismatch")
	}
	if !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Errorf("error = %v, want SHA256 mismatch", err)
	}
}
