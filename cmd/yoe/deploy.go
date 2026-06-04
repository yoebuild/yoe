package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yoebuild/yoe/internal/build"
	"github.com/yoebuild/yoe/internal/device"
	"github.com/yoebuild/yoe/internal/feed"
	"github.com/yoebuild/yoe/internal/repo"
	"github.com/yoebuild/yoe/internal/resolve"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func cmdDeploy(args []string) {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	user := fs.String("user", "root", "ssh user (overridden by user@ in host)")
	port := fs.Int("port", 8765, "feed port")
	hostIP := fs.String("host-ip", "", "advertise this IP instead of <hostname>.local")
	machineName := fs.String("machine", "", "target machine")
	fs.Parse(args)
	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s deploy <unit> <[user@]host[:port]> [--user U] [--port P] [--host-ip IP] [--machine M]\n", os.Args[0])
		os.Exit(1)
	}
	unitName := fs.Arg(0)
	hostArg := fs.Arg(1)

	proj := loadProjectWithMachine(*machineName)
	// AnyUnit suffices to read the unit's class — we only need to
	// know whether it's an image (flash, not deploy) before driving
	// the build/ship/install pipeline. The build path itself uses
	// per-distro views via opts.EffectiveDistro.
	unit := proj.AnyUnit(unitName)
	if unit == nil {
		fmt.Fprintf(os.Stderr, "Error: unit %q not found\n", unitName)
		os.Exit(1)
	}
	if unit.Class == "image" {
		fmt.Fprintf(os.Stderr, "Error: image targets are flashed, not deployed; use `yoe flash %s`\n", unitName)
		os.Exit(1)
	}

	// 1. Build.
	if err := buildUnitForDeploy(proj, unitName, *machineName); err != nil {
		fmt.Fprintf(os.Stderr, "Error: build %s: %v\n", unitName, err)
		os.Exit(1)
	}

	// 2. Resolve a feed URL: existing yoe serve, or start ephemeral.
	feedURL, stopFeed, err := resolveOrStartFeed(proj, projectDir(), *port, *hostIP)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: feed: %v\n", err)
		os.Exit(1)
	}
	defer stopFeed()

	target, err := device.ParseSSHTarget(hostArg, *user)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	deployDistro, err := proj.EffectiveDistro()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: resolve effective distro: %v\n", err)
		os.Exit(1)
	}
	// The suite is only meaningful for apt-family targets (it stamps the
	// apt sources.list line); alpine deploys ignore it. Read it from the
	// project's apt_feed only when deploying an apt distro, so an
	// alpine-only project — which has no apt_feed — doesn't error.
	suite := ""
	if yoestar.IsAptFamily(deployDistro) {
		if suite, err = proj.SuiteForDistro(deployDistro); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
	err = device.Deploy(context.Background(), device.DeployInput{
		Target:  target,
		Unit:    unitName,
		Distro:  deployDistro,
		Suite:   suite,
		FeedURL: feedURL,
		Out:     os.Stdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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
	results, _ := feed.BrowseMDNS(500 * time.Millisecond)
	for _, r := range results {
		if r.Project == proj.Name {
			fmt.Printf("reusing existing feed %s -> %s\n", r.Instance, r.URL())
			return r.URL(), func() {}, nil
		}
	}

	projRepoDir := repo.RepoDir(proj, projDir)
	httpRoot := filepath.Dir(projRepoDir)
	// Arches live under repo/<project>/<distro>/<arch>/ now; advertise
	// arches found under the project's effective-distro subtree.
	deployDistro, derr := proj.EffectiveDistro()
	if derr != nil {
		return "", nil, fmt.Errorf("resolve effective distro: %w", derr)
	}
	archs, _ := repo.ArchDirs(repo.RepoDistroDir(proj, projDir, deployDistro))

	bind := "0.0.0.0"
	hostName := ""
	if hostIP != "" {
		bind = hostIP
		hostName = hostIP
	}
	srv, err := feed.Start(feed.Config{
		RepoDir:  httpRoot,
		BindAddr: net.JoinHostPort(bind, strconv.Itoa(port)),
		Project:  proj.Name,
		Archs:    archs,
		NoMDNS:   true, // ephemeral; do not advertise
		HostName: hostName,
		LogW:     os.Stderr,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start ephemeral feed: %w", err)
	}
	url := strings.TrimSuffix(srv.URL(), "/")
	fmt.Printf("ephemeral feed at %s\n", url)
	return url, func() { _ = srv.Stop() }, nil
}
