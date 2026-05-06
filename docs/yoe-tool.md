# The `[yoe]` Tool

`yoe` is the single CLI tool that drives all `[yoe]` workflows — building
packages and images from units, managing caches and source downloads, and
flashing devices. It is a statically-linked Go binary with no runtime
dependencies.

## Installation

Prerequisites: Linux or macOS with Git and Docker installed. Windows users:
install WSL2 and use the Linux binary (Linux x86_64/Docker is the most tested
configuration). Claude Code is highly recommended, but not required.

```sh
# Download the yoe binary (Linux x86_64)
curl -L https://github.com/yoebuild/yoe/releases/latest/download/yoe-Linux-x86_64 -o yoe
# For other platforms, download from https://github.com/yoebuild/yoe/releases/latest

chmod +x yoe
mkdir -p ~/bin
mv yoe ~/bin/
# Make sure ~/bin is in your PATH (add to ~/.bashrc or ~/.zshrc if needed)
export PATH="$HOME/bin:$PATH"
```

Since `yoe` is a Go binary, it cross-compiles trivially — build on your x86
workstation, run on an ARM build server.

## Command Overview

```
yoe                 Launch the interactive TUI
yoe init            Create a new `[yoe]` project
yoe build           Build units (packages and images)
yoe shell           Open an interactive shell in a unit's build sandbox  [planned]
yoe dev             Manage source modifications (extract, diff, status)
yoe flash           Write an image to a device/SD card
yoe run             Run an image in QEMU
yoe serve           Serve the project's apk repo over HTTP+mDNS
yoe deploy          Build and install a unit on a running yoe device
yoe device          Manage repo configuration on a target device
yoe module          Manage external modules (fetch, sync, list)
yoe repo            Manage the local apk package repository
yoe cache           Manage the build cache (local and remote)  [planned]
yoe bundle          Export/import content-addressed bundles (air-gapped)  [planned]
yoe source          Download and manage source archives/repos
yoe config          View and edit project configuration
yoe desc            Describe a unit, package, or target
yoe refs            Show reverse dependencies
yoe graph           Visualize the dependency DAG
yoe log             Show build log (most recent or specific unit)
yoe diagnose        Launch Claude Code to diagnose a build failure
yoe clean           Remove build artifacts
yoe container       Manage the build container (build, binfmt, status)
```

