package gitutil

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastBackoff shrinks the delay so tests exercise the retry path without
// sleeping the real 2s/4s/6s.
func fastBackoff(t *testing.T) {
	t.Helper()
	orig := Backoff
	Backoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { Backoff = orig })
}

// serveStatus answers every git request with `code`, counting requests.
// Git makes one request per clone attempt before giving up, so the count
// is the attempt count.
func serveStatus(code int) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(code)
	}))
	return srv, &n
}

// newRepo creates a bare repository carrying tag `v1`, to be served over
// HTTP by gitHTTPBackend.
func newRepo(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "-q", "--allow-empty", "-m", "one"},
		{"tag", "v1"},
	} {
		if out, err := Command(work, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	bare := filepath.Join(t.TempDir(), "upstream.git")
	if out, err := Command(work, "clone", "-q", "--bare", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	return bare
}

// gitHTTPBackend serves repo over git's smart HTTP protocol. Shallow clones
// need the smart protocol, so a static file server would not do.
func gitHTTPBackend(t *testing.T, repo string) http.Handler {
	t.Helper()
	backend := filepath.Join(strings.TrimSpace(execPath(t)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("git-http-backend not available: %v", err)
	}
	return &cgi.Handler{
		Path: backend,
		Dir:  filepath.Dir(repo),
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Dir(repo),
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
}

func execPath(t *testing.T) string {
	t.Helper()
	out, err := Command("", "--exec-path").Output()
	if err != nil {
		t.Skipf("git --exec-path: %v", err)
	}
	return string(out)
}

// A forge that is briefly unavailable must not fail the operation on the
// first try. A git source has no mirror to fall back to, so the retry is
// the only resilience there is — a 503 from trustedfirmware.org has failed
// an otherwise healthy machine build.
func TestCloneRetriesTransientFailure(t *testing.T) {
	fastBackoff(t)
	srv, n := serveStatus(http.StatusServiceUnavailable)
	defer srv.Close()

	err := Clone(CloneOptions{
		URL:   srv.URL + "/tfa.git",
		Ref:   "v1",
		Dest:  filepath.Join(t.TempDir(), "c"),
		Bare:  true,
		Depth: 1,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected failure")
	}
	if got := n.Load(); int(got) != Retries {
		t.Errorf("clone attempts = %d, want %d", got, Retries)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("after %d attempts", Retries)) {
		t.Errorf("error should report the attempt count, got: %v", err)
	}
}

// A URL that names no repository fails the same way however often it is
// tried, and the fix is to edit the unit or project. Retrying only delays
// the error the user has to act on.
func TestCloneDoesNotRetryMissingRepo(t *testing.T) {
	fastBackoff(t)
	srv, n := serveStatus(http.StatusNotFound)
	defer srv.Close()

	err := Clone(CloneOptions{
		URL:  srv.URL + "/nosuch.git",
		Dest: filepath.Join(t.TempDir(), "c"),
	}, io.Discard)
	if err == nil {
		t.Fatal("expected failure")
	}
	if got := n.Load(); got != 1 {
		t.Errorf("clone attempts = %d, want 1", got)
	}
}

// A failed clone leaves a partially populated directory behind, and git
// refuses to clone into a directory that is not empty. Clone owns the
// destination so the next attempt starts from nothing — otherwise a retry
// fails for a reason unrelated to the outage it exists to ride out.
func TestCloneClearsDestinationBetweenAttempts(t *testing.T) {
	fastBackoff(t)

	// Serve a 503 to the first two attempts, then a real repository.
	upstream := newRepo(t)
	var n atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(n.Add(1)) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		gitHTTPBackend(t, upstream).ServeHTTP(w, r)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "c")
	// Leave the destination already populated the way a failed attempt
	// would, to prove Clone clears it rather than tripping over it.
	if err := os.MkdirAll(filepath.Join(dest, "leftover"), 0755); err != nil {
		t.Fatal(err)
	}

	err := Clone(CloneOptions{
		URL:   srv.URL + "/upstream.git",
		Ref:   "v1",
		Dest:  dest,
		Bare:  true,
		Depth: 1,
	}, io.Discard)
	if err != nil {
		t.Fatalf("clone: %v (attempts: %d)", err, n.Load())
	}
	if _, err := os.Stat(filepath.Join(dest, "leftover")); err == nil {
		t.Error("debris from the failed attempt survived into the clone")
	}
	if out, err := Run(dest, "rev-parse", "--verify", "--quiet", "v1^{commit}"); err != nil || out == "" {
		t.Errorf("clone does not carry the ref: %v", err)
	}
}

// Permanent marks a failure the caller can see but git's output cannot
// describe, so it must survive wrapping the way any error does.
func TestPermanentSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("cloning module x: %w", Permanent(errors.New("ref missing")))
	if !IsPermanent(err) {
		t.Error("IsPermanent should see through a wrapping error")
	}
	if IsPermanent(errors.New("plain")) {
		t.Error("a plain error is not permanent")
	}
}

// The classifier decides whether a failure is worth another attempt, so
// the exact wording git uses matters. These are messages observed from
// real hosts.
func TestIsPermanentMessage(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{"503 from a forge", "Cloning into bare repository '/c'...\nremote: no healthy upstream\nfatal: unable to access 'https://git.trustedfirmware.org/TF-A/trusted-firmware-a.git/': The requested URL returned error: 503", false},
		{"stalled transfer", "Cloning into bare repository '/c'...\nfatal: unable to access 'https://git.savannah.gnu.org/git/readline.git/': Operation too slow. Less than 1000 bytes/sec transferred the last 60 seconds", false},
		{"connection reset", "Cloning into bare repository '/c'...\nfatal: unable to access 'https://example.com/x.git/': Recv failure: Connection reset by peer", false},
		{"missing repo", "Cloning into bare repository '/c'...\nfatal: repository 'http://127.0.0.1:1/nosuch.git/' not found", true},
		{"missing branch", "Cloning into bare repository '/c'...\nfatal: Remote branch v99 not found in upstream origin", true},
		{"missing ref on fetch", "fatal: couldn't find remote ref v99", true},
		{"private repo prompt", "Cloning into bare repository '/c'...\nfatal: could not read Username for 'https://github.com': No such device or address", true},
		{"healthy clone", "Cloning into bare repository '/c'...\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPermanentMessage(tt.out); got != tt.want {
				t.Errorf("isPermanentMessage = %v, want %v", got, tt.want)
			}
		})
	}
}
