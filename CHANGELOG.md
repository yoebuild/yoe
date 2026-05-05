# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

- **Images with `network-config` and busybox build again.** A path collision
  on `/usr/share/udhcpc/default.script` (busybox ships an example script
  there; network-config installs the real one) was aborting `apk add` at
  image-assembly time.

## [0.10.1] - 2026-05-05

- **TUI flash offers `sudo chown` on permission denied.** Previously the flash
  view just showed "permission denied" and dead-ended — matching the CLI's
  behavior, the TUI now prompts to run `sudo chown $USER /dev/...` and retries
  the write automatically.
- **TUI home screen has tabs.** Press `tab` to cycle between Units (the existing
  list), Modules (declared modules with git status), and Diagnostics (shadowed
  units and duplicate `provides`). The diagnostics tab carries a count badge so
  issues are visible from any tab.
- **`--allow-duplicate-provides` is on by default.** No more passing the flag on
  every yoe invocation while the `linux-firmware-*` fan-out keeps tripping the
  strict check.
- **Modules renamed: `units-*` → `module-*`.** `units-core`, `units-rpi`,
  `units-alpine`, and `units-jetson` are now `module-core`, `module-rpi`,
  `module-alpine`, and `module-jetson`. Update `module(...)` URLs and any
  `path = "modules/units-..."` entries in your `PROJECT.star`.
- **`helix` actually runs on the device.** Was previously bundled as a
  glibc-linked binary that failed silently with `hx: not found`; now uses
  Alpine's musl build.
- **Images that include `apk-tools` or `libcurl` build again.** A collision
  between the source-built `ca-certificates` and Alpine's
  `ca-certificates-bundle` was aborting `apk add` at image-assembly time.
- **`SIZE` column in the TUI updates as each unit finishes.** No more waiting
  for the whole image to complete before transitive deps show their size, and
  partial sizes survive a mid-build failure.
- **Modules show their declared name.** The TUI's `MODULE` column and any
  diagnostic that names a module now use the name set in `MODULE.star`'s
  `module_info(name = ...)` instead of the path basename — so a module
  referenced via `path = "modules/units-core"` displays as `core` if that's what
  it calls itself. Falls back to the path basename when no `module_info` is
  declared.
- **`dev-image` ships `helix` instead of `vim`.** Drops the editor entry that
  was unintentionally resolving to Alpine's `gvim` (and its X11/GTK runtime
  closure), keeping the image lean.
- **Unit detail shows what uses it and what it pulls in.** The detail page now
  opens with two new sections above the build log: **USED BY** traces back
  through runtime_deps to show which packages you wrote in `image()` pulled this
  unit in, e.g. `dev-image → yazi → libpango → cairo`, so you can answer "why is
  this on my device?" at a glance. **PULLS IN** shows the unit's runtime-deps as
  a tree. Drilling into an image starts from exactly the packages you wrote in
  `image()` (plus machine packages), then expands each one to show what it drags
  in transitively.
- **TUI layout overhaul.** Title and banners stay put when the list is long, the
  help bar is always the last line, status messages flash in its place, and
  pressing `/` turns the `Query:` header itself into the search input. Long unit
  names get an ellipsis instead of breaking column alignment.
- **Sort columns from the keyboard.** Press `o` to cycle the unit table through
  `NAME → CLASS → MODULE → SIZE → DEPS → STATUS`; `O` flips direction. The
  active column shows `↑` or `↓` next to its label.
- **Help bar highlights shortcut keys.** Each shortcut letter renders in amber
  matching `[yoe]`, so you can scan keys without reading every word.
- **Cursor follows the work.** The TUI opens with the cursor on the default
  image, jumps to whatever unit is currently building, and the cursor's full
  unit name is always visible just above the help bar.
- **Configure the default image per developer.** `local.star` accepts
  `image = "..."` to override `defaults.image`. Pick an image from the new
  **Image** entry in Setup (`s`) and the choice is saved — and the active search
  re-anchors to `in:<image>`. Flows through `yoe run`, `yoe config show`, and
  the TUI.
- **More columns in the unit table.** Each row now also shows the module that
  owns the unit (after shadow resolution), its install size after build (`.img`
  size for images), and how many units it pulls into a runtime closure — so
  bloat is easy to spot before flashing.
- **`yoe --help` works and lists global options.** `--help`, `-h`, and `help`
  all print usage, including `--project`, `--show-shadows`, and
  `--allow-duplicate-provides`.

## [0.10.0] - 2026-05-05

_Errata: due to an issue in the alpine module, you must currently run with:
`yoe --allow-duplicate-provides`._

- **BREAKING CHANGE** This project has been moved to a new Github org:
  https://github.com/yoebuild. `yoe update` from previous versions will not work
  and you will need to download and manually install the 0.10.0 binary.
