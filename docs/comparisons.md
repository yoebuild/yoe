# Comparisons

How `[yoe]` relates to existing embedded Linux build systems and distributions.
For each, we identify what `[yoe]` adopts, what it leaves behind, and where it
differs.

## In Short: What Makes `[yoe]` Different

The detailed sections below compare `[yoe]` against one system at a time, but
the same handful of choices recur. In general terms, `[yoe]` differs from
existing solutions along these axes:

- **One language, end to end.** Units, machines, and images are all Starlark; the
  engine is Go. There is no second metadata format, no BitBake/Kconfig/Make
  layer underneath, and nothing requiring you to learn a bespoke expression
  language (contrast: Yocto's BitBake, Nix's expression language, Buildroot's
  Kconfig, Gaia's TS+Xonsh+Shell mix). This is also what makes units tractable
  for AI to generate.
- **Native builds, no cross-compilation.** Foreign architectures are handled by
  running native toolchains inside foreign-arch containers under QEMU, never by
  a cross-compile toolchain. Yocto, Buildroot, and Avocado all center on cross
  toolchains; `[yoe]` deliberately does not.
- **The build cache _is_ the package feed.** A unit's content-addressed `.apk`
  lives in a plain S3-compatible bucket — the same bytes CI builds, the cache
  serves, and a device installs. There is no separate sstate, REAPI server, or
  artifact registry to stand up; the cache is a bucket URL. Caching is
  per-unit/per-package, not per-task (Yocto) or per-action (Bazel/Buck2).
- **apk into a shared FHS root.** Packages install into a normal filesystem,
  not snap/sysext/SquashFS loopback mounts (contrast: Ubuntu Core, Avocado,
  distri) and not a `/nix/store` closure model. This keeps the base in the
  single-digit-MB class and the runtime conventional.
- **Embedded and BSP are first-class.** Machine definitions, per-board kernel
  config and device trees, bootloader handling, and image/partition assembly are
  built in — the layer general-purpose distros (Alpine, Arch, Debian, NixOS) and
  meta-build systems (GN, Bazel, Buck2) simply do not have.
- **Resolve-then-build, at unit grain.** The whole unit DAG is resolved and
  validated before anything builds, so graph errors surface up front. This is
  the GN/Bazel discipline, applied at a coarse granularity where it costs almost
  nothing.
- **Pre-1.0, open, no commercial gate.** The repository, signing, and update
  tooling are part of the open project — no brand store, no paid OTA SaaS,
  self-hostable end to end.
- **Sized for small teams, not platform organizations.** Most systems above
  assume an enterprise shape: a dedicated build/platform team to operate sstate
  mirrors or a Remote Execution cluster, a vendor support contract for BSPs, or
  a commercial OTA service. `[yoe]` targets the opposite end — teams of one to a
  handful where the application is the product and nobody can be spared to
  babysit the build system. Every choice above trades enterprise-scale
  flexibility for an operational surface a small team can hold in their heads.

`[yoe]` is honest about where it does not yet compete: vendor BSP breadth,
from-source package coverage (dozens of source-built units vs. thousands of
Yocto recipes — though the prebuilt-distro module makes thousands of packages
directly consumable, Alpine today with other distros planned, so raw
availability is closer to a full distro's), configuration UX, legal-compliance
tooling, and a production track record. The
[Value Proposition](#value-proposition-and-strategic-positioning) section sets
out where `[yoe]` can win despite those gaps; the per-system sections below give
the specifics.

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

| Yocto override                     | `[yoe]` equivalent                                        |
| ---------------------------------- | --------------------------------------------------------- |
| `DEPENDS:append:raspberrypi4`      | `if ctx.machine == "raspberrypi4": extra_deps = [...]`    |
| `SRC_URI:append:aarch64`           | `if ctx.arch == "aarch64": ...` in the unit               |
| `PACKAGECONFIG:remove:musl`        | Module scoping — musl project doesn't include that module |
| `FILESEXTRAPATHS:prepend` + append | `load()` the upstream function, call with different args  |

Starlark has `if` with fields on the predeclared `ctx` struct (`ctx.machine`,
`ctx.arch`), and the function composition pattern handles the "extend from
downstream" case. When machine-specific behavior is needed, it's right there in
the `.star` file — no hidden layering of string operations.

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

**Alpine's prebuilt apks are also directly consumable.** Beyond using APKBUILDs
as a from-source reference, `[yoe]` can wrap Alpine's _published binary_ `.apk`s
as units via the `alpine_pkg` class: the upstream apk is fetched verbatim,
Alpine's signature is stripped and the control stream re-signed with the
project's key, and the package is exposed as an ordinary unit (pinned to one
Alpine release, ABI- and keyring-coupled to the build toolchain's Alpine base).
Thousands of Alpine main/community packages are usable this way with no porting;
a hand-written from-source unit is only needed when a package must be built
under your control or with non-Alpine options. This two-tier model — source
where it matters, prebuilt Alpine for the long tail — is what makes the
package-count gap discussed in the
[Value Proposition](#value-proposition-and-strategic-positioning) much narrower
than the source-built unit count implies. The `*_pkg` wrapper is deliberately
distro-agnostic; Alpine is the only prebuilt source today, with other distros
(see the Debian section) a planned extension of the same shape.

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
| Prebuilt packages | builds its own (`abuild`)         | reuses Alpine's via `alpine_pkg` + source units      |
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

**Consuming Debian/Ubuntu prebuilt packages (planned).** `[yoe]` already wraps
Alpine's published binary apks as units via the `alpine_pkg` class (see the
Alpine section). The wrapper pattern is intentionally distro-agnostic — fetch an
upstream package, re-sign its metadata with the project key, expose it as an
ordinary unit — so the same shape is the natural way to make Debian/Ubuntu
`.deb`s directly consumable: a `deb_pkg`-style class pulling from a pinned
Debian/Ubuntu suite. This is **not implemented today** — only Alpine is wired
up — but it is the expected path for teams that need a specific Debian/Ubuntu
binary (a vendor-provided `.deb`, a package absent from Alpine) without porting
it. The `.deb` maintainer-script tradition (preinst/postinst/debconf) makes
verbatim Debian consumption more invasive than Alpine's near-empty install
scripts, so this tier is most useful for leaf packages, not base-system pieces.

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

## vs. distri

[distri](https://distr1.org/) is Michael Stapelberg's research Linux
distribution, [announced in August 2019](https://michael.stapelberg.ch/posts/2019-08-17-introducing-distri/).
Stapelberg was a Debian Developer for roughly seven years (and is widely known
for the i3 window manager); distri is his vehicle for asking whether
architectural changes could make package management _drastically_ faster and
whether mainstream distro complexity is avoidable. It is explicitly a
proof-of-concept — the project describes itself as "the simplest Linux
distribution that is still useful" and states it is **not recommended for any
use except research**. It is included here not as an alternative but because its
results validate several instincts `[yoe]` shares.

**What `[yoe]` shares in spirit with distri:**

- **The diagnosis** — mainstream package managers are needlessly slow and
  complex, largely because of per-file extraction and serialized maintainer
  hooks/triggers. distri's headline result (package operations in
  milliseconds, parallel installs, no hooks) is the same conclusion that drives
  `[yoe]`'s adoption of Alpine's near-empty-install-script culture and fast apk
  operations.
- **Read-only OS, atomic updates** — distri's images are immutable and activated
  atomically with no per-file extraction. That is the same direction `[yoe]`
  draws from Ubuntu Core and NixOS: ship the OS read-only, update atomically.
- **Hermetic builds with explicit dependency views** — distri builds see only
  declared dependencies through a filtered package store; `[yoe]` builds inside
  a container worker with declared inputs and content-addressed outputs.

**What `[yoe]` leaves behind / where they differ:**

- **The `/ro` FUSE-mounted squashfs-per-package model.** distri mounts each
  package as a read-only SquashFS image at `/ro/<name-arch-version>` via a FUSE
  daemon, with "exchange directories" union-merging the locations where multiple
  packages must contribute files (headers, shared data). `[yoe]` installs apks
  into a shared FHS root — the same contrast drawn against Ubuntu Core's
  snap-per-app loopback model.
- **Store addressing.** distri's store is _versioned-name-addressed_ — image
  names carry a monotonic distri revision, not a content hash. This is **not**
  Nix-style content addressing. `[yoe]`'s cache is input/content-addressed (a
  hash of a unit's inputs selects its `.apk`). The two are different mechanisms
  for "don't rebuild what hasn't changed."
- **Build definitions.** distri's package definitions are Go code under `pkgs/`,
  compiled into the `distri` tool — programmatic, not a declarative DSL.
  `[yoe]` uses Starlark units loaded at runtime, with the build engine in Go and
  the package definitions outside it.
- **Hermeticity mechanism.** distri pins ELF `--dynamic-linker`/rpath to
  versioned package paths and uses `execve` wrappers for environment, rather
  than mount/namespace sandboxing. `[yoe]` relies on the container worker.
- **Scope and maturity.** distri targets x86_64 desktop/server (QEMU, GCE, a
  Dell XPS 13) with no cross, ARM/RISC-V, or embedded story, and has been
  effectively dormant for feature work since its 2020 "supersilverhaze"
  snapshot — an intentionally frozen research artifact, not a build system you
  adopt. `[yoe]` is embedded-first, multi-arch, and under active development.

**Key differences:**

|                   | distri                              | `[yoe]`                            |
| ----------------- | ----------------------------------- | ---------------------------------- |
| Nature            | Research proof-of-concept           | Embedded distro build system       |
| Package model     | Per-package SquashFS, FUSE-mounted  | apk into shared FHS root           |
| Store addressing  | Versioned-name (distri revision)    | Input/content-addressed `.apk`     |
| Build definitions | Go code compiled into the tool      | Starlark units loaded at runtime   |
| Build isolation   | Path-pinning + `execve` wrappers    | Container build worker             |
| Target            | x86_64 desktop/server (research)    | Embedded, multi-arch, custom BSP   |
| Status            | Dormant since ~2020; research-only  | Pre-1.0, active                    |

**When to use distri instead:** for production or embedded work, you wouldn't —
that is not what it is for. Read distri for its ideas: fast, hook-free,
parallel package operations and a concrete demonstration that the slowness of
mainstream package managers is an architectural choice, not a law of nature.
For the shipping equivalents of its immutability and atomic-update properties,
NixOS and Ubuntu Core are the general-purpose options; for embedded hardware,
that is the gap `[yoe]` aims to fill.

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

## vs. Bazel

Bazel is Google's general-purpose build system. Like GN it is not a Linux
distribution builder, but it shaped two of `[yoe]`'s foundational choices:
Starlark as the configuration language, and resolve-then-build as the execution
model.

**What `[yoe]` adopts from Bazel:**

- **Starlark as the configuration language** — adopted directly. Bazel
  popularized Starlark as a safe, deterministic Python subset for build
  definitions; `[yoe]` uses the same language for units, machines, and images.
- **Hermetic, explicit-input builds** — builds depend on declared inputs, not
  ambient host state.
- **Two-phase resolve-then-build** — analysis (construct and validate the graph)
  before execution (run the work). `yoe build` resolves the full unit DAG and
  reports graph errors up front, exactly as in the GN section above.
- **Input-keyed shared cache** — build outputs reused across machines when
  inputs match.

**What `[yoe]` leaves behind:**

- Action-graph granularity at the compiler-invocation level. `[yoe]`'s graph is
  unit-grained — one package per node — not one node per compiler call.
- The Java core and the large set of natively implemented rules.
- Modeling every build step in the build system. `[yoe]` delegates intra-unit
  builds to native toolchains (`go build`, `cargo`, `make`).

**Bazel fetches modules, but is not a distribution builder.** A natural question
is whether Bazel — given how much it is associated with large monorepos — has
anything like Yocto's or `[yoe]`'s ability to pull in many modules and assemble a
distribution. It has the first half, not the second.

Bazel has real external-fetch machinery: **Bzlmod** (`MODULE.bazel` plus the
[Bazel Central Registry](https://registry.bazel.build/), with Minimal Version
Selection) is the modern dependency system that replaced the legacy `WORKSPACE`,
and **repository rules** (`http_archive`, `git_repository`, and ecosystem
fetchers like `rules_go` + gazelle, `rules_rust` crate_universe, `rules_python`
pip, `rules_jvm_external`) wire external source and dependencies into the build
graph. This is genuine multi-module resolution — the closest Bazel analogue to
"pull in many external modules."

But that is **dependency acquisition for a build**, not a _distribution_. Bazel
has no notion of a curated package collection, a machine/BSP abstraction, a
kernel/bootloader/device-tree story, a rootfs assembler, a package feed, or OTA.
It produces artifacts and has no opinion about composing them into a bootable
embedded Linux image. Teams get partway there by bolting on add-on rules —
`rules_pkg` (emit `.tar`/`.deb`/`.rpm`), `rules_oci` (assemble OCI/container
images), and `rules_distroless` / `bazeldnf` (build minimal apt-/rpm-based root
filesystems, as KubeVirt does) — but these are container/appliance-image tools
that assume _prebuilt_ distro packages or a base image. None build a
distribution from source with a curated unit/recipe collection, layered BSP
customization, kernel configuration, and a device-update workflow. That whole
layer — the part that makes Yocto "Yocto" and `[yoe]` "`[yoe]`" — is simply not
a Bazel concern.

| Capability                          | Yocto / `[yoe]`        | Bazel                          |
| ----------------------------------- | ---------------------- | ------------------------------ |
| Fetch many external modules/deps    | Yes (layers / units)   | Yes (Bzlmod, repo rules)       |
| Curated package/recipe collection   | Yes (oe-core, units)   | No — you bring your own        |
| Machine/BSP, kernel, bootloader, DT | Yes                    | No                             |
| Rootfs/image assembly               | Yes                    | Only via add-on rules, from prebuilt pkgs |
| Package feed + OTA/rollback         | Yes                    | No                             |

The closest "research a radically different distribution model with a custom
tool" prior art is Michael Stapelberg's [distri](https://distr1.org/) — but that
is a research distro with its own tool, not Bazel, and its kinship with `[yoe]`
is on fast, hook-free package management and immutable atomic updates, not on
content-addressed caching (distri's store is versioned-name-addressed) or any
BSP/image story. See the [distri section](#vs-distri) above.

**Phase model.** Classical Bazel runs **loading → analysis → execution** as
strict global phases: analysis of the requested graph completes, and
analysis-time Starlark cannot do I/O, before any action runs. Bazel 7+
("Skymeld") relaxes this by pipelining execution per target as each target's
analysis finishes, but the two conceptual phases remain. `[yoe]` is deliberately
in the classical camp — resolve the whole unit DAG, validate, then build —
because at unit granularity a global resolve phase costs almost nothing and buys
"all graph errors reported before anything is built."

**Caching: action-grained CAS vs. unit-grained package cache.** Both Bazel and
`[yoe]` cache build outputs keyed by a hash of their inputs, and both can share
that cache across developers and CI. The difference is everything else.

- **Granularity.** Bazel caches at the _action_ level — one compiler
  invocation, one link step, one codegen run. Its remote cache is a
  content-addressable store (CAS) of blobs plus an action cache mapping each
  action key (command line + input content hashes + environment + platform) to
  its result. Change one source file and Bazel reuses every cached action except
  the handful that transitively depend on it. `[yoe]` caches at the _unit_
  level: one unit produces one `.apk`, and its cache key is a hash of the
  unit's declared inputs (`internal/resolve/hash.go`). Change anything a unit
  hashes and the whole unit rebuilds from source — intra-unit incrementality is
  delegated to the native toolchain underneath (`go build`, `cargo`, `make`
  doing their own object-level caching inside the unit build).
- **What is cached.** Bazel caches _intermediate_ artifacts — object files,
  generated headers, partial trees — in the CAS. `[yoe]` caches the _final
  distributable_ artifact: the same `.apk` bytes that ship to and install on
  the device. The build cache and the package feed are the same S3-compatible
  store, so "what CI built," "what the cache serves," and "what a device pulls"
  are one thing, not three.
- **Correctness model.** Bazel's cache correctness depends on every action
  declaring its inputs completely; an under-declared input silently poisons the
  cache, which is why Bazel leans on sandboxing to enforce hermeticity at action
  granularity. `[yoe]` hashes a unit's declared inputs too, but the blast radius
  of a hashing mistake is one package, the container worker bounds ambient
  inputs, and the project rule that every hash-participating field be added
  deliberately (and stay cache-neutral when unset) keeps the input set auditable
  by reading one unit rather than reasoning about an action graph.
- **Operational cost.** A Bazel remote cache means running and securing a Remote
  Execution API server (bazel-remote, Buildbarn, BuildBuddy), usually paired
  with remote _execution_ so actions run on a cluster. `[yoe]` needs only a
  bucket URL; there is no remote execution — builds always run in the local
  container worker, and the cache only ever serves or stores whole `.apk`s. This
  is the same simplicity argument the Yocto section makes against sstate, applied
  to Bazel's REAPI stack.

The trade is deliberate. Bazel's fine grain extracts maximum reuse from a
million-node action graph but pays for it in input-declaration discipline and
cache infrastructure. `[yoe]`'s coarse grain rebuilds a whole package when any
of its inputs change, which is cheap because packages are the unit of
distribution anyway and the language toolchains cache within — and in exchange
the cache is a plain object bucket whose contents are exactly the artifacts
devices install.

**Key differences:**

|                        | Bazel                            | `[yoe]`                             |
| ---------------------- | -------------------------------- | ----------------------------------- |
| Purpose                | General-purpose build system     | Embedded Linux distribution builder |
| Output                 | Arbitrary build artifacts        | `.apk` packages and disk images     |
| Config language        | Starlark                         | Starlark                            |
| Dependency granularity | Action / target                  | Unit (package)                      |
| Rule implementation    | Java core + Starlark rules       | Starlark units/classes              |
| Phase model            | Analysis then execution (phased) | Resolve then build (phased)         |
| Build execution        | Sandboxed action graph           | `yoe` orchestrates unit builds      |
| Cache granularity      | Per action (compiler/link step)  | Per unit (one `.apk`)               |
| What is cached         | Intermediate artifacts in a CAS  | Final distributable `.apk`          |
| Cache == package feed  | No — separate from any artifact repo | Yes — same S3-compatible store   |
| Remote cache infra     | REAPI server (bazel-remote, etc.)| Plain object bucket (URL only)      |
| Remote execution       | Yes (action offload to a cluster)| No — always local container worker  |

Bazel is not an alternative to `[yoe]` — it builds artifacts, `[yoe]` builds a
distribution and bootable images. But Starlark, resolve-then-build, and an
input-keyed shared cache are battle-tested Bazel patterns that `[yoe]` carries
into the embedded Linux domain.

## vs. Buck2

[Buck2](https://buck2.build/) is Meta's Rust rewrite of Buck, open-sourced in
2023. Like Bazel and GN it is a general meta-build system, not a distribution
builder. Its relevance here is a sharp architectural contrast with Bazel that
sharpens a choice `[yoe]` also has to make.

**Single graph vs. two phases.** This is the core Buck2-vs-Bazel distinction,
and the framing holds up — with one nuance:

- **Bazel** has a hard analysis/execution split. It builds and validates the
  action graph, then executes it; analysis-time Starlark cannot do I/O, so a
  rule cannot read a generated file to decide what to build next, which makes
  dynamic dependencies awkward. Bazel 7+ Skymeld pipelines this per target, but
  the two conceptual phases remain.
- **Buck2** has no phases. Loading, configuration, analysis, and execution are
  all nodes in one incremental computation graph (its **DICE** engine).
  Different targets' analysis and execution interleave and are recomputed
  incrementally together, and a rule can produce an artifact, read it, then
  declare further actions (`dynamic_actions` / dynamic outputs) — natural in a
  unified graph, hard across Bazel's split. Buck2 also pushes **all** rules
  (even C++/Java) into a Starlark "prelude"; the binary carries zero built-in
  language knowledge, whereas Bazel still implements core rules in Java.

**Where `[yoe]` sits: closer to Bazel/GN than to Buck2.** `[yoe]` deliberately
runs a global resolve phase and then builds. That is the right trade at unit
granularity: a few hundred coarse package nodes resolve in well under a second,
so the whole-graph-analysis cost that Buck2's unified graph exists to eliminate
barely registers, while the strict resolve phase buys clean "errors first, no
half-finished builds" behavior. Buck2's single-graph model earns its complexity
at Meta-monorepo scale with millions of fine-grained action nodes; `[yoe]` does
not operate there, and adopting that model would be complexity without payoff.

**What `[yoe]` adopts from Buck2:**

- **Validation that Starlark-everywhere is viable** — Buck2 demonstrates a
  serious build system can keep zero language knowledge in the core and put all
  rules in Starlark. That is precisely `[yoe]`'s unit/class model.
- **Precise incremental recomputation** — `[yoe]`'s per-unit content-addressed
  cache rebuilds only what changed, the same instinct as DICE's change
  tracking, at coarser grain.

**What `[yoe]` leaves behind:**

- The single unified incremental graph (DICE). `[yoe]`'s two-stage
  resolve-then-build is intentional at unit grain.
- Action-level granularity, dynamic action graphs, and the remote-execution
  worker model.

**Key differences:**

|                        | Bazel                  | Buck2                       | `[yoe]`                   |
| ---------------------- | ---------------------- | --------------------------- | ------------------------- |
| Core language          | Java                   | Rust                        | Go                        |
| Graph model            | Phased (analysis/exec) | Single incremental graph    | Phased (resolve/build)    |
| Dynamic dependencies   | Awkward (phase split)  | First-class (`dynamic_*`)   | N/A — unit grain          |
| Rule implementation    | Java core + Starlark   | All Starlark (prelude)      | Starlark units/classes    |
| Dependency granularity | Action / target        | Action / target             | Unit (package)            |
| Scale target           | Large monorepos        | Meta-scale monorepos        | Embedded distro graphs    |

Buck2 is not an alternative to `[yoe]` — it solves a different problem at a
different scale. It is included because the single-graph-vs-two-phase contrast
clarifies why `[yoe]`'s phased resolve-then-build is a deliberate fit for
unit-grained embedded builds, not an unconsidered default.

## vs. Pigweed

[Pigweed](https://pigweed.dev/) is Google's collection of embedded libraries
("modules") and developer tooling for microcontroller, bare-metal, and RTOS
firmware — embedded C++ and, increasingly, Rust on parts like Cortex-M, RP2350,
and STM32. It is not a Linux distribution or rootfs builder. It operates one
layer below where `[yoe]` lives — the MCU firmware on a board, not the Linux
application SoC — so it is a complement, not a competitor. A single product can
run `[yoe]`-built Linux on the application processor and Pigweed-built firmware
on a companion microcontroller.

**What `[yoe]` shares in spirit with Pigweed:**

- **Per-module consumption** — Pigweed is explicitly designed so you take only
  the modules you need into an existing project rather than adopting a
  monolith. `[yoe]`'s unit and module composition shares this instinct.
- **Ergonomic single front-door CLI** — the `pw` command aggregates per-module
  subcommands as plugins, and `pw_env_setup` builds a hermetic toolchain
  environment without mutating the host. `[yoe]`'s single `yoe` CLI plus
  container-as-build-worker chase the same goal: one tool to drive everything,
  no changes to the developer's machine.

**What's different:**

- **Target layer** — Pigweed produces bare-metal/RTOS firmware libraries;
  `[yoe]` produces a full Linux userspace with BSP, image, and update tooling.
  Their outputs do not overlap.
- **Build system** — Pigweed historically used GN, with
  [Bazel now the strategic direction](https://pigweed.dev/seed/0111.html) and
  the recommendation for new projects and the Pigweed SDK; GN remains the
  primary build system for upstream Pigweed development as of 2025. `[yoe]` is
  its own Go engine plus Starlark. Pigweed *consumes* GN/Bazel; `[yoe]`
  *replaces* that layer for its own domain.

**When to use Pigweed instead — or alongside:** if the target is an MCU running
bare-metal or an RTOS, Pigweed is the right toolbox and `[yoe]` simply does not
apply. On a mixed design (Linux SoC plus companion MCU), use both: `[yoe]` for
the Linux side, Pigweed for the firmware side. They meet at the board, not in
the build.

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

### Enterprise vs. Small Teams

The sharpest dividing line between `[yoe]` and the established systems is not a
technical capability — it is the organizational shape each one assumes.

Yocto, Bazel/Buck2, Ubuntu Core, and Avocado are calibrated for **enterprises**.
They assume there is headcount to operate complexity (a platform team running
sstate mirrors and hash-equivalence servers, or a Remote Execution cluster), a
vendor relationship to lean on (a silicon vendor's supported BSP layer, a
Canonical brand store, a Peridio OTA contract), and a multi-year support
horizon. In return they offer breadth, fine-grained control, and a deep
ecosystem. At that scale the operational weight is a worthwhile trade — the org
already has the people, and the flexibility pays for itself across hundreds of
products and engineers.

`[yoe]` inverts the calibration. It optimizes for the **team of one to ten**
building a product where the application is the differentiator and the base OS
is plumbing — a team that cannot spare an engineer to become the in-house Yocto
or Bazel expert and cannot justify standing up build infrastructure. For that
team the right system is one that is near-zero-maintenance (a cache that is a
bucket URL), learnable in an afternoon (one language, no metadata stack
underneath), and self-hostable with no commercial gate. The cost of that
calibration is real and acknowledged below: fewer packages, no vendor BSP moat,
less tooling maturity.

This is deliberately **not** "`[yoe]` is a smaller Yocto." A team that already
has a platform group, a Yocto BSP from its silicon vendor, and products shipping
on that stack should keep it — `[yoe]` is not trying to win that team, and
porting away from a working enterprise build system is rarely worth it. The
opportunity is the large population of small embedded-product teams for whom the
enterprise systems are overkill and Buildroot is too limited — the same way
Alpine never displaced Debian but became the obvious default for containers (see
[The Alpine Linux Precedent](#the-alpine-linux-precedent) below).

### Where `[yoe]` Cannot Compete (Yet)

Be honest about the gaps:

**Vendor BSP support is Yocto's real moat.** Every major SoC vendor (NXP, TI,
Qualcomm, Intel, Renesas, MediaTek) ships Yocto BSP layers and supports them.
This is not a technology problem — it's an ecosystem problem that Linux
Foundation backing solves. No amount of technical superiority overcomes "the
silicon vendor gives us a Yocto BSP and supports it."

**Source-built package count.** Yocto has ~5,000 recipes across oe-core +
meta-openembedded, Buildroot has ~2,800 packages, Alpine has ~36,000, Debian
has ~35,000, and Nixpkgs has ~142,000. `[yoe]` builds dozens from source. The
raw-availability gap is smaller than that number suggests: the Alpine module
(`alpine_pkg`) wraps Alpine's prebuilt `.apk`s as units — thousands of
main/community packages, fetched verbatim, re-signed with the project key, and
pinned to a single Alpine release — so most of "I just need `dbus`/`python3`/
`ffmpeg` on the device" is a one-line dependency, not a porting task. The
honest gap is narrower and more specific: a package only Alpine ships as a
binary is _consumed_, not _built from source under your control_, and anything
Alpine does not carry (or carries with the wrong build options) still needs a
written unit. The prebuilt-wrapper pattern is deliberately distro-agnostic — a
`*_pkg` class fetches an upstream package, re-signs it, and exposes it as a
unit; Alpine is the only prebuilt source today, and the same shape is intended
to extend to other distros (Debian/Ubuntu binary packages, for example) so the
binary-availability tier is not tied to a single upstream. Yocto's value is that
everything is from source by default; `[yoe]`'s bet is that prebuilt-distro
packages plus source-where-it-matters covers most real products with far less
work.

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

**AI-assisted unit generation.** With prebuilt distro packages already
consumable via `alpine_pkg` (Alpine today, other distros planned), the gap is
from-source coverage. If an AI can generate a working Starlark unit from a
project URL faster than porting a Yocto unit, even that gap stops mattering.
Starlark is far more tractable for AI than BitBake's metadata format.

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

6. **Prebuilt-distro consumption + AI unit generation + aports conversion.** The
   `alpine_pkg` module already closes most of the _binary-availability_ gap —
   thousands of Alpine packages consumable as prebuilt `.apk`s with no porting —
   and the same `*_pkg` pattern is meant to extend to other distros so that tier
   is not Alpine-bound. The remaining work is the _from-source_ tier: packages
   that must be built under your control or with non-distro options. Lean into
   the AI-native angle there — generating a from-source unit from a project URL
   should be a conversation, not a manual porting exercise — and _also_ ship a
   mechanical APKBUILD → Starlark converter, since Alpine's ~36,000 APKBUILDs are
   the most predictable path to broad from-source coverage. AI for novel cases,
   mechanical conversion for the long tail, prebuilt-distro `*_pkg` for
   everything that just needs to be present.

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

**Google GN / Bazel / Buck2 / Pigweed** — out of scope; none build Linux rootfs
images as a first-class concern.

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
| Packages available      | ~5,000   | ~2,800    | ~36,000  | ~15,000  | ~35,000  | ~10,000   | ~142,000  | **Dozens from source + ~Alpine prebuilt** |

_UC = Ubuntu Core. "Min base image size" is the approximate on-disk footprint of
the smallest practical bootable/usable root filesystem (core-image-minimal for
Yocto, `minbase` debootstrap for Debian, minirootfs for Alpine, a minimal Ubuntu
Core 24 model with no app snaps, a minimal NixOS closure). Actual sizes vary
with architecture, kernel, and configuration. "Packages available" is the rough
count of ready-to-use packages/recipes in the standard/common repositories;
Yocto counts typical oe-core + meta-openembedded, Arch excludes the ~90,000 AUR
packages, UC counts snaps in the public store — a different delivery model that
is not directly comparable. `[yoe]`'s entry is two-tier: dozens of packages
built from source in `module-core`, plus thousands of distro packages consumed
as prebuilt binaries via a `*_pkg` module (Alpine today via `alpine_pkg` —
pinned to one Alpine release, re-signed with the project key; other distros a
planned extension of the same pattern) — so the practical availability ceiling
is close to the upstream distro's, while the from-source set is intentionally
small. Sources: project documentation,
[repology.org](https://repology.org/repositories/packages)._
