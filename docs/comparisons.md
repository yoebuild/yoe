# Comparisons

How `[yoe]` relates to existing embedded Linux build systems and distributions.
For each, we identify what `[yoe]` adopts, what it leaves behind, and where it
differs.

## vs. Yocto / OpenEmbedded

Yocto is the industry standard for custom embedded Linux. It is extremely
capable but carries significant complexity.

**What `[yoe]` adopts from Yocto:**

- **Machine abstraction** — a declarative way to define board-specific
  configuration (kernel defconfig, device tree, bootloader, partition layout).
- **Image units** — composable definitions of what goes into a root filesystem
  image and how it's laid out on disk.
- **Module architecture** — the ability to overlay vendor BSP customizations on
  top of a common base without forking.
- **OTA integration** — first-class support for update frameworks (RAUC,
  SWUpdate).

**What `[yoe]` leaves behind:**

- BitBake and the task-level dependency graph.
- The unit/bbappend/bbclass metadata system.
- sstate-cache complexity — Yocto's sstate is per-task and requires careful
  configuration of mirrors, hash equivalence servers, and signing. `[yoe]`'s
  cache is per-unit, stored in S3-compatible object storage, and needs only a
  bucket URL.
- Cross-compilation toolchains.
- Python as the tooling language.

**No conditional override syntax.** Yocto's
[override system](https://docs.yoctoproject.org/bitbake/bitbake-user-manual/bitbake-user-manual-metadata.html#conditional-syntax-overrides)
(`DEPENDS:append:raspberrypi4`, `SRC_URI:remove:aarch64`, etc.) exists because
BitBake's metadata model is variable-based — you set global variables and then
layer conditional string operations on top. The result is powerful but
notoriously hard to debug (you need `bitbake -e` to see what a variable actually
resolved to).

`[yoe]`'s model is function-based, which covers the same use cases more
explicitly:

| Yocto override                     | `[yoe]` equivalent                                            |
| ---------------------------------- | ------------------------------------------------------------- |
| `DEPENDS:append:raspberrypi4`      | `if ctx.machine == "raspberrypi4": extra_deps = [...]`        |
| `SRC_URI:append:aarch64`           | `if ctx.arch == "aarch64": ...` in the unit                   |
| `PACKAGECONFIG:remove:musl`        | Module scoping — musl project doesn't include that module     |
| `FILESEXTRAPATHS:prepend` + append | `load()` the upstream function, call with different args      |

Starlark has `if` with fields on the predeclared `ctx` struct (`ctx.machine`,
`ctx.arch`), and the function composition pattern handles the "extend from
downstream" case. When
machine-specific behavior is needed, it's right there in the `.star` file — no
hidden layering of string operations.

**Key differences:**

|                     | Yocto                                        | `[yoe]`                                       |
| ------------------- | -------------------------------------------- | --------------------------------------------- |
| Build system        | BitBake (Python)                             | `yoe` (Go)                                    |
| Package format      | rpm / deb / ipk                              | apk                                           |
| Config format       | BitBake units (.bb/.bbappend)                | Starlark (Python-like)                        |
| Cross-compilation   | Required, central design assumption          | None — native builds only                     |
| Dependency model    | Task-level DAG (do_fetch → do_compile → ...) | Unit-level DAG (simpler, atomic per-unit)     |
| Language ecosystems | Wrapped in units                             | Native toolchains (go modules, cargo, etc.)   |
| Learning curve      | Steep — weeks to become productive           | Shallow — Starlark (Python-like)              |
| Build caching       | sstate (per-task, hash-based, complex setup) | Per-unit `.apk` hashes in S3-compatible cache |
| Multi-image support | Yes — multiple images from one project       | Yes — image inheritance + machine matrix      |
| On-device updates   | Possible but complex (smart image)           | Built-in via apk repositories                 |

**When to use Yocto instead:** when you need extremely fine-grained control over
every component, must support exotic architectures with no native build
infrastructure, or are in an organization that already has deep Yocto expertise
and tooling invested.

## vs. Buildroot

Buildroot is the simplest of the established embedded Linux build systems. It
shares `[yoe]`'s preference for simplicity.

**What `[yoe]` adopts from Buildroot:**

- The principle that simpler is better.
- Minimal base system approach.

**What `[yoe]` leaves behind:**

- Kconfig as the configuration interface.
- Make as the build engine.
- The assumption that cross-compilation is required.
- Full-rebuild-on-config-change behavior.

**Key differences:**

|                    | Buildroot                                     | `[yoe]`                                             |
| ------------------ | --------------------------------------------- | --------------------------------------------------- |
| Configuration      | Kconfig (menuconfig)                          | Starlark files                                      |
| Build engine       | Make                                          | `yoe` (Go)                                          |
| Cross-compilation  | Required                                      | None — native builds only                           |
| On-device packages | None — monolithic image only                  | apk — incremental updates                           |
| Incremental builds | Limited — config change triggers full rebuild | Content-addressed cache, only rebuild what changed  |
| Modern languages   | Wraps Go/Rust/etc. in Make, often poorly      | Delegates to native toolchains                      |
| Build caching      | ccache at best, no output caching             | Content-addressed `.apk` cache, shareable across CI |
| CI/team sharing    | Everyone rebuilds from scratch                | Push/pull from shared package repo                  |
| Composable images  | No — single image output                      | Yes — assemble different images from same packages  |

**The biggest structural difference** is the unit/package split. Buildroot has
no concept of installable packages — it builds everything into a monolithic
rootfs. This means:

- You can't update a single component on a deployed device without reflashing.
- You can't share build outputs between developers or CI runs.
- You can't compose different images from the same set of built packages.

**Caching gap:** Buildroot has no output caching at all — every developer and
every CI run rebuilds from source. `ccache` can help with C/C++ compilation but
doesn't help with configure steps, language-native builds, or package assembly.
`[yoe]`'s S3-backed cache means a typical developer build pulls pre-built
packages for everything except the component they're actively changing.

**Multi-image gap:** Buildroot produces a single image per configuration. To
build a "dev" variant and a "production" variant, you need separate build
directories with separate configs. With `[yoe]`, both images share the same
package repository — only the package lists differ.

**When to use Buildroot instead:** when you want the absolute simplest build
system for a truly minimal, single-purpose, static embedded system (firmware for
a sensor, a network appliance with no field updates). If the device never needs
a partial update and the image is small enough to rebuild in minutes,
Buildroot's simplicity is hard to beat.

## vs. Alpine Linux

Alpine is the closest existing distribution to what `[yoe]`'s target runtime
looks like.

**What `[yoe]` adopts from Alpine:**

- **apk as the package manager** — adopted directly. Fast, simple, proven.
- **busybox as coreutils** — minimal userspace in a single binary.
- **Minimal base image size** — target single-digit MB base images before
  application payload.
- **Security-conscious defaults** — no unnecessary services, no open ports, no
  setuid binaries unless explicitly required.
- **Fast package operations** — install/remove measured in milliseconds.
- **Minimal install scripts** — Alpine packages do little or nothing in
  postinst. Most ship with no install scripts at all; those that need them
  typically run a handful of lines (`addgroup`, `adduser`, maybe an
  `rc-update`). apk supports the full lifecycle (`.pre-install`,
  `.post-install`, `.pre-upgrade`, `.post-upgrade`, `.pre-deinstall`,
  `.post-deinstall`, plus triggers), but the culture is to keep them empty. This
  is a sharp contrast with Debian's `.deb` maintainer-script tradition —
  preinst/postinst/prerm/postrm with debconf prompts, alternatives,
  `dpkg-divert`, and complex migrations — which is exactly what made EmDebian's
  busybox replacement effort unsustainable (see Debian section below).

**Alpine APKBUILDs are the reference implementation for `[yoe]` units.** When
writing a new unit, the corresponding Alpine `APKBUILD` is the first place to
look. Alpine has already solved configure flags, build-time dependencies,
patches, and — most importantly — the install-script question (usually: nothing
to do). Following Alpine keeps `[yoe]` out of the Debian-style postinst trap,
where package install becomes imperative system mutation that's hard to
reproduce, hard to sandbox, and hard to roll back. If Alpine doesn't need a
postinst for it, `[yoe]` shouldn't either.

**What `[yoe]` leaves behind:**

- **musl** — planning to use glibc instead for maximum compatibility with
  language runtimes and pre-built binaries (`[yoe]` currently still inherits
  musl from Alpine's toolchain; the move is pending).
- **Limited BSP/hardware story** — Alpine doesn't target custom embedded boards.

**On the init system:** Alpine uses OpenRC. `[yoe]` currently uses busybox init,
the same as Alpine's minirootfs default. **systemd may become an option in the
future** — it's the pragmatic choice for developer-facing systems with rich
service management, journal logging, and udev — but the project has not
committed to shipping it as part of the base. Today, service management is
whatever busybox init + plain scripts give you.

**Key differences:**

|                   | Alpine                            | `[yoe]`                                              |
| ----------------- | --------------------------------- | ---------------------------------------------------- |
| C library         | musl                              | musl today; glibc planned                            |
| Init system       | OpenRC                            | busybox init today; systemd a future option          |
| Target            | Containers, small servers         | Custom embedded hardware                             |
| BSP support       | Generic x86/ARM images            | Per-board machine definitions                        |
| Image assembly    | `alpine-make-rootfs`              | `yoe build <image>` with machine + partition support |
| Build system      | `abuild` + APKBUILD shell scripts | `yoe build` + Starlark units                         |
| Kernel management | Generic kernels                   | Per-machine kernel config, device trees              |
| OTA updates       | Standard apk upgrade              | apk + full image update + rollback                   |

**When to use Alpine instead:** when you're targeting containers or generic
server hardware and don't need custom BSP, kernel configuration, or image
assembly tooling. Alpine is an excellent base for Docker containers and small
VMs.

## vs. Arch Linux

Arch is a philosophy as much as a distribution. Its commitment to simplicity and
transparency directly influences `[yoe]`'s design.

**What `[yoe]` adopts from Arch:**

- **Rolling release model** — no big-bang version upgrades; packages update
  continuously against a single branch.
- **Minimal base, user-assembled** — ship the smallest useful system and let the
  integrator compose what they need.
- **PKGBUILD-style simplicity** — build definitions should be concise, readable
  shell-like scripts, not complex metadata. `[yoe]`'s Starlark units aim for
  similar auditability — simple units read like declarative config.
- **Documentation culture** — invest in clear, practical docs rather than tribal
  knowledge.

**What `[yoe]` leaves behind:**

- x86-centric assumptions.
- pacman (using apk instead).
- The expectation of interactive manual system administration.
- Lack of reproducibility guarantees.

**Key differences:**

|                   | Arch                      | `[yoe]`                         |
| ----------------- | ------------------------- | ------------------------------- |
| Target            | Desktop/server, x86-first | Embedded, multi-arch            |
| Package manager   | pacman                    | apk                             |
| Package format    | tar.zst + .PKGINFO        | apk (tar.gz + .PKGINFO)         |
| Build definitions | PKGBUILD (bash)           | Starlark units                  |
| Reproducibility   | Not a goal                | Content-addressed builds        |
| Image assembly    | Manual (pacstrap)         | Automated (`yoe build <image>`) |
| Administration    | Interactive (hands-on)    | Declarative (config-driven)     |

**When to use Arch instead:** when you're building a desktop or server system
for personal use and value having full manual control. Arch's philosophy works
well for power users on general-purpose hardware.

## vs. Debian

Debian is the oldest and most conservative general-purpose Linux distribution.
Many embedded projects start on Debian (or a derivative like Raspberry Pi OS)
before hitting its limits on custom hardware.

**What `[yoe]` adopts from Debian:**

- **Signed binary package repositories** — apt's approach to package
  authenticity and repository signing is the model. `[yoe]`'s apk repositories
  follow the same principle.
- **Policy-driven package conventions** — Debian Policy defines where files go,
  how services are declared, and how packages relate. `[yoe]` inherits this
  culture through Alpine's `abuild` conventions.
- **Package metadata as data** — control files (or APKBUILDs) are declarative,
  not imperative install scripts.
- **Multi-arch awareness** — Debian has long taken non-x86 architectures
  seriously. `[yoe]` does too, by design.

**What `[yoe]` leaves behind:**

- **dpkg/apt** in favor of apk — smaller, faster, designed for minimal systems.
- **The stable/testing/unstable release model** — `[yoe]` is rolling by default;
  deployed devices pin to a known-good snapshot of the repo.
- **The maintainer-centric model** — one maintainer per package, committee-
  driven policy. `[yoe]` units are part of the project; whoever changes the
  build changes the unit.
- **debconf and interactive post-install configuration** — images are assembled
  from declarative Starlark, not from prompts during package install.
- **Desktop/server default set** — Debian's standard install assumes a huge set
  of tools are present. `[yoe]` starts near zero and adds only what's declared.
- **In-place `dist-upgrade`** — `[yoe]` prefers atomic image updates with
  rollback over mutating a running root filesystem.

**Key differences:**

|                   | Debian                            | `[yoe]`                         |
| ----------------- | --------------------------------- | ------------------------------- |
| Target            | General-purpose server/desktop    | Embedded, custom hardware       |
| Package manager   | apt / dpkg                        | apk                             |
| Package format    | .deb (ar + tar)                   | apk (tar.gz + .PKGINFO)         |
| Release model     | Stable/testing/unstable + LTS     | Rolling, pinned snapshots       |
| Build definitions | `debian/` dir (rules + control)   | Starlark units                  |
| Image assembly    | debootstrap / live-build          | `yoe build <image>`             |
| BSP support       | Generic kernels; no board tooling | Per-board machine definitions   |
| Kernel management | Distro-provided kernel packages   | Per-machine kernel config + DTs |
| OTA updates       | `apt upgrade` (in-place)          | apk + atomic image + rollback   |
| Footprint         | Standard install ~1 GB+           | Target single-digit MB base     |

**Debian derivatives (Raspberry Pi OS, Ubuntu, etc.)** inherit most of these
properties. Teams often start on Raspberry Pi OS and hit three walls: (1) it's
not built from source under their control, (2) it's difficult to trim below a
couple hundred MB, and (3) there's no clean story for deploying the same
software to a custom board.

### Minimum footprint

The smallest documented Debian install path is
[`debootstrap --variant=minbase`](https://wiki.debian.org/Debootstrap), which
installs only Essential and Priority: required packages (base-files,
base-passwd, bash, dash, dpkg, apt, libc, perl-base, and a handful of others) —
no systemd, no standard utilities beyond the essential set. In practice minbase
produces a root filesystem in the ~150–250 MB range depending on release and
architecture. A default debootstrap (which also pulls Priority: important,
including systemd) lands closer to 300–500 MB, and a "standard" Debian install
is well over 1 GB.

Even minbase is one-to-two orders of magnitude larger than a minimal Alpine or
`[yoe]` base, which can reach single-digit MB before application payload. The
floor is set by the GNU userland itself: glibc + coreutils + perl-base + bash +
dpkg + apt are ~60–80 MB combined before anything application-specific is
installed. Dropping perl-base or coreutils breaks dpkg maintainer scripts (see
Emdebian, below), so this floor is structural, not a tuning problem.

### Embedded Debian efforts

**[EmDebian (2007–2014)](https://wiki.debian.org/Embedded_Debian)** was the most
serious attempt at a minimal, embedded-focused Debian. It shipped two variants:

- **Emdebian Grip** — a binary-compatible subset of Debian with a smaller
  curated package set, still using GNU coreutils and glibc. "Debian, but
  smaller."
- **Emdebian Crush** — a more aggressive variant that
  [replaced GNU coreutils with busybox](https://wiki.debian.org/EmdebianCrush),
  dropped optional dependencies (LDAP from curl, etc.), and cross-built
  packages. Closer in spirit to what `[yoe]` does with Alpine-style apks.

The project posted an
[end-of-life notice on 13 July 2014](https://en.wikipedia.org/wiki/Emdebian_Grip),
with Emdebian Grip 3.1 (tracking Debian 7 "wheezy") as the last stable release.
The cited reasons were (1) embedded hardware had moved to expandable storage
where full Debian's size was no longer painful, and (2) the maintenance burden
of tracking Debian upstream while patching maintainer scripts for a busybox
userland was unsustainable. Crush specifically documented recurring problems
replacing coreutils components with busybox because of `.deb` postinst scripts —
the exact ecosystem-level incompatibility that any "Debian + busybox" attempt
runs into. Someone has already taken that path to its natural conclusion.

**[debos](https://github.com/go-debos/debos)** is the modern Debian image
builder, created by Sjoerd Simons at Collabora (introduced in 2018, Go
codebase). It is the closest structural analogue to `[yoe]`'s image assembly in
the Debian ecosystem:

- Written in Go, like `yoe`.
- YAML recipes describe a sequence of actions (debootstrap, apt install,
  partition, mkfs, bootloader install, overlay files, export as
  tarball/OSTree/disk image).
- Runs actions without root via a `fakemachine` VM helper — similar intent to
  `[yoe]`'s "container as build worker" model.
- Targets ARM embedded boards as a first-class use case.

`[yoe]` and debos cover overlapping ground. Key differences: debos starts from
existing Debian `.deb`s (inheriting the size and package-model properties
above), while `[yoe]` builds from source into content-addressed apks; debos
recipes are flat action sequences, while `[yoe]`'s Starlark units form a
dependency graph with a shared, content-addressed build cache.

**[aptly](https://www.aptly.info/)** is the canonical tool for running a
private, pinned Debian/Ubuntu repository. For teams that do ship Debian-based
devices, aptly plays the role that `[yoe]`'s S3 package cache plays:

- Mirror remote Debian/Ubuntu repos, partial or full, filtered by
  component/architecture.
- Take immutable, dated snapshots of a mirror or local repo — fixing package
  versions at a point in time.
- Publish snapshots as apt-consumable repositories with signed metadata.
- CLI plus REST API for CI integration.

The snapshot model is what gives a Debian-based deployment the reproducibility
`[yoe]` gets from content-addressed apks — different mechanism, same goal.

**[Gaia Build System](https://github.com/gaiaBuildSystem)** is the most active
modern example of a full build system (not just an image builder) layered on
Debian. It ships three reference distributions:

- **DeimOS** — a base Debian-derived reference distro.
- **PhobOS** — a [Torizon](https://www.torizon.io/)-compatible Debian derivative
  that boots via OSTree, uses Aktualizr for OTA updates, bundles a Docker
  runtime, and keeps native `apt-get install` available on deployed devices.
- **PergamOS** — a library of Debian-based container images used as build and
  application bases.

Architecturally:

- **Cookbook model** — a Yocto-inspired multi-repo structure where each
  "cookbook" is a git repo and a `manifest.json` ties them together.
- **Container-based builds** — each build runs inside a Debian Docker container,
  matching `[yoe]`'s "container as build worker" approach.
- **Multi-language recipes** — the `gaia` core is TypeScript (running on Bun);
  cookbook logic is a mix of Xonsh (Python-flavored shell), plain shell, and
  JSON distro definitions. `[yoe]` consolidates to a single config language
  (Starlark) for units, machines, and images.
- **Targets** — Raspberry Pi, NXP i.MX (e.g., iMX95 Verdin EVK via Toradex), and
  QEMU x86-64/arm64.

Contrast with `[yoe]`:

- Gaia inherits Debian's size and package-model properties (huge archive, `.deb`
  maintainer scripts, ~150 MB+ floor); `[yoe]` is apk-based and targets
  single-digit MB bases.
- Gaia's deployment model is **OSTree + Aktualizr** (Torizon-compatible);
  `[yoe]` uses apk plus atomic image updates with rollback.
- Gaia's recipe surface is multi-language (TS + Xonsh + Shell + JSON); `[yoe]`
  is Starlark end-to-end.
- Both build inside containers, both target custom ARM hardware, both aim for
  reproducibility through pinned inputs.

**When to prefer Gaia:** when you specifically want a Debian userland with
`apt-get install` still functional on the device, and especially when targeting
Toradex/Torizon-adjacent hardware where OSTree-based deployment is already
established.

This doesn't mean Debian is absent from embedded — it absolutely is present —
but the pattern is "Debian/Ubuntu-on-an-x86-or-Jetson-box," not "Debian in a
consumer electronics device with a custom SoC." That second case is where Yocto
and `[yoe]` live.

**When to use Debian instead:** when you're targeting general-purpose hardware
where the standard package archive _is_ the product ("I need a server with
Postgres, Nginx, and our application"), when long-term security support from a
volunteer organization matters more than image size, or when your team already
runs Debian in production and wants consistency between infrastructure and edge
devices. For early prototyping on a Raspberry Pi before moving to custom
hardware, Raspberry Pi OS is often the right starting point.

## vs. Ubuntu Core

[Ubuntu Core](https://ubuntu.com/core) is Canonical's IoT- and embedded-focused
Ubuntu variant. Architecturally it's a sharp departure from classic
Debian/Ubuntu: every component on the device — kernel, board support, base OS,
applications — is delivered as a [snap](https://snapcraft.io/) package, mounted
read-only via squashfs-over-loopback, and updated transactionally with rollback.
Ubuntu Core 24 (the current LTS) carries a 12-year support commitment and
targets production IoT, edge, and appliance devices.

**What `[yoe]` adopts from Ubuntu Core:**

- **Immutable root filesystem** — the shipping OS is never mutated in place;
  changes flow through an update mechanism with rollback.
- **Gadget-snap-style board config** — Ubuntu Core's
  [gadget snap](https://documentation.ubuntu.com/core/how-to-guides/image-creation/build-a-gadget-snap/)
  bundles bootloader assets, partition layout, and device-specific defaults.
  `[yoe]`'s machine definitions cover the same ground (kernel config, device
  tree, partition schema, bootloader choice).
- **Model assertion as device identity** — UC's signed model assertion declares
  exactly which snaps constitute a device. `[yoe]`'s image + machine Starlark is
  the structural analogue (which packages + which hardware = which shipping
  image).
- **Atomic updates with rollback** — shared goal, different mechanism (snap
  revisions plus a recovery seed system vs. `[yoe]`'s apk + atomic image
  update).

**What `[yoe]` leaves behind:**

- **Snaps** — the squashfs-per-app loopback model. `[yoe]` uses apk, which
  installs into a shared FHS root.
- **snapd** — UC's always-running daemon mediating confinement, updates, and
  interfaces. Significant runtime footprint and attack surface.
- **Brand store requirement** — commercial UC deployments require a
  [Canonical-hosted dedicated snap store](https://documentation.ubuntu.com/core/explanation/stores/dedicated-snap-store/)
  to control what runs on devices. This is a commercial gate. `[yoe]` ships its
  own signed apk repository with no vendor lock-in.
- **Default-strict AppArmor confinement** — UC apps run in a sandbox with
  explicit interfaces. Valuable for general-purpose appliances, often
  heavyweight for single-purpose embedded where the whole image is already
  curated.
- **Canonical-centric tooling** — ubuntu-image, snapcraft, Launchpad, Landscape.
  `[yoe]` is self-hostable end to end.

### Size: Ubuntu Core's snap model has a floor

The snap delivery model has a real footprint cost. From Canonical's own
[partition-sizing guidance](https://documentation.ubuntu.com/core/how-to-guides/image-creation/calculate-partition-sizes/),
a minimum Ubuntu Core 24 installation with no additional application snaps lands
at approximately **2,493 MiB (~2.5 GiB)** of on-disk layout:

| Partition     | Minimum size | Purpose                                         |
| ------------- | ------------ | ----------------------------------------------- |
| `system-seed` | 457 MiB      | Recovery boot loader plus recovery system snaps |
| `system-save` | 32 MiB       | Device identity and recovery data               |
| `system-boot` | 160 MiB      | Kernel EFI image(s), boot loader state          |
| `system-data` | Variable     | Writable — snaps, retained revisions, user data |

The 2.5 GiB floor is driven by the snap refresh model: UC keeps
`refresh.retain + 1` old revisions of each snap plus a temporary copy during
updates — effectively **4× per-snap storage** with the default
`refresh.retain = 2`. Each "revision" is a full squashfs image, not a delta. The
kernel snap alone is around 52 MiB and is retained four times over.

For comparison:

| Target                   | Minimum image size |
| ------------------------ | ------------------ |
| Ubuntu Core 24 (no apps) | ~2,500 MiB         |
| Debian `minbase` rootfs  | ~150–250 MiB       |
| Alpine minimal rootfs    | ~5–10 MiB          |
| `[yoe]` base target      | Single-digit MiB   |

Ubuntu Core is in a different footprint class. For devices with tens of GiB of
storage this is irrelevant; for cost-sensitive embedded products with 128–512
MiB of flash it's disqualifying before any application code is added.

### Key differences

|                  | Ubuntu Core                           | `[yoe]`                                |
| ---------------- | ------------------------------------- | -------------------------------------- |
| Packaging format | Snaps (squashfs, loopback-mounted)    | apk (installed into shared rootfs)     |
| Root filesystem  | Composed read-only snap mounts        | Standard FHS, shipped read-only        |
| Package daemon   | snapd (always running)                | apk (run at build + update time only)  |
| Board config     | Gadget snap                           | Machine definition (Starlark)          |
| Image metadata   | Signed model assertion                | Image + machine Starlark               |
| Updates          | Snap revisions + recovery seed system | Atomic image update + rollback         |
| Confinement      | AppArmor interfaces (default strict)  | Standard Linux DAC; sandboxing per app |
| Distribution     | Canonical brand store (hosted)        | Self-hosted signed apk repository      |
| Size floor       | ~2.5 GiB                              | Single-digit MiB                       |
| Build tool       | `ubuntu-image`, `snapcraft`           | `yoe build <image>`                    |
| Recipe language  | YAML (snapcraft.yaml, model, gadget)  | Starlark                               |
| LTS              | 12 years (Canonical)                  | N/A — project is pre-1.0               |

**When to use Ubuntu Core instead:** when you want Canonical's 12-year LTS
commitment, when strict per-app confinement via snaps/AppArmor is a product
requirement, when your team already operates a Canonical stack (Landscape for
fleet management, brand store for distribution, Anbox Cloud, etc.), or when your
device has ample storage (tens of GiB+) and the 2.5 GiB floor is an acceptable
trade for the operational simplicity of signed transactional updates.

## vs. Avocado OS

[Avocado OS](https://www.avocadolinux.org/) is an embedded Linux distribution
[announced in April 2025](https://blog.peridio.com/announcing-avocado-os) by
[Peridio](https://www.peridio.com/), a US-based company with roots in the
Elixir/Nerves OTA ecosystem. It is not a new build system — it is a curated
**Yocto distro layer**
([`meta-avocado`](https://github.com/avocado-linux/meta-avocado)) plus a
**Rust-written CLI** (`avocado-cli`) layered on top of
`systemd-sysext`/`confext` semantics. The pitch is "production-grade Linux for
edge AI and physical AI" — heavy focus on NVIDIA Jetson Orin, NXP i.MX 8M Plus,
Rockchip, and Raspberry Pi. The project shipped with paying customers and is
backed by a commercial OTA SaaS
([Peridio Core](https://www.peridio.com/avocado-os)).

**What `[yoe]` adopts from Avocado OS:**

- **Ergonomic CLI on top of a build system** — Avocado wraps Yocto in a Rust CLI
  to hide BitBake's rough edges. `[yoe]` shares the diagnosis (the underlying
  tooling needs an ergonomic front door) but reaches a different conclusion:
  replace BitBake rather than wrap it.
- **Immutable rootfs + atomic updates as the deployment model** — Avocado uses
  btrfs + `systemd-sysext` overlays verified with `dm-verity`. `[yoe]` shares
  the immutability goal (already drawn from Ubuntu Core and NixOS), though the
  mechanism is still an open design decision (apk + atomic image, A/B, RAUC,
  etc.).
- **Binary extension feeds for the common case** — Avocado bets that most teams
  consume pre-built extensions rather than customizing the base. `[yoe]`'s
  S3-backed apk repository plays the same role: a CI build seeds the cache and
  most developers never compile from source.
- **Live development against the deployed image** — Avocado's NFS-mounted sysext
  lets a developer iterate on an extension without reflashing. `yoe dev` aims at
  the same pain point from a different angle (edit a unit's source git tree,
  rebuild the apk, push to the device).

**What `[yoe]` leaves behind:**

- **BitBake / Yocto** — Avocado is still BitBake-bound for actual building.
  Custom hardware support means writing Yocto layers on top. `[yoe]` replaces
  the whole engine; see the Yocto section above for why.
- **`systemd-sysext` as the runtime composition primitive** — sysext is powerful
  but ties the OS tightly to systemd, dm-verity, and a particular filesystem
  layout. `[yoe]` uses apk into a shared FHS rootfs; composition is at build
  time (image units), not runtime (overlay mounts).
- **glibc baseline** — Avocado inherits Yocto's glibc default. `[yoe]` is
  musl-first via Alpine.
- **Cross-compilation toolchains** — Avocado uses Yocto's standard cross
  toolchain. `[yoe]` is native-only.
- **Commercial OTA tie-in** — Avocado's business model is "free OS, paid Peridio
  Core for fleet management and OTA." `[yoe]` has no commercial gate; the
  repository, signing, and update tooling are part of the open project.
- **Multi-language tooling stack** — Avocado mixes BitBake, Shell, and Rust
  (`avocado-cli`, `avocadoctl`, `avocado-conn`). `[yoe]` is Go + Starlark end to
  end.

**Key differences:**

|                     | Avocado OS                                 | `[yoe]`                                      |
| ------------------- | ------------------------------------------ | -------------------------------------------- |
| Build engine        | Yocto / BitBake (Python)                   | `yoe` (Go)                                   |
| Recipe language     | BitBake (`.bb`/`.bbappend`)                | Starlark                                     |
| CLI language        | Rust (`avocado-cli`)                       | Go (`yoe`)                                   |
| Cross-compilation   | Yes (Yocto default)                        | None — native builds only                    |
| C library           | glibc                                      | musl                                         |
| Package format      | IPK/RPM internally; sysext DDI on device   | apk                                          |
| Runtime composition | `systemd-sysext` overlays + `dm-verity`    | apk into shared FHS rootfs                   |
| Init system         | systemd (required by sysext model)         | busybox init today; systemd a future option  |
| Filesystem          | btrfs root, immutable                      | ext4 today; immutability planned             |
| OTA mechanism       | Peridio Core (commercial SaaS)             | Self-hosted; mechanism TBD                   |
| Build caching       | Yocto sstate                               | Content-addressed apk in S3-compatible cache |
| Container model     | SDK containers for dev                     | Container as build worker                    |
| Hardware focus      | Edge AI: Jetson, i.MX, Rockchip, RPi       | Generic embedded; RPi/BBB/QEMU first         |
| Commercial backing  | Peridio (VC-backed)                        | None — open project                          |
| Status              | Production (April 2025+), paying customers | Pre-1.0                                      |

**Structural distance.** Avocado OS and `[yoe]` agree on the _symptoms_ —
unwrapped Yocto is too sharp, embedded teams need atomic updates with rollback,
most users want to consume binaries rather than rebuild — but disagree on the
_cure_. Avocado keeps Yocto and bets that systemd-sysext + btrfs + dm-verity is
the modern way to ship and update a device. `[yoe]` replaces Yocto and bets that
a smaller, single-language, apk-based stack with content-addressed caching is
enough, without taking on the systemd/btrfs/ dm-verity dependency.

**When to use Avocado OS instead:** when you're shipping edge-AI hardware today
on the platforms Peridio supports (especially NVIDIA Jetson Orin), want a
vendor-backed OTA SaaS rather than running your own update infrastructure, are
comfortable with the systemd + btrfs + dm-verity baseline, and prefer to ride
Yocto's BSP ecosystem rather than write machine definitions for new silicon. If
you need production deployment now and a paid support relationship is
acceptable, Avocado is several years ahead of `[yoe]` on maturity.

## vs. NixOS / Nix

Nix is the most intellectually ambitious of the systems `[yoe]` draws from. Its
ideas about reproducibility and declarative configuration are adopted wholesale;
its implementation complexity is not.

**What `[yoe]` adopts from Nix:**

- **Content-addressed build cache** — build outputs keyed by their inputs so
  identical builds produce cache hits regardless of when or where they run.
- **Declarative system configuration** — the entire system image is defined by
  configuration files; rebuilding from that config produces the same result.
- **Hermetic builds** — builds do not depend on ambient host state; inputs are
  explicit and pinned.
- **Atomic system updates and rollback** — deploy new system images atomically
  with the ability to boot into the previous version.

**What `[yoe]` leaves behind:**

- The Nix expression language.
- The `/nix/store` path model and its massive closure sizes.
- The steep learning curve.
- The assumption of abundant disk space and bandwidth.

**Key differences:**

|                 | NixOS                                | `[yoe]`                                    |
| --------------- | ------------------------------------ | ------------------------------------------ |
| Config language | Nix (custom functional language)     | Starlark (Python-like)                     |
| Store model     | Content-addressed `/nix/store` paths | Standard FHS with apk                      |
| Closure size    | Often 1GB+ for simple systems        | Target single-digit MB base                |
| Target          | Desktop, server, CI                  | Embedded hardware                          |
| BSP support     | Minimal                              | Per-board machine definitions              |
| Package manager | Nix                                  | apk                                        |
| Reproducibility | Bit-for-bit (aspirational)           | Content-addressed, functionally equivalent |
| Rollback        | Via Nix generations                  | Planned; mechanism TBD (apk, A/B, RAUC, …) |
| Learning curve  | Steep (must learn Nix language)      | Shallow (Starlark, Python-like)            |

**Caching comparison:** Nix's binary cache (Cachix, or self-hosted with
`nix-serve`) is conceptually similar to `[yoe]`'s remote cache — both store
content-addressed build outputs in S3-compatible storage. The key differences:
Nix caches _closures_ (a package plus all its transitive runtime dependencies),
which can be very large. `[yoe]` caches individual `.apk` packages, which are
smaller and more granular. Nix's content addressing is based on the full
derivation hash (all inputs); `[yoe]` uses a similar scheme but at unit
granularity rather than Nix's per-output granularity.

**When to use Nix instead:** when you need the strongest possible
reproducibility guarantees, are building for desktop/server/CI, and are willing
to invest in learning the Nix ecosystem. NixOS is unmatched for declarative
system management on general-purpose hardware.

## vs. Google GN

GN is not a Linux distribution — it's a meta-build system used by Chromium and
Fuchsia. But several of its architectural ideas directly influenced `[yoe]`'s
tooling design.

**What `[yoe]` adopts from GN:**

- **Two-phase resolve-then-build** — GN fully resolves and validates the
  dependency graph before generating any build files. `yoe build` does the same:
  resolve the entire unit DAG, check for errors, then build. No partial builds
  from graph errors discovered mid-way.
- **Config propagation** — GN's `public_configs` automatically apply compiler
  flags to anything that depends on a target. `[yoe]` propagates machine-level
  settings (arch flags, optimization, kernel headers) through the unit graph.
- **Build introspection** — GN provides `gn desc` (what does this target do?)
  and `gn refs` (what depends on this?). `[yoe]` provides `yoe desc`,
  `yoe refs`, and `yoe graph` for the same purpose.
- **Label-based references** — GN uses `//path/to:target` for unambiguous target
  identification. `[yoe]` uses a similar scheme for composable unit references
  across repositories.

**What `[yoe]` leaves behind:**

- Ninja file generation — `[yoe]`'s unit builds are coarse-grained enough that
  `yoe` orchestrates directly.
- GN's custom scripting language — Starlark serves the same purpose for `[yoe]`.
- C/C++ build model specifics — GN is deeply tied to source-file-level
  dependency tracking, which isn't relevant for unit-level builds.

**Key differences:**

|                        | GN                      | `[yoe]`                             |
| ---------------------- | ----------------------- | ----------------------------------- |
| Purpose                | C/C++ meta-build system | Embedded Linux distribution builder |
| Output                 | Ninja build files       | `.apk` packages and disk images     |
| Config language        | GN (custom)             | Starlark (Python-like)              |
| Dependency granularity | Source file / target    | Unit (package)                      |
| Build execution        | Ninja                   | `yoe` directly                      |
| Introspection          | `gn desc`, `gn refs`    | `yoe desc`, `yoe refs`, `yoe graph` |

GN is not an alternative to `[yoe]` — they solve different problems. But GN's
approach to graph resolution, config propagation, and introspection are
well-proven patterns that `[yoe]` applies to the embedded Linux domain.

## Value Proposition and Strategic Positioning

### The Core Thesis

Yocto's model of wrapping every dependency in a unit made sense when C/C++ was
the only game in town and there was no dependency management beyond "whatever
headers are on the system." Modern languages have solved this:

- **Go**: `go.sum` is a cryptographic lock file. Builds are already
  reproducible.
- **Rust**: `Cargo.lock` pins every transitive dependency.
- **Zig**: Hash-pinned dependencies.
- **Node/Python**: Lock files are standard practice.

Yocto's response is to re-declare every dependency the language toolchain
already knows about — `SRC_URI` with checksums for each crate,
`LIC_FILES_CHKSUM` for each module. This is busywork that duplicates what
`Cargo.lock` and `go.sum` already guarantee.

`[yoe]`'s position: **let the language package manager do its job.** A Go unit
should declare _what_ to build, not _how to resolve every transitive
dependency_. Content-addressed caching hashes the output — if inputs haven't
changed, the output is the same. You get reproducibility without micromanaging
the build.

### Where `[yoe]` Cannot Compete (Yet)

Be honest about the gaps:

**Vendor BSP support is Yocto's real moat.** Every major SoC vendor (NXP, TI,
Qualcomm, Intel, Renesas, MediaTek) ships Yocto BSP layers and supports them.
This is not a technology problem — it's an ecosystem problem that Linux
Foundation backing solves. No amount of technical superiority overcomes "the
silicon vendor gives us a Yocto BSP and supports it."

**Package count.** Yocto has ~5,000 recipes across oe-core + meta-openembedded,
Buildroot has ~2,800 packages, Alpine has ~36,000, Debian has ~35,000, and
Nixpkgs has ~142,000. `[yoe]` has dozens. Need curl, dbus, python3, or ffmpeg?
You have to write the unit.

**Configuration UX.** Buildroot's `make menuconfig` is a killer feature —
visual, discoverable, searchable. You can explore what's available without
reading unit files. `[yoe]` requires editing Starlark by hand.

**Documentation and community.** Yocto has comprehensive manuals, Bootlin
training materials, and years of mailing list archives. Buildroot has a
well-maintained manual and active list. Problems are googleable. `[yoe]` has
design docs and a small team.

**Legal compliance tooling.** Yocto's `do_populate_lic` and Buildroot's
`make legal-info` generate license manifests and source archives. This is
required for shipping products in many industries. `[yoe]` has nothing here yet.

**Proven production track record.** Thousands of products ship with Yocto.
Buildroot runs on millions of devices. `[yoe]` is a prototype.

### Where `[yoe]` Can Win

**Target audience:** Teams building Go/Rust/Zig services for embedded Linux —
edge computing, IoT gateways, network appliances. Teams where the application
_is_ the product, not the base OS. Teams that want "Alpine + my app on custom
hardware" not "custom Linux distro with 200 hand-tuned units."

These teams currently use Buildroot, hack together Docker-based builds, or
cross-compile manually. They would never adopt Yocto because the overhead is
absurd for their use case.

**First-class modern language support.** Go/Rust/Zig unit classes should be
trivial to use. The build system should get out of the way and let `go build`,
`cargo build`, and `zig build` do their jobs. This is where Yocto is most out of
touch.

**Custom hardware without desktop distro limitations.** Desktop distros (Debian,
Fedora, Alpine) have great package management but no story for custom kernels,
device trees, bootloaders, board-specific firmware, or flash/deploy workflows.
This is the entire reason Yocto and Buildroot exist. `[yoe]` should provide BSP
tooling (machine definitions, kernel units, `yoe flash`, `yoe run`) that is
simpler than Yocto's but more capable than anything desktop distros offer.

**Incremental builds and shared caching.** Buildroot rebuilds everything from
scratch. Yocto's sstate is powerful but complex to set up. `[yoe]`'s
content-addressed `.apk` cache in S3-compatible storage is conceptually simpler:
push packages to a bucket, pull them on other machines. CI builds once,
developers reuse the output.

**AI-assisted unit generation.** If an AI can generate a working Starlark unit
from a project URL faster than porting a Yocto unit, the small package count
stops mattering. Starlark is far more tractable for AI than BitBake's metadata
format.

### The Alpine Linux Precedent

Alpine didn't supplant Debian — it became the default for containers because it
was radically smaller and simpler for that specific use case. `[yoe]` doesn't
need to replace Yocto for automotive or aerospace. It needs to be the obvious
choice for a specific class of embedded product where Yocto is overkill and
Buildroot is too limited.

### What to Focus On

1. **Modern language unit classes** — Go, Rust, Zig should be first-class, not
   afterthoughts. These are the differentiator. A Go developer should go from "I
   have a binary" to "I have a bootable image on custom hardware" in minutes.

2. **BSP tooling** — machine definitions, kernel/bootloader units, `yoe flash`,
   `yoe run`. This is what desktop distros lack and what justifies `[yoe]`'s
   existence as a build system rather than just another distro.

3. **Shared build cache** — the S3-backed package cache is a major advantage
   over Buildroot. Make it trivial to set up so teams see the value immediately.

4. **Size discipline.** The summary matrix shows `[yoe]`'s single-digit-MB base
   as a structural advantage against Ubuntu Core (~2,500 MB), NixOS (~1,500 MB),
   and Debian (~150 MB minbase). That floor bloats silently — one "convenient
   default," one "might as well include it" at a time. Every new feature, class,
   and base-system addition should survive an explicit size review. Losing the
   size story means losing the most defensible position on the matrix.

5. **Atomic update + rollback story.** Ubuntu Core's pitch is "signed
   transactional updates with rollback"; Gaia's is "OSTree + Aktualizr"; Yocto's
   is RAUC/SWUpdate. `[yoe]` needs an equivalent first-class, opinionated,
   documented update workflow — not a "you can wire this up yourself" footnote.
   The underlying mechanism is still an open design decision — candidates
   include apk upgrade with snapshot/rollback, A/B partition swap, RAUC-style
   bundle updates, and OSTree-style file trees. The commitment is to _some_
   well-integrated shippable story, not to any specific mechanism. For any team
   shipping a product, this is table stakes.

6. **AI unit generation + Alpine aports conversion.** Lean into the AI-native
   angle: generating a new unit from a project URL should be a conversation, not
   a manual porting exercise. _Also_ ship a mechanical APKBUILD → Starlark
   converter — Alpine has ~36,000 ready-to-port APKBUILDs, and a reliable
   converter closes the package-count gap faster and more predictably than pure
   AI generation. AI for novel cases, mechanical conversion for the long tail.

7. **Board support** — start with popular, accessible boards (Raspberry Pi,
   BeagleBone, common QEMU targets). Every board that works out of the box is a
   potential user who doesn't need Yocto.

8. **Don't chase Yocto's or Canonical's tails.** Resist adding Yocto-like
   features (task-level DAGs, unit splitting, bbappend equivalents) to win Yocto
   users, and equally resist Canonical-style add-ons (brand store, snap-style
   confinement, a Landscape clone) to win Ubuntu Core users. Both directions
   lead away from the minimal, single-language, AI-tractable design that is
   `[yoe]`'s actual positioning. Make the simple path so good that teams choose
   `[yoe]` because it fits their workflow, not because it mimics something they
   already have.

## Rootfs Ownership: How Each Project Handles It

A recurring problem when building an embedded image unprivileged: the installed
rootfs needs files owned by `root:root` (and sometimes by specific service
users), but the build itself ideally does not run as real root. `mkfs.ext4 -d`
copies ownership straight out of `stat()`, so whatever the filesystem says at
image-pack time is what the booted system sees. Every serious build tool has had
to solve this.

There are only three real options, and the industry has converged on them:

**1. Real root (`sudo`).** Traditional flow. `sudo debootstrap`, `apk add` on an
Alpine host, a container running as root — the simplest approach, but needs
privileges on the build host.

**2. fakeroot (LD_PRELOAD).** A small library that intercepts `chown`, `stat`,
and friends. `chown` updates an in-memory database instead of the kernel; later
`stat` calls return the faked ownership. Files on disk stay owned by the build
user, but `tar` / `mkfs.ext4` / `dpkg-deb` see the virtual ownership and pack
that into the archive or image. Invented by Debian; now standard.

**3. User namespaces (`unshare -U`).** Linux kernel feature. Inside the
namespace the build process sees itself as uid 0; subuid/subgid mapping
translates writes back to a range owned by the build user on the host. No
LD_PRELOAD tricks, no real root — but requires subuid configuration on the host
kernel.

### How specific projects apply these

**Alpine Linux** — two halves:

- **Package build** (`abuild`) wraps the whole build in `fakeroot` so the
  resulting `.apk` tar records `root:root` ownership regardless of who ran
  abuild.
- **Rootfs assembly** (`apk add`, `alpine-make-rootfs`) runs as real root on a
  live system or inside a build chroot.

**Debian / Ubuntu** — historically real root; modern tooling offers all three:

- **Package build** — `dpkg-buildpackage` runs under `fakeroot`
  (`fakeroot debian/rules binary`). This is universal — essentially every `.deb`
  on the planet has its ownership laundered through fakeroot.
- **Rootfs assembly** — the original `debootstrap` requires `sudo`. Its
  successor `mmdebstrap` explicitly exposes the full menu via `--mode=root`,
  `--mode=fakeroot`, `--mode=fakechroot`, `--mode=unshare` (user namespaces),
  `--mode=proot`, and `--mode=chrootless`. `--mode=unshare` is the recommended
  modern unprivileged default.

**Buildroot** — wraps image packaging in plain `fakeroot`. Works, but fakeroot's
in-memory database doesn't persist across process invocations, so Buildroot does
the whole image pack in one fakeroot session.

**Yocto / OpenEmbedded** — uses `pseudo` instead of `fakeroot`. `pseudo` is an
enhanced fakeroot that persists state to an on-disk SQLite database, so
ownership survives across the many separate steps a Yocto task graph spawns.
This is necessary for OE's execution model and is one of the reasons Yocto
builds have a heavier tooling footprint than Alpine/Buildroot.

**NixOS** — builds entirely under a sandboxing daemon (`nix-daemon`) running as
root; individual builders drop privileges. Image assembly for NixOS system
closures happens inside the daemon's controlled environment with proper root, so
the ownership problem doesn't surface the same way.

**Google GN / Bazel** — out of scope; neither builds Linux rootfs images as a
first-class concern.

### How `[yoe]` applies these

- **APK build** — `internal/artifact/apk.go` normalizes every tar header to
  `root:root` directly in Go's `archive/tar` writer. This is the structural
  equivalent of what Alpine's `abuild` gets from `fakeroot` and what Debian gets
  from `dpkg-buildpackage` under fakeroot — just implemented in the build tool
  rather than via LD_PRELOAD, because Go writes the tar anyway.
- **Rootfs assembly** (`modules/module-core/classes/image.star`) currently runs
  inside the Docker build container, which is already privileged. The image
  class `chown -R 0:0`s the assembled rootfs before `mkfs.ext4 -d`, and chowns
  `$DESTDIR` back to the host build user at the end so the next build's
  host-side cleanup works. This is roughly Alpine's "run as real root" path,
  adapted to our docker-with-host-ownership cache model.
- **Future direction** — the planned move of image assembly to the host via
  `bwrap --unshare-user --uid 0 --gid 0`
  (`docs/superpowers/plans/host-image-building-bwrap.md`) is the user-namespace
  approach: the same category as `mmdebstrap --mode=unshare`. When it lands, the
  `chown` dance disappears — bwrap's namespace provides pseudo-root with
  host-owned files for free.

The short version: we match Alpine's tar-ownership convention for packages,
we're currently doing the "real root in a container" move for rootfs assembly,
and we have a documented path to the `mmdebstrap --mode=unshare` equivalent for
the host.

## Summary Matrix

| Feature                 | Yocto    | Buildroot | Alpine   | Arch     | Debian   | UC        | NixOS     | **`[yoe]`** |
| ----------------------- | -------- | --------- | -------- | -------- | -------- | --------- | --------- | ----------- |
| Embedded focus          | Yes      | Yes       | Partial  | No       | No       | Yes       | No        | **Yes**     |
| Simple config           | No       | Moderate  | Moderate | Yes      | Moderate | No        | No        | **Yes**     |
| Native builds           | No       | No        | Yes      | Yes      | Yes      | Yes       | Yes       | **Yes**     |
| On-device packages      | Optional | No        | Yes      | Yes      | Yes      | Yes       | Yes       | **Yes**     |
| Content-addressed cache | Partial  | No        | No       | No       | No       | No        | Yes       | **Yes**     |
| Remote shared cache     | Complex  | No        | No       | No       | No       | No        | Yes       | **Yes**     |
| Pre-built package cache | No       | No        | Yes      | Yes      | Yes      | Yes       | Yes       | **Yes**     |
| Declarative images      | Yes      | Partial   | No       | No       | Partial  | Yes       | Yes       | **Yes**     |
| Multi-image support     | Yes      | No        | No       | No       | No       | Partial   | Yes       | **Yes**     |
| Image inheritance       | Partial  | No        | No       | No       | No       | No        | Yes       | **Yes**     |
| Custom BSP support      | Yes      | Yes       | No       | No       | Minimal  | Yes       | Minimal   | **Yes**     |
| Incremental updates     | Complex  | No        | Yes      | Yes      | Yes      | Yes       | Yes       | **Yes**     |
| Hermetic builds         | Partial  | No        | No       | No       | No       | Partial   | Yes       | **Yes**     |
| Fast package ops        | N/A      | N/A       | Yes      | Moderate | Moderate | Slow      | Slow      | **Yes**     |
| Min base image size     | ~15 MB   | ~5 MB     | ~5 MB    | ~500 MB  | ~150 MB  | ~2,500 MB | ~1,500 MB | **~5 MB**   |
| Packages available      | ~5,000   | ~2,800    | ~36,000  | ~15,000  | ~35,000  | ~10,000   | ~142,000  | **Dozens**  |

_UC = Ubuntu Core. "Min base image size" is the approximate on-disk footprint of
the smallest practical bootable/usable root filesystem (core-image-minimal for
Yocto, `minbase` debootstrap for Debian, minirootfs for Alpine, a minimal Ubuntu
Core 24 model with no app snaps, a minimal NixOS closure). Actual sizes vary
with architecture, kernel, and configuration. "Packages available" is the rough
count of ready-to-use packages/recipes in the standard/common repositories;
Yocto counts typical oe-core + meta-openembedded, Arch excludes the ~90,000 AUR
packages, UC counts snaps in the public store — a different delivery model that
is not directly comparable. Sources: project documentation,
[repology.org](https://repology.org/repositories/packages)._
