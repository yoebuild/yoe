// Package gitutil holds the git plumbing yoe's dev-mode paths share:
// running a command in a repository, rewriting a clone URL, and fetching
// from origin at a chosen depth.
//
// Plumbing only. The policy that decides what to do with a working tree
// — when to create a branch versus check one out, whether a transition
// may discard commits — deliberately stays with each caller. A user's
// source tree and a module clone are the same mechanism but different
// promises, and collapsing those promises into one function is how a
// dev-mode path starts quietly resetting work someone has not pushed.
package gitutil

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"slices"
	"strings"
)

// stallGuard bounds git's transfers the way internal/httputil bounds yoe's
// own downloads, and for the same reason: a remote that accepts the
// connection and then stops sending leaves git waiting forever, which hangs
// the build with no retry ever running. Git ships the mechanism but leaves it
// off by default.
//
// The threshold is a floor, not a deadline. Git aborts only when the transfer
// averages under 1 KB/s for a full minute, so a large clone from a slow mirror
// still completes however long it takes — a total timeout would abandon it.
// At under 1 KB/s sustained, a source archive would need days, so the transfer
// is dead rather than slow.
//
// This governs git's HTTP transport. Clones over SSH are not covered: the
// equivalent knobs live in core.sshCommand, and overriding that would discard
// whatever ssh configuration the user has set for their own remotes.
var stallGuard = []string{
	"-c", "http.lowSpeedLimit=1000",
	"-c", "http.lowSpeedTime=60",
}

// Command returns a git command running in dir, configured so a transfer that
// stops making progress fails instead of hanging.
//
// Every git invocation that may reach the network must be built here rather
// than with exec.Command directly. The configuration is inert for purely
// local commands, so there is no reason for a caller to opt out.
func Command(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append(slices.Clone(stallGuard), args...)...)
	cmd.Dir = dir
	return cmd
}

// Run executes git in dir and returns its standard output.
//
// Only stdout is returned: git writes progress and hints to stderr, and
// callers here parse the result (rev-list counts, rev-parse SHAs, branch
// names). Folding stderr into that would put human-readable noise into
// values used as data. On failure, the error carries git's stderr, which
// is where the explanation actually is.
func Run(dir string, args ...string) (string, error) {
	out, err := Command(dir, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				return "", errors.New(msg)
			}
		}
		return "", err
	}
	return string(out), nil
}

// HTTPSToSSH rewrites an https clone URL to its SSH form, reporting
// whether it did. Entering dev mode on a repository the user may push to
// wants the SSH remote; anything that is not an https URL with a path
// (an existing git@ remote, a git:// URL) is returned unchanged.
func HTTPSToSSH(httpsURL string) (string, bool) {
	u, err := url.Parse(httpsURL)
	if err != nil || u.Scheme != "https" {
		return httpsURL, false
	}
	path := strings.TrimPrefix(u.Path, "/")
	if path == "" {
		return httpsURL, false
	}
	return "git@" + u.Host + ":" + path, true
}

// FetchOptions controls FetchOrigin.
type FetchOptions struct {
	// Depth, when positive, fetches only that many commits of PinnedRef
	// and passes --filter=blob:none, so the transfer is commits and
	// trees without file contents.
	Depth int

	// PinnedRef narrows a depth fetch to a single tag or branch. Ignored
	// unless Depth is positive.
	PinnedRef string

	// SkipWhenFull returns without fetching when the repository already
	// has full history and no depth was requested. Module clones set
	// this: toggling a module that is already complete has nothing to
	// fetch, and a silent no-op beats a network round trip. Unit source
	// trees leave it false — a plain fetch there picks up upstream
	// commits the user has not seen yet.
	SkipWhenFull bool
}

// FetchOrigin fetches from origin in dir using the depth strategy in
// opts. A shallow repository with no requested depth is unshallowed, so
// the caller ends up with the history that dev-mode work needs.
func FetchOrigin(dir string, opts FetchOptions) error {
	shallow, _ := Run(dir, "rev-parse", "--is-shallow-repository")
	isShallow := strings.TrimSpace(shallow) == "true"

	var args []string
	var refspec string
	useFilter := false
	switch {
	case opts.Depth > 0:
		args = []string{"fetch", fmt.Sprintf("--depth=%d", opts.Depth)}
		refspec = opts.PinnedRef
		useFilter = true
	case isShallow:
		args = []string{"fetch", "--unshallow"}
	case opts.SkipWhenFull:
		return nil
	default:
		args = []string{"fetch"}
	}
	if useFilter {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, "origin")
	if refspec != "" {
		args = append(args, refspec)
	}
	if _, err := Run(dir, args...); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