- **TUI search is now a query language; defaults to your image's working set.**
  Press `/` to filter by `type:`, `module:`, `status:`, or `in:` (closure of any
  unit), in addition to plain substring search. `Tab` completes field names and
  values. The TUI starts filtered to `in:<your-default-image>`, so a project
  with thousands of units shows just what your image needs. Press `S` to save
  the current query to `local.star` as the new default; press `\` to snap back
  to it. The header shows `Query: …  Units: N/M` so you always know how many of
  the project's units the current filter is showing.
- **Use `apk-tools` from alpine layer for now.** It is built with docs.
- **`yoe repo clean` drops stale `.apk` files.** Removes any `.apk` in the
  project's local repo whose name+version no longer matches a current unit (unit
  deleted, version bumped, release suffix changed) and re-signs the regenerated
  APKINDEX. Without this, `apk add` happily picks the highest- versioned
  candidate even when that candidate is leftover from a since-deleted unit —
  which is how a `LUA=no`-built `apk-tools` ("apk has been built without help")
  could keep winning over Alpine's prebuilt long after the source unit was
  removed.
- **Source-built `openssl` no longer collides with Alpine's `libcrypto3` /
  `libssl3`.** The `openssl` unit in `units-core` now declares
  `provides = ["libcrypto3", "libssl3"]`, so any package whose `runtime_deps`
  reach `libcrypto3` or `libssl3` (e.g. `units-alpine`'s `apk-tools`) routes
  back to the source-built openssl instead of pulling Alpine's split libcrypto3
  /libssl3 packages alongside. Without this, image-time `apk add` aborted with
  `trying to overwrite usr/lib/libcrypto.so.3 owned by openssl-3.4.1-r0`.
- **`units-alpine` now lives in its own repo.** `yoe init` and the e2e project
  pull `units-alpine` and `units-jetson` from `github.com/yoebuild/` instead of
  carrying units-alpine inside this repo. Existing projects with
  `path = "modules/units-alpine"` should switch to a remote `module(...)` ref.
- **Shadow notices are off by default.** Cross-module unit shadowing and
  `provides` overrides no longer print a stderr notice on every load. Pass
  `--show-shadows` to see them when you actually want to audit which module won.
- **`--allow-duplicate-provides` lets multiple units share a virtual.** When
  set, units in the same module may declare the same `provides` (apk-style "any
  of these satisfies"); the first one wins for `PROVIDES` lookup. Needed for
  `units-alpine`'s `linux-firmware-*` fan-out, where ~100 packages all provide
  `linux-firmware-any`.
- **`patches=` resolves relative to the unit's own .star file directory.** A
  unit can now ship its patches alongside its definition (e.g.
  `units/bsp/foo/patches/0001-fix.patch` next to `units/bsp/foo.star`), and the
  same `patches=["patches/foo/0001-fix.patch"]` works whether the unit is loaded
  from a local module override or a fetched remote module. Previously patches
  were resolved against the project root, which meant module-shipped patches
  couldn't be found unless every consumer copied them.

## [0.9.1] - 2026-05-01

- **`yoe deploy <unit>` now installs the package's runtime deps too.**
  Previously it only built and published the named unit, so deploying a package
  with `runtime_deps` outside what the device already had on disk failed with a
  cryptic `apk add` error like `sqlite (no such package)`. Deploy now walks the
  full runtime closure (the same expansion `image()` does at image-build time),
  so every transitive dep ends up in the feed before `apk add` runs.
- **Deploy refreshes the device's apk index every time.** The on-device
  `apk update` step now uses `apk --no-cache update`, forcing a refetch of every
  repo's `APKINDEX` instead of trusting whatever is in `/var/cache/apk/`.
  apk-tools 2.x can otherwise hold onto a stale index across a yoe-dev rebuild
  and silently miss packages you just published.
- **Added sqlite unit**

## [0.9.0] - 2026-05-01

- **New design doc on libc and init choice.** `docs/libc-and-init.md` lays out
  why yoe is musl + OpenRC + Alpine today, where that stack works (gateways,
  IoT, networking gear), where it doesn't (Jetson, vendor BSPs, Adaptive
  AUTOSAR), and the planned rootfs-base abstraction that would let a single yoe
  codebase serve both Alpine and Ubuntu/L4T projects. Establishes the invariant
  that yoe stays apk-native on every target — Debian-derived bases get a
  `deb_pkg` conversion class, not dpkg/apt on the device.
- **Pull packages straight from Alpine.** A new `units-alpine` module wraps
  prebuilt Alpine `.apk` files as yoe units via the `alpine_pkg()` class — no
  source build, no patches, just fetch + verify + repack. `musl` and
  `sqlite-libs` ship today; add more by pinning a version and sha256.
- **`musl` now comes from Alpine.** The hand-rolled musl unit that copied the
  dynamic linker out of the build container is gone; `musl` is now an Alpine apk
  wrapped by `alpine_pkg()`. Output is byte-identical to the Alpine package
  other projects already ship.
- **`.apk` URLs work as a source type.** Yoe's source workspace now recognises
  `.apk` extensions and bare-copies them so the unit's install task can extract
  the multi-stream gzip with GNU tar. Bare-copied sources also keep their URL
  filename, so install steps can reference the file by name instead of by cache
  hash.
- **Override an upstream unit by name.** Define a unit with the same name in a
  higher-priority module (or in the project itself) and it shadows the upstream
  one — no `provides` boilerplate needed. The project root beats every module,
  and later modules beat earlier ones. A notice on stderr tells you which one
  won.
- **Deploy from the TUI.** Press `D` on a non-image unit to deploy it to a
  running yoe device — host prompt is pre-filled from the last-used target,
  build + ssh + apk add output stream into the view, and the host is saved back
  to `local.star` on success.
- **Deploy actually updates the device's apk index.** `yoe deploy` and
  `yoe device repo add` previously wrote to
  `/etc/apk/repositories.d/yoe-dev.list`, which apk-tools 2.x ignores. They now
  append a marker block to `/etc/apk/repositories` so the next `apk update`
  actually fetches the dev feed and `apk add <unit>` finds the freshly built
  package.
- **TUI starts a feed automatically.** When you launch `yoe`, it brings up the
  project's apk feed (or reuses one already running on the LAN), so devices
  configured with `yoe device repo add` can pull packages without any extra
  setup. Status is shown in the header.
- **SSH target shorthand.** `yoe deploy` and `yoe device repo {add,remove,list}`
  accept `[user@]host[:port]` — e.g. `yoe device repo add localhost:2222` for a
  QEMU vm or `yoe deploy myapp pi@dev-pi.local:2200`. The `--ssh-port` flag is
  gone.
- **APK live deployment tooling.** `yoe deploy <unit> <host>` builds and
  installs a unit on a running yoe device with full apk dependency resolution.
  Pair with `yoe serve` and `yoe device repo add` to keep a device pointed at
  your dev feed for ad-hoc `apk add` from the device. See
  [docs/feed-server.md](docs/feed-server.md).

## [0.8.6] - 2026-04-30

- **Container runtime build path documented.**
  [docs/containers.md](docs/containers.md) now walks through what it takes to
  ship Docker, containerd, and runc on a musl yoe rootfs — why prebuilt "static"
  binaries don't work, the per-component build breakdown, and how cgo units like
  runc plug into yoe's existing Go toolchain and `toolchain-musl` container via
  `deps` instead of needing a new Go+GCC container image.
- **Rename `debug` units to `dev`.**
- **Expand [roadmap](docs/roadmap.md).** Reorganized as a pointer index into the
  design docs, with new sections for the app-developer build/deploy loop,
  hardware access, testing, self-hosting, and distribution variants.
- **New testing design doc** at [docs/testing.md](docs/testing.md) covers the
  planned `yoe test` driver, build-time package QA, on-device upstream tests
  (Yocto `ptest` analog), image smoke tests, and CI integration.
- **Kernel modules now ship in images** — the `linux`, `linux-rpi4`, and
  `linux-rpi5` units previously built only the in-tree kernel image, so drivers
  compiled as loadable modules (Wi-Fi, USB, sound, many filesystems) were
  silently dropped. Modules are now built and installed to
  `/lib/modules/<kver>/` in the rootfs, so `modprobe` finds them at runtime.
- **Fix rPI4 builds** package arch did not match what apk was expecting.

## [0.8.5] - 2026-04-30

- **`Yazi, Zellij, and Go units added.**
- **Clear error when an image's rootfs won't fit the partition.** Yoe points at
  the partition size to bump instead of failing mid-`mkfs.ext4` with a cryptic
  ext2 error.
- **SSH works out of the box on `dev-image`.** `sshd` starts on boot with
  per-device host keys; `ssh -p 2222 user@localhost` (password `password`) just
  works, and passwordless root SSH matches the serial console.
- **Image rebuilds recover from prior failed builds.** A previous failure no
  longer wedges the next run on "Permission denied" — yoe reports the real error
  and cleans up automatically.
