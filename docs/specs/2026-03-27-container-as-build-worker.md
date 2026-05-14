# Container-as-Build-Worker Design

Date: 2026-03-27

## Problem

The current architecture re-execs the entire `yoe` CLI inside a Docker/Podman
container for most commands. This causes:

1. **Unnecessary container dependency** -- read-only commands like `config`,
   `desc`, `refs`, `graph` that only evaluate Starlark don't need a container
2. **Root-owned files** -- files created inside the container are owned by root
   on the host filesystem
3. **Architectural confusion** -- the container is the CLI runtime rather than a
   build tool
4. **Slow iteration** -- every command pays container startup overhead

## Goal

`yoe` always runs on the host. The container becomes a stateless build worker
invoked only when container-provided tools (gcc, bwrap, mkfs, etc.) are needed.

## What Runs Where

### Host (no container)

- CLI dispatch, argument parsing
- Starlark evaluation, DAG resolution (`loadProject`, `resolve.*`)
- Source fetch (`source.*` -- git clone, HTTP download in Go)
- Config/desc/refs/graph/clean -- pure Go + Starlark
- Repo management -- reads `.apk` files from disk
- Dev extract/diff/status -- git operations
- Cache checking, hash computation
- APK packaging (`packaging.CreateAPK`) -- Go code creating tar.gz
- Sysroot install (`InstallToSysroot`) -- `cp -a`
- Flash -- `dd` to device
- QEMU -- try host `qemu-system-*` first, fall back to container

### Container (build toolchain + privileged tools)

