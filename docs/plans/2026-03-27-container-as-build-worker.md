# Container-as-Build-Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move container usage from CLI re-exec to build-only invocation so
`yoe` always runs on the host and only invokes Docker/Podman for actual build
commands.

**Architecture:** Replace `ExecInContainer` (re-execs entire yoe binary) with
`RunInContainer` (runs a single shell command). The container becomes a
stateless worker. All CLI logic, Starlark eval, DAG resolution, source fetch,
and packaging run on the host.

**Tech Stack:** Go, Docker/Podman, bubblewrap

---

## File Map

| File                              | Action | Responsibility                              |
| --------------------------------- | ------ | ------------------------------------------- |
| `internal/container.go`           | Modify | Replace re-exec API with RunInContainer API |
| `internal/container_test.go`      | Create | Unit tests for container API                |
| `internal/build/sandbox.go`       | Modify | Route bwrap/simple through RunInContainer   |
| `internal/build/sandbox_test.go`  | Create | Unit tests for bwrap command construction   |
| `internal/build/executor.go`      | Modify | Remove UseSandbox option, always use bwrap  |
| `internal/build/executor_test.go` | Modify | Update tests for new sandbox API            |
| `cmd/yoe/main.go`                 | Modify | Flatten dispatch, remove container re-exec  |
| `internal/bootstrap/bootstrap.go` | Modify | Route build commands through container API  |
| `internal/image/disk.go`          | Modify | Route mkfs/sfdisk/bootloader via container  |
| `internal/device/qemu.go`         | Modify | Try host QEMU first, fall back to container |
| `containers/Dockerfile.build`     | Modify | Remove YOE_IN_CONTAINER, update comments    |
| `CLAUDE.md`                       | Modify | Update container policy section             |

---

### Task 1: New Container API (`internal/container.go`)

**Files:**

- Modify: `internal/container.go`
- Create: `internal/container_test.go`

This is the foundation -- all subsequent tasks depend on it.

- [ ] **Step 1: Write test for RunInContainer argument construction**

Create `internal/container_test.go`:

```go
package internal

import (
	"fmt"
	"os/user"
	"testing"
)

func TestContainerRunArgs_Basic(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:    "echo hello",
		ProjectDir: "/home/user/myproject",
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	// Should contain: run --rm --privileged --user UID:GID -v project:/project
	assertContains(t, args, "--rm")
	assertContains(t, args, "--privileged")

	// Should have --user with current uid:gid
	u, _ := user.Current()
	assertContains(t, args, "--user")
	assertContains(t, args, fmt.Sprintf("%s:%s", u.Uid, u.Gid))

	// Should mount project dir
	assertContains(t, args, "-v")
	assertContains(t, args, "/home/user/myproject:/project")

	// Last args should be the image tag then sh -c "echo hello"
	last3 := args[len(args)-3:]
	if last3[0] != containerTag() {
		t.Errorf("expected image tag %q, got %q", containerTag(), last3[0])
	}
	if last3[1] != "sh" || last3[2] != "-c" {
		t.Errorf("expected 'sh -c', got %v", last3)
	}
}

func TestContainerRunArgs_Mounts(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:    "make",
		ProjectDir: "/project",
		Mounts: []Mount{
			{Host: "/tmp/src", Container: "/build/src", ReadOnly: false},
			{Host: "/tmp/sysroot", Container: "/build/sysroot", ReadOnly: true},
		},
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	assertContains(t, args, "/tmp/src:/build/src")
	assertContains(t, args, "/tmp/sysroot:/build/sysroot:ro")
}

func TestContainerRunArgs_Env(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:    "make",
		ProjectDir: "/project",
		Env:        map[string]string{"PREFIX": "/usr", "NPROC": "4"},
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	assertContains(t, args, "-e")
	found := false
	for _, a := range args {
		if a == "PREFIX=/usr" || a == "NPROC=4" {
			found = true
		}
	}
	if !found {
		t.Error("env vars not found in args")
	}
}

func TestContainerRunArgs_Interactive(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:     "qemu-system-x86_64",
		ProjectDir:  "/project",
		Interactive: true,
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	assertContains(t, args, "-it")
}

func TestContainerRunArgs_NoUser(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:    "losetup /dev/loop0 image.img",
		ProjectDir: "/project",
		NoUser:     true,
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	for _, a := range args {
		if a == "--user" {
			t.Error("should not have --user when NoUser is true")
		}
	}
}

func assertContains(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args %v does not contain %q", args, want)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/ -run TestContainerRun -v`

