# libc, init, and the rootfs base

Yoe's default and most mature base is musl + OpenRC + Alpine-derived. It now
also builds experimental **glibc + systemd** images on Debian and Ubuntu bases
(see [Yoe and distributions](distro.md), [module-debian.md](module-debian.md),
[module-ubuntu.md](module-ubuntu.md)). This document explains the musl/Alpine
choice, what it implies for the products yoe serves, where the libc/init
boundary lies, and how the glibc/systemd path closes it — most notably for
edge-AI hardware where glibc and systemd are non-negotiable.

## What yoe ships today

The default and most mature configuration is:

- **musl libc.** All units build against musl. The build container
  (`toolchain-musl`) is Alpine-based. The `module-alpine` module pulls prebuilt
  apks from Alpine, which are themselves musl builds.
- **busybox + a curated GNU userland on top.** The `replaces` mechanism manages
  file conflicts where util-linux, coreutils, etc. shadow busybox applets that
  ship a real implementation.
- **OpenRC service supervision.** Yoe-specific units ship native OpenRC scripts
  (`#!/sbin/openrc-run`) under `/etc/init.d/<name>`, and the `services = [...]`
  declaration in a unit becomes a runlevel symlink at
  `/etc/runlevels/default/<name>`. busybox init remains PID 1; `/etc/inittab`
  triggers OpenRC's `sysinit`, `boot`, and `default` runlevels in order. There
  is no systemd integration and no plan to add one inside `module-core`.
- **apk packaging.** On the Alpine base, all yoe units produce signed `.apk`
  artifacts, installed with apk-tools at image-assembly time. (On the Debian and
  Ubuntu bases, units produce signed `.deb`s installed with dpkg/apt instead —
  see below.)

This stack runs cleanly on x86_64, arm64, and (with limitations) riscv64. It
boots on QEMU, Raspberry Pi, BeagleBone, and any board where an upstream
mainline kernel + a sane bootloader handle the hardware.

**Beyond the default (experimental).** yoe also builds glibc + systemd images on
Debian and Ubuntu bases. These are selected per image via the **distro** axis
(`distro = "debian" | "ubuntu"`), pull their userland as native `.deb`s through
`apt_feed(...)`, and run systemd as PID 1. They build, boot in QEMU, and accept
SSH in the nightly CI matrix on both x86_64 and arm64, but are not yet
production-hardened. This is how the glibc/systemd boundary discussed below is
crossed today; the rest of this document explains why that boundary exists and
what the glibc path unlocks. See [Yoe and distributions](distro.md) for the full
distro model, and [module-debian.md](module-debian.md) /
[module-ubuntu.md](module-ubuntu.md) for the per-base specifics.

## Where this stack works well

The musl/OpenRC/Alpine foundation is a fine choice — often the better choice —
for products that share these properties:

- **The developer controls the entire software stack.** Custom apps, language
  runtimes the project picks, no closed-source vendor binaries in the critical
  path.
- **Footprint, boot time, and simplicity matter.** Alpine-derived images are
  typically half the size of a comparable Ubuntu image and boot in seconds.
  OpenRC is dramatically simpler than systemd.
- **No regulatory dependence on a specific OS baseline.** No Adaptive AUTOSAR,
  no FedRAMP/FIPS profile that names glibc, no telecom CNF spec that assumes
  RHEL.
- **Hardware works with mainline drivers.** No SoC vendor blob that was written
  against a specific Ubuntu LTS.

This covers a lot of real embedded territory: hobbyist SBC products, industrial
gateways and edge controllers, networking equipment, custom IoT, industrial
sensors, single-purpose appliances. It is a large and underserved market.

## Where this stack does not work

Some products genuinely cannot ship on musl + OpenRC. The blockers are not
theoretical — they are concrete proprietary binaries or specification
requirements that yoe alone cannot work around.

### Hard blockers (you must have glibc)

