package device

import (
	"context"
	"fmt"
	"io"
)

// RepoOps groups Add/Remove/List against a remote target. SSH and SCP are
// pluggable so tests can substitute stubs; production wiring uses
// DefaultSSH and DefaultSCP.
type RepoOps struct {
	SSH SSHRunner
	SCP SCPRunner
}

// RepoAddInput carries the parameters for Add.
type RepoAddInput struct {
	Name        string // basename for /etc/apk/repositories.d/<name>.list
	FeedURL     string
	PushKeyFrom string    // local path; empty = skip
	PushKeyTo   string    // remote path
	Out         io.Writer // streams ssh stdout/stderr
}

// Add writes the repo entry into /etc/apk/repositories on the target and
// runs apk update. Entries are bracketed by `# >>> yoe-<name>` /
// `# <<< yoe-<name>` markers so Add is idempotent and Remove can strip
// only this entry without touching the rest of the file. apk-tools 2.x
// reads /etc/apk/repositories directly — it does not read
// /etc/apk/repositories.d/*.list, so we cannot use that convention.
func (r RepoOps) Add(ctx context.Context, t SSHTarget, in RepoAddInput) error {
	if in.Name == "" {
		return fmt.Errorf("repo name is empty")
	}
	if in.FeedURL == "" {
		return fmt.Errorf("feed URL is empty")
	}
	if r.SSH == nil {
		return fmt.Errorf("SSH runner is nil")
	}
	if in.Out == nil {
		in.Out = io.Discard
	}

	if in.PushKeyFrom != "" {
		if r.SCP == nil {
			return fmt.Errorf("SCP runner is nil but key push requested")
		}
		if err := r.SCP(ctx, t, in.PushKeyFrom, in.PushKeyTo, in.Out, in.Out); err != nil {
			return fmt.Errorf("scp key %s -> %s: %w", in.PushKeyFrom, in.PushKeyTo, err)
		}
	}

	script := "set -e\n" + apkRepoWriteBlock(in.Name, in.FeedURL) + "apk update\n"

	return r.SSH(ctx, t, script, in.Out, in.Out)
}

// Remove strips the yoe-<name> block from /etc/apk/repositories on the
// target. Idempotent — missing block is success.
func (r RepoOps) Remove(ctx context.Context, t SSHTarget, name string, out io.Writer) error {
	if name == "" {
		return fmt.Errorf("repo name is empty")
	}
	if r.SSH == nil {
		return fmt.Errorf("SSH runner is nil")
	}
	if out == nil {
		out = io.Discard
	}
	script := "set -e\n[ -f /etc/apk/repositories ] || exit 0\n" + apkRepoStripBlock(name)
	return r.SSH(ctx, t, script, out, out)
}

// List cats /etc/apk/repositories with each line prefixed by its source.
func (r RepoOps) List(ctx context.Context, t SSHTarget, stdout, stderr io.Writer) error {
	if r.SSH == nil {
		return fmt.Errorf("SSH runner is nil")
	}
	script := `set -e
for f in /etc/apk/repositories /etc/apk/repositories.d/*.list; do
    [ -e "$f" ] || continue
    while IFS= read -r line; do
        printf '%s: %s\n' "$f" "$line"
    done < "$f"
done
`
	return r.SSH(ctx, t, script, stdout, stderr)
}