Expected: compilation errors -- `ContainerRunConfig`, `containerRunArgs`,
`Mount`, `NoUser` not defined.

- [ ] **Step 3: Implement new container API**

Replace the contents of `internal/container.go`. Keep `EnsureImage`,
`detectRuntime`, `findGitRoot`, `containerTag`, `ContainerVersion`. Remove
`ExecInContainer`, `InContainer`. Add:

```go
package internal

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sync"

	"github.com/YoeDistro/yoe-ng/containers"
)

const (
	containerVersion = "10" // bump: removed YOE_IN_CONTAINER, yoe binary mount
	containerImage   = "yoe-ng"
)

func containerTag() string {
	return containerImage + ":" + containerVersion
}

// Mount describes a bind mount for the container.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// ContainerRunConfig configures a single command execution inside the container.
type ContainerRunConfig struct {
	Command     string            // shell command to run
	ProjectDir  string            // mounted as /project
	Mounts      []Mount           // additional bind mounts
	Env         map[string]string // environment variables
	Interactive bool              // attach TTY (-it)
	NoUser      bool              // run as root (for losetup/mount)
}

var ensureOnce sync.Once
var ensureErr error

// RunInContainer executes a shell command inside the build container.
// The container image is built lazily on first invocation.
func RunInContainer(cfg ContainerRunConfig) error {
	ensureOnce.Do(func() {
		ensureErr = EnsureImage()
	})
	if ensureErr != nil {
		return fmt.Errorf("container image: %w", ensureErr)
	}

	runtime, err := detectRuntime()
	if err != nil {
		return err
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		return err
	}

	// Append the actual command
	args = append(args, "sh", "-c", cfg.Command)

	fmt.Fprintf(os.Stderr, "[yoe] container: %s\n", cfg.Command)

	cmd := exec.Command(runtime, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if cfg.Interactive {
		cmd.Stdin = os.Stdin
	}

	return cmd.Run()
}

// containerRunArgs builds the docker/podman run arguments (without the
// runtime binary name and without the trailing command).
func containerRunArgs(cfg ContainerRunConfig) ([]string, error) {
	args := []string{"run", "--rm", "--privileged"}

	// User mapping for file ownership
	if !cfg.NoUser {
		u, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("getting current user: %w", err)
		}
		args = append(args, "--user", fmt.Sprintf("%s:%s", u.Uid, u.Gid))
	}

	// Project mount
	if cfg.ProjectDir != "" {
		args = append(args, "-v", cfg.ProjectDir+":/project")
	}

	// Additional mounts
	for _, m := range cfg.Mounts {
		mount := m.Host + ":" + m.Container
		if m.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	// Environment variables
	for k, v := range cfg.Env {
		args = append(args, "-e", k+"="+v)
	}

	// Interactive
	if cfg.Interactive {
		args = append(args, "-it")
	}

	// Working directory
	args = append(args, "-w", "/project")

	// Image
	args = append(args, containerTag())

	return args, nil
}

// EnsureImage checks if the versioned container image exists and builds it
// if not.
func EnsureImage() error {
	runtime, err := detectRuntime()
	if err != nil {
		return err
	}

	tag := containerTag()
	cmd := exec.Command(runtime, "image", "inspect", tag)
	if err := cmd.Run(); err == nil {
		return nil
	}

	fmt.Fprintf(os.Stderr, "[yoe] building container image %s...\n", tag)

	tmpDir, err := os.MkdirTemp("", "yoe-container-build-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dockerfilePath := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(containers.Dockerfile), 0644); err != nil {
		return fmt.Errorf("writing Dockerfile: %w", err)
	}

	cmd = exec.Command(runtime, "build", "-t", tag, tmpDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building container image: %w", err)
	}

	return nil
}

// ContainerVersion returns the container version embedded in this binary.
func ContainerVersion() string {
	return containerVersion
}

func findGitRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func detectRuntime() (string, error) {
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err == nil {
			return rt, nil
		}
	}
	return "", fmt.Errorf("neither docker nor podman found — install one to use yoe")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/ -run TestContainerRun -v`

Expected: all 5 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/container.go internal/container_test.go
git commit -m "refactor: replace ExecInContainer with RunInContainer API