1. **SoC-vendor binary blobs.** NVIDIA Jetson's CUDA/cuDNN/TensorRT, Qualcomm
   display and camera HALs, NXP i.MX VPU and ISP blobs, Mali and Vivante GPU
   drivers. These are glibc-only proprietary binaries shipped by the silicon
   vendor with no plans to support musl.
2. **Commercial industrial-control runtimes.** Codesys, ISaGRAF, vendor PLC
   stacks, fieldbus stacks (PROFINET / EtherCAT closed implementations).
3. **Vendor BSP ecosystems.** Yocto BSPs from SoC vendors default to glibc +
   systemd and assume both throughout.
4. **Strict standards regimes.** Adaptive AUTOSAR, telecom 5G CNF profiles,
   certain medical-device certifications.
5. **Enterprise Java app servers.** WebSphere, WebLogic, some Oracle middleware
   — validated only on glibc.

### Hard blockers (you must have systemd)

1. **Applications linking `libsystemd` directly** (sd-bus, sd-journal).
2. **Service hardening directives** (`PrivateTmp`, `ProtectSystem`, namespace
   policy) used as primary architecture rather than a sidecar.
3. **Container runtimes configured with the systemd cgroup driver** — many
   edge-AI inference deployments fall into this.
4. **Apps shipping systemd-only `.service` files**, where porting to OpenRC
   means touching every app rather than the OS.

### Soft blockers (workable but real)

- musl's locale and i18n support is intentionally minimal.
- DNS resolver edge cases (musl historically did not do DNS-over-TCP for large
  responses by default).
- libstdc++ and a handful of glibc-specific extensions (`LD_AUDIT`, `nscd`,
  certain printf format specifiers, `getaddrinfo` quirks).
- Debug tooling — `gdb`, `perf`, eBPF — has rougher edges on musl.

These are workable individually; in aggregate, on a complex product, they add
up.

## The case yoe should serve next: edge AI on Jetson

The natural next market for yoe is **edge AI on Jetson-class hardware**. This is
where embedded budget is concentrated through 2026–2030, and it is where the
existing tooling story is genuinely poor — NVIDIA's SDK Manager hands you a
stock Ubuntu image, customization is painful and non-reproducible, and
meta-tegra (the Yocto path) is heavy and lags the official BSP.

It is also a market that yoe **cannot serve in its current configuration**,
because Jetson forces glibc + systemd:

- CUDA, cuDNN, TensorRT, DeepStream, Triton, Argus, MMAPI — all glibc, all
  proprietary.
- L4T (Linux for Tegra) is an Ubuntu derivative; NVIDIA's docs, support,
  reference designs, and customer projects all assume Ubuntu-shaped systems.
- `nvidia-container-runtime` integrates with Docker/containerd configured
  against systemd's cgroup driver.
- Out-of-tree NVIDIA kernel modules must be built against L4T's kernel tree with
  NVIDIA's patches.

There is no clever way around this. A "musl Jetson" is a research project, not a
product.

## Strategic options

### A. Stay where we are

Keep yoe aimed at the non-AI segment. Don't pursue Jetson. This is the simplest
path and the one the existing architecture serves cleanly. It is a smaller
market than (C), but a real one.

### B. Pivot fully to edge AI

Discard the Alpine-first foundation. Build yoe around Ubuntu/L4T as the default
rootfs source. The `alpine_pkg` work becomes mostly irrelevant. Different
foundation, different competition (SDK Manager, balenaOS, Foundries.io's LmP,
meta-tegra), different positioning.

### C. Make yoe agnostic about the rootfs base

Keep what we have, add a project-level abstraction that lets each project pick
its own rootfs source. The same yoe DAG, dev loop, image assembly, signing, and
OTA serve both "minimal Alpine gateway" and "CUDA-enabled Jetson edge AI box."

This is yoe's most defensible long-term identity. There is no other tool that
gives you a consistent embedded dev experience across heterogeneous distribution
bases. The work already done on shadowing, unit override, the `alpine_pkg`
class, and the apk-feed model is the right architecture for this future — the
base-source abstraction sits **above** it, not in place of it.

