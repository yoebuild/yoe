# Tasks, Starlark Build Functions, and Machine-Portable Images

**Date:** 2026-04-01 **Status:** Draft

## Problem

Three related limitations in the current build model:

1. **Build steps are shell strings.** The `build = [...]` list generates
   commands that Go executes later. When something fails, the user debugs
   machine-generated shell, not the code they wrote. Complex build logic (image
   assembly, conditional steps) is awkward as string concatenation.

2. **Images aren't portable across machines.** `base-image` must hard-code
   machine-specific packages (`syslinux` on x86, `rpi-firmware` on RPi) and
   partition layouts. Every new machine needs a new image definition or ugly
   conditionals.

3. **All units share one container.** Different units need different toolchains
   (Go SDK, Rust, protobuf). Currently everything runs in the Alpine build
   container.

## Solution

Three new concepts that work together:

### 1. Tasks Replace Build Step Lists

The `build = [...]` string list is replaced by `tasks = [...]`. Each task is a
named build phase that accepts one of three forms:

- **`run`** — single shell command string
- **`fn`** — single Starlark callable
- **`steps`** — list of shell strings and/or Starlark callables

```python
# Single command per task
unit(
    name = "my-app",
    tasks = [
        task("configure", run="./configure --prefix=$PREFIX"),
        task("compile", run="make -j$NPROC"),
        task("install", run="make DESTDIR=$DESTDIR install"),
    ],
)

# Multiple steps in one task (natural migration from build = [...])
unit(
    name = "busybox",
    tasks = [
        task("build", steps=[
            "make defconfig",
            "sed -i 's/# CONFIG_STATIC is not set/CONFIG_STATIC=y/' .config",
            "make -j$NPROC",
            "make CONFIG_PREFIX=$DESTDIR install",
        ]),
    ],
)

# Mixed steps — strings and Starlark functions
unit(
    name = "linux-rpi4",
    tasks = [
        task("build", steps=[
            "make ARCH=arm64 bcm2711_defconfig",
            patch_kernel_config,  # Starlark function called with run() available
            "make ARCH=arm64 -j$NPROC Image dtbs",
        ]),
        task("install", fn=install_kernel_files),
    ],
)
```

All existing units must be migrated from `build = [...]` to `tasks = [...]`. The
`build` field is removed.

### 2. Starlark Build Functions

A task's `fn` field accepts a Starlark callable instead of a shell string. The
function can call `run()` to execute commands directly:

```python
def install_packages(packages):
    run("mkdir -p $DESTDIR/rootfs")
    for pkg in packages:
        result = run("tar xzf $REPO/%s-*.apk -C $DESTDIR/rootfs" % pkg,
                     check=False)
        if result.exit_code != 0:
            print("warning: package %s not found, skipping" % pkg)

unit(
    name = "my-image",
    tasks = [
        task("install", fn=lambda: install_packages(["busybox", "openssh"])),
        task("configure", run="echo yoe > $DESTDIR/rootfs/etc/hostname"),
    ],
)
```

**`run()` builtin:**

- Executes a shell command in the current build environment (container or bwrap)
- Default: raises Starlark error on non-zero exit (like `set -e`)
- `check=False`: returns a result struct with `exit_code`, `stdout`, and
  `stderr`
- Error traces show the `.star` file and line number, not generated shell
- Commands run with CWD set to the source directory (same as today's
  `build = [...]`)
- Respects context cancellation — if the user cancels a build in the TUI,
  in-flight `run()` calls are terminated via the context

**`run()` return value:**

```python
result = run("uname -m", check=False)
result.exit_code    # int
result.stdout       # string
result.stderr       # string
```

**Mixed tasks:** A build can mix shell strings and functions:

```python
tasks = [
    task("configure", run="./configure"),
    task("patch-config", fn=patch_kernel_config),  # Starlark function
    task("compile", run="make -j$NPROC"),
]
```

### 3. Per-Task Containers

Each task can specify a Docker container image. When set, that task runs in the
specified container instead of the default Alpine build container:

```python
unit(
    name = "my-go-app",
    container = "golang:1.22-alpine",   # default for all tasks
    tasks = [
        task("codegen",
             container="protoc:latest",  # overrides unit default
             run="protoc --go_out=. api/*.proto"),
        task("build",                    # inherits golang container
             run="go build -o $DESTDIR/usr/bin/app"),
    ],
)
```

**Resolution order:** task container → unit container → default Alpine container
with bwrap.

## Machine-Portable Images

### MACHINE_CONFIG

A predeclared Starlark struct set after machine evaluation (phase 1), available
during unit/image evaluation (phase 2):

```python
# Available as a predeclared variable:
MACHINE_CONFIG.name         # "raspberrypi4"
MACHINE_CONFIG.arch         # "arm64"
MACHINE_CONFIG.packages     # ["rpi-firmware", "rpi4-config"]
MACHINE_CONFIG.partitions   # [partition(...), partition(...)]
MACHINE_CONFIG.kernel       # kernel config struct
```