Container is now a build worker -- runs shell commands directly instead
of re-exec'ing the yoe binary. Adds Mount, ContainerRunConfig types.
Lazy EnsureImage via sync.Once. --user uid:gid for file ownership."
```

---

### Task 2: Update sandbox to route through container (`internal/build/sandbox.go`)

**Files:**

- Modify: `internal/build/sandbox.go`
- Create: `internal/build/sandbox_test.go`

- [ ] **Step 1: Write test for bwrap command string construction**

Create `internal/build/sandbox_test.go`:

```go
package build

import (
	"strings"
	"testing"
)

func TestBwrapCommand(t *testing.T) {
	cfg := &SandboxConfig{
		BuildRoot: "",
		SrcDir:    "/tmp/src",
		DestDir:   "/tmp/dest",
		Sysroot:   "/tmp/sysroot",
		Env: map[string]string{
			"PREFIX": "/usr",
			"NPROC":  "4",
		},
	}

	cmd := bwrapCommand(cfg, "make -j4")

	if !strings.HasPrefix(cmd, "bwrap ") {
		t.Errorf("command should start with 'bwrap ': %s", cmd)
	}
	if !strings.Contains(cmd, "--bind / /") {
		t.Errorf("should bind root: %s", cmd)
	}
	if !strings.Contains(cmd, "--bind /build/src /build/src") {
		t.Errorf("should bind src: %s", cmd)
	}
	if !strings.Contains(cmd, "--bind /build/destdir /build/destdir") {
		t.Errorf("should bind dest: %s", cmd)
	}
	if !strings.Contains(cmd, "--ro-bind /build/sysroot /build/sysroot") {
		t.Errorf("should ro-bind sysroot: %s", cmd)
	}
	if !strings.Contains(cmd, "make -j4") {
		t.Errorf("should contain build command: %s", cmd)
	}
	if !strings.Contains(cmd, "export PREFIX=") {
		t.Errorf("should export PREFIX: %s", cmd)
	}
}

func TestBwrapCommand_WithBuildRoot(t *testing.T) {
	cfg := &SandboxConfig{
		BuildRoot: "/tmp/buildroot",
		SrcDir:    "/tmp/src",
		DestDir:   "/tmp/dest",
		Env:       map[string]string{},
	}

	cmd := bwrapCommand(cfg, "gcc -o test test.c")

	if !strings.Contains(cmd, "--bind /tmp/buildroot /") {
		t.Errorf("should bind build root as /: %s", cmd)
	}
}

