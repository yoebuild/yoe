package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	yoe "github.com/yoebuild/yoe/internal"
	"github.com/yoebuild/yoe/internal/artifact"
	"github.com/yoebuild/yoe/internal/repo"
	"github.com/yoebuild/yoe/internal/resolve"
	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
	"go.starlark.net/starlark"
)

// Options controls build behavior.
// BuildEvent is sent to Options.OnEvent during a build.
type BuildEvent struct {
	Unit   string
	Status string // "cached", "building", "done", "failed"
}

type Options struct {
	Ctx        context.Context // optional; nil means background
	Force      bool   // rebuild even if cached
	Clean      bool   // delete build dir before rebuilding (implies Force)
	NoCache    bool   // skip all caches
	DryRun     bool   // show what would be built
	Verbose    bool   // show build output in console (default: log only)
	ProjectDir string // project root
	Arch       string // target architecture
	Machine    string // target machine name
	// ProjectCommit is the git rev-parse HEAD of ProjectDir, captured once
	// per build so PKGINFO records build provenance. Empty means "not a git
	// repo" or "couldn't determine" — the apk omits the `commit` field then.
	ProjectCommit string
	// Signer holds the project's RSA signing key, loaded once per build so
	// each apk and the APKINDEX can be signed without per-call key I/O.
	// Nil means "build unsigned apks" — apk add then needs --allow-untrusted.
	Signer     *artifact.Signer
	OnEvent    func(BuildEvent) // optional callback for build progress
}

// ScopeDir returns the build subdirectory for a unit based on its scope.
// "machine" → machine name, "noarch" → "noarch", default → arch.
//
// This drives where we keep build state under build/ — it's a per-build
// concept, not a packaging concept. Machine-scoped units need their own
// build dir so two machines targeting the same arch don't collide.
func ScopeDir(unit *yoestar.Unit, arch, machine string) string {
	switch unit.Scope {
	case "machine":
		return machine
	case "noarch":
		return "noarch"
	default:
		return arch
	}
}

// RepoArchDir returns the per-arch subdirectory under repo/ where a unit's
// .apk is published. apk-tools expects `<repo>/<arch>/APKINDEX.tar.gz` and
// derives <arch> from `apk --print-arch` (the kernel arch name). yoe's
// internal arch token is the Go-style "arm64", but apk reports "aarch64",
// so we translate at the apk boundary — the repo dir, the PKGINFO `arch =`
// field, and the APKINDEX `A:` field all need to match what apk-tools
// looks up at install time. Machine-scoped units are built for a specific
// arch and live alongside arch-scoped apks of the same arch; the unique
// pkgname (e.g., `linux-rpi4` vs `linux-imx6ul`) keeps them from colliding.
func RepoArchDir(unit *yoestar.Unit, arch string) string {
	if unit.Scope == "noarch" {
		return "noarch"
	}
	return ApkArch(arch)
}

// ApkArch translates yoe's internal architecture token to the value
// apk-tools uses for the same architecture. yoe uses "arm64" everywhere
// (matching Go's GOARCH and Docker's --platform), but apk-tools — like the
// Linux kernel — calls it "aarch64". Other architectures (x86_64, riscv64)
// share a name across both ecosystems and pass through unchanged.
func ApkArch(arch string) string {
	if arch == "arm64" {
		return "aarch64"
	}
	return arch
}

