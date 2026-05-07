# libc, init, and the rootfs base

Yoe today is a musl + OpenRC + Alpine-derived distribution builder. This is a
deliberate choice, not an accident, but it is also not a permanent one. This
document explains the choice, what it implies for the products yoe can serve,
where the boundary lies, and the planned direction for serving products that sit
on the other side of that boundary — most notably edge-AI hardware where glibc
and systemd are non-negotiable.

## What yoe ships today

The default and currently only fully-supported configuration is:

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
- **apk packaging.** All yoe units produce signed `.apk` artifacts. Packages are
  installed with apk-tools at image-assembly time.

This stack runs cleanly on x86_64, arm64, and (with limitations) riscv64. It
boots on QEMU, Raspberry Pi, BeagleBone, and any board where an upstream
mainline kernel + a sane bootloader handle the hardware.

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

## Rootfs-base abstraction (planned)

> **Status:** Not implemented. Yoe today only supports the Alpine/musl/OpenRC
> configuration described in [What yoe ships today](#what-yoe-ships-today). The
> abstraction sketched here is a forward design for serving glibc/systemd
> products (notably Jetson) without forking the project. No code, Starlark
> builtin, project field, or class described below exists in the current
> implementation.

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
  bases. Yoe converts whatever the upstream uses into apks during fetch (see
  [Package format stays apk regardless of base](#package-format-stays-apk-regardless-of-base-planned)
  below). dpkg and apt never run on the target.

What yoe **continues to own regardless of base**:

- Image assembly: partition layout, bootloader install, signing, OTA.
- The DAG and content-addressed cache.
- The dev loop: `yoe build`, `yoe dev`, `yoe deploy`, `yoe run`, `yoe flash`.
- The unit format and the override/composition model.
- The signed apk feed. Every package on every target is a yoe-signed apk,
  regardless of where the bits originally came from.
- The on-target installer (apk-tools, glibc-built or musl-built depending on
  base).
- The TUI and the project orchestration commands.

The bits that vary with the base:

- The toolchain container (`toolchain-musl` for Alpine, `toolchain-glibc-arm64`
  for Jetson, etc.).
- The init system integration (OpenRC scripts vs systemd unit files).
- The `network-config`-style yoe-defining units (would have a systemd-flavored
  variant for systemd bases).
- The conversion class invoked when consuming upstream packages (`alpine_pkg`,
  `deb_pkg`, …).

## Package format stays apk regardless of base (planned)

> **Status:** Forward design. Today only `alpine_pkg` exists, and it consumes
> packages that are already apks — no format conversion is performed. The
> `deb_pkg` class described below is unimplemented; this section captures the
> design that the rootfs-base abstraction is expected to follow when
> Debian-derived bases land.

A core invariant of the rootfs-base abstraction: **the on-target package format
is apk, always.** When yoe consumes packages from an upstream feed that uses a
different format (apt/deb, RPM, …), the conversion happens at fetch time and
produces a yoe-signed apk. The target image runs apk-tools, not dpkg or rpm.

The wins:

- The dev loop, override model, signed feed, DAG, and cache are identical across
  bases. A developer working on an Alpine gateway and a developer working on a
  Jetson box write the same kind of unit, deploy with the same `yoe deploy`, and
  get the same dev experience.
- Yoe's signing key is the only key the target trusts. Upstream signing keys
  (NVIDIA's apt key, Ubuntu's keyring) never need to be installed on the target.
- A single installer toolchain on the target — apk-tools — instead of carrying
  dpkg + apt + their dependencies.

For Debian-derived bases, this implies a `deb_pkg` class symmetric to
`alpine_pkg`. Mechanically: `ar x` the .deb, extract `data.tar.{gz,xz,zst}`,
re-pack the file tree as an apk, translate metadata (`Depends:` → `D:`,
`Provides:` → `p:`, `Replaces:` → `r:`), sign with the project key.

Glibc binaries on a glibc base, systemd unit files on a systemd base, multiarch
paths on a Debian-conventions base — all of this is handled by **the base**, not
by the format conversion. Once libc + init + conventions match what the upstream
package was built for, the binaries inside the package run unchanged regardless
of whether they're delivered as a deb or a yoe-converted apk.

### Residual dpkg-userland concerns

The conversion is mechanically straightforward. The non-trivial part is that
many Debian packages ship maintainer scripts that call dpkg-specific userland
tools — `update-alternatives`, `dpkg-divert`, `debconf` — which exist on
Debian/Ubuntu but not on a yoe target. Each has a bounded mitigation:

1. **`update-alternatives`.** Many Ubuntu packages register `/usr/bin/python` →
   `python3.10`, `/usr/bin/editor` → `vim.basic`, etc. Three viable strategies,
   in order of preference:
   - **Bake at conversion time.** Resolve alternatives statically during deb→apk
     repackaging — pick the priority-winning symlink, embed it as a real symlink
     in the apk's data tree. Stateless, deterministic, works for the common case
     where embedded products don't switch alternatives at runtime.
   - **Ship a tiny `update-alternatives` stub.** A few hundred lines of shell
     that mimics the file format and CLI surface. Required if any package will
     be installed/upgraded post-deploy via `apk add` and its postinst calls
     update-alternatives.
   - **Translate calls during script conversion.** Postinst calls like
     `update-alternatives --install ...` get rewritten to direct `ln -sf` during
     conversion.

2. **`dpkg-divert`.** Used to relocate a file shipped by package A so package B
   can put its own version there. Rare in practice; effectively absent from the
   L4T set. Defer until a package actually needs it.

3. **Triggers.** Debian's file-trigger mechanism (`/etc/ld.so.conf.d/` triggers
   `ldconfig`, `/usr/share/man/` triggers `mandb`, etc.). apk has no equivalent.
   Run `ldconfig` once at end-of-rootfs-assembly; skip mandb / desktop-database
   / icon-cache for embedded images, or run them as a post-image step. None
   affect runtime behaviour.

4. **`debconf` interactive prompts.** Conversion has to pre-answer them.
   NVIDIA's debs are mostly non-interactive; the few that aren't get a
   per-package preseed declared in the unit.

5. **`/var/lib/dpkg/` probes.** Some scripts test for the dpkg database. If it
   matters for a specific package, ship a stub dpkg database (an empty directory
   tree with a `status` file marking everything "installed"). Tiny, one-time
   work in the rootfs base.

6. **License redistribution.** CUDA / cuDNN / TensorRT / DeepStream EULAs allow
   inclusion in shipped product images but generally not public mirroring. Yoe's
   converted apks are fine for a customer's private product feed; they should
   not be hosted on a public mirror. `alpine_pkg` has this concern in principle
   but Alpine is FOSS-dominant; NVIDIA's stack is where it actually bites.

7. **APT mirror semantics.** Apt's repo format (signed `Release` files,
   `Packages.gz`, version constraints with epochs and tildes) is more complex
   than Alpine's flat `APKINDEX`. The conversion class needs to read it
   correctly. Several mature Go libraries handle this; not novel work.