func TestContainerMountsForBuild(t *testing.T) {
	cfg := &SandboxConfig{
		SrcDir:  "/home/user/project/build/zlib/src",
		DestDir: "/home/user/project/build/zlib/destdir",
		Sysroot: "/home/user/project/build/sysroot",
	}

	mounts := containerMountsForBuild(cfg)

	if len(mounts) != 3 {
		t.Fatalf("expected 3 mounts, got %d", len(mounts))
	}

	for _, m := range mounts {
		if m.Container == "/build/sysroot" && !m.ReadOnly {
			t.Error("sysroot mount should be read-only")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
`cd /scratch4/yoe/yoe-ng && go test ./internal/build/ -run "TestBwrapCommand|TestContainerMounts" -v`

Expected: compilation errors -- `bwrapCommand` and `containerMountsForBuild` not
defined.

- [ ] **Step 3: Rewrite sandbox.go**

Replace `internal/build/sandbox.go` with:

```go
package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	yoe "github.com/YoeDistro/yoe-ng/internal"
)

// SandboxConfig defines the bubblewrap sandbox for a unit build.
type SandboxConfig struct {
	// BuildRoot is the Tier 1 build root (ro-bind mounted as /)
	BuildRoot string
	// SrcDir is the unit source directory (bind mounted as /build/src)
	SrcDir string
	// DestDir is the staging directory (bind mounted as /build/destdir)
	DestDir string
	// Sysroot is the shared build sysroot containing installed deps.
	Sysroot string
	// Env is the build environment variables
	Env map[string]string
	// ProjectDir is the host project root (for container mount)
	ProjectDir string
}

// RunInSandbox executes a command inside a bubblewrap sandbox within the
// build container.
func RunInSandbox(cfg *SandboxConfig, command string) error {
	bwrapCmd := bwrapCommand(cfg, command)
	mounts := containerMountsForBuild(cfg)

	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Command:    bwrapCmd,
		ProjectDir: cfg.ProjectDir,
		Mounts:     mounts,
	})
}

// RunSimple executes a command directly in the container (no bwrap sandbox).
// Used for Stage 0 bootstrap where we use the container's Alpine toolchain.
func RunSimple(cfg *SandboxConfig, command string) error {
	var envExports []string
	for k, v := range cfg.Env {
		envExports = append(envExports, fmt.Sprintf("export %s=%q", k, v))
	}
	fullCmd := strings.Join(envExports, "; ")
	if fullCmd != "" {
		fullCmd += "; "
	}
	fullCmd += "cd /build/src && " + command

	mounts := containerMountsForBuild(cfg)

	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Command:    fullCmd,
		ProjectDir: cfg.ProjectDir,
		Mounts:     mounts,
	})
}

// bwrapCommand constructs the full bwrap command string to run inside the
// container.
func bwrapCommand(cfg *SandboxConfig, command string) string {
	var parts []string
	parts = append(parts, "bwrap", "--die-with-parent")

	if cfg.BuildRoot != "" {
		parts = append(parts, "--bind", cfg.BuildRoot, "/")
	} else {
		parts = append(parts, "--bind", "/", "/")
	}

	if cfg.Sysroot != "" {
		parts = append(parts, "--ro-bind", "/build/sysroot", "/build/sysroot")
	}

	parts = append(parts,
		"--bind", "/build/src", "/build/src",
		"--bind", "/build/destdir", "/build/destdir",
		"--dev-bind", "/dev", "/dev",
		"--ro-bind", "/proc", "/proc",
		"--tmpfs", "/tmp",
		"--chdir", "/build/src",
	)

	var envExports []string
	for k, v := range cfg.Env {
		envExports = append(envExports, fmt.Sprintf("export %s=%q", k, v))
	}
	envStr := strings.Join(envExports, "; ")
	fullCmd := envStr
	if fullCmd != "" {
		fullCmd += "; "
	}
	fullCmd += command

	parts = append(parts, "--", "sh", "-c", fullCmd)
	return strings.Join(parts, " ")
}

// containerMountsForBuild returns the Mount list for a build step.
func containerMountsForBuild(cfg *SandboxConfig) []yoe.Mount {
	var mounts []yoe.Mount

	if cfg.SrcDir != "" {
		mounts = append(mounts, yoe.Mount{
			Host: cfg.SrcDir, Container: "/build/src",
		})
	}
	if cfg.DestDir != "" {
		mounts = append(mounts, yoe.Mount{
			Host: cfg.DestDir, Container: "/build/destdir",
		})
	}
	if cfg.Sysroot != "" {
		mounts = append(mounts, yoe.Mount{
			Host: cfg.Sysroot, Container: "/build/sysroot", ReadOnly: true,
		})
	}

	return mounts
}

// SysrootDir returns the shared build sysroot path for a project.
func SysrootDir(projectDir string) string {
	return filepath.Join(projectDir, "build", "sysroot")
}

// InstallToSysroot copies a unit's destdir contents into the shared sysroot.
func InstallToSysroot(destDir, sysrootDir string) error {
	if err := os.MkdirAll(sysrootDir, 0755); err != nil {
		return err
	}
	cmd := exec.Command("cp", "-a", destDir+"/.", sysrootDir+"/")
	return cmd.Run()
}

// EnsureDir creates a directory if it doesn't exist.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// NProc returns the number of available CPU cores.
func NProc() string {
	out, err := exec.Command("nproc").Output()
	if err != nil {
		return "1"
	}
	return strings.TrimSpace(string(out))
}

// Arch returns the current machine architecture in Yoe-NG format.
func Arch() string {
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

// RecipeBuildDir returns the build directory for a unit.
func RecipeBuildDir(projectDir, recipeName string) string {
	return filepath.Join(projectDir, "build", recipeName)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
`cd /scratch4/yoe/yoe-ng && go test ./internal/build/ -run "TestBwrapCommand|TestContainerMounts" -v`

Expected: all 3 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/build/sandbox.go internal/build/sandbox_test.go
git commit -m "refactor: route sandbox execution through container API

RunInSandbox and RunSimple now invoke container.RunInContainer instead
of exec'ing bwrap/sh directly. Remove HasBwrap -- bwrap is always
available inside the container. Add bwrapCommand and
containerMountsForBuild helpers."
```

---

### Task 3: Update build executor (`internal/build/executor.go`)

**Files:**

- Modify: `internal/build/executor.go`
- Modify: `internal/build/executor_test.go`

- [ ] **Step 1: Update executor.go**

Remove `UseSandbox` from `Options` struct:

```go
type Options struct {
	Force      bool   // rebuild even if cached
	NoCache    bool   // skip all caches
	DryRun     bool   // show what would be built
	ProjectDir string // project root
	Arch       string // target architecture
}
```

Replace the build step loop in `buildOne` (lines 130-148) with:

```go
	// Execute each build step inside the container with bwrap
	for i, cmd := range commands {
		fmt.Fprintf(w, "  [%d/%d] %s\n", i+1, len(commands), cmd)

		cfg := &SandboxConfig{
			SrcDir:     srcDir,
			DestDir:    destDir,
			Sysroot:    sysroot,
			Env:        env,
			ProjectDir: opts.ProjectDir,
		}
		if err := RunInSandbox(cfg, cmd); err != nil {
			return err
		}
	}
```

- [ ] **Step 2: Update executor_test.go**

In `TestBuildRecipes_WithDeps`, remove `UseSandbox: false` from opts. Add Docker
skip guard at the top of the test:

```go
func TestBuildRecipes_WithDeps(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		if _, err := exec.LookPath("podman"); err != nil {
			t.Skip("docker/podman not available")
		}
	}
	// ... rest unchanged ...
```

Update opts:

```go
	opts := Options{
		ProjectDir: projectDir,
		Arch:       "x86_64",
	}
```

- [ ] **Step 3: Run tests to verify they compile and pass**

Run:
`cd /scratch4/yoe/yoe-ng && go test ./internal/build/ -run "TestBuildCommands|TestDryRun|TestCacheMarker|TestFilterBuildOrder" -v`

Expected: existing unit tests pass (they don't need Docker).

- [ ] **Step 4: Commit**

```bash
git add internal/build/executor.go internal/build/executor_test.go
git commit -m "refactor: executor always uses container sandbox

Remove UseSandbox option -- builds always go through RunInSandbox
which routes to the container. Add Docker skip guard to integration
test."
```

---

### Task 4: Flatten main.go dispatch (`cmd/yoe/main.go`)

**Files:**

- Modify: `cmd/yoe/main.go`

- [ ] **Step 1: Remove container re-exec and flatten dispatch**

Replace the `main()` function (lines 23-104). Remove the `InContainer` check and
`ExecInContainer` call. Merge the two switch blocks into one:

```go
func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	args := os.Args[2:]

	switch command {
	case "version":
		fmt.Println(version)
	case "update":
		cmdUpdate()
	case "init":
		cmdInit(args)
	case "container":
		cmdContainer(args)
	case "tui":
		cmdTUI(args)
	case "layer":
		cmdLayer(args)
	case "build":
		cmdBuild(args)
	case "bootstrap":
		cmdBootstrap(args)
	case "flash":
		cmdFlash(args)
	case "run":
		cmdRun(args)
	case "config":
		cmdConfig(args)
	case "repo":
		cmdRepo(args)
	case "source":
		cmdSource(args)
	case "dev":
		cmdDev(args)
	case "desc":
		cmdDesc(args)
	case "refs":
		cmdRefs(args)
	case "graph":
		cmdGraph(args)
	case "clean":
		cmdClean(args)
	default:
		if !tryCustomCommand(command, args) {
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", command)
			printUsage()
			os.Exit(1)
		}
	}
}
```

Update `cmdBuild` -- remove the `HasBwrap()` call:

```go
	opts := build.Options{
		Force:      force,
		NoCache:    noCache,
		DryRun:     dryRun,
		ProjectDir: projectDir(),
		Arch:       build.Arch(),
	}
```

Update `cmdContainer` status subcommand -- remove `InContainer()` reference:

```go
	case "status":
		fmt.Printf("Container version: %s (image: yoe-ng:%s)\n",
			yoe.ContainerVersion(), yoe.ContainerVersion())
		if err := yoe.EnsureImage(); err != nil {
			fmt.Println("Container image: not built")
		} else {
			fmt.Println("Container image: ready")
		}
```

Remove the `"os/exec"` import if no longer used.

- [ ] **Step 2: Verify compilation**

Run: `cd /scratch4/yoe/yoe-ng && go build ./cmd/yoe/`

Expected: compiles without errors.

- [ ] **Step 3: Run existing tests**

Run: `cd /scratch4/yoe/yoe-ng && go test ./... 2>&1 | head -40`

Expected: tests that don't need Docker pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/yoe/main.go
git commit -m "refactor: flatten main.go -- all commands run on host

Remove InContainer check and ExecInContainer re-exec. Single flat
switch dispatch. Container is invoked lazily by the build executor
when a build command actually needs container tools."
```

---

### Task 5: Update bootstrap to use container API (`internal/bootstrap/bootstrap.go`)

**Files:**

- Modify: `internal/bootstrap/bootstrap.go`

- [ ] **Step 1: Update Stage0 to route through container**

Replace the build loop in `Stage0` (lines 80-85):

```go
		for i, cmd := range commands {
			fmt.Fprintf(w, "  [%d/%d] %s\n", i+1, len(commands), cmd)
			cfg := &build.SandboxConfig{
				SrcDir:     buildDir,
				DestDir:    destDir,
				Env:        env,
				ProjectDir: projectDir,
			}
			if err := build.RunSimple(cfg, cmd); err != nil {
				return fmt.Errorf("stage0 %s step %d: %w", unit.Name, i+1, err)
			}
		}
```

- [ ] **Step 2: Update Stage1 to route through container**

Replace the build loop in `Stage1` (lines 151-169). Remove the `HasBwrap()`
check -- always use `RunInSandbox`:

```go
		for i, cmd := range commands {
			fmt.Fprintf(w, "  [%d/%d] %s\n", i+1, len(commands), cmd)

			cfg := &build.SandboxConfig{
				BuildRoot:  buildRoot,
				SrcDir:     buildDir,
				DestDir:    destDir,
				Env:        env,
				ProjectDir: projectDir,
			}
			if err := build.RunInSandbox(cfg, cmd); err != nil {
				return fmt.Errorf("stage1 %s step %d: %w", unit.Name, i+1, err)
			}
		}
```

- [ ] **Step 3: Update createBuildRoot to use container for apk**

Replace `createBuildRoot` function. Add `projectDir` parameter. Use
`RunInContainer` for the apk command:

```go
func createBuildRoot(buildRoot, repoDir, projectDir string, w io.Writer) error {
	fmt.Fprintf(w, "Creating build root at %s...\n", buildRoot)

	os.RemoveAll(buildRoot)
	os.MkdirAll(buildRoot, 0755)

	args := []string{
		"apk",
		"--root", "/build/buildroot",
		"--initdb",
		"--no-scripts",
		"--no-cache",
		"--repository", "/build/repo",
		"add",
	}
	args = append(args, bootstrapRecipes...)
	cmd := strings.Join(args, " ")

	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Command:    cmd,
		ProjectDir: projectDir,
		Mounts: []yoe.Mount{
			{Host: buildRoot, Container: "/build/buildroot"},
			{Host: repoDir, Container: "/build/repo", ReadOnly: true},
		},
	})
}
```

Update the `Stage1` call to `createBuildRoot` to pass `projectDir`:

```go
	if err := createBuildRoot(buildRoot, repoDir, projectDir, w); err != nil {
```

Remove the `extractPackages` fallback function. Add
`yoe "github.com/YoeDistro/yoe-ng/internal"` import.

- [ ] **Step 4: Verify compilation**

Run: `cd /scratch4/yoe/yoe-ng && go build ./...`

Expected: compiles.

- [ ] **Step 5: Run bootstrap tests**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/bootstrap/ -v`

Expected: tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/bootstrap/bootstrap.go
git commit -m "refactor: bootstrap routes build commands through container

Stage 0 uses RunSimple (container sh -c), Stage 1 uses RunInSandbox
(container bwrap). createBuildRoot uses RunInContainer for apk.
Remove extractPackages fallback -- apk always available in container."
```

---

### Task 6: Update image assembly to use container for disk tools (`internal/image/disk.go`)

**Files:**

- Modify: `internal/image/disk.go`
- Modify: `internal/image/rootfs.go`

- [ ] **Step 1: Update disk.go to use container for mkfs/sfdisk**

Update `GenerateDiskImage` signature to accept `projectDir`:

```go
func GenerateDiskImage(rootfs, imgPath string, unit *yoestar.Unit,
	projectDir string, w io.Writer) error {
```

For `partitionImage`, replace direct `exec.Command("sfdisk", ...)` with
`RunInContainer`. Convert host paths to `/project`-relative container paths:

```go
func partitionImage(imgPath string, partitions []yoestar.Partition,
	projectDir string, w io.Writer) error {
	script := "label: dos\n"
	for i, p := range partitions {
		size := ""
		sizeMB := parseSizeMB(p.Size)
		if sizeMB > 0 && i < len(partitions)-1 {
			size = fmt.Sprintf("size=%dMiB, ", sizeMB)
		}
		ptype := "83"
		if p.Type == "vfat" {
			ptype = "c"
		}
		bootable := ""
		if p.Root {
			bootable = ", bootable"
		}
		script += fmt.Sprintf("%stype=%s%s\n", size, ptype, bootable)
	}

	fmt.Fprintln(w, "  Partitioning (MBR)...")

	rel, err := filepath.Rel(projectDir, imgPath)
	if err != nil {
		return fmt.Errorf("image path not under project: %w", err)
	}
	containerImg := filepath.Join("/project", rel)

	cmd := fmt.Sprintf("echo %q | sfdisk --quiet %s", script, containerImg)
	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Command:    cmd,
		ProjectDir: projectDir,
	})
}
```

Apply similar container wrapping to `createVfatPartition`,
`createExt4Partition`, and `installBootloader`. Each converts host paths to
`/project`-relative container paths and runs the tool command via
`RunInContainer`.

For `installBootloader`, use `NoUser: true` since it needs root for
`losetup`/`mount`:

```go
	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Command:    bootloaderCmd,
		ProjectDir: projectDir,
		NoUser:     true,
	})
