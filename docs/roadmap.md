# Roadmap

> **About this document:** the roadmap is a list of pointers, not a design spec.
> Each item should be a one-line "we want to do this" with a link to the design
> doc that owns the detail. Keep design discussion in the relevant `docs/*.md`
> and link from here. If a topic doesn't have a design doc yet, leave the entry
> brief — write the design doc when the work is actually picked up.

## Next

- Weight units by how much time they take to build for displaying accurate build
  progress
- Why does simpleiot list musl as dep?
- Pin modules from dev src
- Should we pin modules in default projects? Seems like we should
- Progress bar for build and build status, units complete/total, % done
- Search completion can we display a list vertical under search bar?
- Warn if units specify Git branches. These are not deterministic.
- Use alpine docker-init. Had some problems with consuming packages with
  multiple outputs.
- Is there any advantage in sources to storing filename as a hash? What is this
  a hash of?
- On unit detail page, provide a way to switch the Git URL to the upstream
  source and record this in the unit build state file. Display this state on
  main unit and unit detail page. Thinking several states: nothing, up,
  modified, etc.
- Can we watch the unit state files and automatically update the TUI if
  something changes? The idea is you could be building in two different TUIs and
  both TUIs would show current status of the other.
- alpine should have unit deps, not just runtime deps
- alpine packages like gvim provides vim. This could be a source of pain.
- document BSP and package moat
- mDNS on target (we have a mdns component, why is it not working?)
- base-files is modified by machine
  - machine package feed?
  - this needs to be solve before start building multiple machines in one tree.
- e2e testing
- Data partition for rPI targets
  - Fill/format data partition
- rPI updater
- Error reading OS version: searching /etc/os-release, got: field VERSION not
  found
- Parallel build
- Flash progress bar rewinds before display if there has been a previous flash
- Multiple projects
  - add example to e2e
  - Support selecting and saving to local.star
- Open to unit source on web.

## Bugs / Improvements

- `apk help` — hard to use right now.
- Helix prebuilt is glibc-only and won't run on yoe's musl rootfs. Needs a
  cargo-from-source build (or a third-party musl tarball) to actually work.
- modprobe from busybox and kmod both in image at different locations.
- kmod: `Error loading shared library liblzma.so.5: No such file or directory`
  (needed by `/usr/sbin/modprobe`).
- Rename rpi machines to simple rpi names.

## Developer Experience

The biggest leverage area: making yoe pleasant for the developer writing apps
that run on yoe-built devices, not just for the author of a distro.

- Plugins to create custom commands and TUI features
  - Need to make it easy to extend the automation for custom needs.

### Source can directly embed units

- star file directly in source code
- declares dependencies (modules, containers)
- can be directly included in a PROJECT.star

This allows yoe to be an application build tool as well as a system build tool.

### Build & Deploy Loop

Goal: app developers work directly in their app's git repo, not against an
extracted SDK. The build container _is_ the SDK. See [dev-env.md](dev-env.md)
for the design.

- Local-path unit sources: `source = path("./")` so a unit builds from a working
  tree without a clone-tag cycle. Foundation for everything below.