- **New `binary` class for prebuilt binaries.** Units can ship upstream release
  binaries with SHA256 verification, no rebuild from source. Used by `go`,
  `helix`, and `yazi`.
- **`apk add` works against the signed repo.** Image-time and on-target `apk`
  commands no longer fail with "BAD signature" or need `--allow-untrusted` /
  `--keys-dir`.
- **`apk add` and `apk upgrade` work on yoe-built devices.** `dev-image` ships
  `apk-tools` and the project's signing key, so OTA-style updates use stock
  `apk` commands. See `docs/on-device-apk.md`.
- **Signed apks and APKINDEX.** Every artifact is RSA-signed at build time and
  verified by stock `apk` on the target. `yoe key generate` / `yoe key info`
  manage the project key; see `docs/signing.md`.
- **Rootfs builds with APK**. Much faster.
- **`provides` is now a list.** Use `provides = ["a", "b"]`; the string form
  `provides = "x"` no longer parses.
- **`replaces` is documented.** New "Shadow files" section in
  `docs/naming-and-resolution.md` covers when to use it and how to read apk's
  "trying to overwrite" errors.
- **"One .apk per unit" principle, documented.** Image-to-image variation
  belongs at runtime, not in build-flag forks. See
  `docs/naming-and-resolution.md`.
- **SSH configured to autostart and work with blank passwords for dev builds.**

## [0.8.4] - 2026-04-29

- **Networking picks the better DHCP client when available.** The default
  `S10network` runs `dhcpcd` if it's on `PATH` (IPv6 SLAAC, DHCPv6, IPv4LL
  fallback) and falls back to busybox `udhcpc` otherwise — so an image that
  ships `dhcpcd` gets the modern client without changing the init script.
- **File conflicts in image builds now fail loudly.** Units can declare
  `replaces = ["pkg", ...]` to opt into shadowing another package's files (e.g.
  `util-linux` over busybox's `/bin/dmesg`); apk honors that at install time and
  rejects any conflict that wasn't declared. Image assembly no longer passes
  `--force-overwrite`, so a new shadow becomes a real error instead of a buried
  warning.
- **Unit edits no longer get masked by stale cache hits.** Editing a unit's
  description, license, runtime deps, replaces, conffiles, build environment,
  scope, image partitions, image excludes, or install-step files now invalidates
  the cache as it should — previously these silently kept the old apk. A new
  test in `internal/resolve` fails if a future Unit field is added without being
  incorporated into the cache key.
- **`ip` works again on `dev-image`.** iproute2 no longer pulls in libelf at
  link time, so `/sbin/ip` runs without "Error relocating /sbin/ip: elf_getdata:
  symbol not found" on images that don't ship elfutils.
- **Boot no longer hangs when DHCP fails.** The default network init script
  waits briefly for the link to come up before starting udhcpc, runs udhcpc in
  the background, and limits its retries — so `dev-image` reaches a login shell
  even when no DHCP server is reachable, instead of looping on "Network is
  down".
- **Image rootfs is assembled by upstream `apk add`.** yoe no longer loops
  `tar xzf` over each apk; image builds run `apk add` against the project's
  local repo, getting real dependency resolution, file-conflict detection, and
  an installed-package database in `/lib/apk/db` for free. On-target you can now
  `apk info`, `apk verify`, and (once apk-tools ships as a unit) `apk add` and
  `apk upgrade` against the same repo.
- **Service symlinks ship inside the apk.** A unit's `services = [...]`
  declaration is materialized as real `/etc/init.d/SXX<name>` symlinks inside
  the package's data tar at build time. On-target `apk add <pkg>` produces the
  same rootfs as image-time assembly — yoe never patches the rootfs after
  install.
- **Repo layout switched to Alpine-native** —
  `repo/<project>/<arch>/<pkg>-<ver>-r<N>.apk` plus a per-arch
  `APKINDEX.tar.gz`. `.apk` filenames no longer carry a scope suffix. Existing
  `repo/` directories are obsolete; the next build repopulates the new layout.
- **Yoe-built apks install with upstream Alpine apk-tools.** `.apk` files and
  `APKINDEX` produced by yoe now round-trip through stock
  `apk add --allow-untrusted`: no checksum errors, no format warnings, and
  package metadata (name, version, arch, deps, origin, commit, install size)
  matches what `apk index` itself would emit.
- **Nine new units in `dev-image`** — `e2fsprogs` (mkfs.ext4 / fsck.ext4 /
  tune2fs on the target), `eudev` (full udev for dynamic /dev), `iproute2` (full
  `ip`/`tc`), `dhcpcd` (a DHCP client beyond busybox udhcpc), `bash`, `less`,
  `file`, `procps-ng` (real `ps`/`top`/`free`/`vmstat`), and `htop` are now
  built and included in `dev-image` so they're available out of the box on a
  booted dev system. `gperf` is also added as a build-time dependency for eudev.
- **Updated units roadmap** — `util-linux`, `kmod`, and `ca-certificates` are
  marked done; `dropbear` is dropped (the project standardizes on `openssh`);
  remaining work is now `nftables` (blocked on libmnl/libnftnl/gmp deps) and
  `dbus`.
- **Documented when NOT to use `provides`** — `docs/naming-and-resolution.md`
  now spells out that `provides` is for leaf artifacts only (kernel, base-files,
  init, bootloader). Using it for build-time libraries or runtime alternatives
  forks every transitive consumer into a per-machine apk. Runtime alternatives
  like `mdev` vs `eudev` should ship side-by-side and be selected at boot from
  init scripts.
- **Image rootfs assembly now warns on path collisions** — when two packages
  install to the same path (e.g., busybox's `/sbin/ip` symlink vs iproute2's
  full binary), the later package silently overwrote the earlier one with no
  trace. Image assembly now emits a `warning:` line per collision naming the
  surviving package and the shadowed ones, plus a total count. The warnings
  appear in the image's `build.log` (and on terminal when `yoe build -v` is
  used). Existing dev-image builds surface 27 expected shadows of busybox
  applets by full alternatives — no behavior change, just visibility.

## [0.8.3] - 2026-04-28

- **mDNS via new `mdnsd` unit** — the dev-image now answers `<hostname>.local`
  on the LAN, so `ssh user@yoe-dev.local` works without knowing the device's IP.
  Uses troglobit/mdnsd (a small dbus-free mDNS responder) and ships a default
  `_ssh._tcp` service record so the host A record is advertised and SSH
  discovery works for Bonjour-aware tools.
- **NTP at boot via new `ntp-client` unit** — boards without a battery- backed
  RTC (e.g., Raspberry Pi) booted at 1970, which broke TLS with "certificate is
  not yet valid". `ntp-client` does a blocking initial sync at S20 (retried a
  few times to cover DNS settling right after udhcpc) so subsequent services
  start with real time, then leaves a busybox `ntpd` daemon running to
  discipline drift over uptime. Added to `dev-image` by default. `base-files`
  also gets `/var/run` so daemons that write a pidfile have a place to put it.