```

Pass `projectDir` through from `GenerateDiskImage` to all subfunctions.

Add `yoe "github.com/YoeDistro/yoe-ng/internal"` import.

- [ ] **Step 2: Update rootfs.go to pass projectDir through**

Update `generateImage` call in `Assemble`:

```go
	if err := GenerateDiskImage(rootfs, imgPath, unit, projectDir, w); err != nil {
```

- [ ] **Step 3: Verify compilation**

Run: `cd /scratch4/yoe/yoe-ng && go build ./...`

Expected: compiles.

- [ ] **Step 4: Run image tests**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/image/ -v`

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/image/disk.go internal/image/rootfs.go
git commit -m "refactor: image disk tools run inside container

partitionImage, createVfatPartition, createExt4Partition, and
installBootloader now invoke tools via RunInContainer. Host paths
converted to /project-relative container paths. Bootloader install
uses NoUser for root access (losetup/mount)."
```

---

### Task 7: Update QEMU to try host first (`internal/device/qemu.go`)

**Files:**

- Modify: `internal/device/qemu.go`

- [ ] **Step 1: Add host-first QEMU fallback**

In `RunQEMU`, try the host QEMU binary first. If not found, fall back to
container. After the existing unit/machine/image lookup, replace the command
execution section:

```go
	qemuBin := qemuBinary(machine.Arch)
	args := baseQEMUArgs(machine, opts)
	args = append(args, "-drive",
		fmt.Sprintf("file=%s,format=raw,if=virtio", imgPath))

	// Port forwarding
	for _, port := range opts.Ports {
		args = append(args, "-netdev",
			fmt.Sprintf("user,id=net0,hostfwd=tcp::%s", port))
		args = append(args, "-device", "virtio-net-pci,netdev=net0")
	}
	if len(opts.Ports) == 0 {
		args = append(args, "-netdev", "user,id=net0")
		args = append(args, "-device", "virtio-net-pci,netdev=net0")
	}

	// Try host QEMU first
	if _, err := exec.LookPath(qemuBin); err == nil {
		fmt.Fprintf(w, "Starting QEMU (host): %s %s\n", qemuBin, machine.Arch)
		cmd := exec.Command(qemuBin, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if opts.Daemon {
			cmd.Stdin = nil
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Start(); err != nil {
				return fmt.Errorf("starting QEMU: %w", err)
			}
			fmt.Fprintf(w, "QEMU running in background (PID %d)\n",
				cmd.Process.Pid)
			return nil
		}
		return cmd.Run()
	}

	// Fall back to container
	fmt.Fprintf(w, "Starting QEMU (container): %s %s\n", qemuBin, machine.Arch)

	rel, err := filepath.Rel(projectDir, imgPath)
	if err != nil {
		return fmt.Errorf("image path not under project: %w", err)
	}

	containerImgPath := filepath.Join("/project", rel)
	containerArgs := baseQEMUArgs(machine, opts)
	containerArgs = append(containerArgs, "-drive",
		fmt.Sprintf("file=%s,format=raw,if=virtio", containerImgPath))
	for _, port := range opts.Ports {
		containerArgs = append(containerArgs, "-netdev",
			fmt.Sprintf("user,id=net0,hostfwd=tcp::%s", port))
		containerArgs = append(containerArgs, "-device",
			"virtio-net-pci,netdev=net0")
	}
	if len(opts.Ports) == 0 {
		containerArgs = append(containerArgs, "-netdev", "user,id=net0")
		containerArgs = append(containerArgs, "-device",
			"virtio-net-pci,netdev=net0")
	}

	fullCmd := qemuBin + " " + strings.Join(containerArgs, " ")
	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Command:     fullCmd,
		ProjectDir:  projectDir,
		Interactive: !opts.Daemon,
		NoUser:      true,
	})
