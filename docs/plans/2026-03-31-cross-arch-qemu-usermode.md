# Cross-Architecture Builds via QEMU User-Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable building arm64/riscv64 images on x86_64 hosts using QEMU
user-mode emulation, transparent to the build system.

**Architecture:** Target arch flows from the machine definition through build
options. A foreign-arch Docker container (built lazily) runs all compilation
under QEMU user-mode emulation via binfmt_misc. Build and repo directories
include arch to avoid collisions.

**Tech Stack:** Go, Docker/Podman, QEMU user-mode (binfmt_misc),
tonistiigi/binfmt

**Spec:** `docs/superpowers/specs/2026-03-31-cross-arch-qemu-usermode-design.md`

---

### Task 1: Arch-Aware Build Directories

Add arch to `UnitBuildDir` so builds for different architectures don't collide.
This is the foundational change — almost everything else depends on it.

**Files:**

- Modify: `internal/build/sandbox.go` (UnitBuildDir)
- Modify: `internal/build/executor.go` (all callers of UnitBuildDir,
  CacheMarkerPath, HasBuildLog, BuildingLockPath, IsBuildInProgress)
- Modify: `internal/device/flash.go` (findImage)
- Modify: `internal/tui/app.go` (status check, build log paths)
- Modify: `internal/build/executor_test.go`

- [ ] **Step 1: Update UnitBuildDir to accept arch**

In `internal/build/sandbox.go`, change:

```go
// UnitBuildDir returns the build directory for a unit.
func UnitBuildDir(projectDir, unitName string) string {
	return filepath.Join(projectDir, "build", unitName)
}
```

to:

```go
// UnitBuildDir returns the build directory for a unit.
func UnitBuildDir(projectDir, arch, unitName string) string {
	return filepath.Join(projectDir, "build", arch, unitName)
}
```

- [ ] **Step 2: Update all callers in executor.go**

In `internal/build/executor.go`, update every call to `UnitBuildDir` to pass
`opts.Arch`. There are multiple call sites:

Line 142 in `buildOne`:

```go
buildDir := UnitBuildDir(opts.ProjectDir, opts.Arch, unit.Name)
```

Line 201 in `AssembleSysroot` (sandbox.go):

```go
stageDir := filepath.Join(UnitBuildDir(projectDir, arch, dep), "sysroot-stage")
```

`AssembleSysroot` needs an `arch` parameter added to its signature:

```go
func AssembleSysroot(sysrootDir string, dag *resolve.DAG, unit string, projectDir string, arch string) error {
```

And its caller in executor.go (~line 218):

```go
if err := AssembleSysroot(sysroot, dag, unit.Name, opts.ProjectDir, opts.Arch); err != nil {
```

Update `CacheMarkerPath`, `IsBuildCached`, `HasBuildLog`, `BuildingLockPath`,
`IsBuildInProgress`, and `writeCacheMarker` — all use
`filepath.Join(projectDir, "build", name, ...)`. Change them to go through
`UnitBuildDir`:

```go
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

func HasBuildLog(projectDir, arch, name string) bool {
	_, err := os.Stat(filepath.Join(UnitBuildDir(projectDir, arch, name), "build.log"))
	return err == nil
}

func BuildingLockPath(projectDir, arch, name string) string {
	return filepath.Join(UnitBuildDir(projectDir, arch, name), ".lock")
}

func IsBuildInProgress(projectDir, arch, name string) bool {
	data, err := os.ReadFile(BuildingLockPath(projectDir, arch, name))
	if err != nil {
		return false
	}
	pid := strings.TrimSpace(string(data))
	_, err = os.Stat(fmt.Sprintf("/proc/%s", pid))
	return err == nil
}

func writeCacheMarker(projectDir, arch, name, hash string) {
	path := CacheMarkerPath(projectDir, arch, name, hash)
	EnsureDir(filepath.Dir(path))
	os.WriteFile(path, []byte(hash), 0644)
}
```

Update all callers of these functions throughout `executor.go` to pass
`opts.Arch`. Key call sites:

- Line 84: `IsBuildCached(opts.ProjectDir, opts.Arch, name, hash)`
- Line 110: `IsBuildCached(opts.ProjectDir, opts.Arch, name, hash)`
- Line 132: `writeCacheMarker(opts.ProjectDir, opts.Arch, name, hash)`
- Line 146:
  `os.Remove(CacheMarkerPath(opts.ProjectDir, opts.Arch, unit.Name, hash))`
