package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	yoe "github.com/yoebuild/yoe/internal"
	"github.com/yoebuild/yoe/internal/artifact"
	"github.com/yoebuild/yoe/internal/deb"
	"github.com/yoebuild/yoe/internal/repo"
	"github.com/yoebuild/yoe/internal/resolve"
	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
	"go.starlark.net/starlark"
)

// DefaultParallel is the number of units BuildUnits builds concurrently
// when Options.Parallel is unset (<= 0) and local.star declares nothing.
const DefaultParallel = yoestar.DefaultParallelBuilds

// Options controls build behavior.
// BuildEvent is sent to Options.OnEvent during a build.
type BuildEvent struct {
	Unit   string
	Status string // "cached", "building", "done", "failed"
}

type Options struct {
	Ctx        context.Context // optional; nil means background
	Force      bool            // rebuild even if cached
	Clean      bool            // delete build dir before rebuilding (implies Force)
	NoCache    bool            // skip all caches
	DryRun     bool            // show what would be built
	Verbose    bool            // show build output in console (default: log only)
	ProjectDir string          // project root
	Arch       string          // target architecture
	Machine    string          // target machine name
	// ProjectCommit is the git rev-parse HEAD of ProjectDir, captured once
	// per build so PKGINFO records build provenance. Empty means "not a git
	// repo" or "couldn't determine" — the apk omits the `commit` field then.
	ProjectCommit string
	// Signer holds the project's RSA signing key, loaded once per build so
	// each apk and the APKINDEX can be signed without per-call key I/O.
	// Nil means "build unsigned apks" — apk add then needs --allow-untrusted.
	Signer  *artifact.Signer
	OnEvent func(BuildEvent) // optional callback for build progress
	// Parallel caps how many units build concurrently. Values <= 0 fall
	// back to DefaultParallel; 1 forces fully sequential builds.
	Parallel int
	// EffectiveDistro is the consuming image's effective distro. When
	// set, it folds into every unit's input hash so an untagged source
	// unit consumed by both an alpine and a debian image hashes
	// differently per consumer. Empty falls back to the project-level
	// EffectiveDistro() (DefaultDistroOverride -> DefaultDistro). Per-
	// image callers should pass the image's value explicitly via
	// proj.EffectiveDistroForImage(name).
	EffectiveDistro string
}