- **Fix `simpleiot` failing to start at boot** — the unit installed the binary
  as `/usr/bin/simpleiot` but its init script invoked `/usr/bin/siot`, so
  booting the dev image showed `siot: not found` and the service never ran. The
  binary now installs as `siot` to match upstream. `go_binary` gains a `binary`
  kwarg for cases where the installed command name should differ from the apk
  package name.
- **Per-developer machine override via `local.star`** — when you switch machines
  from the TUI's setup view, yoe now writes `local.star` at the project root
  with your selection. Subsequent `yoe` commands use that machine without you
  re-passing `--machine` every time. The file is gitignored so each developer
  can pin their own target. `--machine` on the command line still wins.
- **`yoe flash list` and TUI device picker** — `yoe flash list` enumerates
  removable USB sticks and SD cards (filtered against the disk hosting the
  running system). In the TUI, pressing `f` on an image unit opens a device
  picker with a live progress bar during the write. `yoe` never invokes `sudo`
  itself; if the device isn't writable, it prompts once for consent and runs
  `sudo chown <you> /dev/...`.
- **Honest flash progress** — `yoe flash` now opens the target device with
  `O_DIRECT` so writes bypass the kernel page cache and the progress bar tracks
  actual device throughput. Previously the bar could hit 100% with hundreds of
  MB still buffered in RAM, freezing the UI for tens of seconds during the final
  flush. With `O_DIRECT` the wait is paid out across the write itself, and
  "Flash complete" appears when the data is really on the card.
- **Fix `yoe flash` rejecting non-system disks** — `flash` previously refused to
  write to `/dev/sda`, `/dev/nvme0n1`, and `/dev/vda` regardless of the actual
  layout. It now detects which disk hosts the running system (`/`, `/boot`,
  `/boot/efi`, `/usr`) and refuses only that disk, so flashing to a USB or
  external SATA drive named `/dev/sda` works on machines whose root is on NVMe.
- **Fix images silently shipping without packages** — if an artifact's apk was
  missing from the local repo (e.g., its build was cancelled), the image used to
  build anyway with a `warning: package X not found, skipping` and produce a
  kernel-panicking rootfs. Image assembly now hard-fails with a clear message
  naming the missing package. The build cache now also treats a unit as
  out-of-date when its apk has gone missing, and rebuilding any unit invalidates
  its dependents — so reruns auto-recover instead of reusing stale outputs.

## [0.8.2] - 2026-04-24

- **Fix extlinux install under Docker 29** — `--privileged` containers no longer
  auto-populate `/dev/loop*`, so `losetup --find` failed during image assembly.
  Pre-create `/dev/loop0..31` with `mknod` before calling `losetup`.

## [0.8.1] - 2026-04-24

- **Fix rootfs ownership on booted systems** — files under `/`, `/bin`, `/etc`,
  `/usr`, etc. are now owned by `root:root` on the booted system instead of
  showing up as whatever user built the project.
- **Compare rootfs ownership handling across projects** — `docs/comparisons.md`
  now has a section explaining how Alpine, Debian, Buildroot, Yocto, and NixOS
  handle root ownership during image builds, and where `[yoe]` fits.

## [0.8.0] - 2026-04-24

- **Class task merge semantics** — units passing `tasks=[...]` to a class
  (`autotools`, `cmake`, `go_binary`) no longer fully replace the class's
  default task list. Instead, overrides are merged by name: a same-named task
  replaces in place (preserving position and using the override's `steps`
  fully), a new-named task is appended, and `task("name", remove=True)` drops a
  base task. This lets units add a new task (e.g., `init-script`) without
  restating the class-generated `build` task. The merge is implemented in a new
  `classes/tasks.star` helper (`merge_tasks(base, overrides)`) shared by the
  three classes. The `simpleiot` unit dropped its duplicated `build` task as a
  result; existing units that override `build` are unaffected (replace-in-place
  yields the same result as the previous full-replacement semantics).
- **Fix install_template/install_file path resolution for helper functions** —
  template paths now resolve relative to the `.star` file containing the
  `install_template()`/`install_file()` call, not to the file that ultimately
  calls `unit()`. Previously, a helper like
  `base_files(name = "base-files-dev")` in `units/base/base-files.star` invoked
  from `images/dev-image.star` looked for templates under
  `images/base-files-dev/` instead of `units/base/base-files/`, breaking the
  `dev-image` build. The base directory is now captured at install-step
  construction time from the Starlark caller frame; existing units that define
  and use install steps in the same `.star` file are unaffected.
- **File templates** — units can declare external template files (`.tmpl`) and
  static files in a directory alongside the `.star` file and install them via
  new `install_template()` and `install_file()` step-value constructors placed
  directly in `task(..., steps=[...])` alongside shell strings. Templates render
  through Go `text/template` with a unified `map[string]any` context
  auto-populated with
  `name`/`version`/`release`/`arch`/`machine`/`console`/`project` and any extra
  kwargs passed to `unit()`. The context map and the contents of the unit's
  files directory are hashed so template edits and extra-kwarg changes
  invalidate the cache. Install steps run on the host (not inside the sandbox),
  so `$DESTDIR` / `$SRCDIR` / `$SYSROOT` in install paths expand to host paths
  rather than the container bind-mount paths. `base-files`, `network-config`,
  and `simpleiot` migrated off inline heredocs. See `docs/file-templates.md`.
- **CLI flag parsing with flag.NewFlagSet** — refactored all subcommands
  (`build`, `run`, `flash`, `init`, `clean`, `log`, `refs`, `graph`) from manual
  switch-based parsing to Go's `flag.NewFlagSet`. Adds free `--help` for every
  subcommand, consistent `-flag`/`--flag` support, and repeatable flags (e.g.,
  `--port`). Net reduction of ~70 lines.
- **Go module cache** — Go units now persist module and build caches across
  builds via `cache_dirs = {"/go/cache": "go"}`. The executor mounts `cache/go/`
  from the project directory into the container, and `GOMODCACHE` and `GOCACHE`
  point to it. Subsequent builds skip module downloads.
- **Fix service enablement for S-prefixed init scripts** — services declared
  with an `S<NN>` prefix (like `S10network`) no longer get a symlink created on
  top of the actual script, which was causing a symlink loop and breaking
  networking at boot.
- **Unit environment field** — units can declare `environment = {"KEY": "VAL"}`
  which the executor merges into the build environment for all tasks. The Go
  class uses this for `GOMODCACHE`/`GOCACHE` so custom tasks (like simpleiot)
  get the cache env vars automatically.
- **QEMU port forwarding in machine config** — `qemu_config()` now accepts a
  `ports` field (e.g., `ports = ["2222:22", "8118:8118"]`) for default port
  forwarding. CLI `--port` flags extend these. Fixed a bug where multiple ports
  created duplicate QEMU netdevs. Fixed hostfwd syntax to use QEMU's
  `host-:guest` format. QEMU machines default to SSH (2222:22), HTTP (8080:80),
  and SimpleIoT (8118:8118).