- Line 149:
  `lockPath := BuildingLockPath(opts.ProjectDir, opts.Arch, unit.Name)`
- Line 370: `IsBuildCached(opts.ProjectDir, opts.Arch, name, hashes[name])`
- Line 386: `IsBuildCached(opts.ProjectDir, opts.Arch, name, hashes[name])`

- [ ] **Step 3: Update findImage in device/flash.go**

`findImage` needs arch to locate the correct build output directory:

```go
func findImage(projectDir, arch, unitName string) string {
	outputDir := filepath.Join(projectDir, "build", arch, unitName, "output")

	tarPath := filepath.Join(outputDir, unitName+".img.tar.gz")
	if _, err := os.Stat(tarPath); err == nil {
		return tarPath
	}

	imgPath := filepath.Join(outputDir, unitName+".img")
	if _, err := os.Stat(imgPath); err == nil {
		return imgPath
	}

	return ""
}
```

Update callers in `flash.go` and `qemu.go` to pass arch (from the machine).

- [ ] **Step 4: Update TUI status checks**

In `internal/tui/app.go`, the `Run` function checks build status using
`IsBuildCached`, `IsBuildInProgress`, and `HasBuildLog`. These all need the arch
parameter. The TUI needs to know the project's default arch:

```go
arch := build.Arch() // host arch — will be refined in Task 3

for _, name := range units {
    hash := hashes[name]
    if build.IsBuildCached(projectDir, arch, name, hash) {
        statuses[name] = statusCached
    } else if build.IsBuildInProgress(projectDir, arch, name) {
        statuses[name] = statusBuilding
    } else if build.HasBuildLog(projectDir, arch, name) {
        statuses[name] = statusFailed
    }
}
```

Also update the log path in `case "l"` and the clean path in `updateConfirm` to
use `build.UnitBuildDir(m.projectDir, m.arch, name)`. Store `arch` as a field on
the `model` struct.

- [ ] **Step 5: Update executor_test.go**

Update the test to use the new directory layout:

```go
srcDir := filepath.Join(projectDir, "build", "x86_64", "hello", "src")
```

And verify the marker is in the right place:

```go
markerDir := filepath.Join(projectDir, "build", "x86_64", "hello")
```

- [ ] **Step 6: Build and run tests**

Run: `go build ./... 2>&1 | grep -v "internal/tui"` (TUI may have pre-existing
issues)

Run: `go test ./internal/build/ -v`

Expected: all tests pass with the new directory layout.

- [ ] **Step 7: Commit**

```bash
git add internal/build/ internal/device/ internal/tui/
git commit -m "add arch to build directory paths: build/<arch>/<unit>/

UnitBuildDir, CacheMarkerPath, HasBuildLog, BuildingLockPath, and all
callers now include architecture in the path. Builds for different
architectures no longer collide."
```

---

### Task 2: Arch-Aware Repo and APK Packaging

Parameterize the APK arch metadata and repo directory so packages are stored
per-architecture.

**Files:**

- Modify: `internal/repo/local.go` (RepoDir)
- Modify: `internal/repo/index.go` (GenerateIndex)
- Modify: `internal/artifact/apk.go` (CreateAPK, generatePKGINFO)
- Modify: `internal/image/rootfs.go` (findAPK, Assemble)
- Modify: `internal/build/executor.go` (repo dir call in buildOne)
- Modify: `internal/bootstrap/bootstrap.go` (repo dir calls)

- [ ] **Step 1: Update RepoDir to accept arch**

In `internal/repo/local.go`, change:

```go
func RepoDir(proj *yoestar.Project, projectDir string) string {
	base := filepath.Join(projectDir, "build", "repo")
	if proj != nil && proj.Repository.Path != "" {
		base = proj.Repository.Path
	}
	// TODO: get arch from project/machine config instead of hardcoding
	return filepath.Join(base, "x86_64")
}
```

to:

```go
func RepoDir(proj *yoestar.Project, projectDir, arch string) string {
	base := filepath.Join(projectDir, "build", "repo")
	if proj != nil && proj.Repository.Path != "" {
		base = proj.Repository.Path
	}
	return filepath.Join(base, arch)
}
```