**(C) is the recommended direction.**

## Rootfs-base abstraction (partially realized)

> **Status:** Partially realized. yoe now builds glibc + systemd images on
> Debian and Ubuntu bases (experimental) — but via the **distro axis**
> (`distro = "debian" | "ubuntu"`) and per-distro modules, not the `base = …`
> project field sketched below. Treat the `base = ubuntu_l4t(...)` /
> `alpine_rootfs(...)` syntax here as illustrative of the goal, not the shipped
> API. The Jetson/L4T base specifically — CUDA, a `toolchain-glibc-arm64`, an
> L4T rootfs — is still forward design and does not exist yet.

The shape of the abstraction:

```python
project(
    name = "edge-ai-camera",
    base = ubuntu_l4t(version = "36.4", flavor = "minimal"),
    machines = [...],
    modules = [
        module("...", path = "modules/units-l4t"),    # CUDA, TensorRT, DeepStream
        module("...", path = "modules/my-app"),       # the actual product
    ],
)
```

Or for the existing Alpine path:

```python
project(
    name = "industrial-gateway",
    base = alpine_rootfs(version = "v3.21"),
    machines = [...],
    modules = [
        module("https://github.com/yoebuild/module-alpine.git", ref = "main"),
        module("https://github.com/yoebuild/yoe.git", ref = "main", path = "modules/module-core"),
    ],
)
```

Or for the from-source extreme:

```python
project(
    name = "minimal-bootloader-test",
    base = yoe_native(),                  # build everything from source
    ...
)
```

A base is a tuple of
`(libc, init, filesystem conventions, upstream feed format)`. The first three
are runtime properties of the target. The fourth is a conversion-time concern
handled by yoe, **not** something that propagates to the target.

The base provides:

- **A starting rootfs.** Tarball, deb-bootstrap, apk-bootstrap, or "build it
  yourself."
- **The libc and init choice.** Implied by the base — `ubuntu_l4t` implies
  glibc + systemd, `alpine_rootfs` implies musl + OpenRC, `yoe_native` implies
  whatever yoe builds explicitly.
- **Filesystem conventions.** Multiarch lib paths under Debian-derived bases,
  flat paths under Alpine, etc.
- **The "given" packages.** Things the base distribution already ships, that yoe
  consumes rather than rebuilds (CUDA on Jetson, busybox on Alpine).