- **Service enablement moved to units** — units now declare
  `services = ["sshd"]` to indicate which init scripts they provide. The image
  assembly auto-enables services by reading `service` metadata from installed
  APKs and creating `S50<name>` symlinks (or custom priority like `S10network`).
  The `services` parameter on `image()` is removed.
- **Design specs** — added `docs/starlark-packaging-images.md` (move packaging
  and image assembly to composable Starlark tasks) and `docs/file-templates.md`
  (external template files using Go `text/template`, replacing inline heredocs
  in units).
- **Go class uses golang container** — `go_binary()` now defaults to the
  `golang:1.24` external container image instead of `toolchain-musl`.
  Cross-compilation is handled via `GOARCH`/`GOOS` environment variables with
  `CGO_ENABLED=0` for static binaries, so the container always runs at host
  architecture (no QEMU overhead).
- **Per-unit sandbox and shell selection** — units now have `sandbox` (bool,
  default false) and `shell` (string, default "sh") fields. The autotools,
  cmake, and image classes set `sandbox=True, shell="bash"` for bwrap isolation.
  External containers (like `golang:1.24`) use the defaults — no bwrap, POSIX sh
  — since they don't ship bwrap or bash.
- **simpleiot unit** — new `go_binary` unit for SimpleIoT v0.18.5, an IoT
  application for sensor data, telemetry, and device management.
- **ca-certificates unit** — Mozilla CA bundle for TLS verification. Added to
  dev-image alongside simpleiot.
- **Per-task container resolution** — tasks can override the unit-level
  container via `task(container = "...")`. The executor resolves the container
  per-task, falling back to the unit default.
- **TUI: amber `[yoe]` title** — the top-left title in the TUI now renders
  `[yoe]` in amber on black, matching the project logo.
- Fix module URLs in `init` generated project file.

## [0.7.1] - 2026-04-06

- **Unit `release` field** — units can now specify `release = N` for packaging
  revisions (apk `-rN` suffix). Defaults to 0. Bump when the unit definition
  changes but the upstream version doesn't.
- **Build metadata** — each unit's build directory now contains a `build.json`
  with status, start/finish times, duration, build disk usage, installed size
  (destdir/apk), and input hash. The TUI detail view shows build time and sizes
  alongside the unit name.
- **Persistent build output** — executor output (`executor.log`) is now written
  for both CLI and TUI builds, so the TUI detail view shows build output
  regardless of how the build was triggered.

## [0.7.0] - 2026-04-06

- **Container units** — build containers are now Starlark units
  (`toolchain-musl`) instead of an embedded Dockerfile. Containers participate
  in the DAG, caching, and versioning. Classes set `container` and
  `container_arch` explicitly. `run(host = True)` enables host-side execution
  for container builds. The embedded Dockerfile and `EnsureImage()` are removed.
  Container images are tagged with arch for explicitness
  (`yoe-ng/toolchain-musl:15-x86_64`). Cross-arch containers use `docker buildx`
  automatically.
- **Container image prefix renamed** — Docker image prefix changed from
  `yoe-ng/` to `yoe/` (e.g., `yoe/toolchain-musl:15-x86_64`). Arch is always
  included in the tag for explicitness. Cross-arch containers use
  `docker buildx` automatically.
- **TUI: detail view log search** — press `/` in the unit detail view to search
  build output and logs. Matching lines are highlighted in yellow; `n`/`N` jump
  to next/previous match. First `esc` clears the search, second returns to the
  unit list.
- **TUI: color-coded unit types** — unselected units are now subtly colored by
  class: blue for regular units, magenta for images, cyan for containers.
  Selected unit uses a brighter green for visibility. Search (`/`) also matches
  unit class, so typing "image" or "container" filters to units of that type.
- **E2E build test scripts** — added `yoe_e2e`, `yoe_e2e_x86_64`, and
  `yoe_e2e_arm64` shell functions in `envsetup.sh` that build `base-image` from
  the e2e test project for x86_64 and arm64 (cross-build via QEMU user-mode).

## [0.6.0] - 2026-04-03

- **TUI: ctrl+f/ctrl+b page scrolling** — added vim-style page-forward and
  page-back keybindings in both the unit list and detail views, alongside the
  existing PgUp/PgDn keys.
- **Heavy development notice** — GitHub releases and `yoe update` now remind
  users to clean their build directory and re-create projects with each new
  release.
- **Updated plan/spec indexes** — all specs and plans marked with current
  implementation status; added plans INDEX.
- **Remove `repository()` builtin** — the `repository(path = "...")` config in
  `PROJECT.star` is removed. APK repos are now always at `repo/<project-name>/`,
  derived from the project name. This eliminates a confusing override that
  defeated per-project repo scoping.
- **TUI: show all units** — removed the filter that only showed units reachable
  from image definitions. The TUI now lists all units in the project.
- **README: "Is Yoe-NG Right for You?"** — new section clarifying when to use
  Yocto vs Yoe-NG. Added container workloads on the target device to the roadmap
  in Design Priorities.
- **Fix `yoe update` download URL** — binary name now matches goreleaser's
  naming convention (`yoe-Linux-x86_64`) instead of incorrectly including the
  version (`yoe-v0.1.0-Linux-x86_64`), which caused 404 errors.
- **Unit name collision detection** — duplicate unit names now error at
  evaluation time with a clear message showing which module first defined the
  unit.
- **PROVIDES collision detection** — two units providing the same virtual name
  in the same module now error. Units from higher-priority modules (later in the
  module list) override lower-priority ones with a notice.
- **`--project` flag** — `yoe --project projects/customer-a.star build` selects
  an alternate project file. Available on all subcommands.
- **Per-project APK repo** — package repositories are now scoped per project
  name (`repo/<project>/`) to prevent stale packages across project switches.
- **README: Principles section** — added six core design principles covering
  leveraging existing infrastructure, aggressive caching, custom containers per
  unit, no intermediate formats, one tool for all levels, and tracking upstream
  closely.
- **README: Build dependencies and caching** — new section explaining the three
  kinds of build dependencies (host tools via containers, library deps via
  sysroot/apk, language-native deps via their own package managers), symmetric
  caching at the unit level, and how native builds unlock existing package
  ecosystems (e.g., PyPI wheels on ARM).
- **README: Cross-compilation is optional** — updated from "no cross
  compilation" to "cross compilation is optional," acknowledging that Go and
  some C/C++ packages cross-compile easily while fussy packages can avoid it.
- **Raspberry Pi in yoe init** — rpi machine added to the project initialization
  template.
- **Fix false "old build layout" warning** — `warnOldLayout` was written for the
  old `build/<arch>/<unit>/` directory structure but the current layout is
  `build/<unit>.<scope>/`, causing every build directory to trigger a spurious
  warning.

## [0.5.1] - 2026-04-02