All commands except `init`, `version`, and `container` run inside an Alpine
build container automatically. The container is built on first use from
`containers/Dockerfile.build`. See
[Build Environment](build-environment.md#tier-0-bootstrap-module-automatic-container)
for details.

## Commands

### `yoe init`

Scaffolds a new `[yoe]` project directory with the standard layout.

```sh
yoe init my-project
```

Creates:

```
my-project/
├── PROJECT.star
├── machines/
├── units/
├── classes/
└── overlays/
```

Optionally specify a machine to start with:

```sh
yoe init my-project --machine beaglebone-black
```

### `yoe build`

Builds one or more units. Package units (`unit()`, `autotools()`, etc.) produce
`.apk` packages and publish them to the local repository. Image units
(`image()`) assemble a root filesystem and produce a disk image. The class
function used in the `.star` file determines the behavior — the command is the
same for both.

```sh
# Build a single package unit
yoe build openssh

# Build multiple units
yoe build openssh zlib openssl

# Build an image unit (assembles rootfs, produces disk image)
yoe build base-image

# Build an image for a specific machine
yoe build base-image --machine raspberrypi4

# Build for ARM64 on an x86_64 host (uses QEMU user-mode emulation)
yoe build base-image --machine qemu-arm64

# Build all units (packages and images)
yoe build --all

# Build all image units for all machines (full matrix)
yoe build --all --class image             # planned: --class filter

# Build a unit and all its dependencies
yoe build --with-deps myapp               # planned: --with-deps flag

# Rebuild even if the cache is fresh
yoe build --force openssh

# Skip remote cache — only check local cache
yoe build --no-remote-cache openssh       # planned: remote cache

# Skip all caches — force build from source
yoe build --no-cache openssh

# Dry run — show what would be built and why
yoe build --dry-run --all

# List available image/machine combinations
yoe build --list-targets                  # planned
```

**What happens during a build:**

Inspired by Google's GN, `yoe build` uses a **two-phase resolve-then-build**
model. The entire dependency graph is resolved and validated _before_ any build
work starts. This catches missing dependencies, cycles, and configuration errors
up front rather than mid-build.

1. **Sync modules** — fetch or update external modules declared in
   `PROJECT.star` (skipped if already up to date). See `yoe module sync`.
2. **Evaluate Starlark** — load and evaluate all `.star` unit files (including
   those from modules) to produce the set of build targets. Each class function
   call (`unit()`, `autotools()`, `image()`, etc.) registers a target.
3. **Resolve dependencies** — topologically sort the build order from declared
   dependencies. Validate that all referenced units exist and the graph is
   acyclic. **If any errors are found, stop here** — no partial builds.
4. **Check cache** — compute a content hash of the unit + source + build
   dependencies. If a cached `.apk` with that hash exists (locally or in a
   remote cache), skip the build.
5. **Fetch source** — download the source archive or clone the git repo (see
   `yoe source` below). Sources are cached in `$YOE_CACHE/sources/`.
6. **Prepare build environment** — set up an isolated build root with only
   declared build dependencies installed via `apk`. This ensures hermetic
   builds.
7. **Execute build steps** — run the build commands defined by the class
   function in the build root. The environment provides:
   - `$PREFIX` — install prefix (typically `/usr`)
   - `$DESTDIR` — staging directory for installed files
   - `$NPROC` — number of available CPU cores
   - `$ARCH` — target architecture
8. **Package** — collect files from `$DESTDIR`, generate `.PKGINFO` from the
   unit metadata, and create the `.apk` archive.
9. **Publish** — add the `.apk` to the local repository and update the repo
   index.

**For image units** (`image()` class), steps 5-9 are replaced with image
assembly:

1. **Sync modules** — same as above.
2. **Evaluate Starlark** — same as above.
3. **Resolve dependencies** — same as above.
4. **Check cache** — same as above.
5. **Read machine definition** — evaluate `machines/<name>.star` for
   architecture, kernel, bootloader, and partition layout.
6. **Create empty rootfs** — set up a temporary directory.
7. **Install packages** — run `apk add --root <rootfs>` with the `[yoe]`
   repository to install all declared packages. apk handles dependency
   resolution.
8. **Apply configuration** — set hostname, timezone, locale, and enable services
   per the image unit's configuration (via the active init system — busybox init
   today, systemd a possible future option).
9. **Apply overlays** — copy files from `overlays/` into the rootfs.
10. **Install kernel + bootloader** — build (or fetch from cache) the kernel and
    bootloader per the machine definition, install into the rootfs/boot
    partition.
11. **Generate disk image** — partition the output image per the partition
    layout and populate each partition.

Output format can be specified with `--format`:

```sh
yoe build base-image --format sdcard    # raw disk image with partitions
yoe build base-image --format rootfs    # tar.gz of the rootfs only
yoe build base-image --format squashfs  # squashfs for read-only roots
```

### `yoe flash`

Writes a built image to a block device or SD card.

```sh
# Flash to SD card (auto-detects the most recent image build)
yoe flash /dev/sdX

# Flash a specific image unit's output
yoe flash base-image /dev/sdX

# Flash for a specific machine
yoe flash base-image --machine beaglebone-black /dev/sdX

# Dry run — show what would happen
yoe flash --dry-run /dev/sdX
```

Safety: `yoe flash` requires explicit confirmation before writing and refuses to
write to mounted devices or devices that look like system disks.

### `yoe run`

Launches a built image in QEMU for development and testing. When the host and
target architecture match, QEMU uses KVM hardware virtualization for near-native
speed. For cross-architecture images (e.g., ARM64 on x86_64), QEMU runs in
software emulation mode automatically.

```sh
# Run the most recently built image (auto-detects machine/image)
yoe run

# Run a specific image unit
yoe run dev-image --machine qemu-x86_64

# Run an ARM64 image on an x86_64 host (software emulation)
yoe run base-image --machine qemu-arm64

# Forward an extra host port (default qemu machines already forward 2222→22,
# 8080→80, and 8118→8118 — `--port` adds to that list)
yoe run --port 9000:9000

# Allocate more memory
yoe run --memory 2G

# Run with graphical output (default is serial console)
yoe run --display

# Run headless in the background
yoe run --daemon
```

**What happens:**

1. **Detect architecture** — read the machine definition to determine the target
   architecture (x86_64, aarch64, riscv64).
2. **Select QEMU binary** — map to the correct `qemu-system-*` binary.
3. **Configure machine** — for x86_64, use the `q35` machine type with UEFI
   firmware (OVMF). For aarch64, use `virt` with UEFI (AAVMF). For riscv64, use
   `virt` with OpenSBI.
