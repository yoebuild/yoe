package gitutil

import (
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newHistoryRepo creates a bare repository with two commits on main and
// returns it with the SHA of the first. Only its child points at that
// commit, which is the shape of a pin partway down a branch.
func newHistoryRepo(t *testing.T) (repo, first string) {
	t.Helper()
	work := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := Command(work, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")
	git("commit", "-q", "--allow-empty", "-m", "one")
	first = git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "two")

	repo = filepath.Join(t.TempDir(), "upstream.git")
	git("clone", "-q", "--bare", work, repo)
	return repo, first
}

func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := Run(dir, "rev-parse", rev)
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", rev, dir, err)
	}
	return strings.TrimSpace(out)
}

// A unit pinned to a commit clones that commit into the bare source cache,
// and the build tree is then cloned from the cache. The commit has to
// survive that second clone, which carries only what the cache can reach.
func TestCloneCommitBare(t *testing.T) {
	repo, first := newHistoryRepo(t)
	srv := httptest.NewServer(gitHTTPBackend(t, repo))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "c")
	err := Clone(CloneOptions{
		URL:   srv.URL + "/upstream.git",
		Ref:   first,
		Dest:  dest,
		Bare:  true,
		Depth: 1,
	}, io.Discard)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if got := revParse(t, dest, "HEAD"); got != first {
		t.Errorf("cache HEAD = %s, want %s", got, first)
	}
	if got := revParse(t, dest, "--is-shallow-repository"); got != "true" {
		t.Errorf("Depth 1 should leave a shallow clone, is-shallow = %s", got)
	}

	tree := filepath.Join(t.TempDir(), "src")
	if out, err := Command("", "clone", "-q", "--shared", dest, tree).CombinedOutput(); err != nil {
		t.Fatalf("clone of the cache: %v\n%s", err, out)
	}
	if got := revParse(t, tree, "HEAD"); got != first {
		t.Errorf("tree HEAD = %s, want %s", got, first)
	}
}

// A work tree clone of a commit checks the commit out and leaves origin
// pointing at the remote, as `git clone` would, so a later fetch from
// origin reaches the same repository.
func TestCloneCommitWorkTree(t *testing.T) {
	repo, first := newHistoryRepo(t)

	dest := filepath.Join(t.TempDir(), "c")
	if err := Clone(CloneOptions{URL: repo, Ref: first, Dest: dest, Depth: 1}, io.Discard); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if got := revParse(t, dest, "HEAD"); got != first {
		t.Errorf("HEAD = %s, want %s", got, first)
	}
	if got, _ := Run(dest, "config", "--get", "remote.origin.url"); strings.TrimSpace(got) != repo {
		t.Errorf("origin = %q, want %q", strings.TrimSpace(got), repo)
	}
}

// A commit the remote does not have is reported the same way on every
// attempt, so it fails at once.
func TestCloneMissingCommitDoesNotRetry(t *testing.T) {
	fastBackoff(t)
	repo, _ := newHistoryRepo(t)

	var log strings.Builder
	err := Clone(CloneOptions{
		URL:   repo,
		Ref:   strings.Repeat("1", 40),
		Dest:  filepath.Join(t.TempDir(), "c"),
		Bare:  true,
		Depth: 1,
	}, &log)
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(log.String(), "retrying") {
		t.Errorf("a missing commit was retried:\n%s", log.String())
	}
}

// A remote that will not serve a commit by name refuses on every attempt,
// and the error has to say why. Git's original wire protocol refuses by
// default, which stands in for such a host.
func TestCloneCommitRefusedDoesNotRetry(t *testing.T) {
	fastBackoff(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.version")
	t.Setenv("GIT_CONFIG_VALUE_0", "0")
	repo, first := newHistoryRepo(t)

	var log strings.Builder
	err := Clone(CloneOptions{
		URL:   repo,
		Ref:   first,
		Dest:  filepath.Join(t.TempDir(), "c"),
		Bare:  true,
		Depth: 1,
	}, &log)
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(err.Error(), "does not allow request for unadvertised object") {
		t.Errorf("error should name the refusal, got: %v", err)
	}
	if strings.Contains(log.String(), "retrying") {
		t.Errorf("a refused commit fetch was retried:\n%s", log.String())
	}
}

func TestIsCommitSHA(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"7d1826930811232688a50c99c540fbb137aed081", true},
		{"7D1826930811232688A50C99C540FBB137AED081", true},
		{strings.Repeat("a", 64), true},
		{"7d1826930811", false},                             // abbreviated
		{"20260911", false},                                 // date-stamped tag
		{"v0.18.5", false},                                  // tag
		{"rpi-6.12.y", false},                               // branch
		{"7d1826930811232688a50c99c540fbb137aed08g", false}, // not hex
		{"", false},
	}
	for _, tt := range tests {
		if got := isCommitSHA(tt.ref); got != tt.want {
			t.Errorf("isCommitSHA(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}
