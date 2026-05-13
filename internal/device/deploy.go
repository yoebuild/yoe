package device

import (
	"context"
	"fmt"
	"io"
)

// DeployInput parameterizes Deploy.
//
// Deploy itself is the post-build orchestration step: ssh to the target
// and run apk add. Building <unit> is the caller's job — the CLI runs
// `yoe build` ahead of this and starts/stops the feed.
type DeployInput struct {
	Target  SSHTarget
	Unit    string
	FeedURL string // already resolved (mDNS reuse or ephemeral)
	Out     io.Writer
	SSH     SSHRunner // defaults to DefaultSSH if nil
}

// Deploy writes /etc/apk/repositories.d/yoe-dev.list with FeedURL and
// runs `apk add --upgrade <Unit>` on the target. The repo file is left
// in place — that's the persistent feed config the spec requires.
func Deploy(ctx context.Context, in DeployInput) error {
	if in.Unit == "" {
		return fmt.Errorf("unit is empty")
	}
	if in.FeedURL == "" {
		return fmt.Errorf("feed URL is empty")
	}
	ssh := in.SSH
	if ssh == nil {
		ssh = DefaultSSH
	}
	if in.Out == nil {
		in.Out = io.Discard
	}

	script := fmt.Sprintf(`set -e
mkdir -p /etc/apk
touch /etc/apk/repositories
# Strip any existing yoe-dev block, then append a fresh one. apk-tools 2.x
# reads /etc/apk/repositories directly — there is no repositories.d/.
sed -i '/^# >>> yoe-dev$/,/^# <<< yoe-dev$/d' /etc/apk/repositories
{
    printf '# >>> yoe-dev\n'
    printf '%%s\n' '%s'
    printf '# <<< yoe-dev\n'
} >> /etc/apk/repositories
# --no-cache forces apk to bypass /var/cache/apk and refetch the
# APKINDEX from each repo on every run. Without it, apk-tools 2.x can
# hold onto a stale cached index even when the server has fresh content
# (observed when the yoe-dev feed pushes a new package between deploys),
# silently giving "no such package" errors for the new package.
apk --no-cache update
# --force-reinstall installs the apk from the feed even when the apk
# version (pkgver-r<rel>) hasn't changed. Dev iteration on a unit
# rebuilds the apk content without bumping the version, so a plain
# 'apk add --upgrade' sees the same version on the device and skips
# the install — silently dropping the user's edits. --force-reinstall
# is the right behavior for an explicit deploy: the user asked for
# this content on the device, install it.
apk add --force-reinstall %s
`, in.FeedURL, in.Unit)

	return ssh(ctx, in.Target, script, in.Out, in.Out)
}