- [ ] **Step 2: Update GenerateIndex to accept arch**

In `internal/repo/index.go`, change the `GenerateIndex` signature to accept arch
and use it instead of hardcoded `x86_64`:

```go
func GenerateIndex(repoDir, arch string) error {
```

Line 65, change:

```go
fmt.Fprintf(&buf, "A:x86_64\n")
```

to:

```go
fmt.Fprintf(&buf, "A:%s\n", arch)
```

Update `Publish` in `local.go` to pass arch through to `GenerateIndex`:

```go
func Publish(apkPath, repoDir, arch string) error {
	// ... existing copy logic ...
	return GenerateIndex(repoDir, arch)
}
```

- [ ] **Step 3: Update CreateAPK and generatePKGINFO**

In `internal/artifact/apk.go`, add arch parameter:

```go
func CreateAPK(unit *yoestar.Unit, destDir, outputDir, arch string) (string, error) {
```

Line 46, pass arch:

```go
pkginfo := generatePKGINFO(unit, destDir, "", arch)
```

Update `generatePKGINFO`:

```go
func generatePKGINFO(unit *yoestar.Unit, destDir, dataHashHex, arch string) string {
```

Line 217, change:

```go
fmt.Fprintf(&b, "arch = x86_64\n")
```

to:

```go
fmt.Fprintf(&b, "arch = %s\n", arch)
```

- [ ] **Step 4: Update image rootfs findAPK**

In `internal/image/rootfs.go`, update `findAPK` to use target arch:

```go
func findAPK(repoDir, pkgName, arch string) string {
```

Line 120, change:

```go
for _, dir := range []string{repoDir, filepath.Join(repoDir, "x86_64")} {
```

to:

```go
for _, dir := range []string{repoDir, filepath.Join(repoDir, arch)} {
```

Update caller in `Assemble` to pass arch. `Assemble` needs arch added to its
signature:

```go
func Assemble(unit *yoestar.Unit, proj *yoestar.Project, projectDir, outputDir, arch string, w io.Writer) error {
```

- [ ] **Step 5: Update callers in executor.go and bootstrap.go**

In `internal/build/executor.go` line 264:

```go
apkPath, err := artifact.CreateAPK(unit, destDir, filepath.Join(buildDir, "pkg"), opts.Arch)
```

Line 270:

```go
repoDir := repo.RepoDir(nil, opts.ProjectDir, opts.Arch)
```

Line 271:

```go
if err := repo.Publish(apkPath, repoDir, opts.Arch); err != nil {
```

In `image.Assemble` call (~line 163 in executor.go):

```go
if err := image.Assemble(unit, proj, opts.ProjectDir, outputDir, opts.Arch, logW); err != nil {
```

In `internal/bootstrap/bootstrap.go`, update all `repo.RepoDir` calls to pass
arch. The bootstrap functions use `build.Arch()` — keep that for now (bootstrap
always targets host arch for Stage 0/1).

- [ ] **Step 6: Build and run tests**

Run: `go build ./...`

Run: `go test ./internal/build/ -v`

Run: `go test ./internal/artifact/ -v`

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add internal/repo/ internal/artifact/ internal/image/ internal/build/ internal/bootstrap/
git commit -m "parameterize arch in APK packaging and repo layout

RepoDir, GenerateIndex, CreateAPK, generatePKGINFO, findAPK, and
Assemble now accept target arch instead of hardcoding x86_64.
Repo layout: build/repo/<arch>/."
```

---

### Task 3: Target Arch From Machine Definition

Plumb the machine's arch as the target arch through the build system, distinct
from the host arch.

**Files:**

- Modify: `cmd/yoe/main.go` (cmdBuild, cmdRun, cmdFlash)
- Modify: `internal/tui/app.go` (startBuild)
- Modify: `internal/build/executor.go` (resolve target arch from image deps)

- [ ] **Step 1: Resolve target arch in cmdBuild**

In `cmd/yoe/main.go`, after `loadProject()`, resolve the target arch. When
building an image unit, look up the machine to get its arch. Otherwise default
to host arch:

```go
func resolveTargetArch(proj *yoestar.Project, units []string) string {
	// If building an image unit, use the machine's arch
	for _, name := range units {
		if u, ok := proj.Units[name]; ok && u.Class == "image" {
			machineName := proj.Defaults.Machine
			if m, ok := proj.Machines[machineName]; ok {
				return m.Arch
			}
		}
	}
	// Default to host arch
	return build.Arch()
}
```

Update `cmdBuild` to use it:

```go
proj := loadProject()
targetArch := resolveTargetArch(proj, units)
opts := build.Options{
    Ctx:        ctx,
    Force:      force,
    Clean:      clean,
    NoCache:    noCache,
    DryRun:     dryRun,
    Verbose:    verbose,
    ProjectDir: projectDir(),
    Arch:       targetArch,
}
```

- [ ] **Step 2: Add --machine flag to cmdBuild**

Allow explicit machine override on build:

```go
case "--machine":
    if i+1 < len(args) {
        machineName = args[i+1]
        i++
    }