The kernel-module problem (NVIDIA's out-of-tree drivers built against L4T's
specific kernel ABI) is orthogonal to package format — it's a Jetson-target
problem, not a deb-vs-apk problem.

## Base bootstrap

Yoe does not have a "bootstrap" phase in the `debootstrap` sense — there is no
separate first stage that builds a minimum environment before normal package
installation can run. The rootfs assembly is a single procedure that works the
same way today on Alpine and would work the same way on a glibc/systemd base
tomorrow:

1. `mkdir <rootfs>` — the starting rootfs is an empty directory.
2. Create the apk DB skeleton:
   `mkdir -p <rootfs>/lib/apk/db && touch <rootfs>/lib/apk/db/installed`.
3. Drop the project's signing key into `<rootfs>/etc/apk/keys/`.
4. Write `<rootfs>/etc/apk/repositories` pointing at the project's signed feed
   (and any auxiliary feeds the base wants to consume directly, if the project
   opts in).
5. `apk add --root <rootfs> --initdb <package list>` — run from inside the
   toolchain container, against the project's feed.

That is the whole assembly. Everything in the rootfs lands via apks. The first
packages installed (`base-files`, `musl` or `libc6`, the userland shell,
apk-tools, init system) carry the filesystem skeleton — `/etc/passwd`,
`/etc/group`, `/dev`, `/proc` mountpoints, default config files — inside their
data segments.

The only things that have to exist before this loop runs are the **toolchain
container** (provides apk-tools as the orchestrator binary) and the **project's
signed feed** (provides the apks to install).

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

**Option A: From-apks (purist, fully reproducible).** Every package, including
the essentials, comes from a yoe-built or conversion-class-wrapped apk in the
project's feed. The starting rootfs is empty; yoe owns the entire chain. For a
glibc/systemd base, this means wrapping `libc6`, `libstdc++6`, `systemd`,
`bash`, etc. as `deb_pkg` units. More setup work, total reproducibility.

**Option B: From-tarball (pragmatic, vendor-blessed).** The project's `base()`
declaration points at a vendor-supplied rootfs tarball — NVIDIA's official L4T
sample rootfs for Jetson, `ubuntu-base-<version>.tar.gz` for generic Ubuntu, or
`alpine-minirootfs-<version>.tar.gz` for an Alpine shortcut. Yoe extracts the
tarball as the starting rootfs, then runs `apk add --root` to overlay
yoe-installed apks on top. apk-tools installs into a non-empty rootfs without
conflict — it owns its own DB and ignores files it didn't put there, except
where its package contents collide. Faster to set up because the wrapping work
for "every essential package" is replaced by trusting the tarball. Less
reproducible because the tarball is a black box.