// BuildUnits builds the specified units (or all if names is empty).
func BuildUnits(proj *yoestar.Project, names []string, opts Options, w io.Writer) error {
	// Capture project HEAD commit once for PKGINFO provenance. Failure is
	// non-fatal — apks just omit the `commit` field.
	if opts.ProjectCommit == "" {
		opts.ProjectCommit = readProjectCommit(opts.ProjectDir)
	}

	// Load (or auto-generate) the project signing key once per build.
	// Subsequent apk emissions and APKINDEX generation reuse the same
	// Signer so we don't re-read PEM bytes on every artifact.
	if opts.Signer == nil {
		signer, err := artifact.LoadOrGenerateSigner(proj.Name, proj.SigningKey)
		if err != nil {
			return fmt.Errorf("loading signing key: %w", err)
		}
		opts.Signer = signer
	}

	// Make the project's public key available under <repo>/keys/ before any
	// unit's tasks run. Units that ship the key (base-files puts it under
	// /etc/apk/keys/ in the rootfs) need it on disk during their own build,
	// not after the first apk is published. Idempotent — Publish would
	// rewrite the same bytes later.
	repoDir := repo.RepoDir(proj, opts.ProjectDir)
	if err := repo.WritePublicKey(repoDir, opts.Signer); err != nil {
		return fmt.Errorf("publishing project public key: %w", err)
	}

	dag, err := resolve.BuildDAG(proj)
	if err != nil {
		return err
	}

	// Determine build order
	order, err := dag.TopologicalSort()
	if err != nil {
		return err
	}

	// Compute hashes for cache
	hashes, err := resolve.ComputeAllHashes(dag, opts.Arch, opts.Machine)
	if err != nil {
		return err
	}

	// Filter to requested units (and their deps)
	requested := make(map[string]bool)
	if len(names) > 0 {
		for _, n := range names {
			requested[n] = true
		}
		order, err = filterBuildOrder(dag, order, names)
		if err != nil {
			return err
		}
	}

	if opts.DryRun {
		return dryRun(w, proj, order, hashes, opts, requested)
	}

	notify := func(unit, status string) {
		if opts.OnEvent != nil {
			opts.OnEvent(BuildEvent{Unit: unit, Status: status})
		}
	}

	// Pre-scan: emit cached/waiting status for all units so the TUI
	// can show the full build queue before any work starts.
	for _, name := range order {
		hash := hashes[name]
		unit := proj.Units[name]
		sd := ScopeDir(unit, opts.Arch, opts.Machine)
		forceThis := (opts.Force || opts.Clean) && (len(requested) == 0 || requested[name])
		if !forceThis && !opts.NoCache && cacheValid(proj, opts.ProjectDir, unit, sd, opts.Arch, hash) {
			notify(name, "cached")
		} else {
			notify(name, "waiting")
		}
	}

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// Track which units we rebuilt in this run, so we can invalidate
	// downstream caches: if a dep is rebuilt, its dependents must rebuild
	// too — the cache marker alone won't catch this because input hashes
	// don't change just because an apk got reproduced.
	rebuilt := map[string]bool{}

	// Build in order
	for _, name := range order {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("build cancelled")
		}

		unit := proj.Units[name]
		hash := hashes[name]
		sd := ScopeDir(unit, opts.Arch, opts.Machine)

		// --force/--clean only apply to explicitly requested units;
		// dependencies still use the cache.
		forceThis := (opts.Force || opts.Clean) && (len(requested) == 0 || requested[name])

		// Direct deps are sufficient: topological order guarantees that
		// any rebuilt transitive dep already invalidated the intermediate
		// dep, which is therefore in `rebuilt`.
		depRebuilt := false
		for _, d := range dag.Nodes[name].Deps {
			if rebuilt[d] {
				depRebuilt = true
				break
			}
		}

		if !forceThis && !opts.NoCache && !depRebuilt {
			if cacheValid(proj, opts.ProjectDir, unit, sd, opts.Arch, hash) {
				fmt.Fprintf(w, "%-20s [cached] %s\n", name, hash[:12])
				continue
			}
		}

		fmt.Fprintf(w, "%-20s [building]\n", name)
		notify(name, "building")

		if err := buildOne(ctx, proj, dag, unit, hash, opts, w); err != nil {
			notify(name, "failed")
			fmt.Fprintf(w, "%-20s [failed] %v\n", name, err)
			// Show which remaining units are blocked by this failure
			blocked := blockedUnits(dag, name, order)
			if len(blocked) > 0 {
				fmt.Fprintf(w, "  the following units depend on %s and cannot be built:\n", name)
				for _, b := range blocked {
					fmt.Fprintf(w, "    - %s\n", b)
				}
			}
			return fmt.Errorf("building %s: %w", name, err)
		}

		// Write cache marker
		writeCacheMarker(opts.ProjectDir, sd, name, hash)
		rebuilt[name] = true
		fmt.Fprintf(w, "%-20s [done] %s\n", name, hash[:12])
		notify(name, "done")
	}

	return nil
}