- **The upstream feed format.** apt/deb for Ubuntu/L4T bases, apk for Alpine
  bases. yoe is pragmatic about what it serves on the target: it matches the
  base's native format rather than forcing a single one everywhere (see
  [Package format follows the base](#package-format-follows-the-base) below).

What yoe **continues to own across every base**:

- Image assembly: partition layout, bootloader install, signing, OTA.
- The DAG and content-addressed cache.
- The dev loop: `yoe build`, `yoe dev`, `yoe deploy`, `yoe run`, `yoe flash`.
- The unit format and the override/composition model.
- A single project-signed feed and a single project trust root on the target —
  whatever the package format underneath.
- The on-target installer appropriate to the base (apk-tools on musl/Alpine,
  dpkg/apt on glibc/Debian).
- The TUI and the project orchestration commands.

The bits that vary with the base:

- The toolchain container (`toolchain-musl` for Alpine, `toolchain-glibc-arm64`
  for Jetson, etc.).
- The init system integration (OpenRC scripts vs systemd unit files).
- The `network-config`-style yoe-defining units (would have a systemd-flavored
  variant for systemd bases).
- The on-target package format and the mechanism that consumes upstream packages
  (`alpine_feed` on Alpine; native `.deb` via `apt_feed` on Debian/Ubuntu — see
  [module-debian.md](module-debian.md)).

## Package format follows the base

> **Status:** Implemented for the Debian/Ubuntu bases. yoe builds its own units
> as native `.deb`s and serves a project-signed apt repo; upstream `.deb`s
> mirror in verbatim via `apt_feed(...)`. The Alpine base uses apk as before.
> See [module-debian.md](module-debian.md) and
> [module-ubuntu.md](module-ubuntu.md) for the shipped design.

yoe is pragmatic about the on-target package format: **apk-everywhere is a
default, not a hard requirement.** An earlier version of this doc stated "apk
always, convert everything at fetch time" as an invariant. That is the right
call on the Alpine/musl base. On a Debian/glibc base it is one of two reasonable
options, and probably not the better one — because a project picks exactly one
base, the musl and glibc worlds never share an image, so a cross-base single
format buys a uniformity little actually consumes while costing a conversion
layer and dpkg-userland emulation. But that argument makes conversion _less
attractive_, not forbidden; the choice stays open and can be per-project.

How each base resolves it:

- **Alpine / musl base** → apk + apk-tools, as today. Upstream apks are consumed
  via `alpine_feed`, re-signed with the project key.
- **Debian / glibc base** → native deb end to end: yoe builds its units as
  `.deb`s and serves a signed apt repo, with upstream `.deb`s mirrored verbatim
  and no conversion layer. (An early design also weighed converting `.deb`s to
  project-signed apk; native deb won, since a project picks exactly one base and
  the musl/glibc worlds never share an image, so a cross-base single format buys
  little.)

What stays constant across bases is the part that matters: one project-signed
feed, one trust root on the target, the same DAG/cache, the same dev loop, and
the same `yoe deploy`. Upstream signing keys (NVIDIA's apt key, Ubuntu's
keyring) are used only at fetch/mirror time to verify what yoe pulls in; they
never reach the target.

Glibc binaries on a glibc base, systemd unit files on a systemd base, multiarch
paths on a Debian-conventions base — all handled by **the base**. Once libc +
init + conventions match what an upstream package was built for, its binaries
run unchanged, delivered in the base's native format with no repackaging.

The kernel-module problem (NVIDIA's out-of-tree drivers built against L4T's
specific kernel ABI) is orthogonal to package format — it's a Jetson-target
problem, tracked separately.

See [module-debian.md](module-debian.md) for the shipped Debian-base design: how
the base rootfs is obtained, the signed apt-feed work, the verbatim
upstream-mirror model, and the systemd image-assembly integration.

## Base bootstrap

Yoe does not have a "bootstrap" phase in the `debootstrap` sense — there is no
separate first stage that builds a minimum environment before normal package
installation can run. The rootfs assembly is a single procedure that works the
same way across bases — Alpine today, and the glibc/systemd Debian and Ubuntu
bases today as well. The shape is the same; the installer and metadata layout
follow the base's package format. On the Alpine base it runs as:

1. `mkdir <rootfs>` — the starting rootfs is an empty directory.
2. Create the apk DB skeleton:
   `mkdir -p <rootfs>/lib/apk/db && touch <rootfs>/lib/apk/db/installed`.
3. Drop the project's signing key into `<rootfs>/etc/apk/keys/`.
4. Write `<rootfs>/etc/apk/repositories` pointing at the project's signed feed
   (and any auxiliary feeds the base wants to consume directly, if the project
   opts in).
5. `apk add --root <rootfs> --initdb <package list>` — run from inside the
   toolchain container, against the project's feed.

That is the whole assembly. Everything in the rootfs lands via packages. The
first packages installed (`base-files`, `musl` or `libc6`, the userland shell,
the package tool, init system) carry the filesystem skeleton — `/etc/passwd`,
`/etc/group`, `/dev`, `/proc` mountpoints, default config files — inside their
data segments. The Debian and Ubuntu bases follow the equivalent steps with
`dpkg`/`apt` against a signed apt repo instead: initialize the dpkg admin dir,
trust the project key, point at the project's apt feed, and install the
foundation set.

The only things that have to exist before this loop runs are the **toolchain
container** (provides the package tool — apk-tools or dpkg/apt — as the
orchestrator binary) and the **project's signed feed** (provides the packages to
install).