- Remove version from release binary name to fix stable download URL.

## [0.5.0] - 2026-04-02

**BASE-IMAGE boots on RPI4**

- **Tasks replace build steps** — `build = [...]` replaced by `tasks = [...]`
  with named build phases. Each task has `run` (shell string), `fn` (Starlark
  function), or `steps` (mixed list). Classes (autotools, cmake, go) are now
  pure Starlark.
- **`run()` builtin** — Starlark functions can execute shell commands directly
  during builds. Errors show `.star` file and line number, not generated shell.
  `run(cmd, check=False)` returns exit code/stdout/stderr for conditional logic.
  `run(cmd, privileged=True)` runs directly in the container as root for
  operations like losetup/mount that bwrap can't do.
- **Unit scope** — units declare `scope = "machine"`, `"noarch"`, or `"arch"`
  (default). Machine-scoped units (kernels, images) build per-machine. Build
  directories are flat: `build/<name>.<scope>/`. Repo is flat with scope in
  filenames: `repo/<name>-<ver>-r0.<scope>.apk`.
- **Machine-portable images** — images no longer hard-code machine-specific
  packages or partitions. `MACHINE_CONFIG` and `PROVIDES` inject machine
  hardware specifics automatically. `base-image` works across QEMU x86, QEMU
  arm64, and Raspberry Pi without changes.
- **`PROVIDES` virtual packages** — units and kernels declare `provides` to
  fulfill virtual names. `provides = "linux"` on `linux-rpi4` means images that
  list `"linux"` get the RPi kernel when building for `raspberrypi4`.
- **Image assembly in Starlark** — disk image creation moved from Go to
  `classes/image.star` using `run()`. Fully readable, customizable, forkable.
- **Raspberry Pi BSP module** (`units-rpi`) — machine definitions, kernel fork
  units, GPU firmware, and boot config for Raspberry Pi 4 and 5.
- **Runtime dependency resolution** — image assembly now resolves transitive
  runtime dependencies automatically. `RUNTIME_DEPS` predeclared variable
  available after unit evaluation. Three-phase loader: machines → units →
  images.
- **Layers renamed to modules** — `layer()` → `module()`, `LAYER.star` →
  `MODULE.star`, `yoe layer` → `yoe module`, `layers/` → `modules/`. Aligns
  terminology with Go modules model used for dependency resolution.

## [0.4.0] - 2026-03-31

**ARM BUILDS ON X86 NOW WORK**

- **TUI global notifications** — the TUI now shows a yellow banner for
  background operations like container image rebuilds. Previously these events
  were only visible in build log files.
- **cmake added to build container** — cmake is now available as a bootstrap
  tool in the container (version bump to 14), enabling units that use the cmake
  build system.
- **xz switched to cmake** — the xz unit now uses the cmake class instead of
  autotools with gettext workarounds, simplifying the build definition.
- **TUI reloads .star files before each build** — editing unit definitions or
  classes no longer requires restarting the TUI. The project is re-evaluated
  from Starlark on each build, picking up any changes to build steps, deps, or
  configuration.
- **Fix xz autoreconf failure** — xz's `configure.ac` uses `AM_GNU_GETTEXT`
  macros which require gettext's m4 files. The xz unit now provides stub m4
  macros and skips `autopoint`, allowing `autoreconf` to succeed without gettext
  installed in the container.
- **Cross-architecture builds** — build arm64 and riscv64 images on x86_64 hosts
  using QEMU user-mode emulation. Target arch is resolved from the machine
  definition. Run `yoe container binfmt` for one-time setup, then
  `yoe build base-image --machine qemu-arm64` works transparently.
- **Arch-aware build directories** — build output is now stored under
  `build/<arch>/<unit>/` and APK repos under `build/repo/<arch>/`, supporting
  multi-arch builds in the same project. **Note:** existing build caches under
  `build/<unit>/` will need to be rebuilt (`yoe clean --all`).
- **`yoe container binfmt`** — new command to register QEMU user-mode emulation
  for cross-architecture container builds. Shows what it will do and prompts for
  confirmation.
- **Multi-arch QEMU** — `yoe run` now auto-detects cross-architecture execution
  and uses software emulation (`-cpu max`) instead of KVM. Container includes
  `qemu-system-aarch64` and `qemu-system-riscv64`.
- **TUI setup menu** — press `s` to open a setup view for selecting the target
  machine. Shows available machines with their architecture and highlights the
  current selection. Designed to accommodate future setup options.

## [0.3.4] - 2026-03-30

- **Build lock files** — a PID-based `.lock` file is written during builds so
  other `yoe` instances can detect in-progress work instead of marking active
  builds as failed. Builds are skipped if another process is already building
  the same unit.
- **`yoe clean --locks`** — removes stale lock files left behind by crashed or
  killed builds.
- **TUI edit for cached layers** — pressing `e` on a unit now also searches the
  layer cache, so editing works for units from layers cloned via
  `yoe layer sync`.

## [0.3.3] - 2026-03-30

- **HTTPS layer URLs** — `yoe init` now uses HTTPS URLs for the units-core layer
  instead of SSH, removing the need for SSH key setup to get started.

## [0.3.2] - 2026-03-30

- **TUI scrolling** — both the unit list and detail log views are now
  scrollable. The unit list shows `↑`/`↓` overflow indicators when there are
  more units than fit on screen. The detail view supports `j`/`k`,
  `PgUp`/`PgDn`, `g`/`G` navigation through the full build output and log, with
  auto-follow during active builds.
- **Auto-sync layers** — `yoe build` and other commands that load the project
  now automatically clone missing layers on first use, matching the lazy
  container-build pattern. Existing cached layers are not fetched/updated, so
  there is no added latency on subsequent runs. Explicit `yoe layer sync` is
  still available to update layers.
- **TUI confirmation prompts** — quitting (`q`/`ctrl+c`) and cancelling a build
  (`x`) now prompt for confirmation when builds are active, preventing
  accidental loss of in-progress builds. Declining a prompt clears the message
  cleanly.
- **Fix build cancellation not stopping containers** — cancelling a build (via
  TUI quit or `ctrl+c` on the CLI) now explicitly stops the Docker container
  (`docker stop`) instead of only killing the CLI client, which left containers
  running in the background.
- **Fix stale cache after cancelled builds** — the cache marker is now removed
  before building so a cancelled or failed rebuild no longer appears cached from
  a previous successful build.

## [0.3.1] - 2026-03-30

**ALL UNITS ARE NOW BUILDING**

- **Per-unit sysroots** — each unit's build sysroot is assembled from only its
  transitive `deps`, not every previously built unit. Fixes busybox symlinks
  shadowing container tools (e.g., musl-linked `expr` breaking autoconf).
- **Run from TUI** — press `r` on an image unit to launch it in QEMU.
- **Log writer plumbing** — container stdout/stderr in image assembly and source
  fetch/prepare output now route through the build log writer instead of
  os.Stdout. Fixes TUI alt-screen corruption during background builds.