```

When set, use the specified machine's arch instead of the default:

```go
func resolveTargetArch(proj *yoestar.Project, units []string, machineName string) string {
	if machineName != "" {
		if m, ok := proj.Machines[machineName]; ok {
			return m.Arch
		}
	}
	for _, name := range units {
		if u, ok := proj.Units[name]; ok && u.Class == "image" {
			mn := proj.Defaults.Machine
			if m, ok := proj.Machines[mn]; ok {
				return m.Arch
			}
		}
	}
	return build.Arch()
}
```

- [ ] **Step 3: Update TUI to use default machine arch**

In `internal/tui/app.go`, add `arch` field to the `model` struct and set it from
the project's default machine:

```go
arch := build.Arch()
if m, ok := proj.Machines[proj.Defaults.Machine]; ok {
    arch = m.Arch
}
```

Pass `m.arch` to `build.Options` in `startBuild`.

- [ ] **Step 4: Update cmdRun and cmdFlash**

`RunQEMU` and `Flash` already resolve the machine. Update `findImage` calls to
pass `machine.Arch`.

In `qemu.go` line 44:

```go
imgPath := findImage(projectDir, machine.Arch, unitName)
```

In `flash.go`, similarly pass arch from the resolved machine.

- [ ] **Step 5: Build and verify**

Run: `go build ./...`

Verify existing x86_64 workflow is unbroken (all paths now include `x86_64`
subdirectory).

- [ ] **Step 6: Commit**

```bash
git add cmd/yoe/ internal/tui/ internal/device/ internal/build/
git commit -m "resolve target arch from machine definition

cmdBuild resolves the machine's arch for image builds. Adds --machine
flag to override. TUI uses default machine arch. Non-image unit builds
default to host arch."
```

---

### Task 4: Arch-Aware Container Images

Make `EnsureImage` and `RunInContainer` support foreign-arch containers.

**Files:**

- Modify: `internal/container.go` (EnsureImage, containerTag, RunInContainer,
  ContainerRunConfig, containerRunArgs)
- Modify: `internal/build/sandbox.go` (RunInSandbox, RunSimple)

- [ ] **Step 1: Add Arch to ContainerRunConfig**

```go
type ContainerRunConfig struct {
	Ctx         context.Context
	Arch        string            // target architecture (empty = host arch)
	Command     string
	ProjectDir  string
	Mounts      []Mount
	Env         map[string]string
	Interactive bool
	NoUser      bool
	Stdout      io.Writer
	Stderr      io.Writer
}
```

- [ ] **Step 2: Update containerTag to accept arch**

```go
func containerTag(arch string) string {
	hostArch := hostArch()
	if arch == "" || arch == hostArch {
		return containerImage + ":" + containerVersion
	}
	return containerImage + ":" + containerVersion + "-" + arch
}