// syncWriter serializes concurrent writes from parallel unit builds so
// orchestration lines (and verbose subprocess output) stay intact rather
// than interleaving mid-line on the shared destination (stdout / a log).
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
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

	// Make the project's public key available under
	// <repo>/<distro>/keys/ before any unit's tasks run. Units that ship
	// the key (base-files puts it under /etc/apk/keys/ in the rootfs)
	// need it on disk during their own build, not after the first apk
	// is published. Idempotent — Publish would rewrite the same bytes
	// later. The pubkey lives under the per-distro subtree so each
	// backend (apk + deb) owns its own key surface — Alpine's RSA key
	// here, Debian's GPG key under debian/. Effective distro derivation
	// moved up so it's available for this early bootstrap step.
	effectiveDistro := opts.EffectiveDistro
	if effectiveDistro == "" {
		var derr error
		effectiveDistro, derr = proj.EffectiveDistro()
		if derr != nil {
			return fmt.Errorf("resolving effective distro for build: %w", derr)
		}
		opts.EffectiveDistro = effectiveDistro
	}
	repoDir := repo.RepoDistroDir(proj, opts.ProjectDir, effectiveDistro)
	if err := repo.WritePublicKey(repoDir, opts.Signer); err != nil {
		return fmt.Errorf("publishing project public key: %w", err)
	}

	// BuildDAG iterates the per-distro view so cross-distro same-name
	// collisions resolve to the variant the consuming distro expects.
	dag, err := resolve.BuildDAG(proj, effectiveDistro)
	if err != nil {
		return err
	}

	// Determine build order
	order, err := dag.TopologicalSort()
	if err != nil {
		return err
	}

	// Compute hashes for cache. Pin units pass empty (cache-neutral);
	// dev units fold in HEAD sha and, when the work tree is dirty,
	// the dirty diff sha so an in-place edit invalidates the cache.
	//
	// The persisted BuildMeta.SourceState only ever records the
	// toggle decision ("dev"); the dev-mod / dev-dirty refinement is
	// a live observation. We therefore read the persisted state to
	// decide *whether* the unit is under user control, and then run
	// source.DetectState on the actual src dir to discover the live
	// state — without that step, an uncommitted edit didn't change
	// the hash and the build was served from cache, silently
	// dropping the user's edits.
	// effectiveDistro is already resolved above (the WritePublicKey
	// path needs it). Re-use the pinned value rather than re-deriving.
	srcInputs := SrcInputsFn(opts.ProjectDir, opts.Arch, opts.Machine, effectiveDistro)
	hashes, err := resolve.ComputeAllHashes(dag, opts.Arch, opts.Machine, srcInputs, effectiveDistro)
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
		unit := proj.LookupUnit(effectiveDistro, name)
		sd := ScopeDir(unit, opts.Arch, opts.Machine)
		forceThis := (opts.Force || opts.Clean) && (len(requested) == 0 || requested[name])
		if !forceThis && !opts.NoCache && cacheValid(proj, opts.ProjectDir, unit, sd, opts.Arch, hash, effectiveDistro) {
			notify(name, "cached")
		} else {
			notify(name, "waiting")
		}
	}

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	parallel := opts.Parallel
	if parallel <= 0 {
		parallel = DefaultParallel
	}

	// Serialize the shared destination so parallel workers don't interleave
	// mid-line. buildOne layers its own per-unit executor.log on top of this.
	sw := &syncWriter{w: w}

	// orderSet bounds dependency accounting to units actually in this build
	// (a filtered build only includes a unit and its transitive deps).
	orderSet := make(map[string]bool, len(order))
	for _, name := range order {
		orderSet[name] = true
	}

	// indeg[name] = number of not-yet-finished deps that are part of this
	// build. A unit becomes schedulable when it reaches zero — that is the
	// parallel analogue of the old topological for-loop.
	indeg := make(map[string]int, len(order))
	for _, name := range order {
		c := 0
		for _, d := range dag.Nodes[name].Deps {
			if orderSet[d] {
				c++
			}
		}
		indeg[name] = c
	}

	var (
		mu sync.Mutex
		// rebuilt: units actually rebuilt this run, so dependents invalidate
		// their cache even when input hashes are unchanged. Read/written by
		// workers, so guarded by mu.
		rebuilt  = map[string]bool{}
		started  = map[string]bool{}
		firstErr error
		stop     bool // set on first failure or ctx cancel; no new work scheduled
	)

	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var launchReady func() // declared first; recurses via worker completion

	worker := func(name string) {
		defer wg.Done()
		// Bound concurrent *work* here, not goroutine creation, so the
		// scheduler never blocks holding mu (which would deadlock).
		sem <- struct{}{}
		defer func() { <-sem }()

		mu.Lock()
		if stop {
			mu.Unlock()
			return
		}
		depRebuilt := false
		for _, d := range dag.Nodes[name].Deps {
			if rebuilt[d] {
				depRebuilt = true
				break
			}
		}
		mu.Unlock()

		unit := proj.LookupUnit(effectiveDistro, name)
		hash := hashes[name]
		sd := ScopeDir(unit, opts.Arch, opts.Machine)

		if err := ctx.Err(); err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = fmt.Errorf("build cancelled")
			}
			stop = true
			mu.Unlock()
			return
		}

		// --force/--clean only apply to explicitly requested units;
		// dependencies still use the cache.
		forceThis := (opts.Force || opts.Clean) && (len(requested) == 0 || requested[name])

		built := false
		if !forceThis && !opts.NoCache && !depRebuilt &&
			cacheValid(proj, opts.ProjectDir, unit, sd, opts.Arch, hash, effectiveDistro) {
			fmt.Fprintf(sw, "%-20s ⚡ [cached] %s\n", name, hash[:12])
		} else {
			fmt.Fprintf(sw, "%-20s 🔨 [building]\n", name)
			notify(name, "building")
			if err := buildOne(ctx, proj, dag, unit, hash, opts, sw); err != nil {
				notify(name, "failed")
				fmt.Fprintf(sw, "%-20s ❌ [failed] %v\n", name, err)
				blocked := blockedUnits(dag, name, order)
				if len(blocked) > 0 {
					fmt.Fprintf(sw, "  the following units depend on %s and cannot be built:\n", name)
					for _, b := range blocked {
						fmt.Fprintf(sw, "    - %s\n", b)
					}
				}
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("building %s: %w", name, err)
				}
				stop = true
				mu.Unlock()
				return
			}
			writeCacheMarker(opts.ProjectDir, sd, name, hash, effectiveDistro)
			fmt.Fprintf(sw, "%-20s ✅ [done] %s\n", name, hash[:12])
			notify(name, "done")
			built = true
		}

		// Mark finished, release dependents, and schedule whatever that
		// unblocked. Done under mu so indeg/rebuilt stay consistent.
		mu.Lock()
		if built {
			rebuilt[name] = true
		}
		for _, rd := range dag.Nodes[name].Rdeps {
			if orderSet[rd] {
				indeg[rd]--
			}
		}
		launchReady()
		mu.Unlock()
	}

	// launchReady starts every not-yet-started unit whose deps are all
	// finished. Caller must hold mu. Goroutines are cheap; the semaphore in
	// worker() is what actually bounds concurrency.
	launchReady = func() {
		if stop {
			return
		}
		for _, name := range order {
			if !started[name] && indeg[name] == 0 {
				started[name] = true
				wg.Add(1)
				go worker(name)
			}
		}
	}

	mu.Lock()
	launchReady()
	mu.Unlock()
	wg.Wait()

	if firstErr != nil {
		// The failing unit and its blocked dependents were already
		// reported to the writer by the worker that hit the error.
		return firstErr
	}
	return nil
}

