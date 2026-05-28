package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	yoe "github.com/yoebuild/yoe/internal"
	"github.com/yoebuild/yoe/internal/artifact"
	"github.com/yoebuild/yoe/internal/bootstrap"
	"github.com/yoebuild/yoe/internal/build"
	"github.com/yoebuild/yoe/internal/device"
	"github.com/yoebuild/yoe/internal/feeds/alpine"
	"github.com/yoebuild/yoe/internal/feeds/debian"
	"github.com/yoebuild/yoe/internal/module"
	"github.com/yoebuild/yoe/internal/repo"
	"github.com/yoebuild/yoe/internal/resolve"
	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
	"github.com/yoebuild/yoe/internal/tui"
)

var version = "dev"

var (
	globalProjectFile string
	globalShowShadows bool
	// Default true while units-alpine's linux-firmware-* fan-out (~100
	// packages all providing `linux-firmware-any`) keeps tripping the
	// strict intra-module collision check. Flip back once that's fixed
	// upstream.
	globalAllowDuplicateProvides = true
)

// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	// Parse global flags before command dispatch
	args := os.Args[1:]
	for i := 0; i < len(args); {
		switch {
		case args[i] == "--project" && i+1 < len(args):
			globalProjectFile = args[i+1]
			args = append(args[:i], args[i+2:]...)
		case args[i] == "--show-shadows":
			globalShowShadows = true
			args = append(args[:i], args[i+1:]...)
		case args[i] == "--allow-duplicate-provides":
			globalAllowDuplicateProvides = true
			args = append(args[:i], args[i+1:]...)
		default:
			i++
		}
	}

	if len(args) < 1 {
		cmdTUI(nil)
		return
	}

	command := args[0]
	cmdArgs := args[1:]

	switch command {
	case "--help", "-h", "help":
		printUsage()
		return
	case "version":
		fmt.Println(version)
	case "update":
		cmdUpdate()
	case "init":
		cmdInit(cmdArgs)
	case "container":
		cmdContainer(cmdArgs)
	case "module":
		cmdModule(cmdArgs)
	case "update-feeds":
		cmdUpdateFeeds(cmdArgs)
	case "build":
		cmdBuild(cmdArgs)
	case "bootstrap":
		cmdBootstrap(cmdArgs)
	case "flash":
		cmdFlash(cmdArgs)
	case "run":
		cmdRun(cmdArgs)
	case "serve":
		cmdServe(cmdArgs)
	case "device":
		cmdDevice(cmdArgs)
	case "deploy":
		cmdDeploy(cmdArgs)
	case "config":
		cmdConfig(cmdArgs)
	case "repo":
		cmdRepo(cmdArgs)
	case "source":
		cmdSource(cmdArgs)
	case "dev":
		cmdDev(cmdArgs)
	case "desc":
		cmdDesc(cmdArgs)
	case "refs":
		cmdRefs(cmdArgs)
	case "graph":
		cmdGraph(cmdArgs)
	case "log":
		cmdLog(cmdArgs)
	case "diagnose":
		cmdDiagnose(cmdArgs)
	case "clean":
		cmdClean(cmdArgs)
	case "key":
		cmdKey(cmdArgs)
	default:
		if !tryCustomCommand(command, cmdArgs) {
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", command)
			printUsage()
			os.Exit(1)
		}
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [GLOBAL OPTIONS] COMMAND [OPTIONS]\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "Yoe embedded Linux distribution builder\n\n")
	fmt.Fprintf(os.Stderr, "Global Options:\n")
	fmt.Fprintf(os.Stderr, "  --project <file>            Use an alternative project file instead of PROJECT.star\n")
	fmt.Fprintf(os.Stderr, "  --show-shadows              Print stderr notices about cross-module unit shadowing\n")
	fmt.Fprintf(os.Stderr, "                              and intra-module provides overrides\n")
	fmt.Fprintf(os.Stderr, "  --allow-duplicate-provides  Allow multiple units in the same module to declare\n")
	fmt.Fprintf(os.Stderr, "                              the same virtual provide (first registered wins)\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  (no args)               Launch the interactive TUI\n")
	fmt.Fprintf(os.Stderr, "  init <project-dir>      Create a new Yoe project\n")
	fmt.Fprintf(os.Stderr, "  container               Manage the build container (build, shell, status)\n")
	fmt.Fprintf(os.Stderr, "  build [units...]      Build units (--force, --clean, --verbose, --dry-run)\n")
	fmt.Fprintf(os.Stderr, "  dev                     Manage source modifications (extract, diff, status)\n")
	fmt.Fprintf(os.Stderr, "  flash <unit> <device>   Write an image to a device/SD card (also: flash list)\n")
	fmt.Fprintf(os.Stderr, "  run                     Run an image in QEMU\n")
	fmt.Fprintf(os.Stderr, "  serve                   Run an HTTP+mDNS feed for the project's repo\n")
	fmt.Fprintf(os.Stderr, "  device repo             Manage apk repos on a target device (add, remove, list)\n")
	fmt.Fprintf(os.Stderr, "  deploy <unit> <host>    Build and install a unit on a running yoe device\n")
	fmt.Fprintf(os.Stderr, "  module                  Manage external modules (fetch, sync, list)\n")
	fmt.Fprintf(os.Stderr, "  update-feeds            Refresh APKINDEX files for the alpine_feed declarations\n")
	fmt.Fprintf(os.Stderr, "                          in the current module (run inside a module repo)\n")
	fmt.Fprintf(os.Stderr, "  repo                    Manage the local apk package repository\n")
	fmt.Fprintf(os.Stderr, "  cache                   Manage the build cache (local and remote)\n")
	fmt.Fprintf(os.Stderr, "  source                  Download and manage source archives/repos\n")
	fmt.Fprintf(os.Stderr, "  config                  View and edit project configuration\n")
	fmt.Fprintf(os.Stderr, "  desc <unit>           Describe a unit or target\n")
	fmt.Fprintf(os.Stderr, "  refs <unit>           Show reverse dependencies\n")
	fmt.Fprintf(os.Stderr, "  graph                   Visualize the dependency DAG\n")
	fmt.Fprintf(os.Stderr, "  log [unit] [-e]         Show build log (most recent, or specific unit; -e to edit)\n")
	fmt.Fprintf(os.Stderr, "  diagnose [unit]         Launch Claude Code to diagnose a build failure\n")
	fmt.Fprintf(os.Stderr, "  clean                   Remove build artifacts\n")
	fmt.Fprintf(os.Stderr, "  key <generate|info>     Manage the project's apk signing key\n")
	fmt.Fprintf(os.Stderr, "  update                  Update yoe to the latest release\n")
	fmt.Fprintf(os.Stderr, "  version                 Display version information\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  %s init my-project --machine beaglebone-black\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s build openssh\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s build base-image --machine raspberrypi4\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Environment Variables:\n")
	fmt.Fprintf(os.Stderr, "  YOE_PROJECT             Project directory (default: cwd)\n")
	fmt.Fprintf(os.Stderr, "  YOE_CACHE               Cache directory (default: cache/ in project dir)\n")
	fmt.Fprintf(os.Stderr, "  YOE_LOG                 Log level: debug, info, warn, error (default: info)\n")
	fmt.Fprintf(os.Stderr, "\n")
}