func buildOne(ctx context.Context, proj *yoestar.Project, dag *resolve.DAG, unit *yoestar.Unit, hash string, opts Options, w io.Writer) (buildErr error) {
	sd := ScopeDir(unit, opts.Arch, opts.Machine)
	buildDir := UnitBuildDir(opts.ProjectDir, sd, unit.Name)
	EnsureDir(buildDir)

	// Skip if another process is already building this unit.
	if IsBuildInProgress(opts.ProjectDir, sd, unit.Name) {
		fmt.Fprintf(w, "  %s: build already in progress, skipping\n", unit.Name)
		return nil
	}

	// Remove the cache marker before starting so a cancelled or failed
	// build does not leave a stale marker that makes it appear cached.
	os.Remove(CacheMarkerPath(opts.ProjectDir, sd, unit.Name, hash))

	// Write a lock file so other yoe instances can detect an in-progress build.
	lockPath := BuildingLockPath(opts.ProjectDir, sd, unit.Name)
	os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(lockPath)

	// Write initial build metadata; update on completion.
	buildStart := time.Now()
	meta := &BuildMeta{
		Status:  "building",
		Started: &buildStart,
		Hash:    hash,
	}
	WriteMeta(buildDir, meta)
	defer func() {
		now := time.Now()
		meta.Finished = &now
		meta.Duration = now.Sub(buildStart).Seconds()
		meta.DiskBytes = DirSize(buildDir)
		// For non-image units this is the destdir (what goes into the .apk).
		// For image units the destdir contains both `rootfs/` (actual file
		// content) and `<name>.img` (the assembled disk image, sized by the
		// machine's partition spec); we report just the rootfs walk so the
		// TUI's SIZE column reflects "what's installed" rather than the
		// partition's reserved free space.
		installedRoot := filepath.Join(buildDir, "destdir")
		if unit.Class == "image" {
			installedRoot = filepath.Join(installedRoot, "rootfs")
		}
		meta.InstalledBytes = DirSize(installedRoot)
		if ctx.Err() != nil {
			meta.Status = "cancelled"
		} else if buildErr != nil {
			meta.Status = "failed"
			meta.Error = buildErr.Error()
		} else {
			meta.Status = "complete"
		}
		WriteMeta(buildDir, meta)
	}()

	// Write executor output to executor.log so TUI detail view can show it
	// even for CLI builds.
	outputPath := filepath.Join(buildDir, "executor.log")
	outputFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output log: %w", err)
	}
	defer outputFile.Close()
	w = io.MultiWriter(w, outputFile)

	// Open build log. In verbose mode, tee to terminal + log file.
	// In normal mode, log only — on error, print the log path.
	logPath := filepath.Join(buildDir, "build.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("creating build log: %w", err)
	}
	defer logFile.Close()

	var logW io.Writer
	if opts.Verbose {
		logW = io.MultiWriter(w, logFile)
	} else {
		logW = logFile
	}

	srcDir := filepath.Join(buildDir, "src")
	destDir := filepath.Join(buildDir, "destdir")

	// Resolve container image early so destdir cleanup can recover from
	// root-owned files left by a previous failed image build.
	containerImage := resolveContainerImage(proj, unit, opts.Arch)

	// Image-class units chown rootfs to root for mkfs.ext4 -d and chown back
	// only on success. If anything between fails, the destdir is left owned
	// by root and the host can't clean it up. Restore ownership after every
	// image build (success or failure) so the next build can proceed.
	if unit.Class == "image" {
		defer func() {
			if err := chownDirToHost(ctx, destDir, opts.ProjectDir, containerImage); err != nil {
				fmt.Fprintf(w, "  (warning: restoring destdir ownership failed: %v)\n", err)
			}
		}()
	}

	if opts.Clean {
		if err := removeDirRobust(ctx, srcDir, opts.ProjectDir, containerImage); err != nil {
			return fmt.Errorf("removing srcdir: %w", err)
		}
	}

	// Always start with an empty destdir.
	if err := removeDirRobust(ctx, destDir, opts.ProjectDir, containerImage); err != nil {
		return fmt.Errorf("removing destdir: %w", err)
	}
	if err := EnsureDir(destDir); err != nil {
		return fmt.Errorf("creating destdir: %w", err)
	}

	// Prepare source (fetch + extract + patch, or reuse dev source).
	// Units without a source field (e.g., musl) skip this step.
	if unit.Source != "" {
		if _, err := source.Prepare(opts.ProjectDir, sd, unit, w); err != nil {
			return fmt.Errorf("preparing source: %w", err)
		}
	} else {
		EnsureDir(srcDir)
	}

	if len(unit.Tasks) == 0 {
		fmt.Fprintf(w, "  (no tasks for %s class %q)\n", unit.Name, unit.Class)
		return nil
	}

	// Assemble per-unit sysroot from transitive deps
	sysroot := filepath.Join(buildDir, "sysroot")
	if err := AssembleSysroot(sysroot, dag, unit.Name, opts.ProjectDir, opts.Arch); err != nil {
		return fmt.Errorf("assembling sysroot: %w", err)
	}
	// Extract console device from machine kernel cmdline (e.g., "console=ttyS0,115200" → "ttyS0")
	console := ""
	if m, ok := proj.Machines[opts.Machine]; ok && m.Kernel.Cmdline != "" {
		for _, part := range strings.Split(m.Kernel.Cmdline, " ") {
			if strings.HasPrefix(part, "console=") {
				c := strings.TrimPrefix(part, "console=")
				if idx := strings.Index(c, ","); idx > 0 {
					c = c[:idx]
				}
				console = c
				break
			}
		}
	}

	env := map[string]string{
		"PREFIX":          "/usr",
		"DESTDIR":         "/build/destdir",
		"NPROC":           NProc(),
		"ARCH":            opts.Arch,
		"MACHINE":         opts.Machine,
		"CONSOLE":         console,
		"HOME":            "/tmp",
		"PATH":            "/build/sysroot/usr/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"PKG_CONFIG_PATH": "/build/sysroot/usr/lib/pkgconfig:/usr/lib/pkgconfig",
		"CFLAGS":          "-I/build/sysroot/usr/include",
		"CPPFLAGS":        "-I/build/sysroot/usr/include",
		"LDFLAGS":         "-L/build/sysroot/usr/lib",
		"LD_LIBRARY_PATH": "/build/sysroot/usr/lib",
		"PYTHONPATH":      "/build/sysroot/usr/lib/python3.12/site-packages",
		"REPO":            filepath.Join("/project", repoRelPath(proj, opts.ProjectDir)),
	}

	// Expose the project's signing key info so units that need to ship the
	// public key (e.g., base-files installs it under /etc/apk/keys/) can
	// find it without hard-coding paths. YOE_KEYS_DIR is a directory; the
	// key file is YOE_KEY_NAME inside it. Both are unset when the build
	// runs without a Signer (apk add then needs --allow-untrusted).
	if opts.Signer != nil {
		env["YOE_KEYS_DIR"] = filepath.Join("/project", repoRelPath(proj, opts.ProjectDir), "keys")
		env["YOE_KEY_NAME"] = opts.Signer.KeyName
	}

	// Merge unit-level environment variables (from classes like go_binary)
	for k, v := range unit.Environment {
		env[k] = v
	}

	// For container units, set the host working directory to the .star file's
	// directory so docker build can find the Dockerfile.
	hostDir := ""
	if unit.Class == "container" && unit.DefinedIn != "" {
		hostDir = unit.DefinedIn
	}

	// Resolve cache dir mounts: unit's cache_dirs maps container paths to
	// subdirectory names under the project's cache directory.
	var cacheDirs map[string]string
	if len(unit.CacheDirs) > 0 {
		cacheDirs = make(map[string]string, len(unit.CacheDirs))
		for containerPath, subdir := range unit.CacheDirs {
			hostPath := filepath.Join(opts.ProjectDir, "cache", subdir)
			os.MkdirAll(hostPath, 0755)
			cacheDirs[hostPath] = containerPath
		}
	}

	// Build the template context data map for install_file / install_template.
	// Unit identity fields + auto-populated machine/arch/console/project,
	// with unit.Extra kwargs overriding on collision.
	projectName := ""
	if proj != nil {
		projectName = proj.Name
	}
	tctxData := BuildTemplateContext(unit, opts.Arch, opts.Machine, console, projectName)

	// Execute tasks
	for ti, t := range unit.Tasks {
		fmt.Fprintf(w, "  [%d/%d] task: %s\n", ti+1, len(unit.Tasks), t.Name)
		fmt.Fprintf(logW, "  task: %s (%d steps)\n", t.Name, len(t.Steps))

		// Per-task container override
		taskContainer := containerImage
		if t.Container != "" {
			taskUnit := *unit
			taskUnit.Container = t.Container
			taskContainer = resolveContainerImage(proj, &taskUnit, opts.Arch)
		}

		for i, step := range t.Steps {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("build cancelled")
			}

			if step.Install != nil {
				fmt.Fprintf(logW, "    [%d/%d] %s\n", i+1, len(t.Steps), installStepLabel(step.Install))
				// doInstallStep runs on the host, not in the sandbox, so
				// override path-valued env vars that would otherwise point
				// at container-side bind mounts (/build/...).
				hostEnv := make(map[string]string, len(env)+3)
				for k, v := range env {
					hostEnv[k] = v
				}
				hostEnv["DESTDIR"] = destDir
				hostEnv["SRCDIR"] = srcDir
				hostEnv["SYSROOT"] = sysroot
				if err := doInstallStep(unit, step.Install, tctxData, hostEnv); err != nil {
					if !opts.Verbose {
						fmt.Fprintf(w, "  build log: %s\n", logPath)
					}
					return fmt.Errorf("task %s: %w", t.Name, err)
				}
				continue
			}

			if step.Command != "" {
				fmt.Fprintf(logW, "    [%d/%d] %s\n", i+1, len(t.Steps), step.Command)
				cfg := &SandboxConfig{
					Ctx:        ctx,
					Arch:       opts.Arch,
					Container:  taskContainer,
					Sandbox:    unit.Sandbox,
					Shell:      unit.Shell,
					SrcDir:     srcDir,
					DestDir:    destDir,
					Sysroot:    sysroot,
					Env:        env,
					ProjectDir: opts.ProjectDir,
					HostDir:    hostDir,
					CacheDirs:  cacheDirs,
					Stdout:     logW,
					Stderr:     logW,
				}
				if err := RunInSandbox(cfg, step.Command); err != nil {
					if !opts.Verbose {
						fmt.Fprintf(w, "  build log: %s\n", logPath)
					}
					return err
				}
			} else if step.Fn != nil {
				fmt.Fprintf(logW, "    [%d/%d] fn: %s\n", i+1, len(t.Steps), step.Fn.Name())
				cfg := &SandboxConfig{
					Ctx:        ctx,
					Arch:       opts.Arch,
					Container:  taskContainer,
					Sandbox:    unit.Sandbox,
					Shell:      unit.Shell,
					SrcDir:     srcDir,
					DestDir:    destDir,
					Sysroot:    sysroot,
					Env:        env,
					ProjectDir: opts.ProjectDir,
					HostDir:    hostDir,
					CacheDirs:  cacheDirs,
					Stdout:     logW,
					Stderr:     logW,
				}
				thread := NewBuildThread(ctx, cfg, RealExecer{})
				if _, err := starlark.Call(thread, step.Fn, nil, nil); err != nil {
					if !opts.Verbose {
						fmt.Fprintf(w, "  build log: %s\n", logPath)
					}
					return fmt.Errorf("task %s: %w", t.Name, err)
				}
			}
		}
	}

	// Package the output into an .apk and publish to the local repo.
	// Then stage destdir for downstream units' per-unit sysroots.
	if unit.Class != "image" && unit.Class != "container" {
		archDir := RepoArchDir(unit, opts.Arch)
		var (
			apkPath string
			err     error
		)
		if unit.PassthroughAPK != "" {
			// Re-sign the upstream apk verbatim — keeps Alpine's PKGINFO
			// and install scripts intact. The tasks above still run so
			// destdir is populated for downstream units' sysroots.
			srcAPK := filepath.Join(srcDir, unit.PassthroughAPK)
			apkPath, err = artifact.RepackAPK(unit, srcAPK, filepath.Join(buildDir, "pkg"), opts.Signer)
			if err != nil {
				return fmt.Errorf("repacking upstream apk: %w", err)
			}
			// Honor upstream PKGINFO's arch when publishing — noarch
			// packages must land in <repo>/noarch/ regardless of the
			// build arch, otherwise apk's solver can't find them.
			if a, aerr := artifact.ReadAPKArch(srcAPK); aerr == nil && a != "" {
				archDir = a
			}
		} else {
			apkPath, err = artifact.CreateAPK(unit, destDir, filepath.Join(buildDir, "pkg"), archDir, opts.ProjectCommit, opts.Signer)
			if err != nil {
				return fmt.Errorf("creating apk: %w", err)
			}
		}
		fmt.Fprintf(w, "  → %s\n", filepath.Base(apkPath))

		repoDir := repo.RepoDir(proj, opts.ProjectDir)
		if err := repo.Publish(apkPath, repoDir, archDir, opts.Signer); err != nil {
			return fmt.Errorf("publishing to repo: %w", err)
		}

		if err := StageSysroot(destDir, buildDir); err != nil {
			fmt.Fprintf(w, "  (warning: sysroot staging failed: %v)\n", err)
		}
	}

	return nil
}