- `RunInSandbox` -- bwrap + gcc/make/cmake/go inside the sandbox
- `RunSimple` -- Stage 0 bootstrap (Alpine's host toolchain)
- Image assembly tools: `mkfs.ext4`, `mkfs.vfat`, `sfdisk`, `mcopy`,
  `losetup`/`mount` (bootloader install)

## Architecture

### New Container API (`internal/container.go`)

Replace the re-exec pattern with a targeted execution API:

```go
type ContainerRunConfig struct {
    Command     string            // shell command to run inside container
    ProjectDir  string            // mounted as /project
    Mounts      []Mount           // additional bind mounts
    Env         map[string]string // environment variables
    Interactive bool              // attach TTY
    Privileged  bool              // --privileged (for losetup/mount)
    User        string            // --user flag (default: host uid:gid)
}

type Mount struct {
    Host      string
    Container string
    ReadOnly  bool
}

func RunInContainer(cfg ContainerRunConfig) error
```

**Removed:**

- `ExecInContainer()` -- no more re-exec
- `InContainer()` / `YOE_IN_CONTAINER` env var -- no longer needed
- Yoe binary bind-mount (`-v exe:/usr/local/bin/yoe:ro`)
- `containerWorkDir` logic

**Kept:**

- `EnsureImage()` -- called lazily on first `RunInContainer`
- `detectRuntime()` -- docker/podman detection
- `findGitRoot()` -- for mount computation
- `containerTag()` / `ContainerVersion()`

### Container Invocation

The container runs shell commands directly, not the `yoe` binary:

```
docker run --rm \
  --user $(id -u):$(id -g) \
  -v <project>:/project \
  -v <srcdir>:/build/src \
  -v <destdir>:/build/destdir \
  -v <sysroot>:/build/sysroot:ro \
  -e PREFIX=/usr -e DESTDIR=/build/destdir ... \
  yoe-ng:9 \
  bwrap --die-with-parent --bind / / ... -- sh -c "<build command>"
```

For privileged operations (bootloader install):

```
docker run --rm --privileged \
  -v <project>:/project \
  yoe-ng:9 \
  sh -c "<losetup/mount/extlinux commands>"
```

### Build Execution Flow

**Before (current):**

```
Host: yoe build foo
  -> ExecInContainer(["build", "foo"])
    -> Container: yoe build foo
      -> loadProject() [Starlark eval in container]
      -> resolve DAG [in container]
      -> source.Prepare() [git clone in container]
      -> RunInSandbox() [bwrap in container]
      -> packaging.CreateAPK() [in container]
      -> InstallToSysroot() [in container]
```

**After (new):**

```
Host: yoe build foo
  -> loadProject() [Starlark eval on host]
  -> resolve DAG [on host]
  -> source.Prepare() [git clone on host]
  -> container.RunInContainer(bwrap ... build command) [only this in container]
  -> packaging.CreateAPK() [on host]
  -> InstallToSysroot() [on host]
```

### Changes to `cmd/yoe/main.go`

The two-tier dispatch collapses into a single flat dispatch. Every command runs
on the host:

```go
func main() {
    command := os.Args[1]
    args := os.Args[2:]

    switch command {
    case "version":   cmdVersion()
    case "update":    cmdUpdate()
    case "init":      cmdInit(args)
    case "container": cmdContainer(args)
    case "tui":       cmdTUI(args)
    case "layer":     cmdLayer(args)
    case "build":     cmdBuild(args)
    case "bootstrap": cmdBootstrap(args)
    case "flash":     cmdFlash(args)
    case "run":       cmdRun(args)
    case "config":    cmdConfig(args)
    case "repo":      cmdRepo(args)
    case "source":    cmdSource(args)
    case "dev":       cmdDev(args)
    case "desc":      cmdDesc(args)
    case "refs":      cmdRefs(args)
    case "graph":     cmdGraph(args)
    case "clean":     cmdClean(args)
    default:          tryCustomCommand(command, args)
    }
}
```

No `InContainer()` check. No `ExecInContainer` call. `EnsureImage` is called
lazily on the first `RunInContainer` invocation.

### Changes to `internal/build/sandbox.go`

`RunInSandbox` and `RunSimple` gain a container wrapper:

- `RunInSandbox` constructs the bwrap command string and passes it to
  `container.RunInContainer` with the appropriate mounts (srcDir, destDir,
  sysroot)
- `RunSimple` (Stage 0) passes the raw `sh -c` command to
  `container.RunInContainer`
- `HasBwrap()` is no longer meaningful on the host -- bwrap availability is
  assumed inside the container. The check can be removed or moved to a container
  probe.

### Changes to `internal/build/executor.go`

- `buildOne()` orchestration stays on host
- The loop over `commands` calls the updated `RunInSandbox`/`RunSimple` which
  now go through `RunInContainer`
- `source.Prepare()` runs on host (git clone, no container)
- `packaging.CreateAPK()` runs on host
- `InstallToSysroot()` runs on host

### Image Assembly (`internal/image/`)

`image.Assemble` orchestration stays on host:

- `installPackages` -- `tar xzf` of `.apk` files runs on host (tar is universal)
- `applyConfig` -- pure file ops, runs on host
- `applyOverlays` -- pure file ops, runs on host
- `generateImage` -- calls `container.RunInContainer` for `mkfs.*`, `sfdisk`,
  `dd`, `mcopy`
- `installBootloader` -- calls `container.RunInContainer` with
  `Privileged: true` for `losetup`/`mount`/`extlinux`

### Bootstrap (`internal/bootstrap/`)

- Stage 0: build commands run via `container.RunInContainer` with `sh -c`
  (Alpine toolchain, no bwrap)
- Stage 1: build commands run via `container.RunInContainer` with bwrap
  (self-hosted toolchain)
- `createBuildRoot` uses `apk` via `container.RunInContainer`
- `Status` and unit verification run on host (pure Go)

### QEMU (`internal/device/qemu.go`)

- Try host `qemu-system-*` first (`exec.LookPath`)
- If not found, fall back to `container.RunInContainer` with `Interactive: true`
  and `--device /dev/kvm`
- Flash (`dd`) runs on host -- universally available

## File Ownership

Since the host `yoe` process does all file management (dirs, cache markers,
sysroot install, APK packaging), those files are owned by the invoking user.

Build artifacts inside the container use `--user $(id -u):$(id -g)` so compiler
output is also host-user-owned.

Build containers still need `--privileged` for bwrap namespace creation (even
with `--user`). The `--privileged` flag grants capabilities but `--user` ensures
file ownership matches the host user.

The only exception: bootloader install needs `--privileged` without `--user`
(root required for `losetup`/`mount`). These operations write to the disk image
file, not the filesystem tree, so ownership impact is limited to the `.img` file
itself.

## What Gets Deleted

- `YOE_IN_CONTAINER` env var
- `InContainer()` function
- `ExecInContainer()` function
- `--entrypoint yoe` pattern
- Yoe binary bind-mount into container
- `containerWorkDir` computation
- The host-vs-container split in `main.go`

## CLAUDE.md Updates

The "Container-Only Build Policy" section needs updating to reflect that the
host runs the CLI and only build execution uses the container. The principle
remains: developers need only Git, Docker, and the `yoe` binary. But now the
container is a build tool, not the CLI runtime.