4. **Enable KVM** — hardware virtualization is always used since host and guest
   architectures match.
5. **Attach image** — use the built disk image as a virtio block device.
6. **Route console** — by default, connect the serial console to the terminal
   (`-nographic`). The guest kernel must have `console=ttyS0` (x86) or
   `console=ttyAMA0` (aarch64) in its command line.
7. **Set up networking** — use QEMU user-mode networking with port forwarding.
   The qemu-x86_64 and qemu-arm64 machines forward `2222:22` (SSH), `8080:80`,
   and `8118:8118` by default, so SSH to the guest works without any extra
   flags. `--port` adds to that list.

**QEMU machine definitions:**

Projects can define QEMU-specific machines alongside hardware ones:

```python
# machines/qemu-x86_64.star
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

When `yoe run` is given a machine with a `qemu` configuration, it uses those
settings directly. When given a hardware machine without `qemu` configuration,
it falls back to a reasonable default QEMU configuration for the machine's
architecture.

### `yoe serve`

Runs an HTTP server rooted at the project's `repo/` tree and advertises it on
mDNS as `_yoe-feed._tcp.local.` so devices and `yoe deploy` discover it
automatically.

```sh
# Serve at the default port (8765) with mDNS advertisement
yoe serve

# Bind to a specific interface or change the port
yoe serve --bind 192.168.1.10 --port 9000

# Skip mDNS (e.g., inside a container without host networking)
yoe serve --no-mdns
```

The default port is pinned (`8765`) so the URL written by `yoe device repo add`
on a target survives `yoe serve` restarts. apks and `APKINDEX.tar.gz` are
already signed by the project key, so plain HTTP transport is fine for
development. See [feed-server.md](feed-server.md) for the full dev-loop guide.

### `yoe deploy`

Builds a unit, exposes the project's repo as a feed (reusing a running
`yoe serve` if one is up, otherwise spinning up an ephemeral feed on the same
pinned port), then ssh's to the device and runs `apk add --upgrade <unit>`.
Transitive dependencies resolve on the device against the same `APKINDEX.tar.gz`
production OTA uses.

```sh
# Build myapp and install it on dev-pi over the LAN
yoe deploy myapp dev-pi.local

# Deploy to a QEMU vm started with `yoe run` (default 2222→22 forward)
yoe deploy myapp localhost:2222

# Non-root ssh user
yoe deploy myapp pi@dev-pi.local

# Cross-subnet or mDNS-hostile network — advertise an explicit IP
yoe deploy myapp 10.0.5.42 --host-ip 10.0.5.1
```

The repo file `/etc/apk/repositories.d/yoe-dev.list` is left in place after
deploy, so the device stays configured to pull from the dev host on any future
`apk add` from the device. Use `yoe device repo remove <host>` to tear it down.
Image targets error with a pointer to `yoe flash`.

### `yoe device`

Configures `/etc/apk/repositories.d/` on a target device so `apk add` from the
device pulls from your dev feed. Useful standalone (without an immediate
`yoe deploy`) to set up a fresh device, configure several devices for a
multi-device QA bench, or inspect what's currently configured.

```sh
# Auto-discover the running yoe serve on the LAN, configure dev-pi
yoe device repo add dev-pi.local

# Same, plus push the project signing pubkey to /etc/apk/keys/ on the
# target — needed if the device was flashed before the project key existed
yoe device repo add dev-pi.local --push-key

# Configure a QEMU vm started with `yoe run` (default 2222→22 forward)
yoe device repo add localhost:2222

# Explicit feed URL (colleague's serve, or non-mDNS network)
yoe device repo add 192.168.4.30 --feed http://laptop.local:8765/myproj

# Tear down
yoe device repo remove dev-pi.local

# Inspect /etc/apk/repositories and /etc/apk/repositories.d/*.list
yoe device repo list dev-pi.local
```

After `yoe device repo add`, run `apk update && apk add htop` (or any unit your
project builds) directly on the device. `yoe deploy` writes the same file by
default (`yoe-dev.list`), so the first deploy doubles as the persistent feed
config.

### `yoe module`

Manages external modules — the Git repositories declared in `PROJECT.star` that
provide units, classes, and machine definitions.

> **Status:** `yoe module sync` and `yoe module list` are implemented.
> `yoe module info`, `yoe module check-updates`, and `yoe module list --tree`
> (transitive tree output) are _planned_ — the CLI dispatches them today with a
> "not yet implemented" stub message.

```sh
# Fetch/update all modules to the refs declared in PROJECT.star
yoe module sync