// buildLogTailLines bounds how much of a failed unit's build.log is echoed
// inline. Enough to capture the compiler/configure error and its context
// without burying the terminal (or a CI log) in the full transcript; the
// path is always printed so the complete log stays one open away.
const buildLogTailLines = 50

// reportBuildFailure surfaces a failed unit's build.log at the point of
// failure. In verbose mode the log was already streamed to the terminal as
// it ran, so only the path is noted. Otherwise the log lived only on disk —
// useless when the build ran somewhere ephemeral like CI, where the runner
// (and its filesystem) is discarded after the job — so echo the tail inline
// so the actual error is visible from stdout alone.
func reportBuildFailure(w io.Writer, unitName, taskName, logPath string, verbose bool) {
	// Lead with the failing unit and task. Parallel builds interleave task
	// lines from several units on the shared writer, and the log path names a
	// scope dir that may not obviously match the unit, so without this header
	// the reader cannot tell which unit actually failed.
	fmt.Fprintf(w, "  ❌ FAILED: %s task: %s\n", unitName, taskName)
	fmt.Fprintf(w, "  build log: %s\n", logPath)
	if verbose {
		return
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return
	}
	if len(lines) > buildLogTailLines {
		lines = lines[len(lines)-buildLogTailLines:]
		fmt.Fprintf(w, "  ── build.log (last %d lines) ──\n", buildLogTailLines)
	} else {
		fmt.Fprintf(w, "  ── build.log ──\n")
	}
	for _, ln := range lines {
		fmt.Fprintf(w, "  │ %s\n", ln)
	}
	fmt.Fprintf(w, "  ──\n")
}