### What varies by base

- **The foundation package set.** Alpine bases install `base-files`, `busybox`,
  `musl`, `apk-tools`, OpenRC. A glibc/systemd base installs something like
  `base-files-systemd`, `libc6`, `bash` (or `busybox-glibc`), `apk-tools-glibc`,
  `systemd`, `dbus`. Each base declaration enumerates its foundation set.
- **The toolchain container.** `toolchain-musl` for Alpine bases, a parallel
  `toolchain-glibc-arm64` (or similar) for glibc bases. The container's libc and
  the target's libc are independent — apk-tools at install time just extracts
  files, it doesn't dlopen them.
- **The signing key trusted in the rootfs.** Always the project key. The
  upstream signing key (Alpine's, NVIDIA's, Ubuntu's) is used during fetch and
  verification by the conversion class but never reaches the target.

### Two source models for foundation packages

**Option A: From-source (purist, fully reproducible).** Every package, including
the essentials, is built from source by yoe and published in the project's feed
in the base's native format. The starting rootfs is empty; yoe owns the entire
chain. For a glibc/systemd base, that means building `libc6`, `libstdc++6`,
`systemd`, `bash`, etc. as `.deb`s. More setup work, total reproducibility.

**Option B: From-tarball (pragmatic, vendor-blessed).** The project's `base()`
declaration points at a vendor-supplied rootfs tarball — NVIDIA's official L4T
sample rootfs for Jetson, `ubuntu-base-<version>.tar.gz` for generic Ubuntu, or
`alpine-minirootfs-<version>.tar.gz` for an Alpine shortcut. yoe extracts the
tarball as the starting rootfs, then overlays yoe-built packages on top using
the base's native installer (`apk add --root` on Alpine, `apt`/`dpkg --root` on
Debian). The installer owns its own DB and ignores files it didn't put there,
except where its package contents collide. Faster to set up because the wrapping
work for "every essential package" is replaced by trusting the tarball. Less
reproducible because the tarball is a black box.

For Jetson, most projects will pick Option B — NVIDIA tests the sample rootfs
and supports it as the basis of L4T. Option A is the right answer when every
byte must be audited, when no vendor tarball exists, or when a project wants the
same provenance story across bases.

### Why an empty starting rootfs works for any libc

A common confusion: if running glibc binaries requires glibc to be present, how
does an empty rootfs get glibc onto itself?

The installer at assembly time (apk-tools on Alpine, `dpkg`/`apt` on Debian) is
a **file extractor**, not an executor. It reads each package's data archive and
writes the files to the target rootfs; nothing ever calls into the binaries it's
installing. The installer process doing the work runs in the toolchain
container, where its own libc is whatever the container provides — musl today,
glibc on a glibc-based toolchain container later. When it extracts the `libc6`
package's data archive into the target rootfs, it places
`/lib/aarch64-linux-gnu/libc.so.6` on disk; nothing tries to dlopen it until the
rootfs actually boots.

So the toolchain container's libc and the target rootfs's libc are independent.
A Jetson target rootfs (glibc) can be assembled from a toolchain container
that's still musl-based, and a glibc-built `dpkg`/`apt` can land on the target
as just another package alongside `libc6`, ready to run on first boot.

The same principle is why on-target package installs after deployment work
across bases: by then the rootfs has its own installer binary linked against its
own libc, and the loop is just "extract files, update DB."

## What changes for yoe-defining units

Today, `network-config`, `base-files`, and similar units assume OpenRC service
scripts under `/etc/init.d/` with runlevel symlinks in
`/etc/runlevels/default/`. In a base-agnostic future, those units gain a
base-aware code path or get split into init-system-specific variants. The
override model already in yoe (name shadowing, `provides` for alternative
selection) handles this cleanly: either the init-system-specific `units-systemd`
module shadows `network-config` with a systemd version, or `network-config`
itself detects the active base.

Either pattern works. The decision is local to each unit.

## Practical roadmap