For Jetson, most projects will pick Option B — NVIDIA tests the sample rootfs
and supports it as the basis of L4T. Option A is the right answer when every
byte must be audited, when no vendor tarball exists, or when a project wants the
same provenance story across bases.

### Why an empty starting rootfs works for any libc

A common confusion: if running glibc binaries requires glibc to be present, how
does an empty rootfs get glibc onto itself?

apk-tools at install time is a **file extractor**, not an executor. It reads
each apk's data tar and writes the files to the target rootfs; nothing ever
calls into the binaries it's installing. The apk-tools process doing the work
runs in the toolchain container, where its own libc is whatever the container
provides — musl today, glibc on a glibc-based toolchain container later. When
apk-tools extracts the `libc6` package's data tar into the target rootfs, it
places `/lib/aarch64-linux-gnu/libc.so.6` on disk; nothing tries to dlopen it
until the rootfs actually boots.

So the toolchain container's libc and the target rootfs's libc are independent.
A Jetson target rootfs (glibc) can be assembled from a toolchain container
that's still musl-based, and a yoe-built `apk-tools-glibc` unit can land on the
target as just another package alongside `libc6`, ready to run on first boot.

The same principle is why on-target `apk add` after deployment works identically
across bases: by then the rootfs has its own `apk-tools` binary linked against
its own libc, and the install loop is just "extract files, update DB."

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

## Practical roadmap (planned)

> **Status:** Forward design, not a commitment. The current focus remains
> finishing the Alpine/musl path described in
> [What yoe ships today](#what-yoe-ships-today) and
> [module-alpine.md](module-alpine.md). The phases below describe the
> approximate order in which the rootfs-base abstraction would be built,
> conditional on demand.

1. **Solidify the Alpine path.** Ship enough that yoe is a viable choice for
   non-AI embedded products today. The same architecture carries forward; this
   is the foundation that proves the dev-loop and image-assembly value before a
   second base is introduced.

2. **Identify the Alpine-coupled seams.** Survey `module-core` and the internal
   Go code for assumptions that won't survive a non-Alpine base: hardcoded
   apk-tool invocations, OpenRC-flavored init paths, busybox-shadow logic in
   `replaces`, the toolchain container's musl-only Dockerfile. Make these
   pluggable but defer the rewrite.

3. **`deb_pkg` class.** Symmetric to `alpine_pkg`: fetch a `.deb`, extract
   `data.tar.{gz,xz,zst}`, repack as a yoe apk with translated metadata, sign
   with the project key. Resolve `update-alternatives` calls statically at
   conversion time. Treat the rest of the dpkg-userland concerns
   ([Residual dpkg-userland concerns](#residual-dpkg-userland-concerns)) as they
   come up, per-package, in priority order.

4. **First Jetson prototype.** Pick a single Jetson SKU (Orin Nano dev kit is
   cheapest), get a yoe-assembled image booting with CUDA working end-to-end.
   Treat it as a learning project — the goal is to discover what abstraction
   breaks, not to ship Jetson support. Likely outputs: a `toolchain-glibc-arm64`
   container, a `ubuntu_l4t` rootfs base implementation that uses `deb_pkg` to
   consume NVIDIA's apt feed, a systemd-flavored `network-config`, glibc
   apk-tools on the target.

5. **Promote the abstraction.** With one working Jetson example, generalize the
   project base configuration so the same yoe codebase serves both Alpine and
   Jetson cleanly. The `deb_pkg` class earns its keep by being reused across
   Ubuntu generic, Debian, L4T, and any future Debian-derived base.

6. **Second base, third base.** Once the abstraction is proven on two distinct
   bases, additional bases (Ubuntu generic, Adelie's glibc/musl mix, Yocto
   layers, custom rootfs tarballs) become incremental wraps rather than
   redesigns.

## Decision rubric

Until the rootfs-base abstraction lands, yoe should refuse to chase
glibc/systemd compatibility through hacks (gcompat shims, dual-libc images,
OpenRC-emulating-systemd compatibility layers). These produce brittle systems
that look like they work and then fail at the worst moment. The right answer for
a glibc/systemd target today is "yoe is not the right tool yet" — say it
explicitly and revisit when the abstraction is real.

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

Today: musl + OpenRC + Alpine, serving non-AI embedded well.

Tomorrow (planned): rootfs-base-agnostic, where each project picks the
foundation appropriate to its hardware and product. Same yoe experience over
Alpine for gateways and over Ubuntu/L4T for Jetson.

Not on the menu: trying to make musl/OpenRC pretend to be glibc/systemd, or
trying to make yoe pretend to be a single-base distribution like Alpine itself.
Those are projects that have already been tried and have not aged well.
