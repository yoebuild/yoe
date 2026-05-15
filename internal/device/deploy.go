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
# update the cache
apk update
# Dev iteration rebuilds an apk with the same pkgver-r<rel> string,
# so the various --upgrade / --force-reinstall / fix --reinstall paths
# all skip the install on apk-tools 2.x (--force-reinstall isn't a
# valid flag in 2.x, and fix --reinstall reports 'APK unavailable,
# skipped' in some apk-tools builds even when the index is fresh).
#
# The portable, always-works pattern is del+add: remove the existing
# package (with --no-scripts so we don't fire the pre-uninstall hook
# that disables the service in OpenRC), then add the fresh apk. The
# 2>/dev/null ignores the "not installed" case on a first deploy.
# The user is expected to restart the service to pick up the new
# binary; apk doesn't restart services on its own anyway.
apk del --no-scripts %s 2>/dev/null || true
apk add %s
`, in.FeedURL, in.Unit, in.Unit)

	return ssh(ctx, in.Target, script, in.Out, in.Out)
}