// cmdUpdateFeeds is the entry point for the `yoe update-feeds`
// subcommand. Runs inside a module repo, peeks MODULE.star for
// alpine_feed() and debian_feed() calls, then runs the matching
// updater(s) in sequence. A module declaring both runs both.
// Verifies each feed's signature against its declared keys/keyring;
// writes only — the maintainer reviews `git diff` and commits
// manually.
func cmdUpdateFeeds(args []string) {
	fs := flag.NewFlagSet("update-feeds", flag.ExitOnError)
	var (
		archCSV          = fs.String("arch", "", "comma-separated arches to fetch (default: every arch with an existing on-disk feed dir, falling back to all supported)")
		moduleDir        = fs.String("module-dir", "", "module directory holding MODULE.star (default: cwd)")
		allowKeyUpdate   = fs.String("allow-key-update", "", "append a fingerprint to keys/allowed-fingerprints before verifying (Debian only)")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s update-feeds [--arch x86_64,arm64] [--module-dir DIR] [--allow-key-update FPR]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Fetch upstream index files for every alpine_feed()/debian_feed()\n")
		fmt.Fprintf(os.Stderr, "declared in the current module's MODULE.star. Verifies each feed's\n")
		fmt.Fprintf(os.Stderr, "signature against the in-tree trust list. Writes only; review the\n")
		fmt.Fprintf(os.Stderr, "diff and commit manually.\n")
	}
	_ = fs.Parse(args)

	dir := *moduleDir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "update-feeds: %v\n", err)
			os.Exit(1)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "MODULE.star")); err != nil {
		fmt.Fprintf(os.Stderr, "update-feeds: %s: no MODULE.star here (run inside the module repo)\n", dir)
		os.Exit(1)
	}

	var arches []string
	if *archCSV != "" {
		for _, a := range strings.Split(*archCSV, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				arches = append(arches, a)
			}
		}
	}

	alpineDecls, alpineErr := alpine.PeekFeedDecls(dir)
	debianDecls, debianErr := debian.PeekFeedDecls(dir)
	if alpineErr != nil && debianErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", alpineErr)
		os.Exit(1)
	}

	ran := false
	if len(alpineDecls) > 0 {
		ran = true
		opts := alpine.UpdateOptions{ModuleDir: dir, Out: os.Stdout, Arches: arches}
		if err := alpine.UpdateFeeds(opts); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
	if len(debianDecls) > 0 {
		ran = true
		opts := debian.UpdateOptions{
			ModuleDir:      dir,
			Out:            os.Stdout,
			Arches:         arches,
			AllowKeyUpdate: *allowKeyUpdate,
		}
		if err := debian.UpdateFeeds(opts); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
	if !ran {
		fmt.Fprintf(os.Stderr, "update-feeds: no alpine_feed() or debian_feed() in %s/MODULE.star\n", dir)
		os.Exit(1)
	}
}

func cmdModule(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s module <sync|list|info> [...]\n", os.Args[0])
		os.Exit(1)
	}

	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}

	switch args[0] {
	case "sync":
		// Read only PROJECT.star, not module contents — so a broken module
		// can still be re-synced to pull in the fix that unblocks it.
		modules, err := yoestar.ProjectModuleRefs(dir, projectLoadOpts()...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if _, err := module.Sync(modules, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "list":
		if err := yoe.ListModules(dir, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "info":
		fmt.Fprintf(os.Stderr, "module info: not yet implemented\n")
		os.Exit(1)
	case "check-updates":
		fmt.Fprintf(os.Stderr, "module check-updates: not yet implemented\n")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "Unknown module subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func resolveTargetArch(proj *yoestar.Project, machineName string) (string, error) {
	if machineName != "" {
		m, ok := proj.Machines[machineName]
		if !ok {
			return "", fmt.Errorf("machine %q not found", machineName)
		}
		return m.Arch, nil
	}
	// Use the default machine's arch
	if m, ok := proj.Machines[proj.Defaults.Machine]; ok {
		return m.Arch, nil
	}
	// Fallback to host arch
	return build.Arch(), nil
}

func cmdBuild(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	force := fs.Bool("force", false, "force rebuild even if cached")
	clean := fs.Bool("clean", false, "clean build directory before building")
	noCache := fs.Bool("no-cache", false, "disable cache lookup")
	dryRun := fs.Bool("dry-run", false, "show what would be built without building")
	verbose := fs.Bool("verbose", false, "verbose output")
	machineName := fs.String("machine", "", "target machine")
	all := fs.Bool("all", false, "build all units")
	jobs := fs.Int("jobs", 0, "max units to build in parallel (saved to local.star; default 5)")
	fs.BoolVar(verbose, "v", false, "verbose output (shorthand)")
	fs.IntVar(jobs, "j", 0, "max units to build in parallel (shorthand)")
	fs.Parse(args)

	_ = all // build all when no positional args — handled by empty units slice
	units := fs.Args()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	proj := loadProjectWithMachine(*machineName)
	targetArch, err := resolveTargetArch(proj, *machineName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	resolvedMachine := *machineName
	if resolvedMachine == "" {
		resolvedMachine = proj.Defaults.Machine
	}
	pdir := projectDir()
	opts := build.Options{
		Ctx:        ctx,
		Force:      *force,
		Clean:      *clean,
		NoCache:    *noCache,
		DryRun:     *dryRun,
		Verbose:    *verbose,
		ProjectDir: pdir,
		Arch:       targetArch,
		Machine:    resolvedMachine,
	}

	// Derive the consuming distro from the requested target. When the
	// user names an image, use that image's effective distro so the
	// per-distro view picks the right variants for cross-distro
	// same-name collisions. When the user names a non-image unit (or
	// no name — build everything), fall back to the project default.
	if len(units) >= 1 {
		for _, n := range units {
			if u := proj.LookupUnit(proj.DefaultDistro, n); u != nil && u.Class == "image" {
				if d, err := proj.EffectiveDistroForImage(n); err == nil {
					opts.EffectiveDistro = d
					break
				}
			}
			// Fall back: scan AllUnits for any module's variant
			// to catch images registered under a non-default distro.
			for name, u := range proj.AllUnits() {
				if name == n && u.Class == "image" {
					if d, err := proj.EffectiveDistroForImage(n); err == nil {
						opts.EffectiveDistro = d
					}
					break
				}
			}
			if opts.EffectiveDistro != "" {
				break
			}
		}
	}

	// Parallelism precedence: -j flag > local.star parallel_builds >
	// build.DefaultParallel. A -j value is also persisted so subsequent
	// builds (and the TUI) reuse it without re-passing the flag.
	if root, err := findProjectRootForLocal(pdir); err == nil {
		ov, _ := yoestar.LoadLocalOverrides(root)
		opts.Parallel = ov.ParallelBuilds
		if *jobs > 0 {
			opts.Parallel = *jobs
			if ov.ParallelBuilds != *jobs {
				ov.ParallelBuilds = *jobs
				if werr := yoestar.WriteLocalOverrides(root, ov); werr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not save parallel_builds to local.star: %v\n", werr)
				}
			}
		}
	} else if *jobs > 0 {
		opts.Parallel = *jobs
	}

	if err := build.BuildUnits(proj, units, opts, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func projectDir() string {
	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

func cmdContainer(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s container <build|shell|status|binfmt>\n", os.Args[0])
		os.Exit(1)
	}

	switch args[0] {
	case "build":
		fmt.Println("Containers are now units. Use: yoe build toolchain-musl")
	case "shell":
		cmdContainerShell()
	case "status":
		fmt.Println("Containers are now units. Use: yoe describe toolchain-musl")
	case "binfmt":
		fmt.Println("This will register QEMU user-mode emulation for foreign architectures")
		fmt.Println("by running a privileged Docker container (tonistiigi/binfmt).")
		fmt.Println()
		fmt.Println("This enables building arm64 and riscv64 images on your " + build.Arch() + " host.")
		fmt.Println("The registration persists until reboot.")
		fmt.Println()
		fmt.Print("Proceed? (y/n) ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Cancelled.")
			return
		}
		if err := yoe.RegisterBinfmt(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown container subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func cmdContainerShell() {
	projectDir := projectDir()
	sysroot := filepath.Join(projectDir, "build", build.Arch(), "shell", "sysroot")
	build.EnsureDir(sysroot)

	// Use a temp dir for src/destdir so the sandbox mounts are valid
	srcDir := filepath.Join(projectDir, "build", build.Arch(), "shell", "src")
	destDir := filepath.Join(projectDir, "build", build.Arch(), "shell", "destdir")
	build.EnsureDir(srcDir)
	build.EnsureDir(destDir)

	cfg := &build.SandboxConfig{
		Sandbox:    true,
		Shell:      "bash",
		SrcDir:     srcDir,
		DestDir:    destDir,
		Sysroot:    sysroot,
		ProjectDir: projectDir,
		Env: map[string]string{
			"PREFIX":          "/usr",
			"DESTDIR":         "/build/destdir",
			"NPROC":           build.NProc(),
			"ARCH":            build.Arch(),
			"HOME":            "/tmp",
			"PATH":            "/build/sysroot/usr/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"PKG_CONFIG_PATH": "/build/sysroot/usr/lib/pkgconfig:/usr/lib/pkgconfig",
			"CFLAGS":          "-I/build/sysroot/usr/include",
			"CPPFLAGS":        "-I/build/sysroot/usr/include",
			"LDFLAGS":         "-L/build/sysroot/usr/lib",
			"PYTHONPATH":      "/build/sysroot/usr/lib/python3.12/site-packages",
		},
	}

	bwrapCmd := build.BwrapShellCommand(cfg)
	mounts := []yoe.Mount{
		{Host: srcDir, Container: "/build/src"},
		{Host: destDir, Container: "/build/destdir"},
		{Host: sysroot, Container: "/build/sysroot", ReadOnly: true},
	}

	// Resolve container image from project
	proj := loadProject()

	if err := yoe.RunInContainer(yoe.ContainerRunConfig{
		Shell:       "bash",
		Image:       yoe.DefaultContainerImage(proj.Units),
		Command:     bwrapCmd,
		ProjectDir:  projectDir,
		Mounts:      mounts,
		Interactive: true,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	machine := fs.String("machine", "", "default machine for the project")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s init <project-dir> [--machine <name>]\n", os.Args[0])
		os.Exit(1)
	}

	if err := yoe.RunInit(fs.Arg(0), *machine); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdConfig(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s config <show|set> [...]\n", os.Args[0])
		os.Exit(1)
	}

	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}

	switch args[0] {
	case "show":
		if err := yoe.ShowConfig(dir, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "set":
		// local.star is yoe-generated and safe to rewrite, so the
		// per-developer settings that live there are settable from the
		// CLI. PROJECT.star is hand-authored Starlark and is not.
		if len(args) == 3 && args[1] == "parallel-builds" {
			n, err := strconv.Atoi(args[2])
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "config set parallel-builds: value must be an integer >= 1\n")
				os.Exit(1)
			}
			root, err := findProjectRootForLocal(projectDir())
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			ov, _ := yoestar.LoadLocalOverrides(root)
			ov.ParallelBuilds = n
			if err := yoestar.WriteLocalOverrides(root, ov); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("parallel-builds = %d (saved to local.star)\n", n)
			return
		}
		if len(args) == 3 && args[1] == "qemu-memory" {
			mem := strings.TrimSpace(args[2])
			root, err := findProjectRootForLocal(projectDir())
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			ov, _ := yoestar.LoadLocalOverrides(root)
			ov.QEMUMemory = mem // empty string clears it — machine default reapplies
			if err := yoestar.WriteLocalOverrides(root, ov); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if mem == "" {
				fmt.Printf("qemu-memory cleared from local.star; the machine default applies\n")
			} else {
				fmt.Printf("qemu-memory = %s (saved to local.star)\n", mem)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "config set: supported keys are 'parallel-builds <n>' and 'qemu-memory <size>'; edit PROJECT.star directly for project config\n")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "Unknown config subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func cmdClean(args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	all := fs.Bool("all", false, "remove all build artifacts")
	force := fs.Bool("force", false, "skip confirmation prompt")
	locks := fs.Bool("locks", false, "remove stale lock files")
	fs.BoolVar(force, "f", false, "skip confirmation prompt (shorthand)")
	fs.Parse(args)

	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}

	if *locks {
		if err := yoe.CleanLocks(dir, build.Arch()); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := yoe.RunClean(dir, build.Arch(), *all, *force, fs.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func loadProject() *yoestar.Project {
	return loadProjectWithMachine("")
}

// tryLoadProject returns nil if no project is loadable from the cwd
// (rather than os.Exit'ing like loadProject). Useful for commands that
// can run inside or outside a project, like `yoe device repo list`.
// projectLoadOpts returns the LoadOptions derived from global CLI flags. The
// TUI also needs these so reloads (after editing .star files or switching
// machines) honor flags like --allow-duplicate-provides.
func projectLoadOpts() []yoestar.LoadOption {
	opts := []yoestar.LoadOption{
		yoestar.WithModuleSync(module.SyncIfNeeded),
		yoestar.WithShowShadows(globalShowShadows),
		yoestar.WithAllowDuplicateProvides(globalAllowDuplicateProvides),
		yoestar.WithBuiltin("alpine_feed", alpine.Builtin),
		yoestar.WithBuiltin("debian_feed", debian.Builtin),
	}
	if globalProjectFile != "" {
		opts = append(opts, yoestar.WithProjectFile(globalProjectFile))
	}
	return opts
}

// globalFlagArgs returns the global flags as argv tokens, suitable for
// prepending to a re-exec of the yoe binary so the child inherits the same
// load behavior as the parent (TUI re-execs `yoe run` for image launches).
func globalFlagArgs() []string {
	var args []string
	if globalProjectFile != "" {
		args = append(args, "--project", globalProjectFile)
	}
	if globalShowShadows {
		args = append(args, "--show-shadows")
	}
	if globalAllowDuplicateProvides {
		args = append(args, "--allow-duplicate-provides")
	}
	return args
}

func tryLoadProject() *yoestar.Project {
	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}
	proj, err := yoestar.LoadProject(dir, projectLoadOpts()...)
	if err != nil {
		return nil
	}
	return proj
}

func loadProjectWithMachine(machineName string) *yoestar.Project {
	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}
	// Precedence: --machine flag > local.star > PROJECT.star defaults.
	// Local image override is also captured here and applied below — it
	// doesn't affect Starlark eval, so we just patch proj.Defaults.Image.
	var ovImage string
	if machineName == "" {
		absDir, err := filepath.Abs(dir)
		if err == nil {
			if root, err := findProjectRootForLocal(absDir); err == nil {
				if ov, err := yoestar.LoadLocalOverrides(root); err == nil {
					machineName = ov.Machine
					ovImage = ov.Image
				} else {
					fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
				}
			}
		}
	}
	opts := projectLoadOpts()
	if machineName != "" {
		opts = append(opts, yoestar.WithMachine(machineName))
	}
	proj, err := yoestar.LoadProject(dir, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if ovImage != "" {
		if _, ok := proj.Units[ovImage]; ok {
			proj.Defaults.Image = ovImage
		} else {
			fmt.Fprintf(os.Stderr, "Warning: local.star image %q not found in project; ignoring\n", ovImage)
		}
	}
	return proj
}

// findProjectRootForLocal walks up from dir looking for PROJECT.star so
// LoadLocalOverrides can be called against the project root (where
// local.star lives) rather than the working dir.
func findProjectRootForLocal(dir string) (string, error) {
	for {
		if _, err := os.Stat(filepath.Join(dir, "PROJECT.star")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no PROJECT.star in %s or parents", dir)
		}
		dir = parent
	}
}

// loadDistroForCWD resolves the project's effective distro (without
// per-image scope) for CLI subcommands that need to navigate the
// per-distro build/<distro>/ subtree (e.g. `yoe log`, `yoe diagnose`).
// Loads PROJECT.star once and returns DefaultDistroOverride ->
// DefaultDistro -> error.
func loadDistroForCWD(dir string) (string, error) {
	proj, err := yoestar.LoadProject(dir)
	if err != nil {
		return "", fmt.Errorf("loading project to resolve distro: %w", err)
	}
	d, err := proj.EffectiveDistro()
	if err != nil {
		return "", err
	}
	return d, nil
}

func defaultArch(proj *yoestar.Project) string {
	if m, ok := proj.Machines[proj.Defaults.Machine]; ok {
		return m.Arch
	}
	// Fallback: pick the first machine's arch
	for _, m := range proj.Machines {
		return m.Arch
	}
	return "unknown"
}

func cmdDesc(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s desc <unit>\n", os.Args[0])
		os.Exit(1)
	}
	proj := loadProject()
	arch := defaultArch(proj)
	if err := resolve.Describe(os.Stdout, proj, args[0], arch); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdRefs(args []string) {
	fs := flag.NewFlagSet("refs", flag.ExitOnError)
	direct := fs.Bool("direct", false, "show only direct dependents")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s refs <unit> [--direct]\n", os.Args[0])
		os.Exit(1)
	}

	proj := loadProject()
	if err := resolve.Refs(os.Stdout, proj, fs.Arg(0), *direct); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdGraph(args []string) {
	fs := flag.NewFlagSet("graph", flag.ExitOnError)
	format := fs.String("format", "text", "output format (text, dot)")
	fs.Parse(args)

	filter := fs.Arg(0)

	proj := loadProject()
	if err := resolve.Graph(os.Stdout, proj, *format, filter); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdDev(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s dev <extract|diff|status> [unit]\n", os.Args[0])
		os.Exit(1)
	}

	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}

	switch args[0] {
	case "extract":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: %s dev extract <unit>\n", os.Args[0])
			os.Exit(1)
		}
		if err := yoe.DevExtract(dir, build.Arch(), args[1], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "diff":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: %s dev diff <unit>\n", os.Args[0])
			os.Exit(1)
		}
		if err := yoe.DevDiff(dir, build.Arch(), args[1], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "status":
		if err := yoe.DevStatus(dir, build.Arch(), os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown dev subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func cmdBootstrap(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s bootstrap <stage0|stage1|status>\n", os.Args[0])
		os.Exit(1)
	}

	proj := loadProject()
	dir := projectDir()

	switch args[0] {
	case "stage0":
		if err := bootstrap.Stage0(proj, dir, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "stage1":
		if err := bootstrap.Stage1(proj, dir, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "status":
		if err := bootstrap.Status(proj, dir, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown bootstrap subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func cmdLog(args []string) {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	edit := fs.Bool("e", false, "open log in editor")
	fs.Parse(args)

	dir := projectDir()
	unitName := fs.Arg(0)
	var logPath string

	if unitName != "" {
		distro, derr := loadDistroForCWD(dir)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", derr)
			os.Exit(1)
		}
		logPath = filepath.Join(build.UnitBuildDir(dir, build.Arch(), unitName, distro), "build.log")
	} else {
		logPath = findLatestBuildLog(dir)
	}

	if logPath == "" {
		fmt.Fprintln(os.Stderr, "No build logs found")
		os.Exit(1)
	}

	if *edit {
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		cmd := exec.Command(editor, logPath)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	os.Stdout.Write(data)
}

func cmdDiagnose(args []string) {
	dir := projectDir()
	unitName := ""
	if len(args) > 0 {
		unitName = args[0]
	}

	var logPath string
	if unitName != "" {
		distro, derr := loadDistroForCWD(dir)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", derr)
			os.Exit(1)
		}
		logPath = filepath.Join(build.UnitBuildDir(dir, build.Arch(), unitName, distro), "build.log")
	} else {
		logPath = findLatestBuildLog(dir)
	}

	if logPath == "" {
		fmt.Fprintln(os.Stderr, "No build logs found")
		os.Exit(1)
	}

	if _, err := os.Stat(logPath); err != nil {
		fmt.Fprintf(os.Stderr, "Build log not found: %s\n", logPath)
		os.Exit(1)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: claude not found in PATH")
		os.Exit(1)
	}

	prompt := fmt.Sprintf("diagnose %s", logPath)
	cmd := exec.Command(claudePath, prompt)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}

func findLatestBuildLog(projectDir string) string {
	archDir := filepath.Join(projectDir, "build", build.Arch())
	entries, err := os.ReadDir(archDir)
	if err != nil {
		return ""
	}

	type logEntry struct {
		path    string
		modTime int64
	}
	var logs []logEntry

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(archDir, e.Name(), "build.log")
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		logs = append(logs, logEntry{p, info.ModTime().UnixNano()})
	}

	if len(logs) == 0 {
		return ""
	}

	sort.Slice(logs, func(i, j int) bool {
		return logs[i].modTime > logs[j].modTime
	})
	return logs[0].path
}

func cmdUpdate() {
	if err := yoe.Update(version); err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		os.Exit(1)
	}
}

func cmdTUI(_ []string) {
	proj := loadProject()
	cfg := tui.Config{
		LoadOpts:       projectLoadOpts(),
		GlobalFlagArgs: globalFlagArgs(),
	}
	if err := tui.Run(proj, projectDir(), cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdFlash(args []string) {
	if len(args) > 0 && args[0] == "list" {
		cmdFlashList(args[1:])
		return
	}

	fs := flag.NewFlagSet("flash", flag.ExitOnError)
	machineName := fs.String("machine", "", "target machine")
	dryRun := fs.Bool("dry-run", false, "show what would be flashed without writing")
	assumeYes := fs.Bool("yes", false, "skip confirmation prompt")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s flash <image-unit> <device> [--machine <name>] [--yes] [--dry-run]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "       %s flash list\n", os.Args[0])
		os.Exit(1)
	}

	unitName := fs.Arg(0)
	devicePath := fs.Arg(1)

	if devicePath == "" && !*dryRun {
		fmt.Fprintf(os.Stderr, "Usage: %s flash <image-unit> <device>\n", os.Args[0])
		os.Exit(1)
	}

	proj := loadProjectWithMachine(*machineName)
	if err := device.Flash(proj, unitName, devicePath, projectDir(), *dryRun, *assumeYes, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdFlashList(_ []string) {
	cands, err := device.ListCandidates()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(cands) == 0 {
		fmt.Println("No removable devices detected.")
		return
	}
	fmt.Printf("%-14s %8s  %-4s %-10s %s\n", "DEVICE", "SIZE", "BUS", "VENDOR", "MODEL")
	for _, c := range cands {
		fmt.Printf("%-14s %8s  %-4s %-10s %s\n",
			c.Path, device.FormatSize(c.Size), c.Bus, c.Vendor, c.Model)
	}
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	machineName := fs.String("machine", "", "target machine")
	memory := fs.String("memory", "", "RAM size, e.g. 8G (overrides the machine's qemu memory; saved to local.star)")
	display := fs.Bool("display", false, "enable graphical display")
	daemon := fs.Bool("daemon", false, "run in background")
	// 8G default gives grow-rootfs ~6 GiB of slack past the 2 GiB
	// partition to expand into — enough to exercise the grow path and
	// hold the Docker image cache during on-target work. Pass an empty
	// string to disable and run against disk.img directly.
	diskSize := fs.String("disk-size", "8G", "grow QEMU disk image to this size for the run (empty to disable)")
	var ports stringSlice
	fs.Var(&ports, "port", "host:guest port forwarding (repeatable); a matching guest port replaces the machine's default forward")
	// Go's flag package stops parsing at the first non-flag argument, so
	// `yoe run base-image --port ...` would silently drop every flag after
	// the image name. Re-parse the tail after each positional so flags and
	// the image name may appear in any order.
	fs.Parse(args)
	var positional []string
	for rest := fs.Args(); len(rest) > 0; rest = fs.Args() {
		positional = append(positional, rest[0])
		fs.Parse(rest[1:])
	}

	// Whether --display was set on the command line (vs. left at its default
	// false). Distinguishes "user asked for no display" from "user didn't
	// say" so the local.star tri-state can take over in the latter case.
	displaySet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "display" {
			displaySet = true
		}
	})

	opts := device.QEMUOptions{
		Ports:    ports,
		Display:  *display,
		Daemon:   *daemon,
		DiskSize: *diskSize,
	}

	// QEMU memory precedence: --memory flag > local.star qemu_memory >
	// the machine's own qemu memory (resolved in device.RunQEMU when
	// opts.Memory is empty). A --memory value is persisted so subsequent
	// runs (and the TUI) reuse it without re-passing the flag.
	//
	// QEMU display precedence: --display flag > local.star qemu_display
	// > false. Only --display on the command line writes to local.star;
	// the TUI is the editor for the persisted preference.
	//
	// QEMU ports: local.star qemu_ports are appended to the CLI --port
	// list before the run-side merge with the machine's declared forwards.
	// The CLI list comes last so a one-off --port still beats a saved
	// override for the same guest port.
	if root, err := findProjectRootForLocal(projectDir()); err == nil {
		ov, _ := yoestar.LoadLocalOverrides(root)
		opts.Memory = ov.QEMUMemory
		if *memory != "" {
			opts.Memory = *memory
			if ov.QEMUMemory != *memory {
				ov.QEMUMemory = *memory
				if werr := yoestar.WriteLocalOverrides(root, ov); werr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not save qemu_memory to local.star: %v\n", werr)
				}
			}
		}
		if !displaySet {
			opts.Display = ov.QEMUDisplay == "on"
		}
		if len(ov.QEMUPorts) > 0 {
			opts.Ports = append(append([]string(nil), ov.QEMUPorts...), opts.Ports...)
		}
	} else if *memory != "" {
		opts.Memory = *memory
	}

	proj := loadProject()
	unitName := ""
	if len(positional) > 0 {
		unitName = positional[0]
	}
	if unitName == "" {
		unitName = proj.Defaults.Image
	}
	if unitName == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s run <image-unit> [--machine <name>]\n", os.Args[0])
		os.Exit(1)
	}

	if err := device.RunQEMU(proj, unitName, *machineName, projectDir(), opts, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdRepo(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s repo <list|info|remove|clean> [args...]\n", os.Args[0])
		os.Exit(1)
	}

	proj := loadProject()
	// `yoe repo list/info/remove/clean` operates on the per-distro
	// subtree at repo/<project>/<distro>/. Use the project's
	// effective distro; cross-distro queries would walk each distro
	// subtree separately.
	distro, err := proj.EffectiveDistro()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	repoDir := repo.RepoDistroDir(proj, projectDir(), distro)

	switch args[0] {
	case "list":
		if err := repo.List(repoDir, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "info":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: %s repo info <package>\n", os.Args[0])
			os.Exit(1)
		}
		if err := repo.Info(repoDir, args[1], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "remove":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: %s repo remove <package>\n", os.Args[0])
			os.Exit(1)
		}
		// Load the project's signing key so the regenerated APKINDEX stays
		// signed. Failure here is fatal — an unsigned index would silently
		// break apk add against this repo.
		signer, err := artifact.LoadOrGenerateSigner(proj.Name, proj.SigningKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: loading signing key: %v\n", err)
			os.Exit(1)
		}
		if err := repo.Remove(repoDir, args[1], signer, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "clean":
		// Drops .apk files no current unit produces, then re-signs the
		// regenerated APKINDEX. Same signer concern as `remove`.
		signer, err := artifact.LoadOrGenerateSigner(proj.Name, proj.SigningKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: loading signing key: %v\n", err)
			os.Exit(1)
		}
		if err := repo.Clean(proj, repoDir, signer, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown repo subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func cmdSource(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s source <fetch|list|verify|clean> [units...]\n", os.Args[0])
		os.Exit(1)
	}

	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}

	switch args[0] {
	case "fetch":
		if err := source.FetchAll(dir, args[1:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "list":
		if err := source.ListSources(dir, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "verify":
		if err := source.VerifyAll(dir, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "clean":
		if err := source.CleanSources(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown source subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// tryCustomCommand checks for a custom command in commands/*.star and runs it.
// Returns true if the command was found and executed.
func tryCustomCommand(command string, args []string) bool {
	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}

	cmds, engines, err := yoestar.LoadCommands(dir)
	if err != nil {
		// No commands directory or eval error — not a custom command
		return false
	}

	cmd, ok := cmds[command]
	if !ok {
		return false
	}

	eng := engines[command]
	if err := yoestar.RunCommand(eng, cmd, args, dir); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return true
}