# List all modules with status (fetched, local override, version)
yoe module list

# Show the full resolved module tree (including transitive deps from MODULE.star)
yoe module list --tree        # planned

# Show details for a specific module
yoe module info @vendor-bsp   # planned

# Check for updates — show if upstream has newer tags
yoe module check-updates      # planned
```

**What happens during `yoe module sync`:**

1. **Read PROJECT.star** — parse the `modules` list.
2. **Read MODULE.star from each module** — discover transitive dependencies.
3. **Resolve versions** — PROJECT.star versions override transitive deps. If a
   required transitive dep is missing, error with an actionable message.
4. **Fetch/update** — clone or update each module's Git repo into
   `$YOE_CACHE/modules/`. Checkout the declared ref.
5. **Verify** — confirm that each module's `MODULE.star` (if present) is valid
   Starlark.

**Module caching:** Modules are cached in `$YOE_CACHE/modules/` as bare Git
repositories with worktree checkouts at the pinned ref. `yoe module sync`
performs incremental fetches — only downloading new objects.

**Automatic sync:** `yoe build` automatically runs module sync if any module is
missing or if `PROJECT.star` has changed since the last sync. You rarely need to
run `yoe module sync` manually.

**Local overrides:** Modules with `local = "..."` in PROJECT.star skip fetching
entirely and use the local directory. `yoe module list` shows these as
`(local: ../path)`.

**Example output of `yoe module list`:**

```
Module                             Ref        Status
@module-core                      v1.0.0     up to date
@vendor-bsp-imx8                   v2.1.0     up to date
  └─ @hal-common                   v1.3.0     up to date (transitive)
  └─ @firmware-imx                 v5.4       up to date (transitive)
@my-local-module                   main       (local: ../my-module)
```

### `yoe repo`

Manages the local apk package repository.

> **Status:** `yoe repo list`, `yoe repo info`, and `yoe repo remove` are
> implemented. `yoe repo push` and `yoe repo pull` (S3-compatible remote
> repository sync) are _planned_ — there is no S3 backend yet.

```sh
# List all packages in the repository
yoe repo list

# Show details of a specific package
yoe repo info openssh

# Remove a package from the repository
yoe repo remove openssh-9.5p1-r0

# Push local repository to a remote (S3-compatible)
yoe repo push                 # planned

# Pull packages from a remote repository
yoe repo pull                 # planned
```

The local repository lives at `repo/<project-name>/` within the project
directory. It's a standard apk-compatible repository — you can point `apk` on a
running device at it directly.

### `yoe cache` (planned)

> **Status:** Not implemented. `cmd/yoe/main.go` has no `cache` case in its
> command switch — invoking `yoe cache` prints "Unknown command". Content
> addressing and a local build cache exist inside the build executor, but there
> is no user-facing cache subcommand, no remote/S3 cache, no signing, and no
> `yoe cache stats` / `gc` / `push` / `pull`. The surface below describes the
> planned design.

Manages the local and remote build caches.

```sh
# Show cache status — local size, remote config, hit rate
yoe cache status

# List cached packages (local)
yoe cache list

# Show what's cached for a specific unit
yoe cache list openssh

# Push locally-built packages to the remote cache
yoe cache push

# Push specific packages
yoe cache push openssh zlib

# Pull packages from the remote cache into local
yoe cache pull

# Remove local cache entries older than retention period
yoe cache gc

# Remove all local cache entries
yoe cache gc --all

# Verify integrity of cached packages (check hashes and signatures)
yoe cache verify

# Show cache hit/miss statistics for the last build
yoe cache stats
```

**Cache push/pull vs. repo push/pull:** `yoe repo` manages the **apk package
repository** (the repo index that `apk` consumes during image assembly).
`yoe cache` manages the **build cache** (content-addressed build outputs keyed
by input hash). In practice, both store `.apk` files, but the cache is keyed by
build inputs while the repo is indexed by package name/version. Pushing to the
cache shares _build avoidance_ with CI/team. Pushing to the repo shares
_installable packages_ with devices.

### `yoe source`

Manages source downloads. Sources are cached locally to avoid repeated
downloads.

```sh
# Download sources for a unit
yoe source fetch openssh

# Download sources for all units
yoe source fetch --all

# List cached sources
yoe source list

