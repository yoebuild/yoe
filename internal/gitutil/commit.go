package gitutil

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// isCommitSHA reports whether ref is a full commit object name: 40 hex
// digits, or 64 in a SHA-256 repository. An abbreviated name does not
// qualify. A remote serves a commit only by its full name, and a short run
// of digits is as likely to be a date-stamped tag (`20260911`) as a prefix.
func isCommitSHA(ref string) bool {
	if len(ref) != 40 && len(ref) != 64 {
		return false
	}
	for _, c := range ref {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// cloneCommit clones the single commit opts.Ref names. `git clone --branch`
// accepts only tag and branch names, so the clone is assembled from the
// steps clone itself would take: an empty repository with origin set to the
// URL, a fetch of the commit by name, and HEAD moved to it. HEAD is what
// keeps the commit reachable, so a later `git clone` of a bare result
// carries the commit the way it would carry a tag.
//
// Fetching by name needs a remote that serves a commit it did not
// advertise. Git's current wire protocol does, and so do GitHub, GitLab,
// and Gitea. A remote that refuses says so, and that answer is the same on
// every attempt.
func cloneCommit(opts CloneOptions, w io.Writer) error {
	initArgs := []string{"init"}
	if opts.Bare {
		initArgs = append(initArgs, "--bare")
	}
	initArgs = append(initArgs, opts.Dest)

	fetchArgs := []string{"fetch"}
	if opts.Depth > 0 {
		fetchArgs = append(fetchArgs, "--depth", fmt.Sprintf("%d", opts.Depth))
	}
	fetchArgs = append(fetchArgs, "origin", opts.Ref)

	// A bare repository has no work tree to check out into, so its HEAD is
	// pointed at the commit directly.
	headArgs := []string{"checkout", "--detach", opts.Ref}
	if opts.Bare {
		headArgs = []string{"update-ref", "--no-deref", "HEAD", opts.Ref}
	}

	// Only the fetch reaches the network. The local steps around it fail
	// the same way on every attempt, so their failures are not retried.
	local := func(dir string, args ...string) (string, error) {
		out, err := Command(dir, args...).CombinedOutput()
		if err != nil {
			return string(out), Permanent(fmt.Errorf("git %s: %s\n%s", strings.Join(args, " "), err, out))
		}
		return string(out), nil
	}

	first := true
	return Retry(w, "clone of "+opts.URL, func() (string, error) {
		if !first {
			if err := os.RemoveAll(opts.Dest); err != nil {
				return "", Permanent(fmt.Errorf("clearing %s before retrying: %w", opts.Dest, err))
			}
		}
		first = false

		if out, err := local("", initArgs...); err != nil {
			return out, err
		}
		if out, err := local(opts.Dest, "remote", "add", "origin", opts.URL); err != nil {
			return out, err
		}
		if out, err := Command(opts.Dest, fetchArgs...).CombinedOutput(); err != nil {
			return string(out), fmt.Errorf("git fetch %s %s: %s\n%s", opts.URL, opts.Ref, err, out)
		}
		return local(opts.Dest, headArgs...)
	})
}