func filterBuildOrder(dag *resolve.DAG, fullOrder []string, names []string) ([]string, error) {
	needed := make(map[string]bool)
	for _, name := range names {
		if _, ok := dag.Nodes[name]; !ok {
			return nil, fmt.Errorf("unit %q not found", name)
		}
		needed[name] = true
		deps, _ := dag.DepsOf(name)
		for _, d := range deps {
			needed[d] = true
		}
	}

	var filtered []string
	for _, name := range fullOrder {
		if needed[name] {
			filtered = append(filtered, name)
		}
	}
	return filtered, nil
}

// blockedUnits returns units remaining in the build order that transitively
// depend on the failed unit.
func blockedUnits(dag *resolve.DAG, failed string, order []string) []string {
	rdeps, err := dag.RdepsOf(failed)
	if err != nil {
		return nil
	}
	rdepSet := make(map[string]bool, len(rdeps))
	for _, r := range rdeps {
		rdepSet[r] = true
	}
	// Return in build order for clarity
	var blocked []string
	for _, name := range order {
		if rdepSet[name] {
			blocked = append(blocked, name)
		}
	}
	return blocked
}

func dryRun(w io.Writer, proj *yoestar.Project, order []string, hashes map[string]string, opts Options, requested map[string]bool) error {
	fmt.Fprintln(w, "Dry run — would build in this order:")
	for _, name := range order {
		unit := proj.Units[name]
		sd := ScopeDir(unit, opts.Arch, opts.Machine)
		cached := ""
		forceThis := (opts.Force || opts.Clean) && (len(requested) == 0 || requested[name])
		if !forceThis && cacheValid(proj, opts.ProjectDir, unit, sd, opts.Arch, hashes[name]) {
			cached = " [cached, skip]"
		}
		fmt.Fprintf(w, "  %-20s [%s] %s%s\n", name, unit.Class, hashes[name][:12], cached)
	}
	return nil
}

