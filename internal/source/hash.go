package source

import (
	"crypto/sha256"
	"fmt"
)

// SrcHashInputs returns a deterministic string representation of the
// src tree's git state, suitable for folding into a unit's content
// hash. Empty string for non-dev states (pin / empty / local) so the
// caller can gate the hash write on emptiness and keep pin units
// cache-neutral.
//
// For dev / dev-mod: includes the current HEAD sha. Captures every
// commit and branch switch.
//
// For dev-dirty: includes HEAD sha plus a sha256 of `git diff HEAD`
// output, so two consecutive edits produce distinct hashes (HEAD
// hasn't changed, but the diff content has).
//
// Network-free, fast: a couple of git invocations against the local
// repo. Returns empty string on any git failure rather than a
// half-formed hash component, so a corrupted src dir doesn't poison
// the unit hash.
func SrcHashInputs(srcDir string, state State) string {
	if !IsDev(state) {
		return ""
	}
	head, err := stateGit(srcDir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	headSha := trim(head)
	if state == StateDevDirty {
		diff, err := stateGit(srcDir, "diff", "HEAD")
		if err != nil {
			return "head:" + headSha
		}
		// Hash the full diff including untracked-file mentions —
		// `git diff HEAD` skips untracked files, so for parity with
		// DetectState's dirty signal also fold a porcelain status
		// listing in. The combined input changes whenever any file
		// in the work tree changes.
		porcelain, _ := stateGit(srcDir, "status", "--porcelain")
		sum := sha256.Sum256([]byte(diff + "\x00" + porcelain))
		return fmt.Sprintf("head:%s:dirty:%x", headSha, sum[:8])
	}
	return "head:" + headSha
}

// SrcDescribe returns `git describe --dirty --always` against srcDir.
// Used by the executor to populate BuildMeta.SourceDescribe and by
// the TUI's SOURCE line for a human-readable git tag.
//
// Returns empty string on any git failure rather than propagating —
// callers display empty as "(unknown)" or omit the field.
func SrcDescribe(srcDir string) string {
	out, err := stateGit(srcDir, "describe", "--dirty", "--always", "--tags")
	if err != nil {
		return ""
	}
	return trim(out)
}

// trim strips trailing whitespace from git command output.
func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