func buildOne(ctx context.Context, proj *yoestar.Project, dag *resolve.DAG, unit *yoestar.Unit, hash string, opts Options, w io.Writer) (buildErr error) {
	sd := ScopeDir(unit, opts.Arch, opts.Machine)
	distro := opts.EffectiveDistro
	buildDir := UnitBuildDir(opts.ProjectDir, sd, unit.Name, distro)
	EnsureDir(buildDir)

	// Skip if another process is already building this unit.
	if IsBuildInProgress(opts.ProjectDir, sd, unit.Name, distro) {
		fmt.Fprintf(w, "  ⏭️  %s: build already in progress, skipping\n", unit.Name)
		return nil
	}

	// Remove the cache marker before starting so a cancelled or failed
	// build does not leave a stale marker that makes it appear cached.
	os.Remove(CacheMarkerPath(opts.ProjectDir, sd, unit.Name, hash, distro))

	// Write a lock file so other yoe instances can detect an in-progress build.
	lockPath := BuildingLockPath(opts.ProjectDir, sd, unit.Name, distro)
	os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(lockPath)

	// Write initial build metadata; update on completion.
	buildStart := time.Now()
	meta := initBuildMeta(buildDir, hash, buildStart)
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
		// Persist live source state so the TUI can render pin/dev
		// Persist the toggle decision, not a live observation. The
		// build itself runs configure/make/etc. which sprinkles
		// untracked artifacts in the src tree — DetectState would see
		// those as dev-dirty, but the user never toggled to dev, so
		// the persisted state should stay pin. Only DevToUpstream and
		// DevToPin change the toggle decision; the build write just
		// records whichever was already in effect.
		cachedState := source.State(meta.SourceState)
		if cachedState == source.StateEmpty {
			cachedState = source.StatePin
		}
		if next := finalizeSourceState(filepath.Join(buildDir, "src"), cachedState); next != source.StateEmpty {
			meta.SourceState = string(next)
		}
		// For dev units, capture `git describe --dirty --always` so the
		// TUI's SOURCE line and the build log can show a meaningful
		// reference (e.g. v3.4.1-3-gabc1234-dirty). Empty for pin units
		// — there's nothing useful to describe against the upstream tag.
		if source.IsDev(source.State(meta.SourceState)) {
			meta.SourceDescribe = source.SrcDescribe(filepath.Join(buildDir, "src"))
		}
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
	containerImage := resolveContainerImage(proj, unit, opts.Arch, opts.EffectiveDistro)

	// No post-image chown-back-to-host defer. The image class deliberately
	// preserves per-file ownership from each apk's tar headers so that
	// destdir/rootfs inspects with the same uid/gid the booted system
	// sees — see docs/security.md and docs/comparisons.md. The next
	// build's removeDirRobust below handles cleanup via the container if
	// host-side RemoveAll hits EACCES on root- or service-user-owned
	// files; that's slower than a plain rm but correct, and it's what
	// makes the visibility-vs-cleanup tradeoff workable.

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
		// Look up the unit's previous BuildMeta so Prepare can honor
		// dev-mode state without re-running source.DetectState
		// itself — the cache is the trusted signal here, set by the
		// internal/dev.go toggle.
		var cachedSourceState string
		if meta := ReadMeta(buildDir); meta != nil {
			cachedSourceState = meta.SourceState
		}
		if _, err := source.Prepare(opts.ProjectDir, sd, opts.EffectiveDistro, unit, cachedSourceState, w); err != nil {
			return fmt.Errorf("preparing source: %w", err)
		}
	} else {
		EnsureDir(srcDir)
	}

	// Skip only when there's genuinely nothing to produce. Companion
	// units with no tasks but services=[...] need to fall through to
	// CreateAPK so materializeServiceSymlinks bakes the runlevel
	// symlinks; the task loop below tolerates an empty Tasks slice.
	if len(unit.Tasks) == 0 && len(unit.Services) == 0 {
		fmt.Fprintf(w, "  (no tasks for %s class %q)\n", unit.Name, unit.Class)
		return nil
	}

	// Assemble per-unit sysroot from transitive deps
	sysroot := filepath.Join(buildDir, "sysroot")
	if err := AssembleSysroot(sysroot, dag, unit.Name, opts.ProjectDir, opts.Arch, distro); err != nil {
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
		"PREFIX":  "/usr",
		"DESTDIR": "/build/destdir",
		"NPROC":   NProc(),
		"ARCH":    opts.Arch,
		"MACHINE": opts.Machine,
		// The consuming image's effective distro, so a build-twice
		// source unit can branch on it (e.g. base-files giving root a
		// bash login shell on Debian but the busybox /bin/sh on
		// Alpine). Already a unit-hash input, so this only surfaces what
		// the cache key already distinguishes.
		"DISTRO":  opts.EffectiveDistro,
		"CONSOLE": console,
		"HOME":    "/tmp",
		"PATH":    "/build/sysroot/usr/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		// Debian's multiarch layout puts arch-specific libs, .pc files,
		// and the core dynamic loader under /usr/lib/<tuple>/ and
		// /lib/<tuple>/. Include those alongside the legacy /usr/lib
		// paths so debian-feed deps (liblzma-dev's liblzma.pc,
		// libssl-dev's libssl.pc, libc6's ld-linux) are visible to
		// pkg-config / ld / rtld during builds. Alpine ignores the
		// multiarch paths since they don't exist in its sysroot.
		"PKG_CONFIG_PATH": fmt.Sprintf("/build/sysroot/usr/lib/pkgconfig:/build/sysroot/usr/lib/%s/pkgconfig:/usr/lib/pkgconfig:/usr/lib/%s/pkgconfig", multiarchTuple(opts.Arch), multiarchTuple(opts.Arch)),
		"CFLAGS":          "-I/build/sysroot/usr/include",
		"CPPFLAGS":        "-I/build/sysroot/usr/include",
		"LDFLAGS":         fmt.Sprintf("-L/build/sysroot/usr/lib -L/build/sysroot/usr/lib/%s -L/build/sysroot/lib/%s", multiarchTuple(opts.Arch), multiarchTuple(opts.Arch)),
		"LD_LIBRARY_PATH": fmt.Sprintf("/build/sysroot/usr/lib:/build/sysroot/usr/lib/%s:/build/sysroot/lib/%s", multiarchTuple(opts.Arch), multiarchTuple(opts.Arch)),
		"PYTHONPATH":      "/build/sysroot/usr/lib/python3.12/site-packages",
		"REPO":            filepath.Join("/project", repoRelPath(proj, opts.ProjectDir), opts.EffectiveDistro),
	}

	// Expose the release codename to the build as $SUITE so the image
	// class's mmdebstrap invocation targets the same suite the repo
	// emitter stamps, both sourced from the project's apt_feed. Only
	// meaningful for apt-family distros (Debian, Ubuntu); an alpine build
	// has no apt_feed and skips it. Errors loudly if an apt build can't
	// resolve a suite — the rootfs assembly can't proceed without one.
	if yoestar.IsAptFamily(opts.EffectiveDistro) {
		suite, serr := proj.SuiteForDistro(opts.EffectiveDistro)
		if serr != nil {
			return fmt.Errorf("resolving %s suite: %w", opts.EffectiveDistro, serr)
		}
		env["SUITE"] = suite
	}

	// Expose the project's signing key info so units that need to ship the
	// public key (e.g., base-files installs it under /etc/apk/keys/) can
	// find it without hard-coding paths. YOE_KEYS_DIR is a directory; the
	// key file is YOE_KEY_NAME inside it. Both are unset when the build
	// runs without a Signer (apk add then needs --allow-untrusted).
	if opts.Signer != nil {
		env["YOE_KEYS_DIR"] = filepath.Join("/project", repoRelPath(proj, opts.ProjectDir), opts.EffectiveDistro, "keys")
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
	projectName, projectVersion := "", ""
	if proj != nil {
		projectName = proj.Name
		projectVersion = proj.Version
	}
	tctxData := BuildTemplateContext(unit, opts.Arch, opts.Machine, console, projectName, projectVersion)

	// Debian image assembly reads the project repo's Packages index as
	// mmdebstrap's copy: source. Regenerate it from the current pool
	// before assembling so the index can never lag the pool: the index
	// is otherwise only rewritten when a unit publishes a .deb, so an
	// image-only rebuild (nothing published) reuses whatever the last
	// publish left. A stale stanza for a .deb that has since been
	// removed makes apt resolve to a version whose file no longer
	// exists and abort the whole rootfs ("Failed to stat ... No such
	// file or directory"). GenerateDebianIndex scans the pool, so a
	// refresh here always matches what is actually on disk.
	if unit.Class == "image" && yoestar.IsAptFamily(opts.EffectiveDistro) {
		suite, serr := proj.SuiteForDistro(opts.EffectiveDistro)
		if serr != nil {
			return fmt.Errorf("refresh %s index: %w", opts.EffectiveDistro, serr)
		}
		if err := repo.GenerateDebianIndex(repo.DebRepoOptions{
			RepoDir:    repo.RepoDistroDir(proj, opts.ProjectDir, opts.EffectiveDistro),
			Suite:      suite,
			Components: []string{"main"},
			Arches:     []string{"amd64", "arm64"},
		}); err != nil {
			return fmt.Errorf("refresh %s index: %w", opts.EffectiveDistro, err)
		}
	}

	// Execute tasks
	for ti, t := range unit.Tasks {
		// Lead with the unit name (same column as the [building]/[done]
		// status lines): parallel builds interleave these task lines from
		// several units on the shared writer, so the name is what tells you
		// which unit a given task line belongs to.
		fmt.Fprintf(w, "%-20s [%d/%d] task: %s\n", unit.Name, ti+1, len(unit.Tasks), t.Name)
		fmt.Fprintf(logW, "  task: %s (%d steps)\n", t.Name, len(t.Steps))

		// Per-task container override
		taskContainer := containerImage
		if t.Container != "" {
			taskUnit := *unit
			taskUnit.Container = t.Container
			taskContainer = resolveContainerImage(proj, &taskUnit, opts.Arch, opts.EffectiveDistro)
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
					reportBuildFailure(w, unit.Name, t.Name, logPath, opts.Verbose)
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
					reportBuildFailure(w, unit.Name, t.Name, logPath, opts.Verbose)
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
					reportBuildFailure(w, unit.Name, t.Name, logPath, opts.Verbose)
					return fmt.Errorf("task %s: %w", t.Name, err)
				}
			}
		}
	}

	// Package the output and publish to the local repo. Then stage
	// destdir for downstream units' per-unit sysroots.
	//
	// Branch by the consuming image's effective distro, not the unit's
	// own Distro tag. A source unit visible to every distro (Distro
	// unset) builds once per distro that reaches it (the build-twice
	// model) and packages in that distro's native format: .deb for a
	// Debian image, .apk otherwise — so module-core's bash becomes a
	// .deb in a Debian closure and a .apk in an Alpine closure. Feed
	// passthrough units only ever appear in their own distro's closure,
	// so EffectiveDistro matches their tag and the branch is unchanged
	// for them.
	if unit.Class != "image" && unit.Class != "container" {
		switch {
		case yoestar.IsAptFamily(opts.EffectiveDistro):
			if err := packageDeb(unit, destDir, srcDir, buildDir, opts, proj, w); err != nil {
				return fmt.Errorf("packaging deb: %w", err)
			}
		default:
			if err := packageAPK(unit, destDir, sysroot, srcDir, buildDir, opts, proj, w); err != nil {
				return err
			}
		}

		if err := StageSysroot(destDir, buildDir); err != nil {
			fmt.Fprintf(w, "  ⚠️  (warning: sysroot staging failed: %v)\n", err)
		}
	}

	return nil
}