- **Autotools maintainer-mode override** — `make` invocations pass
  `ACLOCAL=true AUTOCONF=true AUTOMAKE=true AUTOHEADER=true MAKEINFO=true` to
  prevent re-running versioned autotools (e.g., `aclocal-1.16`) that aren't in
  the container. Fixes gawk and similar packages.
- **rcS init script** — `base-files` now includes `/etc/init.d/rcS` which runs
  all `/etc/init.d/S*` scripts at boot.
- **network-config unit** — new unit that configures a network interface via an
  init script.
- **Build failure context** — when a unit fails, the output now lists all
  downstream units blocked by the failure. The TUI shows cached units in blue
  and displays the full build queue (waiting/cached) before work begins.
- **dev-image** — added `kmod` and `util-linux` to the development image.
- **Image rootfs dep fix** — image assembly now follows only `runtime_deps` when
  resolving packages, not build-time `deps`. Fixes build-only packages (e.g.,
  gettext via xz) being installed into the rootfs and overflowing the partition.

## [0.3.0] - 2026-03-30

**THIS RELEASE DOES NOT WORK** - this release is only to capture rename and TUI
updates. Wait for a future one to do any work.

**BREAKING CHANGE** - due to rename, recommend deleting any external projects
and starting over.

- **Terminology rename** — "recipe" is now "unit" and "package" is now
  "artifact" throughout the codebase. The Starlark `package()` function is now
  `unit()`, the image field `packages` is now `artifacts`, and the `recipes/`
  directory in layers is now `units/`. The `recipes-core` layer is now
  `units-core`. The Go `internal/packaging` package is now `internal/artifact`.
- **`yoe log`** — view build logs from the command line. Shows the most recent
  build log by default, or a specific unit's log with `yoe log <unit>`. Use `-e`
  to open the log in `$EDITOR`.
- **`yoe diagnose`** — launch Claude Code with the `/diagnose` skill to analyze
  a build failure. Uses the most recent build log by default, or a specific
  unit's log with `yoe diagnose <unit>`.
- **TUI rewrite** — `yoe` with no args launches an interactive unit list with
  inline build status (cached/waiting/building/failed). Builds run in-process
  via `build.BuildUnits()` with real-time status events — dependencies show as
  yellow "waiting", then flash green as they build. Features: background builds
  (`b`/`B`), edit unit in `$EDITOR` (`e`), view build log (`l`), diagnose with
  Claude (`d`), add unit with Claude (`a`), clean with confirmation (`c`/`C`),
  search/filter (`/`), and a split detail view showing executor output and build
  log tail. The `yoe tui` subcommand has been removed.
- **Build events** — `build.Options.OnEvent` callback notifies callers (e.g.,
  the TUI) as each unit transitions through cached/building/done/failed states.

## [0.2.10] - 2026-03-30

- **`yoe container shell`** — interactive bash shell inside the build container
  with bwrap sandbox, sysroot mounts, and the same environment variables recipes
  see during builds. Useful for debugging build failures and sandbox issues.

## [0.2.9] - 2026-03-30

- **Bash for build commands** — switched build shell from busybox sh to bash.
  Avoids autoconf compatibility issues (e.g., `AS_LINENO_PREPARE` infinite loop)
  and matches what upstream build scripts expect. Removed per-recipe bash
  workaround from util-linux.
- **User account API** — new `classes/users.star` provides `user()` and
  `users_commands()` functions for defining user accounts in Starlark.
  `base-files` is now a callable `base_files()` function that accepts a `users`
  parameter — image recipes can override it to add users (e.g., dev-image adds a
  `user` account with password `password`).

## [0.2.8] - 2026-03-30

- **meson build system support** — added samurai (ninja-compatible build tool),
  meson, and kmod recipes. Container updated to v11 with python3 and
  py3-setuptools for meson. Build environment now sets `PYTHONPATH` to the
  sysroot so Python packages installed by recipes are discoverable.
- **Container versioning note** — CLAUDE.md now documents that both
  `Dockerfile.build` and `internal/container.go` must be bumped together.
- **gettext recipe** — builds GNU gettext from source as a recipe instead of
  relying on the container. Provides `autopoint` needed by packages like xz that
  use gettext macros in their autotools build.
- **Sysroot binaries on PATH** — `/build/sysroot/usr/bin` is now prepended to
  `PATH` during builds, so executables from dependency recipes are discoverable.
- Autotools class respects explicit `build` steps — no longer prepends default
  autoreconf/configure when a recipe provides its own build commands.
- **Claude Code plugin** — added `.claude/` plugin with AI skills for recipe
  development: `diagnose` (iterative build failure analysis), `new-recipe`
  (generate recipes from URLs/descriptions), `update-recipe` (version bumps),
  `audit-recipe` (review against best practices and other distros).
- **`--clean` build flag** — deletes source and destdir before rebuilding.
  `--force` now only skips the cache check without cleaning.
- **`--force`/`--clean` scoped to requested recipes** — dependency recipes still
  use the cache, only explicitly named recipes are force-rebuilt.
- Fixed `YOE_CACHE` help text — was `~/.cache/yoe-ng`, actually defaults to
  `cache/` in the project directory.

## [0.2.7] - 2026-03-27

- **Per-recipe build logs** — build output written to
  `build/<recipe>/build.log`. Console is quiet by default; on error the log path
  is printed. Use `--verbose` / `-v` to stream build output to the console.
- Fixed QEMU machine templates — removed UEFI firmware (`ovmf`/`aavmf`/
  `opensbi`) incompatible with MBR+syslinux boot, fixed root device `vda2` →
  `vda1`.

## [0.2.6] - 2026-03-27

- **base-files recipe** — provides filesystem skeleton: `/etc/passwd` (root with
  blank password), `/etc/inittab` (busybox init + getty), `/boot/extlinux/`
  (boot config), and essential mount point dirs (`/proc`, `/sys`, `/dev`, etc.).
  Moved from hardcoded Go to a recipe so users can customize via overlays.
- Serial console uses `getty` for proper login prompt.

## [0.2.5] - 2026-03-27

### Added

- **musl libc recipe** — copies the musl dynamic linker from the build container
  into the image so dynamically linked packages work at runtime.
- **Automatic package dep resolution** — image assembly now resolves transitive
  build and runtime deps from recipe metadata. e.g., openssh automatically pulls
  in openssl and zlib without listing them in the image recipe.
- **Recipes without source** — recipes with no `source` field (e.g., musl) skip
  source preparation instead of erroring.

### Fixed

- Disable ext4 features (`64bit`, `metadata_csum`, `extent`) incompatible with
  syslinux 6.03 so bootloader can load kernel from any partition size.
- Image package dep resolution walks both `deps` and `runtime_deps` so shared
  libraries are included.