// hasTask returns true if the unit has a task with the given name.
func hasTask(unit *yoestar.Unit, name string) bool {
	for _, t := range unit.Tasks {
		if t.Name == name {
			return true
		}
	}
	return false
}

// resolveContainerImage returns the Docker image tag for a unit's container.
// For container units (referenced by name), the tag is yoe/<name>:<version>-<arch>.
// For external images (containing ":" or "/"), the value is used directly.
func resolveContainerImage(proj *yoestar.Project, unit *yoestar.Unit, arch string) string {
	container := unit.Container
	if container == "" {
		return ""
	}

	// External image reference (e.g., "golang:1.23")
	if strings.Contains(container, ":") || strings.Contains(container, "/") {
		return container
	}

	// Container unit — look up version and build tag.
	// Always include arch in tag for explicitness.
	if cu, ok := proj.Units[container]; ok {
		imageArch := arch
		if unit.ContainerArch == "host" {
			imageArch = Arch()
		}
		return fmt.Sprintf("yoe/%s:%s-%s", container, cu.Version, imageArch)
	}

	return container
}

// removeDirRobust removes dir and its contents. If RemoveAll fails (typically
// because a previous failed image build left root-owned files behind), it
// attempts to chown the tree back to the host user via the container, then
// retries. Returns an error if the directory cannot be removed.
func removeDirRobust(ctx context.Context, dir, projectDir, image string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		return nil
	}
	if cerr := chownDirToHost(ctx, dir, projectDir, image); cerr != nil {
		return fmt.Errorf("%w (and ownership recovery failed: %v)", err, cerr)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing after ownership recovery: %w", err)
	}
	return nil
}