// packageAPK is the alpine-side packaging branch — extracted from the
// inline body when the deb branch was added. Repacks the upstream apk
// when PassthroughAPK is set, else builds a fresh apk from destdir.
func packageAPK(unit *yoestar.Unit, destDir, sysroot, srcDir, buildDir string, opts Options, proj *yoestar.Project, w io.Writer) error {
	archDir := RepoArchDir(unit, opts.Arch)
	var (
		apkPath string
		err     error
	)
	if unit.PassthroughAPK != "" {
		srcAPK := filepath.Join(srcDir, unit.PassthroughAPK)
		apkPath, err = artifact.RepackAPK(unit, srcAPK, filepath.Join(buildDir, "pkg"), opts.Signer)
		if err != nil {
			return fmt.Errorf("repacking upstream apk: %w", err)
		}
		if a, aerr := artifact.ReadAPKArch(srcAPK); aerr == nil && a != "" {
			archDir = a
		}
	} else {
		apkPath, err = artifact.CreateAPK(unit, destDir, sysroot, filepath.Join(buildDir, "pkg"), archDir, opts.ProjectCommit, opts.Signer)
		if err != nil {
			return fmt.Errorf("creating apk: %w", err)
		}
	}
	fmt.Fprintf(w, "  📦 %s\n", filepath.Base(apkPath))

	repoDir := repo.RepoDistroDir(proj, opts.ProjectDir, opts.EffectiveDistro)
	if err := repo.Publish(apkPath, repoDir, archDir, opts.Signer); err != nil {
		return fmt.Errorf("publishing to repo: %w", err)
	}
	return nil
}

