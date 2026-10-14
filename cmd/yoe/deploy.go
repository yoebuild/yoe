package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/yoebuild/yoe/internal/build"
	"github.com/yoebuild/yoe/internal/device"
	"github.com/yoebuild/yoe/internal/feed"
	"github.com/yoebuild/yoe/internal/resolve"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func cmdDeploy(args []string) {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	user := fs.String("user", "root", "ssh user (overridden by user@ in host)")
	port := fs.Int("port", 8765, "feed port")
	hostIP := fs.String("host-ip", "", "advertise this IP instead of <hostname>.local")
	machineName := fs.String("machine", "", "target machine")
	// flag stops at the first positional, and the usage puts flags after
	// <unit> <host>; keep parsing past each positional so they are not
	// silently dropped.
	var pos []string
	for rest := args; ; {
		fs.Parse(rest)
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(pos) != 2 {
		fail("Usage: %s deploy <unit> <[user@]host[:port]> [--user U] [--port P] [--host-ip IP] [--machine M]", os.Args[0])
	}
	unitName := pos[0]
	hostArg := pos[1]

	proj := loadProjectWithMachine(*machineName)
	// AnyUnit suffices to read the unit's class — we only need to
	// know whether it's an image (flash, not deploy) before driving
	// the build/ship/install pipeline. The build path itself uses
	// per-distro views via opts.EffectiveDistro.
	unit := proj.AnyUnit(unitName)
	if unit == nil {
		fail("Error: unit %q not found", unitName)
	}
	if unit.Class == "image" {
		fail("Error: image targets are flashed, not deployed; use `yoe flash %s`", unitName)
	}

	// 1. Build.
	if err := buildUnitForDeploy(proj, unitName, *machineName); err != nil {
		fail("Error: build %s: %v", unitName, err)
	}

	// 2. Resolve a feed URL: existing yoe serve, or start ephemeral.
	feedURL, stopFeed, err := resolveOrStartFeed(proj, projectDir(), *port, *hostIP)
	if err != nil {
		fail("Error: feed: %v", err)
	}
	defer stopFeed()

	target, err := device.ParseSSHTarget(hostArg, *user)
	if err != nil {
		fail("Error: %v", err)
	}
	deployDistro, err := proj.EffectiveDistro()
	if err != nil {
		fail("Error: resolve effective distro: %v", err)
	}
	// The codename is only meaningful for apt-family targets (it stamps
	// the apt sources.list line); alpine deploys ignore it. Read it from
	// the project's apt_feed only when deploying an apt distro, so an
	// alpine-only project — which has no apt_feed — doesn't error.
	codename := ""
	if yoestar.IsAptFamily(deployDistro) {
		if codename, err = proj.CodenameForDistro(deployDistro); err != nil {
			fail("Error: %v", err)
		}
	}
	err = device.Deploy(context.Background(), device.DeployInput{
		Target:   target,
		Unit:     unitName,
		Distro:   deployDistro,
		Codename: codename,
		FeedURL:  feedURL,
		Out:      os.Stdout,
	})
	if err != nil {
		fail("Error: %v", err)
	}
	fmt.Printf("\ndeployed %s to %s (feed: %s)\n", unitName, hostArg, feedURL)
}

// buildUnitForDeploy invokes the same build path `yoe build <unit>` uses,
// returning an error rather than os.Exit. The unit's full runtime closure
// is built — apk on the target will refuse to install a package whose
// runtime deps are missing from the feed, and the deploy path bypasses
// image()'s Starlark-side closure walk that handles this for image builds.
func buildUnitForDeploy(proj *yoestar.Project, unit, machineName string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	targetArch, err := resolveTargetArch(proj, machineName)
	if err != nil {
		return err
	}
	resolvedMachine := machineName
	if resolvedMachine == "" {
		resolvedMachine = proj.Defaults.Machine
	}
	distro, err := proj.EffectiveDistro()
	if err != nil {
		return fmt.Errorf("deploy: %w", err)
	}
	opts := build.Options{
		Ctx:             ctx,
		ProjectDir:      projectDir(),
		Arch:            targetArch,
		Machine:         resolvedMachine,
		EffectiveDistro: distro,
	}
	closure := resolve.RuntimeClosure(proj, []string{unit}, distro)
	return build.BuildUnits(proj, closure, opts, os.Stdout)
}

// resolveOrStartFeed returns a feed URL and a teardown func. If a yoe
// serve advertising this project is already on the LAN, reuse it
// (teardown is a no-op). Otherwise spin up an ephemeral feed on the
// pinned port.
func resolveOrStartFeed(proj *yoestar.Project, projDir string, port int, hostIP string) (string, func(), error) {
	if found := feed.DiscoverForProject(proj); found != "" {
		// The discovered URL names the host by its mDNS name. A device that
		// can't resolve .local is why --host-ip exists, so swap the host for
		// the given IP and keep the running feed's port and path.
		if hostIP != "" {
			u, err := url.Parse(found)
			if err != nil {
				return "", nil, fmt.Errorf("parse discovered feed URL %q: %w", found, err)
			}
			u.Host = net.JoinHostPort(hostIP, u.Port())
			found = u.String()
		}
		fmt.Printf("reusing existing feed %s\n", found)
		return found, func() {}, nil
	}

	cfg, err := feed.ConfigForProject(proj, projDir)
	if err != nil {
		return "", nil, err
	}
	bind := "0.0.0.0"
	if hostIP != "" {
		bind = hostIP
		cfg.HostName = hostIP
	}
	cfg.BindAddr = net.JoinHostPort(bind, strconv.Itoa(port))
	cfg.NoMDNS = true // ephemeral; do not advertise
	cfg.LogW = os.Stderr

	srv, err := feed.Start(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("start ephemeral feed: %w", err)
	}
	url := strings.TrimSuffix(srv.URL(), "/")
	fmt.Printf("ephemeral feed at %s\n", url)
	return url, func() { _ = srv.Stop() }, nil
}
