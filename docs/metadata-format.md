# Unit & Configuration Format

`[yoe]` uses [Starlark](https://github.com/google/starlark-go) — a
deterministic, sandboxed dialect of Python — for all build definitions. Units,
classes, machine definitions, and project configuration are all `.star` files.
See [Build Languages](build-languages.md) for the rationale behind this choice.

## Units vs. Packages

These are distinct concepts in `[yoe]`:

- **Units** — `.star` files in the project tree that describe _how to build_
  software. They live in version control and are a development/CI concern.
- **Packages** — `.apk` files that units _produce_. They are installable
  artifacts published to a repository and consumed by `apk` during image
  assembly or on-device updates.

The build flow is: **unit → build → .apk unit(s) → repository → image /
device**.

Units are inputs to the build system. Packages are outputs. A developer edits
units; a device only ever sees packages.

### Sub-packages (planned)

> **Status:** Today `[yoe]` produces exactly one `.apk` per unit —
> `internal/artifact/apk.go` packages `$DESTDIR` into a single archive, and the
> Starlark `subpackages =` field is not yet parsed. This section describes the
> intended future model so units and classes can be written with it in mind.

A single unit will be able to produce a small number of `.apk` packages from one
source build. The goal is targeted — keep runtime images lean — not exhaustive
like Yocto's auto-split of every recipe into 7+ packages.

**The only two splits `[yoe]` plans to support as subpackages:**

| Sub-package | Contents                             | Why it's a subpackage                                                     |
| ----------- | ------------------------------------ | ------------------------------------------------------------------------- |
| `<name>`    | Binaries, runtime libs, default conf | The default artifact                                                      |
| `-dev`      | Headers, `.a`, `.pc`, CMake configs  | Never wanted at runtime on a constrained device; needed on build hosts    |
| `-dbg`      | Detached DWARF debug info            | Installable after a field incident; should not occupy flash on the device |

**What is deliberately _not_ a subpackage:**

- **Docs, man pages, info pages, locale data, examples.** Classes strip these
  from `$DESTDIR` by default (e.g., `autotools` removes
  `/usr/share/{doc,man,info,locale,gtk-doc,bash-completion}` and
  `/usr/share/*/examples`). A unit that genuinely needs man pages on the device
  can opt out of the strip; most don't.
- **`-src`, `-staticdev`, `-locale-*`, `-bin` / `-common` style splits.** Yocto
  produces these automatically; `[yoe]` does not. The cognitive cost
  (which-of-seven-packages-holds-this-file) and per-unit metadata surface isn't
  worth it for yoe's target audience.
- **Library SONAME splits (`libfoo0` separate from `foo`).** Debian splits these
  to allow multiple ABI versions to coexist; `[yoe]` is rolling and ships one
  ABI at a time, so the split is unnecessary.

**Rationale.** Yocto's auto-split-everything model exists because recipe authors
cannot be trusted to strip docs/locale/staticdev consistently, so the build
system does it mechanically. That logic doesn't apply to `[yoe]`: the class
library is small, AI-written units follow the class, and the image is already
targeting single-digit MB. A `rm -rf $DESTDIR/usr/share/{doc,man,…}` in the
class does what Yocto's `-doc` subpackage does, with one package instead of two.

**Planned unit surface:**

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "openssl",
    version = "3.2.1",
    source = "https://www.openssl.org/source/openssl-3.2.1.tar.gz",
    deps = ["zlib"],
    # Opt in to the two subpackages that matter on constrained devices.
    subpackages = ["dev", "dbg"],
)
```

With no `subpackages` field, the unit produces a single `.apk` containing
everything in `$DESTDIR` after the class's default strip. That is the expected
case for most units.

**Planned split rules:**

- `-dev` claims `/usr/include/**`, `/usr/lib/*.a`, `/usr/lib/pkgconfig/**`,
  `/usr/lib/cmake/**`, `/usr/share/aclocal/**`, `/usr/share/pkgconfig/**`,
  `/usr/bin/*-config` (e.g., `xml2-config`).
- `-dbg` claims `/usr/lib/debug/**` (produced by running
  `objcopy --only-keep-debug` / `strip --only-keep-debug` on ELF binaries in
  `$DESTDIR` before packaging).
- Everything else stays in the main package.

**For custom splits** (e.g., separating `openssh-server` from `openssh-client`
because an image ships one but not both), the plan is to allow explicit file
lists:

```python
autotools(
    name = "openssh",
    subpackages = ["dev", "dbg"],
    extra_subpackages = {
        "server": files(
            "/usr/sbin/sshd",
            "/etc/ssh/sshd_config",
        ),
        "client": files(
            "/usr/bin/ssh",
            "/usr/bin/scp",
            "/usr/bin/sftp",
        ),
    },
)
```

This path is lower priority; most services can be shipped as one package and
enabled/disabled by the image.

**In image units (planned consumption):**

```python
image(
    name = "production-image",
    artifacts = [
        "openssh",
        "networkmanager",
    ],
)

image(
    name = "dev-image",
    artifacts = [
        "openssh",
        "openssh-dev",          # headers for on-device development
        "gdb",
    ],
)
```

Alpine's apk already supports subpackages natively (Alpine's `openssl` APKBUILD
produces `openssl`, `openssl-dev`, `openssl-dbg`, etc.), so the plumbing in apk
is already proven — what `[yoe]` needs to build is the Starlark surface, the
split engine, and the default strip logic in the shared classes.

### Dependency resolution at image time

There are two places dependency information lives in `[yoe]`, and they serve
different phases:

- **Unit metadata** (`deps`, `runtime_deps` in `.star` files) — drives the
  **build** graph. Tells the build executor what order to compile things in and
  what goes into each unit's sysroot.
- **Package metadata** (`.PKGINFO` inside each `.apk`; aggregated into an
  `APKINDEX`) — drives the **install** graph. Tells apk what to pull in when a
  package is added to a rootfs.

The unit author writes `runtime_deps = [...]` once; the build emits those into
`.PKGINFO` as `depend =` lines. From that point the package metadata is
authoritative for installation: image assembly invokes
`apk add --root <rootfs> -X <local-repo>` inside the build container, and
apk-tools resolves the install graph from APKINDEX. The Starlark-side
`_resolve_runtime_deps` is still used to flatten the artifact list for the build
DAG (so all required apks get built first), but apk-tools owns install-time
ordering, file-conflict detection, and `/lib/apk/db/installed` population.

**Why this is the right split:**

- **Subpackages.** When `openssl` splits into `openssl` and `openssl-dev`, the
  unit graph no longer has a node named `openssl-dev`. The dep
  `openssl-dev → openssl = ${version}` lives only in the generated PKGINFO. A
  unit-graph walker cannot see it; apk's resolver can.
- **`provides:` / `replaces:` / `conflicts:`.** apk's metadata supports virtual
  packages and alternatives (e.g., two SSH implementations both
  `provides = ssh`, one `replaces` the other). A Starlark-only walker would have
  to re-implement apk's resolver to honor these.
- **External repositories compose cleanly.** A project that pulls packages from
  an Alpine aports mirror or a vendor BSP repo has no Starlark unit to walk —
  only APKINDEX metadata. apk treats yoe-built packages and external-repo
  packages identically.
- **Single source of truth on the device.** What the image builder sees is what
  the on-device `apk upgrade` sees: same metadata, same resolver.

## Why Starlark

- **One language** — units, classes, machines, and project config are all
  `.star` files. No TOML + shell + something-else stack.
- **Python-like syntax** — most developers can read it immediately.
- **Deterministic** — no side effects, no mutable global state. Critical for
  content-addressed caching.
- **Sandboxed** — units cannot perform arbitrary I/O or network access.
- **Go-native** — the `go.starlark.net` library embeds directly in the `yoe`
  binary.
- **Composable** — functions, `load()`, and `**kwargs` provide natural
  composition for modules and overrides.
- **Battle-tested** — used by Bazel (Google), Buck2 (Meta), and Pants.

## Unit Types

### Machine Definition (`machines/<name>.star`)

Describes a target board or platform.

```python
machine(
    name = "beaglebone-black",
    arch = "arm64",
    description = "BeagleBone Black (AM3358)",
    kernel = kernel(
        repo = "https://github.com/beagleboard/linux.git",
        branch = "6.6",
        defconfig = "bb.org_defconfig",
        device_trees = ["am335x-boneblack.dtb"],
    ),
    bootloader = uboot(
        repo = "https://github.com/beagleboard/u-boot.git",
        branch = "v2024.01",
        defconfig = "am335x_evm_defconfig",
    ),
)
```

QEMU machines include emulation configuration:

```python
machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    kernel = kernel(
        unit = "linux-qemu",
        cmdline = "console=ttyS0 root=/dev/vda2 rw",
    ),
    qemu = qemu_config(
        machine = "q35",
        cpu = "host",
        memory = "1G",
        firmware = "ovmf",
        display = "none",
    ),
)
```

### Image Unit (`units/<name>.star`)

An image is a unit that assembles a root filesystem from packages and produces a
disk image. Image units use the `image()` class function instead of `unit()`.
They participate in the same DAG, use the same caching, and are built with
`yoe build`.

```python
load("//classes/image.star", "image")

image(
    name = "base-image",
    version = "1.0.0",
    description = "Minimal bootable system",
    # Packages installed into the rootfs.
    # The base system (C library + busybox + init) is implicit unless excluded.
    artifacts = [
        "openssh",
        "networkmanager",
        "myapp",
        "monitoring-agent",
    ],
    # hostname defaults to MACHINE (e.g. "raspberrypi4"); set to override.
    timezone = "UTC",
    locale = "en_US.UTF-8",
    services = ["sshd", "NetworkManager", "myapp"],
    partitions = [
        partition(label="boot", type="vfat", size="64M",
                  contents=["MLO", "u-boot.img", "zImage", "*.dtb"]),
        partition(label="rootfs", type="ext4", size="fill", root=True),
    ],
)
```

### Image Composition and Variants

Image variants use plain Starlark variables and list concatenation — no special
inheritance mechanism:

```python
load("//classes/image.star", "image")

BASE_PACKAGES = [
    "openssh",
    "networkmanager",
    "myapp",
    "monitoring-agent",
]

BASE_SERVICES = ["sshd", "NetworkManager", "myapp"]

BBB_PARTITIONS = [
    partition(label="boot", type="vfat", size="64M",
              contents=["MLO", "u-boot.img", "zImage", "*.dtb"]),
    partition(label="rootfs", type="ext4", size="fill", root=True),
]

image(
    name = "base-image",
    version = "1.0.0",
    packages = BASE_PACKAGES,
    services = BASE_SERVICES,
    partitions = BBB_PARTITIONS,
    # hostname defaults to MACHINE; pass an explicit string to override.
)

image(
    name = "dev-image",
    version = "1.0.0",
    description = "Development image with debug tools",
    packages = BASE_PACKAGES + ["gdb", "strace", "tcpdump", "vim"],
    exclude = ["monitoring-agent"],
    services = BASE_SERVICES,
    partitions = BBB_PARTITIONS,
)
```

**Conditional packages per machine:**

```python
artifacts = ["openssh", "myapp"]
if machine.arch == "arm64":
    packages += ["arm64-firmware"]
```

### Package Unit (`units/<name>.star`)

Describes how to build a system-level package (C/C++ libraries, system daemons,
etc.) and produce an `.apk`. Uses a class function like `autotools()`,
`cmake()`, or the generic `unit()`.

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "openssh",
    version = "9.6p1",
    description = "OpenSSH client and server",
    license = "BSD",
    source = "https://cdn.openbsd.org/pub/OpenBSD/OpenSSH/portable/openssh-9.6p1.tar.gz",
    sha256 = "...",
    configure_args = ["--sysconfdir=/etc/ssh"],
    deps = ["zlib", "openssl"],
    runtime_deps = ["zlib", "openssl"],
    services = ["sshd"],
    conffiles = ["/etc/ssh/sshd_config"],
)
```

Or using the generic `unit()` for custom build steps:

```python
unit(
    name = "openssh",
    version = "9.6p1",
    source = "https://cdn.openbsd.org/pub/OpenBSD/OpenSSH/portable/openssh-9.6p1.tar.gz",
    sha256 = "...",
    deps = ["zlib", "openssl"],
    runtime_deps = ["zlib", "openssl"],
    build = [
        "./configure --prefix=$PREFIX --sysconfdir=/etc/ssh",
        "make -j$NPROC",
        "make DESTDIR=$DESTDIR install",
    ],
    services = ["sshd"],
    conffiles = ["/etc/ssh/sshd_config"],
)
```

### Patches

Units can apply patches to upstream source after fetching and before building.
Patches are listed in order and applied with `git apply` or `patch -p1`:

```python
unit(
    name = "busybox",
    version = "1.36.1",
    source = "https://busybox.net/downloads/busybox-1.36.1.tar.bz2",
    patches = [
        "patches/busybox/fix-ash-segfault.patch",
        "patches/busybox/add-custom-applet.patch",
    ],
    build = ["make -j$NPROC", "make DESTDIR=$DESTDIR install"],
)
```

Patch file paths are relative to the project root. Patch contents are included
in the unit's cache hash — changing a patch triggers a rebuild.

**Module overrides for patches** work through the standard function composition
pattern:

```python
# upstream: @module-core/busybox.star
def busybox(extra_patches=[], **overrides):
    unit(
        name = "busybox",
        version = "1.36.1",
        source = "https://busybox.net/downloads/busybox-1.36.1.tar.bz2",
        patches = [
            "patches/busybox/fix-ash-segfault.patch",
        ] + extra_patches,
        build = ["make -j$NPROC", "make DESTDIR=$DESTDIR install"],
        **overrides,
    )

# vendor module: adds a patch without modifying upstream
load("@module-core//busybox.star", "busybox")
busybox(extra_patches=["patches/vendor-busybox-audit.patch"])
```

**Alternatives to patches:**

- **Git-based sources** — fork the repo, apply changes as commits, point the
  unit at your branch/tag. Cleaner history, easier to rebase on upstream
  updates.
- **Overlay files** — for config file changes on the target, the `overlays/`
  directory is simpler than patching source.

### Tasks and Per-Task Containers (planned)

> **Status:** `task()` and unit-level `container =` are _shipped_ — every
> built-in class in `modules/module-core/classes/` (autotools, cmake, go,
> container, image) already generates `tasks = [task(...)]` and the build
> executor (`internal/build/executor.go`) runs each task's steps inside the
> unit's resolved container. The _per-task_ `container=` override described
> below is _planned_: the task struct in Starlark accepts the field but the
> executor currently ignores it and uses the unit-level container for every task
> in the unit. Wire-through is the remaining work.

Units can define named build tasks via `task()`, each with an optional Docker
container. This replaces the flat `build = [...]` string list with structured
steps that can each run in different environments.

**Container resolution order:** task `container` → package `container` → bwrap
(default).

```python
# Simple — build list works as before (bwrap, no containers)
autotools(name = "zlib", source = "...", ...)

# Package-level container — all tasks inherit it
go_binary(
    name = "myapp",
    container = "golang:1.22-alpine",
    tasks = [
        task("build", run="go build -o $DESTDIR/usr/bin/myapp"),
        task("test", run="go test ./..."),
    ],
)

# Task-level override — codegen uses a different container
unit(
    name = "complex-app",
    container = "golang:1.22-alpine",       # default for all tasks
    tasks = [
        task("codegen",
             container="protoc:latest",     # overrides package default
             run="protoc --go_out=. api/*.proto"),
        task("compile",
             run="go build -o $DESTDIR/usr/bin/app"),  # inherits golang
        task("install",
             run="install -D app.service $DESTDIR/usr/lib/systemd/system/"),
    ],
)

# Mix of container and bwrap in one unit
unit(
    name = "hybrid-tool",
    tasks = [
        task("generate",
             container="codegen-tools:latest",
             run="generate-code --out src/"),
        task("compile", run="make -j$NPROC"),  # no container → bwrap
        task("install", run="make DESTDIR=$DESTDIR install"),
    ],
)
```

The `build = [...]` field remains for backward compatibility — internally
converted to unnamed tasks without containers. Classes generate tasks:

```python
# classes/autotools.star generates three tasks
def autotools(name, version, source, configure_args=[], **kwargs):
    unit(
        name=name, version=version, source=source,
        tasks = [
            task("configure",
                 run="test -f configure || autoreconf -fi && "
                     "./configure --prefix=$PREFIX " + " ".join(configure_args)),
            task("compile", run="make -j$NPROC"),
            task("install", run="make DESTDIR=$DESTDIR install"),
        ],
        **kwargs,
    )
```

**Extending a class's tasks** — when a unit passes `tasks=[...]` to a class
(`autotools`, `cmake`, `go_binary`), the class merges the overrides into its
default task list rather than replacing them entirely. Merge rules:

- Same name → replace at the existing position (the override's `steps` fully
  replace the base's; merging steps is not supported).
- New name → append to the end.
- `task("name", remove=True)` → drop that task from the base list.

```python
# Adds an init-script task without restating the class's default build task.
go_binary(
    name = "simpleiot",
    ...
    tasks = [
        task("init-script", steps = [
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("simpleiot.init",
                         "$DESTDIR/etc/init.d/simpleiot", mode = 0o755),
        ]),
    ],
)
```

Merging is implemented by `merge_tasks(base, overrides)` in
`modules/module-core/classes/tasks.star`. Custom classes that want the same
behavior should `load("//classes/tasks.star", "merge_tasks")` and call it before
passing `tasks` to `unit()`.

See [per-unit containers plan](superpowers/plans/per-unit-containers.md) for the
full design.

### Application Unit (`units/<name>.star`)

Applications built with language-native build systems use language-specific
class functions that delegate to the language toolchain.

```python
load("//classes/go.star", "go_binary")

go_binary(
    name = "myapp",
    version = "1.2.3",
    description = "Edge data collection service",
    license = "Apache-2.0",
    source = "https://github.com/example/myapp.git",
    tag = "v1.2.3",
    package = "./cmd/myapp",
    services = ["myapp"],
    conffiles = ["/etc/myapp/config.toml"],
    environment = {"DATA_DIR": "/var/lib/myapp"},
)
```

Language-specific classes handle the build details — `go_binary()` sets up
`GOMODCACHE`, runs `go build`, and packages the result.

> **Status:** Only `go_binary()` (in `modules/module-core/classes/go.star`) is
> implemented today. Similar classes for Rust (`rust_binary()`), Zig
> (`zig_binary()`), Python (`python_unit()`), and Node.js (`node_unit()`) are
> _planned_ but not yet shipped. Applications in those languages can still be
> built by using `unit()` directly with explicit build steps.

### Project Configuration (`PROJECT.star`)

Top-level configuration that ties everything together.

```python
project(
    name = "yoe",
    version = "0.1.0",
    description = "`[yoe]` embedded Linux distribution",
    defaults = defaults(
        machine = "qemu-arm64",
        image = "base-image",
    ),
    cache = cache(
        path = "/var/cache/yoe/build",
        remote = [
            s3_cache(
                name = "team",
                bucket = "yoe-cache",
                endpoint = "https://minio.internal:9000",
                region = "us-east-1",
            ),
        ],
        retention_days = 90,
        signing = "keys/cache.pub",
    ),
    sources = sources(
        go_proxy = "https://proxy.golang.org",
    ),
    modules = [
        # Module in a subdirectory of a repo — path specifies where MODULE.star is
        module("https://github.com/yoebuild/yoe.git",
              ref = "main",
              path = "modules/module-core"),
        # Module at the root of its own repo
        module("git@github.com:vendor/bsp-units.git", ref = "main"),
    ],
)
```

## Classes

Classes are Starlark functions that define build pipelines for different unit
types. They encapsulate the _how to build_ logic so that units only declare
_what to build_.

### Built-in Classes

These ship with the `module-core` module (at `modules/module-core/classes/`) or
are under the `(planned)` roadmap:

| Class           | Status  | Description                                   |
| --------------- | ------- | --------------------------------------------- |
| `unit()`        | shipped | Generic package — custom build steps as shell |
| `autotools()`   | shipped | configure / make / make install               |
| `cmake()`       | shipped | CMake build                                   |
| `go_binary()`   | shipped | Go application                                |
| `container()`   | shipped | Build a Docker/OCI container image            |
| `image()`       | shipped | Root filesystem image assembly                |
| `meson()`       | planned | Meson + Ninja build                           |
| `rust_binary()` | planned | Rust application (Cargo)                      |
| `zig_binary()`  | planned | Zig application                               |
| `python_unit()` | planned | Python package (pip/uv)                       |
| `node_unit()`   | planned | Node.js package (npm/pnpm)                    |

### Class Composition

Classes compose through function calls. A unit can use multiple classes, and
classes can wrap other classes:

```python
load("//classes/autotools.star", "autotools")
load("//classes/systemd.star", "systemd_service")

# Use both autotools and systemd classes
autotools(
    name = "openssh",
    version = "9.6p1",
    configure_args = ["--sysconfdir=/etc/ssh"],
    deps = ["zlib", "openssl"],
)

systemd_service(
    name = "openssh",
    unit = "sshd.service",
    conffiles = ["/etc/ssh/sshd_config"],
)
```

Or create a combined class:

```python
# classes/systemd_autotools.star
load("//classes/autotools.star", "autotools")
load("//classes/systemd.star", "systemd_service")

def systemd_autotools(name, unit, conffiles=[], **kwargs):
    autotools(name=name, **kwargs)
    systemd_service(name=name, unit=unit, conffiles=conffiles)
```

### Custom Classes

Projects can define their own classes in `classes/` for patterns specific to
their codebase:

```python
# classes/my_go_service.star
load("//classes/go.star", "go_binary")
load("//classes/systemd.star", "systemd_service")

def my_go_service(name, version, source, **kwargs):
    """Standard pattern for our Go microservices."""
    go_binary(
        name = name,
        version = version,
        source = source,
        **kwargs,
    )
    systemd_service(
        name = name,
        unit = name + ".service",
        conffiles = ["/etc/" + name + "/config.toml"],
    )
```

### Extensibility: Starlark and Go

Starlark is not a standalone language — it runs embedded inside the `yoe` Go
binary. Every built-in function (`unit()`, `machine()`, `image()`, etc.) is a Go
function registered into the Starlark environment. When Starlark code calls
`unit(name="openssh", ...)`, it executes Go code that has full access to the
host runtime.

This means the system is extensible in two directions:

**Go to Starlark (primitives):** The `yoe` binary provides built-in functions
that Starlark code can call. These have capabilities Starlark alone cannot —
filesystem I/O, network access, executing system tools (apk, bwrap, git),
managing the build engine state. Adding a new built-in is a Go function with the
right signature:

```go
// In Go: register a new built-in function
func (e *Engine) fnDeploy(thread *starlark.Thread, fn *starlark.Builtin,
    args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
    target := kwString(kwargs, "target")
    // Full access to Go runtime — HTTP, filesystem, exec, etc.
    return starlark.None, nil
}

// Register it in builtins():
"deploy": starlark.NewBuiltin("deploy", e.fnDeploy),
```

Now any `.star` file can call `deploy(target="production")`.

**Starlark to Starlark (composition):** Users define functions in `.star` files
that compose the Go-provided primitives. Classes, macros, and helpers are just
Starlark functions that call built-in functions:

```python
# classes/my_service.star — user-defined class wrapping Go builtins
def my_service(name, version, **kwargs):
    go_binary(name=name, version=version, **kwargs)  # calls Go
    systemd_service(name=name, unit=name + ".service")  # calls Go
```

**The architecture mirrors Bazel:** Go provides the **primitives** (package
creation, image assembly, sandbox execution, cache management), Starlark
provides the **composition layer** (classes, conditionals, module overrides,
shared variables). Starlark code cannot perform arbitrary I/O — it can only call
the Go functions that `yoe` explicitly exposes, maintaining the sandboxed,
deterministic evaluation model.

## Directory Structure

A typical `[yoe]` project layout:

```
my-project/
├── PROJECT.star
├── machines/
│   ├── beaglebone-black.star
│   ├── raspberrypi4.star
│   └── qemu-arm64.star
├── units/
│   ├── base-image.star         # image() class
│   ├── dev-image.star          # image() class, extends base
│   ├── openssh.star            # autotools() class
│   ├── zlib.star
│   ├── openssl.star
│   ├── myapp.star              # go_binary() class
│   └── monitoring-agent.star
├── classes/                    # reusable build rule functions
├── commands/                  # custom yoe subcommands
│   ├── my_go_service.star
│   └── ...
└── overlays/
    └── custom-configs/         # files copied directly into rootfs
        └── etc/
            └── myapp/
                └── config.toml
```

## Build Flow

```
  units/*.star               (all unit types: package and image)
       │
       ▼
  yoe build                    (evaluate Starlark, resolve DAG, build)
       │
       ├─ unit() ──▶ compile source ──▶ *.apk artifacts ──▶ repository/
       │
       └─ image()   ──▶ apk install deps into rootfs
                        ──▶ apply overlays + config
                        ──▶ partition + format
                        ──▶ disk image (.img / .wic)
```

## Modules

Modules are external Git repositories that provide units, classes, and machine
definitions. They are the primary mechanism for reusing and sharing build
definitions across projects — BSP vendors ship modules, and product teams
compose them.

### Declaring Modules in PROJECT.star

```python
project(
    name = "my-product",
    version = "1.0.0",
    modules = [
        # Module in a subdirectory of a repo
        module("https://github.com/yoebuild/yoe.git",
              ref = "main",
              path = "modules/module-core"),
        # Module at the root of its own repo
        module("git@github.com:vendor/bsp-imx8.git", ref = "v2.1.0"),
    ],
)
```

Each `module()` call declares a Git repository URL and a ref (tag, branch, or
commit SHA). The optional `path` field specifies a subdirectory within the repo
where `MODULE.star` lives — this allows a single repo to contain multiple
modules or a module to be part of a larger project. The `yoe` tool fetches and
caches these repositories, making them available as `@module-name` in `load()`
statements. The module name is derived from the last component of `path` (if
set) or the URL.

### Module Manifests (MODULE.star) (planned)

> **Status:** The `module_info()` Starlark builtin is wired up in
> `internal/starlark/builtins.go` and the `ModuleInfo` struct is populated when
> a `MODULE.star` is evaluated, but the module resolver in `internal/module/`
> never reads those declared `deps`. Transitive module resolution — both the v1
> "error on missing" and v2 "auto-fetch" behaviors below — is _planned_. Today
> only the top-level `modules = [...]` list in `PROJECT.star` is fetched.

Modules can declare their own dependencies via a `MODULE.star` file in the
repository root. This enables BSP vendors to ship self-contained modules without
requiring users to manually discover transitive dependencies.

```python
# In github.com/vendor/bsp-imx8/MODULE.star
module_info(
    name = "vendor-bsp-imx8",
    description = "i.MX8 BSP units and machine definitions",
    deps = [
        module("github.com/vendor/hal-common", ref = "v1.3.0"),
        module("github.com/vendor/firmware-imx", ref = "v5.4"),
    ],
)
```

### Dependency Resolution Rules

Module dependencies follow the **Go modules model** — the root project has final
authority over versions:

1. **PROJECT.star always wins.** If PROJECT.star and a MODULE.star both
   reference the same repository, the version in PROJECT.star takes precedence.
   This gives the project owner full control over the dependency tree.

2. **Transitive deps are checked, not silently fetched (v1).** In the initial
   implementation, `yoe` reads each module's `MODULE.star` and **errors** if a
   required dependency is missing from PROJECT.star, rather than silently
   fetching it. The error message tells the user exactly what to add. This is
   explicit and debuggable.

3. **Automatic transitive resolution (v2).** In a future version, transitive
   dependencies declared in `MODULE.star` are fetched automatically when not
   overridden by PROJECT.star. `yoe module list` shows the full resolved tree so
   nothing is hidden.

4. **Diamond dependencies resolve to the highest version.** If two modules
   depend on different versions of the same repository, `yoe` selects the higher
   version (semver comparison) unless PROJECT.star pins a specific version.

**Example — v1 behavior (missing transitive dep):**

```
$ yoe build --all
Error: module "vendor-bsp-imx8" requires "github.com/vendor/hal-common" (ref v1.3.0)
       but it is not declared in PROJECT.star.

Add this to your PROJECT.star modules list:
    module("github.com/vendor/hal-common", ref = "v1.3.0"),
```

**Example — PROJECT.star overriding a transitive version:**

```python
# PROJECT.star
modules = [
    module("github.com/yoe/module-core", ref = "v1.0.0"),
    module("github.com/vendor/bsp-imx8", ref = "v2.1.0"),
    # Override the version that bsp-imx8 requests (v1.3.0 → v1.4.0)
    module("github.com/vendor/hal-common", ref = "v1.4.0"),
]
```

### Local Module Overrides

During development, you often want to work on a module locally instead of
fetching from Git. The `local` parameter overrides the remote URL:

```python
modules = [
    # Local override — point at a checkout on disk instead of fetching
    module("https://github.com/yoebuild/yoe.git",
          local = "../yoe",
          path = "modules/module-core"),
    # Local override for a standalone module
    module("git@github.com:vendor/bsp-imx8.git", local = "../bsp-imx8"),
]
```

When `local` is set, `yoe` uses the local directory directly (no fetch, no ref
checking). If `path` is also set, it is appended to the local path. This is
equivalent to Go's `replace` directive in `go.mod`.

## Label-Based References

Inspired by Bazel's label system and GN's `//path/to:target`, `[yoe]` uses a
label scheme for referencing units and classes across repositories:

```python
# Local references (within the current project)
load("//classes/autotools.star", "autotools")   # from project root
load("//units/openssh.star", "openssh_config") # load shared config

# External references (from modules)
load("@module-core//openssh.star", "openssh")
load("@vendor-bsp//kernel.star", "vendor_kernel")
```

Module names (`@module-core`, `@vendor-bsp`) map to the modules declared in
`PROJECT.star`. When `yoe` evaluates units, it fetches and caches external
modules, then resolves all `load()` references to concrete files.

## Module Composition

Modules enable the vendor BSP / product overlay pattern without modifying
upstream units:

```python
# Module 1: @module-core/openssh.star — base unit as a function
def openssh(extra_deps=[], extra_configure_args=[], **overrides):
    autotools(
        name = "openssh",
        version = "9.6p1",
        deps = ["zlib", "openssl"] + extra_deps,
        configure_args = ["--sysconfdir=/etc/ssh"] + extra_configure_args,
        **overrides,
    )

# Module 2: @vendor-bsp/openssh.star — vendor extends it
load("@module-core//openssh.star", "openssh")
openssh(extra_deps=["vendor-crypto"])

# Module 3: product unit — further customization
load("@vendor-bsp//openssh.star", "openssh")
openssh(extra_configure_args=["--with-pam"])
```

Each module is explicit about what it modifies and where the base comes from.
This is more traceable than Yocto's bbappend system — you can grep for the
function call to find all modifications.

## Design Notes

- **Starlark over TOML/YAML** — pure data formats accumulate escape hatches
  (conditional deps, shell in strings, inheritance). Starlark makes the implicit
  explicit while remaining readable for simple cases. See
  [Build Languages](build-languages.md) for the full analysis.
- **Prefer git sources over tarballs** — git sources give you upstream history,
  clean `git rebase` for patch updates, natural `yoe dev` workflow (edit,
  commit, extract patches), and no SHA256 to maintain. Use
  `source = "https://...git"` with a `tag` to pin the version.
- **One file per unit** — each unit is its own `.star` file. This keeps diffs
  clean and makes it easy to add/remove components.
- **Units and packages are separate concerns** — units are version-controlled
  build instructions; packages are binary artifacts. This separation enables
  building once and deploying many times, sharing packages across teams, and
  on-device incremental updates via `apk`.
- **Classes as functions** — build patterns (autotools, cmake, go) are Starlark
  functions, not a type system. Multiple classes compose through function calls.
  This is simpler and more flexible than Yocto's class inheritance.
- **Unified unit directory** — system packages, application packages, and images
  all live in `units/`. The class function determines the output: `unit()` /
  `autotools()` / etc. produce `.apk` files, `image()` produces disk images. One
  concept (unit), one directory, one DAG.
- **apk for image assembly** — image units declare their packages as
  dependencies. `yoe build <image>` creates a clean rootfs and runs `apk add` to
  populate it from the repository, exactly like Alpine's image builder. This
  leverages apk's dependency resolution rather than reimplementing it.