```

Add imports: `"strings"`, `"path/filepath"`,
`yoe "github.com/YoeDistro/yoe-ng/internal"`.

- [ ] **Step 2: Verify compilation**

Run: `cd /scratch4/yoe/yoe-ng && go build ./...`

Expected: compiles.

- [ ] **Step 3: Commit**

```bash
git add internal/device/qemu.go
git commit -m "refactor: try host QEMU first, fall back to container

RunQEMU checks for qemu-system-* on the host via LookPath. If
available, runs directly. Otherwise invokes via RunInContainer with
Interactive and NoUser flags."
```

---

### Task 8: Update Dockerfile and CLAUDE.md

**Files:**

- Modify: `containers/Dockerfile.build`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update Dockerfile**

Remove `ENV YOE_IN_CONTAINER=1` (line 54). Update the comment header:

```dockerfile
# Yoe-NG build container (Tier 0)
# Version: 10
#
# Tools-only container for build execution. The yoe CLI runs on the host
# and invokes this container only for build commands that need the
# toolchain (gcc, bwrap, mkfs, etc.). No yoe binary inside.
```

Keep all packages including git (build systems may use `git describe`).

- [ ] **Step 2: Update CLAUDE.md container policy section**

Replace the "CRITICAL: Container-Only Build Policy" section with:

```markdown
## Container as Build Worker