func hostArch() string {
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		return "x86_64"
	}
	arch := strings.TrimSpace(string(out))
	switch arch {
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}
```

- [ ] **Step 3: Update EnsureImage for cross-arch builds**

Change `EnsureImage` to accept arch. For cross-arch, use
`docker buildx build --platform`:

```go
func EnsureImage(arch string, w io.Writer) error {
	runtime, err := detectRuntime()
	if err != nil {
		return err
	}

	tag := containerTag(arch)
	cmd := exec.Command(runtime, "image", "inspect", tag)
	if err := cmd.Run(); err == nil {
		return nil
	}

	// Cross-arch: check binfmt_misc first
	host := hostArch()
	if arch != "" && arch != host {
		if err := checkBinfmt(arch); err != nil {
			return err
		}
	}

	if w == nil {
		w = io.Discard
	}
	fmt.Fprintf(w, "[yoe] building container image %s...\n", tag)

	tmpDir, err := os.MkdirTemp("", "yoe-container-build-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dockerfilePath := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(containers.Dockerfile), 0644); err != nil {
		return fmt.Errorf("writing Dockerfile: %w", err)
	}

	if arch != "" && arch != host {
		// Cross-arch: use buildx with --platform
		platform := "linux/" + arch
		cmd = exec.Command(runtime, "buildx", "build",
			"--platform", platform,
			"--load",
			"-t", tag, tmpDir)
	} else {
		cmd = exec.Command(runtime, "build", "-t", tag, tmpDir)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building container image: %w", err)
	}

	return nil
}
```

- [ ] **Step 4: Update RunInContainer to use arch**

Replace the `sync.Once` with a per-arch ensure pattern:

```go
var ensureMu sync.Mutex
var ensuredArches = map[string]error{}

func RunInContainer(cfg ContainerRunConfig) error {
	arch := cfg.Arch
	if arch == "" {
		arch = hostArch()
	}

	ensureMu.Lock()
	if err, ok := ensuredArches[arch]; ok {
		ensureMu.Unlock()
		if err != nil {
			return fmt.Errorf("container image: %w", err)
		}
	} else {
		w := cfg.Stderr
		if w == nil {
			w = os.Stderr
		}
		err := EnsureImage(arch, w)
		ensuredArches[arch] = err
		ensureMu.Unlock()
		if err != nil {
			return fmt.Errorf("container image: %w", err)
		}
	}

	// ... rest of function unchanged, but use containerTag(arch)
	// and add --platform for cross-arch ...
```

- [ ] **Step 5: Update containerRunArgs for cross-arch**

Add `--platform` flag when running a foreign-arch container:

```go
func containerRunArgs(cfg ContainerRunConfig) ([]string, error) {
	arch := cfg.Arch
	if arch == "" {
		arch = hostArch()
	}

	args := []string{"run", "--rm", "--privileged"}

	// Add platform for cross-arch containers
	if arch != hostArch() {
		args = append(args, "--platform", "linux/"+arch)
	}

	// ... rest unchanged, but use containerTag(arch) instead of containerTag()
	args = append(args, "-w", "/project")
	args = append(args, containerTag(arch))
	args = append(args, "bash", "-c")

	return args, nil
}
```

- [ ] **Step 6: Pass arch through sandbox**

In `internal/build/sandbox.go`, `RunInSandbox` and `RunSimple` need to pass arch
to `ContainerRunConfig`. Add `Arch` field to `SandboxConfig`:

```go
type SandboxConfig struct {
	Ctx        context.Context
	Arch       string // target architecture
	BuildRoot  string
	SrcDir     string
	// ... rest unchanged
}
```

In `RunInSandbox`:

```go
return yoe.RunInContainer(yoe.ContainerRunConfig{
    Ctx:        cfg.Ctx,
    Arch:       cfg.Arch,
    Command:    bwrapCmd,
    ProjectDir: cfg.ProjectDir,
    // ...
})
```

Update the caller in `executor.go` `buildOne` to set `cfg.Arch = opts.Arch` on
the `SandboxConfig`.

- [ ] **Step 7: Update existing EnsureImage callers**

In `cmd/yoe/main.go`, `cmdContainer` calls `EnsureImage` directly:

```go
case "build":
    if err := yoe.EnsureImage("", os.Stderr); err != nil {
```

Pass empty string for host arch (preserves current behavior).

- [ ] **Step 8: Build and test**

Run: `go build ./...`

Verify host-arch builds still work (empty arch = host arch throughout).

- [ ] **Step 9: Commit**

```bash
git add internal/container.go internal/build/sandbox.go cmd/yoe/
git commit -m "support per-arch container images

EnsureImage and RunInContainer now accept target arch. Cross-arch
containers are built with docker buildx --platform and tagged as
yoe-ng:11-<arch>. Host-arch behavior unchanged."
```

---

### Task 5: binfmt_misc Detection and Setup Command

Add `yoe container binfmt` command and binfmt detection before cross-arch
container builds.

**Files:**

- Modify: `internal/container.go` (checkBinfmt, RegisterBinfmt)
- Modify: `cmd/yoe/main.go` (cmdContainer binfmt subcommand)

- [ ] **Step 1: Add binfmt detection**

In `internal/container.go`, add:

```go
// checkBinfmt verifies that binfmt_misc is registered for the given
// architecture. Returns nil if registered, or an error with instructions.
func checkBinfmt(arch string) error {
	binfmtName := binfmtArchName(arch)
	path := filepath.Join("/proc/sys/fs/binfmt_misc", binfmtName)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return fmt.Errorf(
		"binfmt_misc not registered for %s.\n"+
			"Run 'yoe container binfmt' to enable cross-architecture builds",
		arch)
}

func binfmtArchName(arch string) string {
	switch arch {
	case "arm64":
		return "qemu-aarch64"
	case "riscv64":
		return "qemu-riscv64"
	case "x86_64":
		return "qemu-x86_64"
	default:
		return "qemu-" + arch
	}
}
```

- [ ] **Step 2: Add RegisterBinfmt function**

```go
// RegisterBinfmt registers QEMU user-mode emulation for foreign architectures
// using the tonistiigi/binfmt Docker image. Requires --privileged.
func RegisterBinfmt(w io.Writer) error {
	runtime, err := detectRuntime()
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "[yoe] registering binfmt_misc handlers...")
	cmd := exec.Command(runtime, "run", "--privileged", "--rm",
		"tonistiigi/binfmt", "--install", "arm64,riscv64")
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("registering binfmt: %w", err)
	}

	fmt.Fprintln(w, "Done. Registered: arm64, riscv64")
	return nil
}
```

- [ ] **Step 3: Add binfmt subcommand to CLI**

In `cmd/yoe/main.go`, add `"binfmt"` case to `cmdContainer`:

```go
case "binfmt":
    fmt.Println("This will register QEMU user-mode emulation for foreign architectures")
    fmt.Println("by running a privileged Docker container (tonistiigi/binfmt).")
    fmt.Println()
    fmt.Println("This enables building arm64 and riscv64 images on your", build.Arch(), "host.")
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
```

- [ ] **Step 4: Build and test**

Run: `go build ./cmd/yoe/`

Manual test: `./yoe container binfmt` (if on x86_64, verify prompt appears).

- [ ] **Step 5: Commit**

```bash
git add internal/container.go cmd/yoe/main.go
git commit -m "add yoe container binfmt command for cross-arch setup