# Verify source integrity (check sha256)
yoe source verify

# Clean stale sources
yoe source clean
```

Sources are stored in `$YOE_CACHE/sources/` with content-addressed naming. For
git sources, bare clones are cached and updated incrementally.

### `yoe config`

View and edit project configuration.

```sh
# Show current configuration
yoe config show

# Set the default machine
yoe config set defaults.machine raspberrypi4

# Set the default image
yoe config set defaults.image dev

# Show resolved configuration for a build
yoe config resolve --machine beaglebone-black --image base
```

### `yoe desc`

Describes a unit, showing its resolved configuration, dependencies, build inputs
hash, and package output. Inspired by GN's `gn desc`.

```sh
# Show full details of a unit
yoe desc openssh

# Example output:
#   Unit:       openssh
#   Version:      9.6p1
#   Source:       https://cdn.openbsd.org/.../openssh-9.6p1.tar.gz
#   Build deps:   zlib, openssl
#   Runtime deps: zlib, openssl
#   Input hash:   a3f8c2...
#   Cached .apk:  yes (openssh-9.6p1-r0.apk)
#   Config:       CFLAGS=-O2 -march=armv8-a (propagated from machine)

# Show only the resolved config for a unit
yoe desc openssh --config

# Show the build inputs that contribute to the hash
yoe desc openssh --inputs
```

### `yoe refs`

Shows reverse dependencies — what units or images depend on a given unit.
Inspired by GN's `gn refs`.

```sh
# What depends on openssl?
yoe refs openssl

# Example output:
#   Build deps:
#     openssh (build + runtime)
#     curl (build + runtime)
#     python (build)
#   Images:
#     base (via openssh, curl)
#     dev (via openssh, curl, python)

# Show only direct dependents
yoe refs openssl --direct

# Show the full transitive tree
yoe refs openssl --tree
```

This is essential for answering "if I update openssl, what needs to rebuild?"

### `yoe graph`

Visualizes the dependency DAG.

```sh
# Print the dependency graph as text
yoe graph

# Output DOT format for graphviz
yoe graph --format dot | dot -Tpng -o deps.png

# Show graph for a single unit and its deps
yoe graph openssh

# Show only units that need rebuilding
yoe graph --stale
```

### `yoe` (no args)

Running `yoe` with no arguments launches an interactive terminal UI showing all
units with their build status.

```
  `[yoe]`  Machine: qemu-x86_64  Image: base-image

  NAME                         CLASS        STATUS
→ base-files                   unit         ● cached
  busybox                      unit         ● cached
  linux                        unit         ▌building...
  musl                         unit         ● waiting
  ncurses                      autotools    ● cached
  openssh                      unit         ● failed
  openssl                      autotools    ● cached
  util-linux                   autotools
  zlib                         autotools    ● cached

  b build  e edit  d diagnose  l log  c clean  / search  q quit