**The `yoe` CLI always runs on the host. The container is a stateless build
worker invoked only when container-provided tools (gcc, bwrap, mkfs, etc.) are
needed.**

- The host runs: CLI dispatch, Starlark evaluation, DAG resolution, source
  fetch, APK packaging, cache management, all query commands
- The container runs: bwrap-sandboxed compilation, image disk tool operations
  (mkfs, sfdisk, bootloader install), Stage 0 bootstrap
- `container.RunInContainer()` is the single entry point -- called from the
  build executor, image assembly, and bootstrap
- The container runs with `--privileged` for bwrap namespaces and disk tools
- Build output uses `--user uid:gid` so files are owned by the host user
- The container image is built lazily on first build command
- Developers need only Git, Docker/Podman, and the `yoe` binary
```

- [ ] **Step 3: Verify build**

Run:
`cd /scratch4/yoe/yoe-ng && go build ./cmd/yoe/ && go test ./... 2>&1 | head -40`

Expected: compiles and tests pass.

- [ ] **Step 4: Commit**

```bash
git add containers/Dockerfile.build CLAUDE.md
git commit -m "docs: update container policy for build-worker architecture

Remove YOE_IN_CONTAINER from Dockerfile (no longer needed). Bump
container version to 10. Update CLAUDE.md to reflect that the CLI
runs on host and container is only for build execution."
```

---

### Task 9: Final verification

- [ ] **Step 1: Full compilation check**

Run: `cd /scratch4/yoe/yoe-ng && go build ./cmd/yoe/`

Expected: clean build.

- [ ] **Step 2: Full test suite**

Run: `cd /scratch4/yoe/yoe-ng && go test ./...`

Expected: all tests pass (some may skip if Docker unavailable).

- [ ] **Step 3: Verify no stale references**

Run:
`cd /scratch4/yoe/yoe-ng && grep -r "InContainer\|ExecInContainer\|YOE_IN_CONTAINER\|HasBwrap\|UseSandbox" --include="*.go" .`

Expected: no matches in `.go` files (only in docs/specs/plans).

- [ ] **Step 4: Smoke test host-only commands**

Run:
`cd /scratch4/yoe/yoe-ng && ./yoe version && ./yoe config show 2>&1 | head -5`

Expected: both run instantly without Docker, no `[yoe] running in container`
message.