Checks /proc/sys/fs/binfmt_misc for QEMU registration. New command
runs tonistiigi/binfmt with user confirmation. EnsureImage checks
binfmt before cross-arch container builds."
```

---

### Task 6: QEMU System Emulation Improvements

Update Dockerfile with multi-arch QEMU binaries and fix `yoe run` for cross-arch
execution (KVM vs TCG detection).

**Files:**

- Modify: `containers/Dockerfile.build`
- Modify: `internal/container.go` (bump version)
- Modify: `internal/device/qemu.go` (KVM detection, CPU override)

- [ ] **Step 1: Add QEMU binaries to Dockerfile**

In `containers/Dockerfile.build`, add arm64 and riscv64 QEMU after the existing
x86_64 line:

```dockerfile
    qemu-system-x86_64 \
    qemu-system-aarch64 \
    qemu-system-riscv64 \
    ovmf \
```

Update the version comment at the top:

```dockerfile
# Version: 12
```

- [ ] **Step 2: Bump container version**

In `internal/container.go`:

```go
const (
	containerVersion = "12"
	containerImage   = "yoe-ng"
)
```

- [ ] **Step 3: Fix QEMU KVM vs TCG detection**

In `internal/device/qemu.go`, replace the unconditional `-enable-kvm` with smart
detection:

```go
func baseQEMUArgs(machine *yoestar.Machine, opts QEMUOptions) []string {
	var args []string

	hostArch := detectHostArch()
	crossArch := machine.Arch != hostArch

	// Machine type
	qemu := machine.QEMU
	if qemu != nil {
		if qemu.Machine != "" {
			args = append(args, "-machine", qemu.Machine)
		}
		if crossArch {
			// Cross-arch: can't use host CPU, use max emulated features
			args = append(args, "-cpu", "max")
		} else if qemu.CPU != "" {
			args = append(args, "-cpu", qemu.CPU)
		}
	} else {
		switch machine.Arch {
		case "arm64":
			args = append(args, "-machine", "virt")
		case "riscv64":
			args = append(args, "-machine", "virt")
		default:
			args = append(args, "-machine", "q35")
		}
		if crossArch {
			args = append(args, "-cpu", "max")
		} else {
			args = append(args, "-cpu", "host")
		}
	}

	// Enable KVM only for same-arch
	if !crossArch {
		args = append(args, "-enable-kvm")
	}

	// ... rest of function (memory, display, firmware) unchanged
```

Add `detectHostArch` helper (reuse logic from build.Arch):

```go
func detectHostArch() string {
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		return "x86_64"
	}
	arch := strings.TrimSpace(string(out))
	switch arch {
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}
```

- [ ] **Step 4: Build**

Run: `go build ./...`

- [ ] **Step 5: Commit**

```bash
git add containers/Dockerfile.build internal/container.go internal/device/qemu.go
git commit -m "add multi-arch QEMU support and KVM/TCG auto-detection

Dockerfile now includes qemu-system-aarch64 and qemu-system-riscv64.
Container version bumped to 12. yoe run auto-detects cross-arch and
uses -cpu max (TCG) instead of -cpu host -enable-kvm."
```

---

### Task 7: Update Changelog and Documentation

- [ ] **Step 1: Update CHANGELOG.md**

Add under `[Unreleased]`:

```markdown
- **Cross-architecture builds** — build arm64 and riscv64 images on x86_64 hosts
  using QEMU user-mode emulation. Target arch is resolved from the machine
  definition. Run `yoe container binfmt` for one-time setup, then
  `yoe build base-image --machine qemu-arm64` works transparently.
- **Arch-aware build directories** — build output is now stored under
  `build/<arch>/<unit>/` and APK repos under `build/repo/<arch>/`, supporting
  multi-arch builds in the same project. **Note:** existing build caches under
  `build/<unit>/` will need to be rebuilt (`yoe clean --all`).
- **`yoe container binfmt`** — new command to register QEMU user-mode emulation
  for cross-architecture container builds. Shows what it will do and prompts for
  confirmation.
- **Multi-arch QEMU** — `yoe run` now auto-detects cross-architecture execution
  and uses software emulation (`-cpu max`) instead of KVM. Container includes
  `qemu-system-aarch64` and `qemu-system-riscv64`.
```

- [ ] **Step 2: Update CLAUDE.md**

Add to the "Key Design Decisions" section:

```markdown
- **Cross-architecture builds** — foreign-arch containers via QEMU user-mode
  emulation (binfmt_misc). Target arch comes from the machine definition. Build
  directories include arch: `build/<arch>/<unit>/`.
```

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md CLAUDE.md
git commit -m "update changelog and docs for cross-arch build support"
```

---

### Task 8: Migration — Clean Old Build Layout

Since build directories changed from `build/<unit>/` to `build/<arch>/<unit>/`,
existing builds will be orphaned. Add a one-time migration check.

- [ ] **Step 1: Add migration warning in BuildUnits**

At the start of `BuildUnits` in `internal/build/executor.go`, check for old
layout and warn:

```go
// Warn if old-style build directories exist (no arch subdirectory)
if entries, err := os.ReadDir(filepath.Join(opts.ProjectDir, "build")); err == nil {
    for _, e := range entries {
        if e.IsDir() && e.Name() != "repo" && e.Name() != "shell" &&
            e.Name() != "sysroot" && e.Name() != opts.Arch {
            // Check if it looks like an old unit dir (has .yoe-hash or build.log)
            old := filepath.Join(opts.ProjectDir, "build", e.Name())
            if _, err := os.Stat(filepath.Join(old, ".yoe-hash")); err == nil {
                fmt.Fprintf(w, "[yoe] warning: old build layout detected. Run 'yoe clean --all' to remove stale artifacts.\n")
                break
            }
            if _, err := os.Stat(filepath.Join(old, "build.log")); err == nil {
                fmt.Fprintf(w, "[yoe] warning: old build layout detected. Run 'yoe clean --all' to remove stale artifacts.\n")
                break
            }
        }
    }
}
```

- [ ] **Step 2: Build and test**

Run: `go build ./...`

- [ ] **Step 3: Commit**

```bash
git add internal/build/executor.go
git commit -m "warn about old build layout after arch directory migration"
```