- `yoe dev` watch mode — rebuild (and optionally redeploy) on save.
- Language and build-system classes beyond `go_binary`: `rust_binary` (Cargo),
  `python_unit`, `node_unit`, `meson`, `zig_binary`. See the class table in
  [metadata-format.md](metadata-format.md#built-in-classes).
- App project scaffolding: `yoe new app --lang go` style generator that creates
  a standalone project with `PROJECT.star`, a unit pinning the language, and a
  happy path.
- Software update — Yoe updater or SWUpdate. Rewrite in Zig?
- Anything we can learn from https://docs.ruuda.nl/deptool/?

### On-Device App UX

- `yoe svc start|stop|restart|status <unit> <host>` over SSH.
- `yoe logs <unit> -f` — tail service logs from the host.
- Persistent `/data` partition pattern so app state survives image updates.
- Health-check / watchdog conventions readable by both OpenRC and a future
  container runtime.

### Diagnostics

- Profilers: `perf`, `bpftrace`, language-specific (`py-spy`, `delve`).
- Metrics agent: `node_exporter` or similar.
- Crash backtrace shipper: capture coredumps to a known path, optionally upload.

### Wireless / Remote

- Wifi setup workflow: `wpa_supplicant` unit + a first-boot configurator.
- Reverse tunnel for remote dev: `yoe tunnel`, or ship `tailscale` /
  `headscale`.

## Hardware Access

- GPIO / I²C / SPI userspace: `libgpiod`, smbus userspace tools.
- Audio: ALSA, PipeWire.
- Camera: `libcamera`.
- GUI stack: minimal Wayland compositor (cage / wlroots) for kiosk apps.

## Needed Units

Existing units can be found via `yoe list` or by browsing
`modules/units-core/units/`.

### Networking and Security

- `nftables` — modern firewall (preferred over legacy iptables). Requires new
  dep units `libmnl`, `libnftnl`, and `gmp` before it can be written.
- `wpa_supplicant` — wifi.

### Diagnostics

- `perf`, `bpftrace`, `py-spy`, `delve`.
- `node_exporter` (or similar metrics agent).

### Hardware

- `libgpiod`, smbus userspace tools.
- ALSA, PipeWire.
- `libcamera`.

### Container Stack

- `runc`, `containerd`, `nerdctl` — first milestone for on-device containers.
- Follow-on: `podman`, then `docker-ce`.

### Nice to Have

- `dbus` — IPC message bus; dependency for many higher-level services. Pulls in
  expat (already present) plus a service supervisor — non-trivial, defer until a
  unit needs it.
- `ripgrep`, `fd`.
- `tailscale` (or `headscale`) — remote-dev tunnel.

## Container Host on Devices

Ship a `container-host-image` that runs containerd (later Podman, then Docker
CE) on yoe-built devices. Design and reference architecture in
[containers.md](containers.md).

`docker-image` already ships the Docker userspace (engine, CLI, buildx,
containerd, runc) alongside dev-image content. The remaining gating work is the
`kernel-container-host.cfg` fragment (overlayfs, bridge/veth, netfilter, cgroup
v2, seccomp, namespaces) and an init system that supervises `dockerd`.

## Image Assembly on Host

Move image assembly (`mkfs.ext4`, bootloader install) from the build container
to the host via `bwrap` user namespaces. Design in
[build-environment.md](build-environment.md#reducing-dependence-on-dockers-dev-planned).

## Auto-depend from ELF DT_NEEDED

Counterpart to the auto-`provides` SONAME scan that already runs in
`internal/artifact/apk.go`. Walk every executable and shared library in the
unit's destdir, read each binary's `DT_NEEDED` entries, and emit
`depend = so:<soname>` lines in PKGINFO — same convention Alpine's abuild uses.
Skip sonames the unit provides itself, plus a small platform-baseline allowlist
(`libc.musl-*.so.1` from `musl`, `ld-musl-*.so.1`, etc.) that's guaranteed
present in any yoe rootfs.

Catches the class of bug where a `.star` declares a `runtime_deps` list that
silently misses a transitive shared-lib dependency: today the unit installs fine
but fails at runtime; with auto-depend, apk refuses the install with a clear
`so:libfoo.so.N (no such package)` message.

## `module-alpine` units as deltas over upstream PKGINFO

Today every cached `alpine_pkg` unit duplicates upstream metadata
(`runtime_deps`, `provides`, `replaces`, …) inline in the `.star`, and yoe's apk
pipeline regenerates PKGINFO from those declarations — silently dropping fields
the generator missed (e.g. `replaces = busybox` on openrc). Now that
`alpine_pkg` re-signs upstream apks instead of rebuilding them, the on-target
PKGINFO comes from upstream verbatim. Next step: turn the `.star` fields into
explicit deltas over that upstream metadata so cached units stay tiny and only
record yoe-specific changes. Proposed shape:

```
provides_extra      / provides_drop      / provides_override
replaces_extra      / replaces_drop      / replaces_override
runtime_deps_extra  / runtime_deps_drop  / runtime_deps_override
triggers_extra      / triggers_drop      / triggers_override
```

`_extra` adds, `_drop` removes, `_override` replaces wholesale. 90% of edits
will be `_extra` / `_drop`; `_override` is the escape hatch. Plain
`runtime_deps` / `provides` / `replaces` (no suffix) stay reserved for
source-built `module-core` units where there's no upstream to merge with.

## Testing

Today: Go unit tests under `internal/*` and a single dry-run e2e test. No
on-device tests, no image smoke tests, no build-time package QA, no CI workflow
that runs builds. Design and intended shape in [testing.md](testing.md), which
also compares to Yocto's `oeqa` / `INSANE.bbclass` / `ptest` / `buildhistory`.

- Build-time package QA (Yocto's `INSANE.bbclass` analog): file ownership, ELF
  stripping, RPATH leaks, missing SONAMEs, host-path contamination. Always-on;
  failures fail the build.
- `yoe test <unit>` — drive per-unit, image, and HIL tests behind one command.
- Per-unit functional tests (destdir assertions in the build sandbox).
- On-device upstream tests (`make check` / `cargo test` shipped as a test
  subpackage; Yocto's `ptest` analog).
- Image-level smoke tests that boot in QEMU (or attach over SSH to a real
  device) and check network, services, basic flows.
- Build-history / regression tracking (Yocto's `buildhistory` analog) for size,
  RDEPENDS, and file-list diffs per PR.
- CI workflows: `go test`, dry-run image build per PR; full build + smoke tests
  on a schedule.
- Kernel QA: run upstream `check-config.sh` against the kernel `.config` for
  container-host images.

## A/B Updates

Read-only rootfs with A/B partitions and signed update bundles. Reference
architecture (Home Assistant OS) in
[containers.md](containers.md#reference-point-home-assistant-os). The Software
update item under Developer Experience evolves toward this once a runtime ships.

## CLI Surface

- `yoe serve` / `yoe deploy <unit> <host>` / `yoe device repo {add,remove,list}`
  — shipped. See [feed-server.md](feed-server.md).
- `yoe svc start|stop|restart|status <unit> <host>`.
- `yoe logs <unit> -f`.
- `yoe dev <unit>` — watch the source tree and rebuild (optionally redeploy) on
  save.
- `yoe test <unit>` — run tests in QEMU or against a real device. See
  [testing.md](testing.md).
- `yoe tunnel` — reverse tunnel for remote dev (or rely on a `tailscale` unit).
- `yoe new app --lang go` — application project scaffolding.
- `yoe cache` — query and prune the build cache (local + future remote/S3).
- `yoe shell` — drop into the build container interactively.
- `yoe bundle` — package modules into a single distributable.
- `yoe module list|info|check-updates` — inspect and update external modules.
- `yoe repo push|pull` — sync the local apk repo to a remote (S3 / HTTP).
- `yoe build` query flags: `--class <type>`, `--with-deps`, `--list-targets`,
  `--no-remote-cache`.
- Config propagation across modules.

See [yoe-tool.md](yoe-tool.md) for design notes on existing `(planned)`
sections.

## Format / Modules

- Sub-packages — one unit producing multiple `.apks`.
- `MODULE.star` manifests for module versioning and inter-module deps.
- Per-task container overrides.
- Track the Starlark class function used to define each unit on the resolved
  `Unit` (e.g., `Unit.BuiltVia = "autotools"`, `"cmake"`, `"alpine_pkg"`,
  `"go_binary"`). Today `Unit.Class` only carries the unit's _type_ (`image` /
  `container` / `unit`); the build-pattern function that wrapped the `unit()`
  call leaves no fingerprint on the resolved data. With a separate field, the
  TUI query language (and `yoe build` flags) can distinguish `type:autotools` —
  meaningless today — from `type:image`, and we can answer questions like "what
  alpine_pkg units are in this image" without scraping `.star` files.

See [metadata-format.md](metadata-format.md).

## Distribution Variants

- **glibc target.** Currently musl-only. glibc support would enable workloads
  whose binaries require it (some cgo, prebuilt vendor SDKs, the upstream Helix
  release, etc.).

## Self-Hosting

The ultimate dogfood test: develop yoe on a yoe-built device. Forces the distro
to be capable enough for real engineering work, not just demo targets, and
surfaces gaps in container hosting, editor experience, and the build cache all
at once.

Compilers stay in the build containers (gcc, binutils, headers, language
toolchains live in `toolchain-musl` and friends, not the rootfs). What the
device itself needs:

- **Container host on the device** so it can run the build containers. See
  [Container Host on Devices](#container-host-on-devices).
- **`yoe` binary in the project's apk repo** so a yoe-built device can
  `apk add yoe` like any other unit.
- **Go on-device** for editing yoe source comfortably (`gopls`, `delve`), not
  for the build itself.
- **`git`** unit.
- **An editor that runs on musl.** Fix the helix glibc issue (cargo-from-source
  build) or commit to neovim as the default.
- **CI gate** that builds yoe from source on a yoe-built image and runs the test
  suite, so toolchain or libc-compatibility regressions break the build instead
  of being discovered later.
