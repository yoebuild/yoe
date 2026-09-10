package gitutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Retries is how many times a git operation that reaches the network is
// attempted before yoe gives up. Forges fail transiently — a 503 while one
// restarts, a reset connection, a transfer that goes silent and trips the
// stall guard — and a single such answer should not fail a build that was
// otherwise healthy. Unlike an archive download, a git source has no mirror
// to fall back to, so the retry is the whole of the resilience available.
const Retries = 4

// Backoff is the delay before attempt n (1-based). Linear rather than
// exponential: a forge that is restarting comes back on its own schedule,
// and waiting longer each time mostly delays the recovery yoe is waiting
// for. It is a var so tests can shrink it rather than sleeping through the
// real backoff.
var Backoff = func(attempt int) time.Duration {
	return time.Duration(attempt) * 2 * time.Second
}

// permanentError marks a failure that another attempt cannot clear.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as not worth retrying, for a condition the caller can
// see but git's output cannot describe — a clone that succeeded without
// carrying the ref that was asked for, say.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was marked by Permanent.
func IsPermanent(err error) bool {
	var pe permanentError
	return errors.As(err, &pe)
}

// Retry runs one network git operation until it succeeds, the budget is
// spent, or the failure is one that another attempt cannot clear. fn
// returns git's output alongside its error so the failure can be
// classified; what names the operation in the retry notice, which goes to
// w along with the reason for the previous failure.
func Retry(w io.Writer, what string, fn func() (string, error)) error {
	var lastErr error
	for attempt := 1; attempt <= Retries; attempt++ {
		if attempt > 1 {
			delay := Backoff(attempt - 1)
			fmt.Fprintf(w, "  retrying %s in %s (attempt %d/%d): %v\n",
				what, delay, attempt, Retries, lastErr)
			time.Sleep(delay)
		}

		out, err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		// A wrong URL, a ref that does not exist upstream, or credentials
		// the remote refuses fails the same way however many times it is
		// tried, and the fix is to edit the project rather than to wait.
		if IsPermanent(err) || isPermanentMessage(out) {
			return err
		}
	}
	return fmt.Errorf("%w (after %d attempts)", lastErr, Retries)
}

// permanentMessages are the parts of git's output that identify a failure
// the remote will keep reporting. Each entry is a set of substrings that
// must all appear, which is what separates the fatal line from the progress
// git prints above it: every clone announces "Cloning into ... repository",
// so "repository" alone would match a healthy run.
//
// The list is deliberately narrow. Retrying a permanent failure only delays
// a clear error, while treating a transient one as permanent brings back
// the fragility the retry exists to remove — so anything not named here (a
// 5xx, a reset connection, a stalled transfer) is treated as transient.
var permanentMessages = [][]string{
	{"fatal: repository", "not found"},         // the URL names no repository
	{"not found in upstream origin"},           // the tag or branch does not exist
	{"couldn't find remote ref"},               // the same, from fetch rather than clone
	{"could not read username"},                // credential prompt, so private or absent
	{"authentication failed"},                  // the credentials are rejected
	{"does not appear to be a git repository"}, // the URL points at something else
	{"permission denied"},                      // the remote refuses this caller
}

// isPermanentMessage reports whether git's output names a condition that
// another attempt cannot change.
func isPermanentMessage(out string) bool {
	low := strings.ToLower(out)
	for _, msg := range permanentMessages {
		matched := true
		for _, part := range msg {
			if !strings.Contains(low, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// CloneOptions controls Clone.
type CloneOptions struct {
	// URL is the repository to clone.
	URL string

	// Ref is the tag or branch to clone. Empty clones the remote's
	// default branch.
	Ref string

	// Dest is where the clone lands. Clone owns this path: a failed
	// attempt leaves a partially populated tree behind and git refuses to
	// clone into a directory that is not empty, so what one attempt wrote
	// is removed before the next one runs. Point it at a path created for
	// this clone or at one that does not exist yet — never at a tree
	// holding work someone would miss.
	Dest string

	// Bare clones without a working tree. Unit sources are bare, since
	// nothing is edited in the cache; module clones are not, since the
	// user may enter dev mode and work in them.
	Bare bool

	// Depth, when positive, limits how much history is transferred.
	// Both callers pass 1: yoe wants the pinned ref, not the project's
	// past, and for something like the kernel that is the difference
	// between 200MB and 4GB.
	Depth int
}

// Clone clones a repository, retrying a transient failure at the remote.
// Progress and any retry notice go to w; git's output is reported with the
// error rather than streamed, so a failure reads as one message.
func Clone(opts CloneOptions, w io.Writer) error {
	args := []string{"clone"}
	if opts.Bare {
		args = append(args, "--bare")
	}
	if opts.Depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", opts.Depth))
	}
	if opts.Ref != "" {
		args = append(args, "--branch", opts.Ref)
	}
	args = append(args, opts.URL, opts.Dest)

	first := true
	return Retry(w, "clone of "+opts.URL, func() (string, error) {
		if !first {
			if err := os.RemoveAll(opts.Dest); err != nil {
				return "", Permanent(fmt.Errorf("clearing %s before retrying: %w", opts.Dest, err))
			}
		}
		first = false

		out, err := Command("", args...).CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("git clone %s: %s\n%s", opts.URL, err, out)
		}
		return string(out), nil
	})
}

// FetchRef fetches a single ref from a remote into an existing repository,
// retrying a transient failure the way Clone does.
func FetchRef(dir, remote, ref string, w io.Writer) error {
	return Retry(w, "fetch of "+ref, func() (string, error) {
		out, err := Command(dir, "fetch", remote, ref).CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("git fetch %s %s: %s\n%s", remote, ref, err, out)
		}
		return string(out), nil
	})
}