// chownDirToHost runs chown -R uid:gid on dir inside the container, where
// the container has the privilege to chown root-owned files. Used to recover
// destdir ownership after a failed image build (image class chowns rootfs to
// root for mkfs.ext4 -d). No-op if dir does not exist.
func chownDirToHost(ctx context.Context, dir, projectDir, image string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	if image == "" {
		return fmt.Errorf("no container image available for ownership recovery of %s", dir)
	}
	parent := filepath.Dir(dir)
	base := filepath.Base(dir)
	uid := os.Getuid()
	gid := os.Getgid()
	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Ctx:        ctx,
		Image:      image,
		Command:    fmt.Sprintf("chown -R %d:%d /__yoe_cleanup/%s", uid, gid, base),
		ProjectDir: projectDir,
		Mounts:     []yoe.Mount{{Host: parent, Container: "/__yoe_cleanup"}},
		NoUser:     true,
		Quiet:      true,
	})
}

// --- Simple file-based cache ---

func CacheMarkerPath(projectDir, arch, name, hash string) string {
	return filepath.Join(UnitBuildDir(projectDir, arch, name), ".yoe-hash")
}

func IsBuildCached(projectDir, arch, name, hash string) bool {
	data, err := os.ReadFile(CacheMarkerPath(projectDir, arch, name, hash))
	if err != nil {
		return false
	}
	return string(data) == hash
}