Set from the machine definition:

```python
machine(
    name = "raspberrypi4",
    arch = "arm64",
    kernel = kernel(
        unit = "linux-rpi4",
        provides = "linux",
        defconfig = "bcm2711_defconfig",
        cmdline = "console=ttyS0,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait rw",
    ),
    packages = ["rpi-firmware", "rpi4-config"],
    partitions = [
        partition(label="boot", type="vfat", size="64M", contents=["*"]),
        partition(label="rootfs", type="ext4", size="256M", root=True),
    ],
)
```

### PROVIDES

A predeclared dict mapping virtual package names to concrete units, built from
`provides` fields on units and `kernel.provides` on machines:

```python
# Unit declares what it provides:
unit(name = "linux-rpi4", provides = "linux", ...)

# Machine's kernel also contributes:
kernel(unit = "linux-rpi4", provides = "linux", ...)

# Result: PROVIDES = {"linux": "linux-rpi4"}

# Image uses the virtual name:
image(name = "base-image", artifacts = ["busybox", "linux"], ...)
# "linux" resolved to "linux-rpi4" via PROVIDES
```

### Portable Image Definition

With these pieces, images become machine-portable:

```python
# classes/image.star
def image(name, artifacts = [], hostname = "", partitions = [], **kwargs):
    # Merge machine packages
    all_artifacts = list(artifacts) + MACHINE_CONFIG.packages

    # Resolve provides
    resolved = [PROVIDES.get(a, a) for a in all_artifacts]

    # Use machine partitions if image doesn't specify its own
    all_partitions = partitions if partitions else MACHINE_CONFIG.partitions

    unit(
        name = name,
        scope = "machine",
        class = "image",
        artifacts = resolved,
        partitions = all_partitions,
        tasks = [
            task("rootfs", fn=lambda: assemble_rootfs(resolved, hostname)),
            task("disk", fn=lambda: create_disk_image(name, all_partitions)),
        ],
        **kwargs,
    )

def assemble_rootfs(packages, hostname):
    run("mkdir -p $DESTDIR/rootfs")
    for pkg in packages:
        run("tar xzf $REPO/%s-*.apk -C $DESTDIR/rootfs --exclude=.PKGINFO" % pkg)
    if hostname:
        run("mkdir -p $DESTDIR/rootfs/etc")
        run("echo %s > $DESTDIR/rootfs/etc/hostname" % hostname)

def create_disk_image(name, partitions):
    # Calculate total size
    total_mb = 1  # MBR overhead
    for p in partitions:
        total_mb += parse_size_mb(p.size)

    img = "$DESTDIR/%s.img" % name
    run("dd if=/dev/zero of=%s bs=1M count=0 seek=%d" % (img, total_mb))

    # Partition with sfdisk
    sfdisk_input = generate_sfdisk_script(partitions)
    run("echo '%s' | sfdisk %s" % (sfdisk_input, img))

    # Create and populate each partition
    offset = 1
    for p in partitions:
        size = parse_size_mb(p.size)
        part_img = img + "." + p.label + ".part"
        run("dd if=/dev/zero of=%s bs=1M count=0 seek=%d" % (part_img, size))

        if p.type == "vfat":
            run("mkfs.vfat -n %s %s" % (p.label.upper(), part_img))
            # Copy boot files
            for pattern in p.contents:
                run("mcopy -sQi %s $DESTDIR/rootfs/boot/%s ::/" % (part_img, pattern))
        elif p.type == "ext4":
            run("mkfs.ext4 -d $DESTDIR/rootfs -L %s %s" % (p.label, part_img))

        run("dd if=%s of=%s bs=1M seek=%d conv=notrunc" % (part_img, img, offset))
        run("rm %s" % part_img)
        offset += size
```

Then `base-image.star` is simply:

```python
load("//classes/image.star", "image")

image(
    name = "base-image",
    artifacts = ["base-files", "busybox", "linux"],
    hostname = "yoe",
)
```

Same image works for QEMU x86, RPi4, RPi5 — the machine provides the kernel,
firmware, config, and partition layout.

### Machine Definitions

**QEMU x86_64:**

```python
machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    kernel = kernel(unit = "linux", provides = "linux", ...),
    packages = ["syslinux"],
    partitions = [
        partition(label="rootfs", type="ext4", size="128M", root=True),
    ],
)
```

**Raspberry Pi 4:**

```python
machine(
    name = "raspberrypi4",
    arch = "arm64",
    kernel = kernel(unit = "linux-rpi4", provides = "linux", ...),
    packages = ["rpi-firmware", "rpi4-config"],
    partitions = [
        partition(label="boot", type="vfat", size="64M", contents=["*"]),
        partition(label="rootfs", type="ext4", size="256M", root=True),
    ],
)
```

## Go Code Changes

### 1. Starlark `run()` Builtin

New builtin function available during build-time Starlark execution:

```go
func (e *Engine) fnRun(thread *starlark.Thread, _ *starlark.Builtin,
    args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
    // Pull sandbox config and context from the thread
    cfg := thread.Local(sandboxKey).(*SandboxConfig)
    // Execute command via RunInSandbox or RunInContainer
    // Return result struct with exit_code, stdout, and stderr
    // If check != False, raise error on non-zero exit
}
```

`run()` is only available during build execution, not during project evaluation.
Calling it during eval raises an error.

**Thread-local context:** The build executor attaches the sandbox config and a
cancellable `context.Context` to the Starlark thread via `Thread.SetLocal()`
before invoking any Starlark build function. This is how `run()` knows which
container/bwrap sandbox to execute in, and how cancellation propagates from the
TUI to in-flight commands. Pattern borrowed from
[Tilt's `local()` implementation](https://github.com/tilt-dev/tilt).

```go
func newBuildThread(ctx context.Context, sandbox *SandboxConfig) *starlark.Thread {
    t := &starlark.Thread{Name: "build"}
    t.SetLocal(sandboxKey, sandbox)
    t.SetLocal(contextKey, ctx)
    return t
}
```

**Execer interface for testability:** Command execution is abstracted behind an
interface so unit tests can verify Starlark build logic without running real
containers:

```go
type Execer interface {
    Run(ctx context.Context, cfg *SandboxConfig, command string) (ExecResult, error)
}

type ExecResult struct {
    ExitCode int
    Stdout   string
    Stderr   string
}
```

The real implementation calls `RunInSandbox()`; tests inject a mock that records
commands and returns canned results.

### 2. Build Executor: Task Dispatch

The executor iterates tasks. For each task, it iterates the task's steps:

- String step → execute in container/bwrap
- Callable step → invoke Starlark function in a build-time thread with `run()`
  available

A task with `run` is shorthand for a single-step task. A task with `fn` is
shorthand for a single callable step.

```go
for _, task := range unit.Tasks {
    for _, step := range task.Steps {
        switch s := step.(type) {
        case string:
            RunInSandbox(cfg, s)
        case starlark.Callable:
            thread := newBuildThread(sandbox)
            _, err := starlark.Call(thread, s, nil, nil)
        }
    }
}
```

### 3. Task and Unit Types

```go
// Step is either a shell command string or a Starlark callable.
type Step struct {
    Command  string            // shell command (set if step is a string)
    Fn       starlark.Callable // Starlark function (set if step is callable)
}

type Task struct {
    Name      string
    Container string // optional container image override
    Steps     []Step // ordered list of shell commands and/or Starlark functions
}

type Unit struct {
    // ... existing fields ...
    Container string   // default container for all tasks
    Tasks     []Task   // named build phases
    Provides  string   // virtual package name (e.g., "linux")
}
```

### 4. Machine Type Extensions

```go
type Machine struct {
    // ... existing fields ...
    Packages   []string    // packages added to every image for this machine
    Partitions []Partition // default partition layout for images
}

type KernelConfig struct {
    // ... existing fields ...
    Provides string // virtual package name (e.g., "linux")
}
```

### 5. MACHINE_CONFIG and PROVIDES

Set as predeclared Starlark variables after phase 1 (machine loading):

- `MACHINE_CONFIG` — struct built from the active machine definition
- `PROVIDES` — dict built from all loaded units' `provides` fields and the
  machine's `kernel.provides`

## Error Handling

When a `run()` call fails inside a Starlark build function:

```
Error building base-image:
  task "rootfs" failed:
  modules/module-core/classes/image.star:24  in assemble_rootfs
    run("tar xzf $REPO/busybox-*.apk ...") failed: exit code 1

  stderr:
    tar: busybox-*.apk: No such file or directory
```

The stack trace points to the `.star` file and line. No generated scripts.

When `check=False` is used, the function handles the error:

```python
result = run("some-command", check=False)
if result.exit_code != 0:
    # handle gracefully
```

## What This Replaces

- **`internal/image/rootfs.go`** — image assembly moves to
  `modules/module-core/classes/image.star`
- **`internal/image/disk.go`** — disk image creation moves to Starlark
  `create_disk_image()` function using shell commands
- **Per-image `MACHINE == ...` conditionals** — replaced by `MACHINE_CONFIG` and
  `PROVIDES`
- **`docs/superpowers/plans/per-recipe-containers.md`** — superseded by this
  spec

## Implementation Order

1. Implement `task()` builtin, remove `build` field, migrate all units to tasks
2. Implement `run()` builtin and Starlark build function support (`fn` field)
3. Add `MACHINE_CONFIG`, `PROVIDES`, machine `packages`/`partitions`
4. Rewrite `image()` class in Starlark using `run()`, remove Go image assembly
5. Migrate all classes (autotools, cmake, go) to use tasks
6. Add per-task container support

## What Doesn't Change

- Starlark evaluation for project/machine/unit resolution (phase 1-2)
- DAG resolution and topological sort
- Source fetching and caching
- APK packaging (run after build tasks complete)
- Content-addressed build caching
- TUI and CLI interfaces