```

#### Status indicators

| Indicator      | Color          | Meaning                     |
| -------------- | -------------- | --------------------------- |
| (none)         | —              | Never built                 |
| `● cached`     | dim/gray       | Built and cached            |
| `● waiting`    | yellow         | Queued, deps building first |
| `▌building...` | flashing green | Actively compiling          |
| `● failed`     | red            | Last build failed           |

When you build a unit, its dependencies appear as "waiting" (yellow), then
transition to "building" (flashing green) as the executor reaches them. Multiple
deps can flash green simultaneously.

#### Key bindings (unit list)

| Key     | Action                                               |
| ------- | ---------------------------------------------------- |
| `b`     | Build selected unit in background                    |
| `e`     | Open unit's `.star` file in `$EDITOR`                |
| `d`     | Launch `claude diagnose` for the unit                |
| `l`     | Open unit's build log in `$EDITOR`                   |
| `a`     | Launch `claude /new-unit`                            |
| `c`     | Clean selected unit's build artifacts (with confirm) |
| `/`     | Search/filter units by name                          |
| `Enter` | Show detail view (build output + log tail)           |
| `B`     | Build all units in background                        |
| `C`     | Clean all build artifacts (with confirm)             |
| `j/k`   | Navigate up/down                                     |
| `q`     | Quit                                                 |

#### Detail view

Pressing Enter on a unit shows a split-pane detail view:

- **BUILD OUTPUT** (top) — executor progress: dependency resolution, cache hits,
  build status for each dep
- **BUILD LOG** (bottom) — tail of the unit's `build.log`, updated in real time
  during a build

| Key   | Action                        |
| ----- | ----------------------------- |
| `Esc` | Return to unit list           |
| `b`   | Build this unit in background |
| `d`   | Launch `claude diagnose`      |
| `l`   | Open build log in `$EDITOR`   |

#### Search

Press `/` to enter search mode. Type to filter — only matching units are shown.
Press Enter to accept the filter, Esc to cancel and show all units.

Builds call `build.BuildUnits()` directly (in-process, no subprocess). The
executor sends events to the TUI as each unit starts and finishes building.

The TUI is built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

### `yoe log`

Shows a build log. With no arguments, shows the most recently modified build
log. Specify a unit name to view that unit's log.

```
yoe log                  # show most recent build log
yoe log openssl          # show openssl build log
yoe log openssl -e       # open openssl build log in $EDITOR
```

The `-e` / `--edit` flag opens the log in your editor (defaults to `vi`).

### `yoe diagnose`

Launches Claude Code to diagnose a build failure. With no arguments, diagnoses
the most recent build failure. Specify a unit name to diagnose that unit.

```
yoe diagnose             # diagnose most recent failure
yoe diagnose util-linux  # diagnose util-linux build failure
```

Requires `claude` to be in your PATH. Claude Code reads the build log and
iteratively identifies root causes, applies fixes, and rebuilds until the unit
succeeds.

### Custom Commands

Projects can define custom commands in `commands/*.star` that become first-class
`yoe` subcommands. This is similar to Zephyr's `west` extensions but uses
Starlark instead of Python classes.

```python
# commands/deploy.star
command(
    name = "deploy",
    description = "Deploy image to target device via SSH",
    args = [
        arg("target", required=True, help="Target device hostname/IP"),
        arg("--image", default="base-image", help="Image to deploy"),
        arg("--reboot", type="bool", help="Reboot after install"),
    ],
)

def run(ctx):
    img = ctx.args.image
    target = ctx.args.target
    ctx.log("Deploying", img, "to", target)
    ctx.shell("scp", "build/output/" + img + ".img", "root@" + target + ":/tmp/update.img")
    ctx.shell("ssh", "root@" + target, "rauc", "install", "/tmp/update.img")
    if ctx.args.reboot == "true":
        ctx.shell("ssh", "root@" + target, "reboot")
```

Usage:

```sh
yoe deploy 192.168.1.100 --image production-image --reboot
```

Custom commands show up alongside built-in commands. If `yoe` doesn't recognize
a command, it checks `commands/*.star` before printing "unknown command".

**The context object** provides:

| Method                | Description                              |
| --------------------- | ---------------------------------------- |
| `ctx.args.<name>`     | Parsed command-line arguments            |
| `ctx.shell(cmd, ...)` | Execute a shell command (returns output) |
| `ctx.log(msg, ...)`   | Print a message                          |
| `ctx.project_root`    | Path to the project root                 |

**Commands from modules:**

Vendor BSP modules can ship custom commands (e.g., `flash-emmc`, `enter-dfu`)
that become available when the module is added to the project.

**Key difference from unit evaluation:** Unit `.star` files are sandboxed — no
I/O, deterministic. Command `.star` files have full I/O access via `ctx.shell()`
because they are actions, not build definitions.

### `yoe dev`

Work with unit source code directly. Every unit's build directory is a git repo
— upstream source is committed with an `upstream` tag, and existing patches are
applied as commits on top. Local edits are just git commits.

There is no "dev mode" to enter or exit. If the build directory has commits
beyond `upstream`, `yoe build` uses them directly instead of re-fetching source.

```sh
# After building, edit source in place
yoe build openssh
cd build/openssh/src
vim auth.c
git commit -am "fix auth timeout handling"

# Rebuild uses your local commits
yoe build openssh

# See what you've changed
yoe dev diff openssh

# Extract commits as patch files
yoe dev extract openssh
# Writes patches/openssh/0001-fix-auth-timeout-handling.patch
# Prints updated patches list for your unit

# Check which units have local modifications
yoe dev status
```

**Subcommands:**

| Subcommand               | Description                                                                                   |
| ------------------------ | --------------------------------------------------------------------------------------------- |
| `yoe dev extract <unit>` | Run `git format-patch upstream..HEAD`, write to `patches/<unit>/`, print updated patches list |
| `yoe dev diff <unit>`    | Show `git log upstream..HEAD` — your local commits                                            |
| `yoe dev status`         | List all units with commits beyond upstream                                                   |

**Rebasing on upstream updates:**

```sh
# Update unit version
$EDITOR units/openssh.star   # bump version to 9.7p1

# Rebuild fetches new source, applies patches via rebase
yoe build openssh

# If patches conflict, resolve in the git repo
cd build/openssh/src
git rebase --continue
yoe dev extract openssh         # re-extract clean patches
```

**Why this is simpler than Yocto's devtool:**

- No separate workspace — the build directory is the workspace
- No mode to enter/exit — local commits are automatically detected
- No state files — git is the only state
- Extracting patches is `git format-patch` — a command developers already know
- Each patch = one git commit, so the patch series is the git log

### `yoe shell` (planned)

> **Status:** Not implemented. The command below describes the intended
> interactive entry point into a unit's build sandbox — the piece that makes the
> no-SDK model (see [Development Environments](dev-env.md)) complete.

Opens an interactive shell inside the build sandbox for a unit. The shell
attaches to the same container, environment variables, and mounted sysroot that
`yoe build` uses — but with a TTY and no automatic build steps.

```sh
# Shell into the sandbox for a unit (uses the unit's container + default machine)
yoe shell myapp

# For a specific machine (cross-arch via QEMU)
yoe shell myapp --machine raspberrypi4

# Shell without targeting a unit — uses the machine's default toolchain container
yoe shell --machine beaglebone-black
```

Inside the shell, `$SRCDIR`, `$DESTDIR`, `$PREFIX`, `$ARCH`, and `$NPROC` are
set exactly as `yoe build` would set them, and the unit's resolved `-dev`
dependencies are already installed into the sandbox via `apk`. Exiting the shell
tears down the sandbox — it is not persistent, so probing with `apk add <pkg>`
for exploration does not pollute subsequent builds.

This replaces the traditional SDK shell (Yocto's `environment-setup-*`). See
[Development Environments](dev-env.md#yoe-shell) for the full model.

### `yoe bundle` (planned)

> **Status:** Not implemented. The `yoe bundle` subcommand below is the
> air-gapped distribution story described in
> [Development Environments](dev-env.md#yoe-bundle-for-air-gapped-distribution).
> Today there is no export/import path, no bundle format, and no signing.

Exports and imports content-addressed bundles — the subset of the build cache,
source cache, module checkouts, and container images needed to reproduce a set
of targets without network access.

```sh
# Export a bundle for a specific image (includes all transitive deps)
yoe bundle export base-image --out bundle-base-v1.0.tar

# Export everything reachable from PROJECT.star
yoe bundle export --all --out bundle-full.tar

# Sign the bundle with the project's cache signing key
yoe bundle export base-image --sign keys/bundle.key --out bundle.tar

# Import on an air-gapped machine (verifies signatures if present)
yoe bundle import bundle-base-v1.0.tar --verify keys/bundle.pub

# Show the contents of a bundle without importing
yoe bundle inspect bundle.tar
```

A bundle contains built `.apk`s, source archives, module checkouts, and
toolchain container OCI archives — all keyed by content hash. After
`yoe bundle import`, subsequent `yoe build` runs resolve everything from the
local cache with no network access required.

### `yoe clean`

Removes build artifacts.

```sh
# Remove build intermediates (keep cached packages)
yoe clean

# Remove everything (build dirs, packages, sources)
yoe clean --all

# Remove only packages for a specific unit
yoe clean openssh
```

## Environment Variables

| Variable                | Default   | Description                                     |
| ----------------------- | --------- | ----------------------------------------------- |
| `YOE_PROJECT`           | `.` (cwd) | Path to the `[yoe]` project root                |
| `YOE_CACHE`             | `cache/`  | Cache directory for sources, builds, packages   |
| `YOE_JOBS`              | nproc     | Parallel build jobs                             |
| `YOE_LOG`               | `info`    | Log level (`debug`, `info`, `warn`, `error`)    |
| `YOE_CACHE_SIGNING_KEY` | (none)    | Path to private key for signing cached packages |
| `YOE_NO_REMOTE_CACHE`   | `false`   | Disable remote cache lookups                    |
| `AWS_ACCESS_KEY_ID`     | (none)    | S3 credentials for remote cache                 |
| `AWS_SECRET_ACCESS_KEY` | (none)    | S3 credentials for remote cache                 |
| `AWS_ENDPOINT_URL`      | (none)    | S3 endpoint override (for MinIO / non-AWS)      |

## Dependency Resolution

`yoe` resolves dependencies at two levels:

1. **Build-time** — unit `deps` entries form a DAG. `yoe build --with-deps`
   topologically sorts this graph and builds in order, parallelizing where the
   DAG allows.

2. **Install-time** — unit `runtime_deps` entries are written into the `.apk`'s
   `.PKGINFO`. When `apk add` runs during image assembly, it pulls in runtime
   dependencies automatically.

This means:

- Build dependencies are resolved by `yoe` (it knows the unit graph).
- Runtime dependencies are resolved by `apk` (it knows the package graph).
- The unit author declares both; the tools handle the rest.

### Config Propagation (planned)

> **Status:** Not implemented. There is no `public_config` field on units, no
> machine-to-unit CFLAGS/optimization propagation, and no resolved-config view
> in `yoe desc`. Units today receive architecture via the build environment and
> nothing else is automatically propagated through the DAG. The section below
> describes the planned GN-inspired design.

Inspired by GN's `public_configs`, machine-level configuration automatically
propagates through the dependency graph. When you build for a specific machine,
settings like architecture flags, optimization level, and kernel headers path
flow to every unit without each unit declaring them:

```
machine (beaglebone-black)
  → arch = "arm64"
  → CFLAGS = "-O2 -march=armv8-a"
  → KERNEL_HEADERS = "/usr/src/linux-6.6/include"
      ↓ propagates to
  unit (zlib)        → builds with arm64 flags
  unit (openssl)     → builds with arm64 flags
  unit (openssh)     → builds with arm64 flags + sees kernel headers
```

Units can also declare `public_config` settings that propagate to their
dependents. For example, a `zlib` unit might export its include path so that
`openssh` (which depends on `zlib`) automatically gets `-I/usr/include` without
the unit author specifying it.

This is resolved during the graph resolution phase (phase 1) so the full
resolved config for every unit is known before any build starts. Use
`yoe desc <unit> --config` to inspect the resolved configuration.

**Design note: unit-level, not task-level dependencies.** Unlike BitBake, which
models dependencies between individual tasks across units (e.g.,
`B:do_configure` depends on `A:do_install`), `yoe` treats each unit as an atomic
unit — unit A depends on unit B means B must be fully built before A starts.
This is a deliberate simplicity trade-off. BitBake's task-level graph enables
fine-grained parallelism (start fetching C while B is still compiling) and
per-task caching (sstate), but it is also the primary source of Yocto's
debugging complexity. Unit-level dependencies are easier to reason about, and
the parallelism loss is minor since independent units still build concurrently
across the DAG. Per-unit caching via content-addressed `.apk` hashes provides
sufficient granularity for fast incremental rebuilds.

## Caching Strategy

Builds are cached at multiple levels:

1. **Source cache** — downloaded tarballs and git clones in
   `$YOE_CACHE/sources/`. Keyed by URL + hash.
2. **Build cache** — content-addressed by hashing the unit, source, and all
   build dependency `.apk` hashes. If the combined hash matches, the build is
   skipped and the cached `.apk` is used.
3. **Package repository** — built `.apk` files in the local repo. Once
   published, packages are available for image assembly and on-device updates.
4. **Remote cache** _(planned — optional)_ — push/pull packages to an
   S3-compatible store so CI and team members share build results. Not yet
   implemented: there is no remote cache backend, no S3 integration, and no
   cache signing today. See the
   [Caching Architecture](build-environment.md#caching-architecture) section for
   the planned S3 configuration, cache signing, and the multi-level fallback
   chain.

Cache invalidation is hash-based, not timestamp-based. Changing a unit, updating
a source, or rebuilding a dependency all produce a new hash and trigger a
rebuild. Use `yoe build --dry-run` to see what would be rebuilt and why, or
`yoe cache stats` to review hit/miss rates from the last build.

## Example Workflow

```sh
# Start a new project
yoe init my-product --machine beaglebone-black

# Add a unit for your application
$EDITOR units/myapp.star

# Build everything (packages and images)
yoe build --all

# Flash to an SD card
yoe flash base-image /dev/sdX

# Later, update just your app and rebuild the image
$EDITOR units/myapp.star  # bump version
yoe build myapp
yoe build base-image         # only myapp's .apk changed, fast rebuild

# Or update the device directly
scp repo/myapp-1.3.0-r0.apk device:/tmp/
ssh device apk add /tmp/myapp-1.3.0-r0.apk
```