// packageDeb is the debian-side packaging branch. PassthroughDeb units
// (mirror-verbatim from a apt_feed) copy the upstream .deb into the
// project pool. Project source units run dpkg-deb --build over destDir.
func packageDeb(unit *yoestar.Unit, destDir, srcDir, buildDir string, opts Options, proj *yoestar.Project, w io.Writer) error {
	pkgDir := filepath.Join(buildDir, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return fmt.Errorf("mkdir pkg: %w", err)
	}

	var debPath string
	if unit.PassthroughDeb != "" {
		// Mirror-verbatim: copy the upstream .deb out of srcDir to
		// the project pool. R15 SHA256 verification fires here.
		src := filepath.Join(srcDir, unit.PassthroughDeb)
		if unit.SHA256 != "" {
			if err := repo.VerifyMirrorSHA256(src, unit.SHA256); err != nil {
				return err
			}
		}
		debPath = filepath.Join(pkgDir, unit.PassthroughDeb)
		if err := copyDebFile(src, debPath); err != nil {
			return fmt.Errorf("copy passthrough deb: %w", err)
		}
	} else {
		// Build from destDir. Derive control fields from the unit's
		// metadata; for v1 we use the unit name, version, runtime
		// deps, and project maintainer.
		debArch := debArchForYoe(opts.Arch)
		fname := fmt.Sprintf("%s_%s_%s.deb", unit.Name, unit.Version, debArch)
		debPath = filepath.Join(pkgDir, fname)

		c := deb.Control{
			Package:      unit.Name,
			Version:      unit.Version,
			Architecture: debArch,
			Maintainer:   "Yoe <build@yoe.local>",
			Description:  unit.Description,
			Depends:      strings.Join(unit.RuntimeDeps, ", "),
			Provides:     debProvides(unit.Provides, unit.Version),
		}
		if c.Description == "" {
			c.Description = unit.Name
		}
		// Bake systemd service wants symlinks before dpkg-deb sees
		// destDir (yoe's "services follow packages" pattern).
		if err := deb.MaterializeSystemdServiceSymlinks(destDir, "", unit.Services); err != nil {
			return fmt.Errorf("service symlinks: %w", err)
		}
		if err := deb.BuildDeb(destDir, c, debPath, ""); err != nil {
			return fmt.Errorf("BuildDeb: %w", err)
		}
	}
	fmt.Fprintf(w, "  📦 %s\n", filepath.Base(debPath))

	// Publish into the project pool and regenerate the per-arch
	// Packages + Release + InRelease at repo/<project>/<distro>/.
	suite, err := proj.SuiteForDistro(opts.EffectiveDistro)
	if err != nil {
		return fmt.Errorf("packaging deb: %w", err)
	}
	repoDir := repo.RepoDistroDir(proj, opts.ProjectDir, opts.EffectiveDistro)
	publishOpts := repo.DebRepoOptions{
		RepoDir:    repoDir,
		Suite:      suite,
		Components: []string{"main"},
		Arches:     []string{"amd64", "arm64"},
	}
	if err := repo.PublishDeb(debPath, publishOpts, "main"); err != nil {
		return fmt.Errorf("PublishDeb: %w", err)
	}
	return nil
}