- OpenSSL recipe uses `--libdir=lib` so libraries install to `/usr/lib` instead
  of `/usr/lib64` — fixes "Error loading shared library libcrypto.so.3".
- Inittab no longer tries to mount `/dev` (already mounted by kernel via
  `devtmpfs.mount=1`).
- Skip `TestBuildRecipes_WithDeps` in CI — GitHub Actions runners don't support
  user namespaces inside Docker.
- Most stuff in `dev-image` now works.

## [0.2.4] - 2026-03-27

- update BL config

## [0.2.3] - 2026-03-27

### Changed

- **Container as build worker** — `yoe` CLI always runs on the host. The
  container is now a stateless build worker invoked only for commands that need
  container tools (gcc, bwrap, mkfs, etc.). Eliminates container startup
  overhead for read-only commands (`config`, `desc`, `refs`, `graph`, `clean`).
- **File ownership** — build output uses `--user uid:gid` so files created by
  the container are owned by the host user, not root.
- **QEMU host-first** — `yoe run` tries host `qemu-system-*` first, falls back
  to the container if not found.
- **`--force` scoped to requested recipes** — `--force` and `--clean` only
  force-rebuild the explicitly requested recipes; dependencies still use the
  cache for incremental builds.
- **Busybox init** — images use busybox `/sbin/init` with a minimal
  `/etc/inittab` instead of `init=/bin/sh`. Shell respawns on exit, clean
  shutdown via `poweroff`.

### Fixed

- Shell quoting in bwrap sandbox commands — semicolons in env exports no longer
  split the command at the outer shell level.
- Package installation in image assembly — always extracts `.apk` files via
  `tar` instead of gating on `apk` binary availability.
- Rootfs mount points (`/proc`, `/sys`, `/dev`, `/tmp`, `/run`) now included in
  disk images via `.keep` placeholder files.
- `devtmpfs.mount=1` added to kernel cmdline so `/dev` is populated before init.

### Removed

- `YOE_IN_CONTAINER` environment variable — no longer needed.
- `ExecInContainer` / `InContainer` / `HasBwrap` APIs — replaced by
  `RunInContainer`.
- Container re-exec pattern — the yoe binary is no longer bind-mounted into the
  container.

## [0.2.2] - 2026-03-27

### Added

- **Layer `path` field** — layers can live in a subdirectory of a repo via
  `path = "layers/recipes-core"`. Layer name derived from path's last component.
- **Project-local cache** — source and layer caches default to `cache/` in the
  project directory instead of `~/.cache/yoe-ng/`
- **`.gitignore` in `yoe init`** — new projects get a `.gitignore` with `/build`
  and `/cache`
- **Autotools `autoreconf`** — autotools class auto-runs `autoreconf -fi` when
  `./configure` is missing (common with git sources)
- SSH URL support for source fetching (`git@host:user/repo.git`)
- **Design: per-recipe tasks and containers** — planned support for named
  `task()` build steps with optional per-task Docker container images. Container
  resolves: task → package → bwrap. See
  `docs/superpowers/plans/per-recipe-containers.md`.

### Changed

- Default layer in `yoe init` uses SSH URL
  (`git@github.com:YoeDistro/yoe-ng.git`) with `path = "layers/recipes-core"`
- Container no longer mounts a separate cache volume — cache/ is accessible
  through the project mount
- Container runs with `--privileged` (needed for losetup/mount during disk image
  creation and /dev/kvm for QEMU)

## [0.2.1] - 2026-03-27

### Added

- **Dev-image with 10+ packages** — new `dev-image` builds end-to-end with
  sysroot, including essential libraries (openssl, ncurses, readline, libffi,
  expat, xz), networking (curl, openssh), and debug tools (strace, vim)
- **Remote layer fetching** — `yoe layer sync` clones/fetches layers from Git
- **Sysroot + image deps in DAG** — build sysroot and image dependencies
  resolved as part of the dependency graph
- **`yoe_sloc`** — source lines of code counter using `scc`

### Fixed

- Correct partition size for `losetup`, ensure sysroot dir exists
- Recipe fixes for end-to-end dev-image builds

### Changed

- Moved design docs into `docs/` directory
- Expanded build-environment and comparisons documentation

## [0.2.0] - 2026-03-26

### Added

- **Bootable QEMU x86_64 image** — end-to-end flow from recipes to a partitioned
  disk image that boots to a Linux kernel with busybox
- **Starlark `load()` support** — class imports and `@layer//path` label-based
  references across layers, `//` resolves to layer root when inside a layer
- **Recursive recipe discovery** — `recipes/**/*.star` directory traversal
- **`recipes-core` layer** — autotools/cmake/go/image classes, busybox/zlib/
  syslinux/linux recipes, base-image, qemu-x86_64 machine
- **APKINDEX generation** — `APKINDEX.tar.gz` for apk dependency resolution
- **Bootstrap framework** — `yoe bootstrap stage0/stage1/status`
- **Container auto-enter** — host `yoe` binary bind-mounted into container,
  Dockerfile embedded in binary, versioned image tags

### Fixed

- Build busybox as static binary (no shared lib dependency on rootfs)
- APKINDEX uses SHA1 base64 as required by apk
- Handle git sources in workspace (tag upstream without re-init)
- bwrap sandbox inside Docker with `--security-opt seccomp=unconfined`
- Mount git root for layer resolution

### Changed

- Prefer git sources with shallow clone over tarballs
- Move build commands to `envsetup.sh` (`yoe_build`, `yoe_test`)

## [0.1.0] - 2026-03-26

Initial release of yoe-ng — a next-generation embedded Linux distribution
builder.

### Added

- **CLI foundation** — `yoe init`, `yoe config show`, `yoe clean`, `yoe layer`
  commands with stdlib switch/case dispatch (no framework)
- **Starlark evaluation engine** — recipe and configuration evaluation using
  go.starlark.net with built-in functions (`project()`, `machine()`,
  `package()`, `image()`, `layer_info()`, etc.)
- **Dependency resolution** — DAG construction, Kahn's algorithm topological
  sort with cycle detection, `yoe desc`, `yoe refs`, `yoe graph`
- **Content-addressed hashing** — SHA256 cache keys from recipe + source +
  patches + dep hashes + architecture
- **Source management** — `yoe source fetch/list/verify/clean` with
  content-addressed cache and patch application
- **Build execution** — `yoe build` with bubblewrap per-recipe sandboxing,
  automatic container isolation via Docker/Podman
- **Package creation** — APK package creation, `yoe repo` commands, local
  repository management
- **Image assembly** — rootfs construction, overlay application, disk image
  generation with syslinux MBR + extlinux
- **Device interaction** — `yoe flash` with safety checks, `yoe run` for QEMU
  with KVM
- **Interactive TUI** — Bubble Tea interface for browsing recipes and machines
- **Developer workflow** — `yoe dev extract/diff/status` for source modification
- **Custom commands** — extensible CLI via `commands/*.star`
- **Patch support** — per-recipe patch files applied as git commits