// cacheValid reports whether a unit's cached build is still usable. The cache
// marker alone is not sufficient: for units that publish an .apk, the marker
// can outlive the apk (deleted manually, or written racily by a parallel run
// while the actual build was cancelled). When the apk is gone, the cache is
// stale and the unit must be rebuilt.
// cacheValid takes both scopeDir (build-tree subdir, may be a machine name)
// and arch (the actual target architecture) because they diverge for
// machine-scoped units: the build cache lives under build/<machine>/, but
// the apk lives under repo/.../<arch>/.
func cacheValid(proj *yoestar.Project, projectDir string, unit *yoestar.Unit, scopeDir, arch, hash string) bool {
	if !IsBuildCached(projectDir, scopeDir, unit.Name, hash) {
		return false
	}
	if unit.Class == "image" || unit.Class == "container" {
		return true
	}
	archDir := RepoArchDir(unit, arch)
	apkName := fmt.Sprintf("%s-%s-r%d.apk", unit.Name, unit.Version, unit.Release)
	_, err := os.Stat(filepath.Join(repo.RepoDir(proj, projectDir), archDir, apkName))
	return err == nil
}

func HasBuildLog(projectDir, arch, name string) bool {
	_, err := os.Stat(filepath.Join(UnitBuildDir(projectDir, arch, name), "build.log"))
	return err == nil
}

// BuildingLockPath returns the path of the lock file written during a build.
func BuildingLockPath(projectDir, arch, name string) string {
	return filepath.Join(UnitBuildDir(projectDir, arch, name), ".lock")
}

// IsBuildInProgress returns true if another process is currently building this unit.
// It checks for the lock file and verifies the PID is still alive.
func IsBuildInProgress(projectDir, arch, name string) bool {
	data, err := os.ReadFile(BuildingLockPath(projectDir, arch, name))
	if err != nil {
		return false
	}
	pid := strings.TrimSpace(string(data))
	// Check if the process is still running
	_, err = os.Stat(fmt.Sprintf("/proc/%s", pid))
	return err == nil
}

func writeCacheMarker(projectDir, arch, name, hash string) {
	path := CacheMarkerPath(projectDir, arch, name, hash)
	EnsureDir(filepath.Dir(path))
	os.WriteFile(path, []byte(hash), 0644)
}


// readProjectCommit returns the trimmed output of `git rev-parse HEAD` run
// in projectDir. Returns "" if the directory isn't a git repo, git isn't
// installed, or the command fails for any other reason — apks just omit the
// `commit` PKGINFO field in that case.
func readProjectCommit(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = projectDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// repoRelPath returns the repo directory path relative to the project root.
func repoRelPath(proj *yoestar.Project, projectDir string) string {
	repoDir := repo.RepoDir(proj, projectDir)
	rel, err := filepath.Rel(projectDir, repoDir)
	if err != nil {
		return "repo"
	}
	return rel
}