// debProvides emits a unit's Provides as versioned self-provides:
// "libssl3" with unit version 3.4.1 becomes "libssl3 (= 3.4.1)". apt
// enforces that an unversioned Provides cannot satisfy a versioned
// Depends (e.g. a feed package's "libssl3 (>= 3.0.0)"), so a
// source-built unit that owns a SONAME-style virtual must declare the
// version it provides or apt rejects the closure as unmet. An entry
// that already carries an explicit "(...)" version is passed through
// untouched. The Alpine path is intentionally not versioned this way —
// apk resolves library deps through auto-generated SONAME provides, and
// the manual names there only claim Alpine's package names to avoid
// file conflicts.
func debProvides(provides []string, version string) string {
	if len(provides) == 0 {
		return ""
	}
	out := make([]string, 0, len(provides))
	for _, p := range provides {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "(") {
			out = append(out, p) // already versioned/qualified
			continue
		}
		out = append(out, fmt.Sprintf("%s (= %s)", p, version))
	}
	return strings.Join(out, ", ")
}

func debArchForYoe(yoeArch string) string {
	switch yoeArch {
	case "x86_64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return yoeArch
	}
}

func copyDebFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
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
		unit := proj.LookupUnit(opts.EffectiveDistro, name)
		sd := ScopeDir(unit, opts.Arch, opts.Machine)
		cached := ""
		forceThis := (opts.Force || opts.Clean) && (len(requested) == 0 || requested[name])
		if !forceThis && cacheValid(proj, opts.ProjectDir, unit, sd, opts.Arch, hashes[name], opts.EffectiveDistro) {
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
//
// Per R9 (toolchain dispatch via provides + distro), a virtual reference
// like Container="toolchain" is dereferenced through the project's
// Provides table to a concrete container unit. The dispatch distro is
// the unit's own Distro tag when set; otherwise effectiveDistro (the
// consuming image's, or the project default for image-less builds) is
// used so untagged source units like module-core's `file` route through
// the correct backend toolchain instead of the global Provides table's
// alphabetical first.
func resolveContainerImage(proj *yoestar.Project, unit *yoestar.Unit, arch, effectiveDistro string) string {
	container := unit.Container
	if container == "" {
		return ""
	}

	// External image reference (e.g., "golang:1.23")
	if strings.Contains(container, ":") || strings.Contains(container, "/") {
		return container
	}

	// Distro context: unit's own tag wins; otherwise fall back to the
	// caller-supplied effectiveDistro so untagged source units still
	// dispatch to the right backend toolchain.
	distroCtx := unit.Distro
	if distroCtx == "" {
		distroCtx = effectiveDistro
	}

	// Virtual reference — dereference through Provides to the concrete
	// container unit, distro-aware per R9. Looks like Container="toolchain"
	// -> "toolchain-glibc" (debian) or "toolchain-musl" (alpine). Falls
	// through to literal interpretation when no provider exists,
	// preserving back-compat for Container="toolchain-musl" literal
	// references.
	if resolved := proj.ResolveProvidesForDistro(container, distroCtx); resolved != "" {
		container = resolved
	}

	// Container unit — look up version and build tag. Resolve in the
	// distro context: an alpine source unit picks toolchain-musl, a
	// debian source unit picks toolchain-glibc. Falls back to the
	// cross-module AnyUnit lookup when nothing matches so the literal-
	// container path (e.g. container="toolchain-musl") still finds its
	// container regardless of which distro registered it.
	cu := proj.LookupUnit(distroCtx, container)
	if cu == nil {
		cu = proj.AnyUnit(container)
	}
	if cu != nil {
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

func CacheMarkerPath(projectDir, arch, name, hash, distro string) string {
	return filepath.Join(UnitBuildDir(projectDir, arch, name, distro), ".yoe-hash")
}

func IsBuildCached(projectDir, arch, name, hash, distro string) bool {
	data, err := os.ReadFile(CacheMarkerPath(projectDir, arch, name, hash, distro))
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
func cacheValid(proj *yoestar.Project, projectDir string, unit *yoestar.Unit, scopeDir, arch, hash, distro string) bool {
	if !IsBuildCached(projectDir, scopeDir, unit.Name, hash, distro) {
		return false
	}
	if unit.Class == "image" || unit.Class == "container" {
		return true
	}
	repoBase := repo.RepoDistroDir(proj, projectDir, distro)

	// Debian units publish a .deb into the pool, not an .apk into an
	// arch dir. Confirm the pooled .deb exists; without this branch the
	// .apk stat below always misses and every debian.main passthrough
	// (and every source .deb) looks stale, rebuilding on every run. The
	// pool layout is pool/<component>/<initial>/<source>/<file>, so glob
	// three levels deep for the package filename.
	if distro == "debian" {
		debName := filepath.Base(unit.PassthroughDeb)
		if debName == "." || debName == "" {
			debName = fmt.Sprintf("%s_%s_%s.deb", unit.Name, unit.Version, debArchForYoe(arch))
		}
		matches, _ := filepath.Glob(filepath.Join(repoBase, "pool", "*", "*", "*", debName))
		return len(matches) > 0
	}

	archDir := RepoArchDir(unit, arch)
	apkName := fmt.Sprintf("%s-%s-r%d.apk", unit.Name, unit.Version, unit.Release)
	if _, err := os.Stat(filepath.Join(repoBase, archDir, apkName)); err == nil {
		return true
	}
	// Passthrough alpine_pkg units with `arch = noarch` in upstream
	// PKGINFO publish to <repo>/noarch/ regardless of the build arch
	// (apk's solver constructs fetch URLs from PKGINFO arch). The unit's
	// Scope on the Starlark side stays empty/arch, so RepoArchDir
	// returns the build arch — fall back to noarch/ before declaring
	// the cache stale.
	if _, err := os.Stat(filepath.Join(repoBase, "noarch", apkName)); err == nil {
		return true
	}
	return false
}

func HasBuildLog(projectDir, arch, name, distro string) bool {
	_, err := os.Stat(filepath.Join(UnitBuildDir(projectDir, arch, name, distro), "build.log"))
	return err == nil
}

// BuildingLockPath returns the path of the lock file written during a build.
func BuildingLockPath(projectDir, arch, name, distro string) string {
	return filepath.Join(UnitBuildDir(projectDir, arch, name, distro), ".lock")
}

// IsBuildInProgress returns true if another process is currently building this unit.
// It checks for the lock file and verifies the PID is still alive.
func IsBuildInProgress(projectDir, arch, name, distro string) bool {
	data, err := os.ReadFile(BuildingLockPath(projectDir, arch, name, distro))
	if err != nil {
		return false
	}
	pid := strings.TrimSpace(string(data))
	// Check if the process is still running
	_, err = os.Stat(fmt.Sprintf("/proc/%s", pid))
	return err == nil
}

func writeCacheMarker(projectDir, arch, name, hash, distro string) {
	path := CacheMarkerPath(projectDir, arch, name, hash, distro)
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

// SrcInputsFn returns the srcInputs callback for ComputeAllHashes: a
// per-unit function that folds dev-state observations into the unit's
// content hash. Pin units (and any unit without a persisted dev
// SourceState) return empty so they're cache-neutral; dev units return
// source.SrcHashInputs against the live working tree.
//
// Shared between the executor (build path) and the TUI (startup +
// recomputeStatuses), so both compute the same hash and IsBuildCached
// agrees about what's cached. Without sharing, the TUI passing nil
// would treat dev units as cache-neutral at startup, never matching
// the executor-written marker, and dev units would always show as
// uncached on TUI restart.
//
// distro is the consuming image's effective distro (drives the
// build-dir path); SrcInputsFn looks at the unit's BuildMeta to decide
// pin-vs-dev, which is per-distro state now that the layout splits.
func SrcInputsFn(projectDir, arch, machine, distro string) func(u *yoestar.Unit) string {
	return func(u *yoestar.Unit) string {
		sd := ScopeDir(u, arch, machine)
		buildDir := UnitBuildDir(projectDir, sd, u.Name, distro)
		persisted := source.StateEmpty
		if meta := ReadMeta(buildDir); meta != nil {
			persisted = source.State(meta.SourceState)
		}
		if !source.IsDev(persisted) {
			return ""
		}
		srcDir := filepath.Join(buildDir, "src")
		liveState, _ := source.DetectState(srcDir, persisted)
		if !source.IsDev(liveState) {
			// Persisted says dev but the live dir disagrees (user
			// wiped it, no .git, etc.). Fall back to the persisted
			// state so we still produce a stable hash component.
			liveState = persisted
		}
		return source.SrcHashInputs(srcDir, liveState)
	}
}

// finalizeSourceState returns the toggle decision to persist into
// BuildMeta.SourceState after a successful build: pin or dev (never
// dev-mod / dev-dirty — those are live refinements the watcher
// computes from the working tree, not states yoe stores).
//
// Crucially, this does NOT call DetectState. A build runs configure,
// make, and other tools that leave untracked artifacts in the src
// tree; if we observed live state here, every pin build would flip to
// dev-dirty because of those artifacts. The toggle decision lives in
// the cached value — only DevToUpstream / DevToPin change it. We
// just preserve and project that into BuildMeta after the build,
// requiring only that the src dir still exists (build wasn't aborted
// before Prepare ran).
func finalizeSourceState(srcDir string, cached source.State) source.State {
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		return source.StateEmpty
	}
	if source.IsDev(cached) {
		return source.StateDev
	}
	if cached == source.StatePin {
		return source.StatePin
	}
	return source.StateEmpty
}
