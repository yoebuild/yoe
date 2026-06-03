package device

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// DeployInput parameterizes Deploy.
//
// Deploy itself is the post-build orchestration step: ssh to the target
// and install <Unit> from the project feed. Building <Unit> is the
// caller's job — the CLI runs `yoe build` ahead of this and starts/stops
// the feed.
type DeployInput struct {
	Target SSHTarget
	Unit   string

	// Distro selects the on-target package manager and the feed
	// sub-path: "alpine" drives apk, "debian" drives apt. Required —
	// the project's effective distro decides which package manager the
	// target runs.
	Distro string

	// Suite is the Debian codename (e.g. "bookworm") written into the
	// apt sources.list line. Required when Distro=="debian"; ignored
	// for alpine.
	Suite string

	// FeedURL is the project feed root, e.g. http://host:8765/<project>.
	// Deploy appends the distro segment itself, since the served repo
	// is laid out as <project>/<distro>/<arch>/...
	FeedURL string

	Out io.Writer
	SSH SSHRunner // defaults to DefaultSSH if nil
}

// Deploy installs in.Unit on the target from the project feed. For
// alpine it writes the feed into /etc/apk/repositories and runs apk
// del+add; for debian it writes /etc/apt/sources.list.d/yoe-dev.list
// (plus a pin) and runs apt-get install --reinstall. The repo config is
// left in place — that's the persistent feed config for repeated dev
// iteration.
func Deploy(ctx context.Context, in DeployInput) error {
	if in.Unit == "" {
		return fmt.Errorf("unit is empty")
	}
	if in.FeedURL == "" {
		return fmt.Errorf("feed URL is empty")
	}
	if in.Distro == "" {
		return fmt.Errorf("distro is empty")
	}
	ssh := in.SSH
	if ssh == nil {
		ssh = DefaultSSH
	}
	if in.Out == nil {
		in.Out = io.Discard
	}

	// The served repo nests one level per distro: <project>/<distro>/...
	// apk wants a URL whose <arch>/APKINDEX.tar.gz sits directly under
	// it; apt wants the same base with dists/<suite>/ under it.
	repoBase := strings.TrimRight(in.FeedURL, "/") + "/" + in.Distro

	var script string
	switch in.Distro {
	case "alpine":
		script = alpineDeployScript(repoBase, in.Unit)
	case "debian":
		if in.Suite == "" {
			return fmt.Errorf("suite is required for debian deploy")
		}
		host, err := feedHost(repoBase)
		if err != nil {
			return err
		}
		script = debianDeployScript(repoBase, in.Suite, host, in.Unit)
	default:
		return fmt.Errorf("unsupported distro %q (want alpine or debian)", in.Distro)
	}

	return ssh(ctx, in.Target, script, in.Out, in.Out)
}

// feedHost extracts the hostname from a feed URL for use as an apt pin
// origin (apt matches a pin's origin against the URI's host component).
func feedHost(feedURL string) (string, error) {
	u, err := url.Parse(feedURL)
	if err != nil {
		return "", fmt.Errorf("parse feed URL %q: %w", feedURL, err)
	}
	return u.Hostname(), nil
}

// alpineDeployScript writes repoBase into /etc/apk/repositories (bracketed
// by yoe-dev markers so it stays idempotent) and reinstalls unit via
// apk del+add.
func alpineDeployScript(repoBase, unit string) string {
	return fmt.Sprintf(`set -e
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
`, repoBase, unit, unit)
}

// debianDeployScript writes the dev feed into apt's sources.list.d (plus
// a high-priority pin) and reinstalls unit via apt-get. host is the feed
// hostname, used as the pin origin.
func debianDeployScript(repoBase, suite, host, unit string) string {
	return fmt.Sprintf(`set -e
export DEBIAN_FRONTEND=noninteractive
install -d -m 0755 /etc/apt/sources.list.d /etc/apt/preferences.d
# The dev feed's InRelease is unsigned, so trusted=yes tells apt to skip
# signature verification for this source only — the apt analog of apk's
# --allow-untrusted. Other configured sources keep their normal trust.
cat > /etc/apt/sources.list.d/yoe-dev.list <<'EOF'
deb [trusted=yes] %s %s main
EOF
# Pin the dev feed above any installed version (priority > 1000) so a
# rebuilt .deb that kept the same version reinstalls, and a rolled-back
# pin downgrades cleanly — the apt analog of apk's remove-then-add dance.
cat > /etc/apt/preferences.d/yoe-dev.pref <<'EOF'
Package: *
Pin: origin "%s"
Pin-Priority: 1001
EOF
# Refresh only the dev feed's index; leave the target's other sources
# untouched so a dev board with no upstream connectivity still updates.
apt-get update \
    -o Dir::Etc::sourcelist="sources.list.d/yoe-dev.list" \
    -o Dir::Etc::sourceparts="-" \
    -o APT::Get::List-Cleanup="0"
# --reinstall lands a same-version rebuild; --allow-downgrades lands a
# rollback to an older pin. The user is expected to restart the service
# to pick up the new binary; apt does not restart services on its own.
apt-get install -y --reinstall --allow-downgrades %s
`, repoBase, suite, host, unit)
}