> **Status:** Phases 1–3 have largely landed — the Alpine path is solid, the
> Alpine-coupled seams were made pluggable, and the Debian package path shipped
> as native deb (experimental Debian and Ubuntu bases). Debian and Ubuntu
> already coexist with Alpine via the distro axis — a second and third base — so
> the multi-base generalization (phases 5–6) is in practice already exercised;
> the remaining gap is the Jetson/L4T base and its toolchain (phase 4). Phases
> 4–6 stay forward design, conditional on demand.

1. **Solidify the Alpine path — done.** Ship enough that yoe is a viable choice
   for non-AI embedded products today. The same architecture carries forward;
   this is the foundation that proves the dev-loop and image-assembly value
   before a second base is introduced.

2. **Identify the Alpine-coupled seams — done.** Survey `module-core` and the
   internal Go code for assumptions that won't survive a non-Alpine base:
   hardcoded apk-tool invocations, OpenRC-flavored init paths, busybox-shadow
   logic in `replaces`, the toolchain container's musl-only Dockerfile. Make
   these pluggable. (The distro axis is what these seams became.)

3. **Debian package path — done.** Landed as a native `.deb` writer + signed
   apt-repo generator, consumed through `apt_feed(...)`; Debian and Ubuntu bases
   build and boot experimentally today. See [module-debian.md](module-debian.md)
   and [module-ubuntu.md](module-ubuntu.md).

4. **First Jetson prototype.** Pick a single Jetson SKU (Orin Nano dev kit is
   cheapest), get a yoe-assembled image booting with CUDA working end-to-end.
   Treat it as a learning project — the goal is to discover what abstraction
   breaks, not to ship Jetson support. Likely outputs: a `toolchain-glibc-arm64`
   container, a `ubuntu_l4t` rootfs base, the chosen Debian package path, a
   systemd-flavored `network-config`, the glibc on-device installer.

5. **Promote the abstraction.** With one working Jetson example, generalize the
   project base configuration so the same yoe codebase serves both Alpine and
   Jetson cleanly. Whichever Debian package path is chosen earns its keep by
   being reused across Ubuntu generic, Debian, L4T, and any future
   Debian-derived base.

6. **Second base, third base.** Once the abstraction is proven on two distinct
   bases, additional bases (Ubuntu generic, Adelie's glibc/musl mix, Yocto
   layers, custom rootfs tarballs) become incremental wraps rather than
   redesigns.

## Decision rubric

yoe should still refuse to chase glibc/systemd compatibility through hacks
(gcompat shims, dual-libc images, OpenRC-emulating-systemd layers) on the Alpine
base. These produce brittle systems that look like they work and then fail at
the worst moment. When a target genuinely needs glibc + systemd, the answer is
to pick a Debian or Ubuntu base (experimental today) rather than bend the Alpine
one — and for Jetson/L4T specifically, "yoe is not the right tool yet" remains
honest until the L4T base lands.

For the Alpine path, the rubric stays as established in
[module-alpine.md](module-alpine.md):

- Yoe builds the easy stuff (small libraries, small userland tools) to preserve
  libc-portability.
- `module-alpine` ships Alpine-native (apk-tools, alpine-keys, musl) and
  hard-to-build packages (when added — openssl, curl, openssh, qtwebengine,
  python, llvm).
- Project-level shadowing remains the override hook for any individual package
  the project wants to swap.

## Summary

Today: musl + OpenRC + Alpine by default, serving non-AI embedded well, plus
experimental glibc + systemd images on Debian and Ubuntu bases selected via the
distro axis.

Tomorrow (planned): extend the same model to Jetson/L4T — a glibc base with
CUDA, an arm64 glibc toolchain, and out-of-tree NVIDIA drivers — so one yoe
experience spans Alpine gateways and edge-AI boxes.

Not on the menu: trying to make musl/OpenRC pretend to be glibc/systemd, or
trying to make yoe pretend to be a single-base distribution like Alpine itself.
Those are projects that have already been tried and have not aged well.
