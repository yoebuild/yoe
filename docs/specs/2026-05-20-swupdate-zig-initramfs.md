---
date: 2026-05-20
topic: swupdate-zig-initramfs
---

# swupdate + Zig init in a kernel-embedded initramfs

## Summary

Replace yoe's update story — currently modelled on the
[yoe-distro `updater.installer` shell script](https://github.com/YoeDistro/yoe-distro/blob/master/sources/meta-yoe/recipes-support/updater/files/updater.installer)
([docs](https://docs.yoedistro.org/updater.html)) — with a two-layer design:

1. **Image transaction layer: [swupdate](https://github.com/sbabic/swupdate).**
   Drives the actual update: cpio bundle parsing, sha256 verification, streaming
   xz decompression, partition write, bootloader-env update, optional
   signing/encryption, optional Hawkbit/OTA. Built from source as a yoe unit
   (swupdate is not packaged in Alpine), statically linked, so yoe controls the
   compile-time feature set — signing support and the v1 handler list.
2. **Orchestration layer: a Zig init binary, PID 1 in the initramfs.** Probes
   block devices, decides between normal-boot / update / rescue, mounts
   overlays, spawns `swupdate -i …`, watches its progress socket, and `execve`s
   `switch_root` into the final rootfs.

The initramfs cpio is **compiled into the kernel image** via
`CONFIG_INITRAMFS_SOURCE`, producing a single bootable artifact (`vmlinuz` or
the platform's equivalent) that contains the kernel, the Zig init, the swupdate
binary, and a minimal userland. This trades independent init-vs-kernel updates
for a simpler boot story and removes the bootloader's need to load a separate
initrd.

This spec covers: the swupdate `.swu` bundle layout yoe will emit, the
responsibilities and interface of the Zig init, the new yoe units and image type
needed to assemble and embed the initramfs, and the kernel-unit changes required
to consume it. It does **not** specify the on-device repartition recipe (that
lives in `sw-description`, per machine), the signing key management (separate
spec), or the field-deploy/feed delivery of `.swu` artifacts (the
[feed-server-and-deploy](2026-04-30-feed-server-and-deploy.md) spec is the
closest neighbour and may extend to cover `.swu` distribution).

---

## Problem Frame

The yoe-distro updater is ~1 kLOC of shell driving busybox tools from an
initramfs. It works, and the design rationale (single image, no A/B, runs from
initramfs, kernel-only recovery) is sound. But it has the structural problems
any large shell script in initramfs has:

- **Error paths are guesswork.** `if cmd; then …; fi` chains that span 20
  commands lose context on failure. The script's recovery is "drop to a rescue
  shell"; the _reason_ a partition couldn't be mounted rarely makes it to the
  console.
- **Re-implements solved problems.** sha256, partition creation/resize,
  bootloader-env writes, signature verification — all of these are
  in-scope-of-swupdate and would no longer be yoe's code to maintain.
- **No signing or encryption.** Adding either to the shell script means bolting
  on openssl invocations and getting the failure semantics right. swupdate does
  both natively (CMS/RSA signing, AES encryption).
- **No network/OTA path.** A future "pull updates from a feed" story duplicates
  work that swupdate's `suricatta` module already implements.
- **Hard to test.** Shell + busybox + real block devices is the only
  reproducible test environment.

Meanwhile, swupdate-the-library is not actually a linkable update engine. The
repo's `include/network_ipc.h` and `include/progress_ipc.h` (LGPL-2.1) expose a
client to a separately-running swupdate **process**, not an embeddable function.
So any caller is structurally a process that spawns swupdate and talks to its
Unix sockets. That shape — small orchestrator process drives a swupdate child —
is the same shape the yoe initramfs already has, with `swupdate` replacing the
cpio/sha256/xzcat/dd body of `updater.installer`.

What's left, then, is the _orchestrator_: device probing, the boot/update/rescue
decision, overlay setup, `switch_root`, and progress reporting. That code is too
gnarly to keep in shell, too small to justify a Yocto-style daemon, and lives in
initramfs where dynamic linking is a tax. Zig fits: single static binary,
`std.os.linux` covers `mount`/`pivot_root`/ `reboot`/`umount2` as direct
syscalls, errors-as-values for the decision tree, and `@cImport` over the LGPL
IPC headers if we want them (or hand-roll the socket protocol to sidestep LGPL
entirely).

The final piece is **where the initramfs lives**. yoe today builds kernels as
units that produce `vmlinuz` and ships a separate rootfs image. For the update
story, the kernel + initramfs are operationally one artifact — they must version
together, the bootloader must always load both, and recovery means "kernel
boots, initramfs takes over." Embedding the initramfs cpio into the kernel image
via `CONFIG_INITRAMFS_SOURCE` makes this a single file the bootloader knows how
to load, removes a class of "bootloader loaded the wrong initrd" failures, and
lets boards with primitive bootloaders (raw uImage in NOR, U-Boot without
filesystem support) work without changes.

---

## Goals

- A bootable image that contains a kernel with an embedded initramfs which can
  install a yoe-built `.swu` from USB, SD, or eMMC onto the target's rootfs
  partition.
- A new yoe unit type for the Zig init binary, statically linked against musl,
  no runtime dependencies.
- swupdate built from source as a `module-core` unit (git, tag-pinned),
  statically linked against musl, with signing compiled in and only the v1
  handlers enabled. Its build deps (libconfig, libubootenv, json-c, zlib,
  openssl) become `deps` entries per the no-container-installs rule.
- A new yoe image type (or extended `image` class) that assembles the initramfs
  cpio from a declared content set.
- A `kernel`-unit field that points at an initramfs source directory/cpio so the
  kernel build sets `CONFIG_INITRAMFS_SOURCE` and produces a kernel image with
  the initramfs embedded.
- The Zig init drives a documented boot decision tree (normal /
  update-from-media / rescue) and exits via `switch_root` for normal/update,
  drops to a shell for rescue.
- Update progress is reported on `/dev/console` and on swupdate's progress IPC
  socket so a splash/audio driver can subscribe.
- yoe's signing story: every `.swu` is signed with a project key and swupdate
  verifies on the target, from the first dev image on. Until the signing spec
  lands, the key is a per-project development key generated at project init.
- Distro-agnostic: the same update mechanism serves every distro yoe images ship
  (Alpine, Debian, Ubuntu). The initramfs is assembled from the same static-musl
  binaries regardless of the target rootfs's distro, and swupdate writes the
  rootfs partition as an opaque image; neither the `initramfs` image type nor
  `package_swu()` may grow distro-specific assumptions.

## Non-Goals

- A/B updates. Single-image, as in the current yoe-distro updater.
- Network/Hawkbit OTA in v1. The architecture leaves room (swupdate's
  `suricatta` is in the same binary) but the spec only commits to media-based
  install.
- Recovery without a working kernel. If the kernel does not boot, recovery is
  "reflash from a host." Same boundary as yoe-distro.
- Reproducing the espeak/splash UX of yoe-distro in v1. Progress goes to
  `/dev/console`; the IPC socket is exposed for a future splash driver.
- Multi-rootfs / dual-bank layouts. Out of scope, see Non-Goals first bullet.

---

## Alternatives considered

### bootc (boot and upgrade via container images)

[bootc](https://github.com/bootc-dev/bootc) is the most architecturally
different alternative: the OS itself is an OCI image, the update mechanism is
`bootc upgrade` (pull a new tag, deploy via ostree, reboot into the new
deployment, roll back on failure). The pitch is strong on paper — atomic A/B,
OCI-layer signing, reuse of the container ecosystem for hosting and
distribution, and the same image runs identically across edge and server.

It is not the right fit for v1, for reasons that are structural rather than
preferential:

- **systemd as PID 1.** bootc assumes systemd plus `systemd-tmpfiles` and
  `systemd-sysusers`. yoe's default image is Alpine + OpenRC, and the
  unit-services contract
  ([2026-04-07-unit-services.md](2026-04-07-unit-services.md)) is OpenRC-shaped.
  Switching the init system is a larger move than the update mechanism this spec
  replaces.
- **ostree on the host and in the image.** bootc requires ostree ≥ 2025.03, the
  `/sysroot/ostree/deploy/` layout, and a build-side step that commits the
  rootfs into an ostree repo and projects it as an OCI image. None of that
  machinery exists in yoe today.
- **Read-only `/usr`, factory `/etc` with 3-way merge, mutable `/var`.**
  Alpine's stock filesystem layout does not honor this split. Enforcing it
  reaches into every unit's install paths.
- **bootupd's bootloader assumptions.** bootupd hardcodes RPM-family paths;
  Debian and Ubuntu integrations already work around this. yoe would either
  carry a patched bootupd unit or substitute its existing bootloader handling
  and bypass bootupd, with the corresponding maintenance cost.
- **Hardware-boot vs. OCI-pull asymmetry.** bootc's update path assumes the
  device can reach an OCI registry. yoe's primary update target is a board that
  may boot from removable media without a network, which is the case
  swupdate-from-USB is designed for. Network/OTA arrives via swupdate's
  `suricatta` later; it is not the v1 floor.

None of this rules bootc out as a future yoe output. The rootfs-closure assembly
this codebase already does is the right input to a `bootc_image(...)` finalize
step that wraps a closure into an ostree-committed OCI image — gated on a
systemd-based image variant and the missing infrastructure listed above. For now
the framing is: swupdate is the v1 update mechanism for the single-image,
OpenRC, hardware-boot target; bootc is an alternative output shape that becomes
interesting once a systemd-based image variant exists and a project needs the
OCI-as-OS deployment model.

Cross-reference: the
[deployable-containers spec](2026-05-25-deployable-containers.md) treats the OS
as a producer of OCI images for application workloads, deliberately not for the
host itself. bootc is the inversion — the host as an OCI image — and lives at
this spec's layer, not that one.

### Why Zig (and not Go or C)

The orchestrator could plausibly be written in Go — the language this codebase
already maintains, with u-root and gokrazy as precedent for Go as PID 1 in an
initramfs — or in C against the static musl toolchain every build container
already ships. Both were weighed:

- **Go.** A static Go binary starts at ~1.5–2 MiB before the orchestrator does
  anything, against a budget where every megabyte rides inside every kernel
  image; and the runtime (GC, signal handling, scheduler) is more machinery than
  a PID-1 decision tree needs in a crash-must-not-happen context.
- **C.** No new toolchain units, but no errors-as-values — the decision tree's
  main failure mode is exactly the lost-error-context problem the Problem Frame
  calls out in shell, and C shares it absent disciplined conventions.
- **Zig.** Tens-of-KiB static binaries, `std.os.linux` syscall coverage,
  errors-as-values for the decision tree. The accepted costs: a `zig` toolchain
  unit, a pre-1.0 version pin (see Open Questions), and validating the compiler
  under QEMU in foreign-arch containers.

---

## Architecture

### Boot flow

```
power-on
  └── bootloader (u-boot / extlinux / EFI)
        └── load kernel image  ──┐
                                 │  (initramfs embedded in vmlinuz)
                                 ▼
            kernel decompresses, mounts rootfs=tmpfs from embedded cpio,
            execve /init   ←── zig binary, PID 1
                 │
                 ├── probe /sys/block, /sys/class/block; identify
                 │   eMMC, SD, USB devices
                 │
                 ├── decision tree:
                 │     1. rescue-trigger file on any media?     → rescue
                 │     2. .swu newer than installed version on
                 │        USB / SD / eMMC data partition?       → update
                 │     3. rootfs partition present and clean?    → boot
                 │     else                                     → rescue
                 │
                 ├── (update path)
                 │     mount media RO, pick newest .swu by numeric-aware
                 │     version compare, spawn `swupdate -i <path>`, attach
                 │     to progress socket, stream phase/percent to console.
                 │     on success: reboot if bundle wrote kernel/dtb;
                 │     fall through to boot for rootfs-only bundles.
                 │     on failure: rescue.
                 │
                 ├── (boot path)
                 │     fsck rootfs partition, mount it,
                 │     mount overlay (lower=rootfs ro, upper=data/overlay,
                 │     work=data/work) at /newroot,
                 │     mount /proc /sys /dev /run into /newroot,
                 │     execve switch_root /newroot /sbin/init
                 │
                 └── (rescue path)
                       execve /bin/sh on /dev/console
```

The update branch is version-gated. The installed version is recorded
persistently at install time (a version file on the data partition, written by
`sw-description` post-install); `package_swu()`'s emitted filename
(`<name>-<version>.swu`) is the canonical version source. The init compares
versions numeric-aware (split on `.`, compare components as integers, fall back
to string compare) and takes the update branch only when the candidate is
strictly newer than what is installed. This one rule provides idempotence (media
left inserted does not re-flash on every boot), correct selection among multiple
bundles (no lexicographic `1.10` < `1.9` inversion), and anti-rollback (older
bundles are never installed). A `.swu` staged on the eMMC data partition is
scanned like removable media; it is also the natural staging location for a
future network-pulled update flow.

A bundle that wrote kernel/dtb images to the boot partition always ends in a
reboot — the in-memory kernel must never `switch_root` into a rootfs staged for
a newer kernel (`/lib/modules` would mismatch). Fall-through to boot without a
reboot is permitted only for rootfs-only bundles.

### Image transaction layer: swupdate

The `.swu` bundle is a cpio archive containing:

```
sw-description           # libconfig-style manifest
sw-description.sig       # CMS or RSA-PKCS1 signature (always present; dev
                         # project key until the signing spec lands)
rootfs.ext4.xz           # streaming-decompressed by swupdate
boot/Image               # kernel for the boot partition (if changed)
boot/<board>.dtb         # device tree (if changed)
```

`sw-description` enumerates the images, their target partitions/files, sha256
sums, and any handler-specific options. Per-machine variation (partition names,
bootloader-env quirks) lives in `sw-description`, not in the orchestrator. Yoe
will generate `sw-description` from a Starlark expression on the project/machine
so the source of truth stays in the project file.

Handlers used in v1:

| Handler      | Used for                                        |
| ------------ | ----------------------------------------------- |
| `raw`        | rootfs partition write (xz-streamed)            |
| `rawfile`    | kernel + dtb to a mounted boot partition        |
| `diskpart`   | first-boot partition table creation/repair      |
| `bootloader` | u-boot-env (or grub/EFI Boot Guard) bookkeeping |

Signing key management is **out of scope for this spec** — separate spec — but
the architectural commitment is: **verification is always on, from the first dev
image**. swupdate's `CONFIG_SIGNED_IMAGES=y` is a compile-time property — a
binary built with it refuses unsigned bundles, and no configuration state
disables verification — so a "ship unsigned now, flip it on later" posture does
not exist in swupdate's model. Retrofitting verification onto a deployed fleet
would also mean rebuilding the kernel-embedded initramfs (where the public key
lives) and delivering it over the still-unverified channel. Instead, yoe
generates a per-project development signing key at project init; every `.swu` is
signed and every initramfs carries the matching public key from day one.
Dev-key-signed builds are a development posture — production deployment requires
the signing spec's key management.

### Orchestration layer: Zig init

A single static binary,
`zig build -Dtarget=<arch>-linux-musl -Doptimize=ReleaseSmall`. Source lives at
<https://github.com/yoebuild/yoe-init>; the binary lives in the initramfs at
`/init`. Responsibilities:

- **Early boot fixups.** `mount /proc`, `/sys`, `/dev` (devtmpfs); set console;
  configure kernel log level. All via `std.os.linux` syscalls.
- **Block-device probing.** Enumerate `/sys/class/block`, filter to candidate
  media (USB sd*, mmcblk*, etc.), read partition labels and filesystem
  signatures (libblkid via `@cImport`, or a hand-rolled superblock probe for the
  FS types we actually use — ext4, FAT, exFAT).
- **Decision tree.** Above. Each branch is a function returning `!ExitMode` so
  error context is preserved up to the rescue handler.
- **Update driver.** `posix.fork` +
  `execve("/usr/sbin/swupdate", &[_]… {"swupdate", "-i", swu_path, "-v"})`. Open
  the progress Unix socket, read `progress_msg` structs in a loop, render
  `phase: %d%%` to console, wait for `IDLE` + child exit.
- **Overlay + switch_root.** `mount` overlayfs with the right lower/upper/work
  dirs, move-mount `/proc`/`/sys`/`/dev`/`/run`, `chdir` to `/newroot`,
  `mount("/newroot", "/", MS_MOVE)`, `chroot(".")`, `execve("/sbin/init", …)`.
- **Rescue.** `execve("/bin/sh", …)` on the console. The shell is busybox from
  Alpine.

The Zig binary depends only on the kernel ABI; everything else (busybox shell,
swupdate) is also in the initramfs as separate binaries.

LGPL note: linking `network_ipc.c` statically requires offering relinkable
object files. Since the IPC protocol is a small, stable struct over a Unix
socket, **v1 hand-rolls the protocol** in Zig and links against nothing LGPL.
Documented in the Zig init's source; if it becomes a maintenance burden later,
switching to `@cImport` and shipping relinkable objects is a one-week change.

**Threat-model boundary.** Physical console access is assumed equivalent to
device compromise — the standard embedded posture — so the rescue shell grants
no privilege an attacker with the device in hand does not already have, and the
rescue-trigger-file-on-media mechanism rides on the same assumption. Deployments
where that assumption fails (publicly reachable consoles or USB ports) need a
hardening pass that is out of scope for v1.

**Trajectory.** In this spec yoe-init is initramfs-scoped and always hands off
to `/sbin/init`. It is a candidate seed for a broader yoe init / service
supervisor role later, but v1 makes no design commitments to that future beyond
what falls out naturally: the binary stays self-contained and depends only on
the kernel ABI.

### Initramfs contents

```
/init                    # Zig binary, statically linked
/bin/sh                  # busybox (from module-alpine)
/bin/busybox             # busybox itself
/usr/sbin/swupdate       # from-source unit, static musl
/usr/sbin/e2fsck         # e2fsprogs (from module-alpine); busybox's fsck
                         # applet only dispatches to an external fsck.<type>
/etc/hwrevision          # machine identifier (templated per-image)
/lib/...                 # only if a binary genuinely needs it; goal is none
/dev, /proc, /sys, /run  # empty mount points
/newroot                 # mount point for final rootfs
```

Target size: under 4 MiB compressed (xz). swupdate's static binary is the
heaviest item; re-baseline the budget once the from-source static build (with
the signing/crypto stack linked in) and e2fsck are measured.

### Kernel-embedded initramfs

The kernel build consumes the initramfs via two `.config` options:

```
CONFIG_BLK_DEV_INITRD=y
CONFIG_INITRAMFS_SOURCE="<path-to-cpio-or-directory-listing>"
CONFIG_INITRAMFS_COMPRESSION_XZ=y    # or _ZSTD=y; one of
CONFIG_RD_XZ=y
```

`CONFIG_INITRAMFS_SOURCE` accepts either a cpio archive or a directory tree (the
kernel build re-cpio's it). Yoe will produce a cpio (`gen_init_cpio` is too
kernel-tree-coupled; yoe's image assembler builds the cpio itself and points the
kernel `.config` at the file). The resulting `vmlinuz` is the only boot artifact
— the bootloader does not need to load a separate `initrd`.

Cache implications: the kernel unit's hash now depends on the initramfs cpio's
content hash. Any change in the Zig binary, busybox, or swupdate re-builds the
kernel. This is correct and unavoidable for embedded initramfs; it's the cost of
the single-artifact property. Mitigation: the initramfs unit produces a cpio
whose name encodes the content hash, so unchanged inputs produce an unchanged
cpio path, and the kernel's content-addressed cache key stays stable.

---

## Yoe changes

### New units

| Unit        | Module                        | Purpose                                                                                                                                                                                                     |
| ----------- | ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `zig`       | `module-core` (toolchain)     | The Zig compiler. Alpine packages zig only for aarch64/x86_64, so the upstream-tarball path is required for other target arches (armv7, riscv64), not just version freshness; v1 targets aarch64 and x86_64 |
| `yoe-init`  | `module-core`                 | The Zig init source tree ([yoebuild/yoe-init](https://github.com/yoebuild/yoe-init)); builds with `zig build` against `zig`. Produces a single static binary `init`                                         |
| `swupdate`  | `module-core`                 | Built from source (git, tag-pinned), static against musl, signing compiled in, only the v1 handlers enabled — swupdate is not packaged in Alpine, so no passthrough is possible                             |
| `busybox`   | `module-alpine` (passthrough) | Already in Alpine                                                                                                                                                                                           |
| `e2fsprogs` | `module-alpine` (passthrough) | e2fsck for the boot path's rootfs check; busybox's fsck applet only dispatches to an external checker                                                                                                       |

`yoe-init` is the first non-trivial Zig unit; v1 inlines the build steps in the
unit.

### New image type: `initramfs`

A new image kind that assembles a cpio archive instead of a partitioned disk.
Inputs:

- A list of packages (`yoe-init`, `swupdate`, `busybox`, `e2fsprogs`) to install
  into a staging root.
- A list of files/dirs to inject (config files, the `/init` symlink, mount
  points).
- A compression choice (`xz`, `zstd`, `none`).

Output: a single `<name>.cpio.<compressor>` file in the image's output
directory. The artifact is content-addressed.

This is a strict subset of what the existing `image` class does (no partition
table, no bootloader, no filesystem) — it can either be a new `initramfs(...)`
builtin or a mode of the existing image builder. Lean toward a separate builder:
the cpio path is simple enough that sharing code with the disk-image path costs
more in conditionals than it saves.

### Kernel-unit extension

Add a kernel-unit field (or `kernel_config(...)` extension) for:

```python
kernel(
    name = "linux-aarch64",
    ...
    initramfs = ":boot-initramfs",   # name of an initramfs image unit
)
```

When set:

- The kernel unit gains the initramfs cpio as a dependency in the resolve graph.
- The kernel `.config` is post-processed before build: `CONFIG_INITRAMFS_SOURCE`
  is set to the resolved cpio path, `CONFIG_BLK_DEV_INITRD=y` is forced, the
  compression option matches the cpio's compression.
- The kernel unit's content hash incorporates the cpio's content hash (see the
  "gate `fmt.Fprintf` on a non-empty value" rule in CLAUDE.md — unconditional
  hash inclusion would re-hash every kernel today and force a full rebuild; gate
  on `initramfs != ""`).

### `.swu` packaging

A new Starlark builtin, `package_swu()`, registered in
`internal/starlark/builtins.go` alongside the existing machine-level builtins
(`kernel()`, `uboot()`, `partition()`) and invoked from a machine or project
task. It takes:

- A rootfs image artifact.
- A kernel image artifact (optional, for boot-partition updates).
- A `sw-description` template (Starlark string).

…and emits `<name>-<version>.swu` (a cpio archive in swupdate's expected
format). This is a new package type alongside `.apk` and disk images.

### Documentation

- `docs/updater.md` — replaces and supersedes any prior updater doc; describes
  the two-layer architecture, how to bring up a new machine, the
  `sw-description` template, signing posture.
- `docs/initramfs.md` — how the initramfs image type works, what goes in it, how
  to extend it (e.g., for board-specific firmware blobs).
- The `kernel` unit reference doc gains the `initramfs = …` field; the flag is
  marked `(planned)` until the kernel-unit extension lands.

---

## Phasing

### Phase 1 — initramfs assembly and embed (no swupdate yet)

- Land `zig` unit (Alpine binary).
- Write a stub Zig init that does: probe, mount basics, print "hello", drop to
  busybox shell. No update, no overlay, no switch_root yet.
- Land `initramfs` image type. Bundle Zig init + busybox.
- Extend kernel unit with `initramfs = …`. Produce a single `vmlinuz` with the
  cpio embedded.
- Boot it under QEMU on the e2e project. Verify the shell drops on console.

**Exit criterion:** `yoe build qemu-x86-image` produces a kernel that boots to
busybox under QEMU, with no separate initrd.

### Phase 2 — switch_root into rootfs

- Extend Zig init: probe rootfs partition, fsck, mount it, overlay, switch_root.
- Update e2e qemu-x86 machine to boot via the embedded initramfs instead of the
  current direct rootfs mount.

**Exit criterion:** e2e qemu-x86 reaches its normal userspace (login prompt) via
the new initramfs.

### Phase 3 — swupdate integration

- Land the from-source `swupdate` unit (static musl, v1 handlers, signing
  compiled in); add to initramfs.
- Generate `sw-description` from Starlark for the qemu-x86 machine.
- Build the `package_swu()` builtin; generate the per-project dev signing key;
  produce a signed `.swu` from the qemu-x86 image.
- Extend Zig init: scan USB/SD and the eMMC data partition for `.swu`,
  version-gate against the installed version, spawn swupdate, watch progress,
  succeed-and-reboot.

**Exit criterion:** boot the QEMU machine with `-drive` pointing at a
`.swu`-bearing virtual USB; the initramfs detects it, runs swupdate, writes the
rootfs, reboots, and comes up on the new rootfs.

### Phase 4 — signing, rescue UX, real hardware

- Replace the dev-key workflow with production key management per the signing
  spec; document key generation, deploy, and rotation.
- Polish rescue path: rescue-trigger file, console banner.
- Bring up one real board (likely a Raspberry Pi from `module-rpi`).
- Move the documentation out of `(planned)`.

**Exit criterion:** the real board boots the embedded-initramfs kernel from its
standard bootloader and applies a signed `.swu` from USB end-to-end, coming up
on the new rootfs.

---

## Open Questions

- **Zig version pin.** Zig is pre-1.0; std-lib shape changes between releases.
  Pin to a specific Zig version per branch; bump deliberately. Decide whether
  the `zig` unit pulls Alpine's `zig` (which version?) or pins upstream
  tarballs.
- **`zig.star` class.** A `zig_build()` class (wrapping the `zig build`
  invocation, vendor-dir handling, target-triple selection) is deferred until a
  second Zig unit appears; one consumer doesn't justify the abstraction.
- **Initramfs as a yoe `image` mode vs. a new type.** Lean separate; revisit
  once the second cpio-style artifact appears.
- **Overlay upper-dir lifecycle.** When the rootfs is rewritten by an update,
  the overlay upper-dir (on the data partition) may contain stale edits that
  target the old rootfs. Document the policy: wipe overlay upper on rootfs
  rewrite, or preserve it. Probably wipe; record in `sw-description`
  post-install.
- **Cross-arch Zig builds.** The Zig binary for the target board is built inside
  the foreign-arch container (per yoe's native-builds-only rule). Verify Zig's
  cross-compilation story works equivalently when _invoked_ natively in a
  foreign-arch container — should be fine since Zig cross-compiles by default,
  but worth a phase-1 sanity check.
- **`.swu` distribution path.** Out of scope here; cross-reference the
  feed-server-and-deploy spec when distribution is added.
- **Bootloader-env coordination.** Some boards need post-update bootloader-env
  writes that the bootloader is the only writer of (e.g. U-Boot env partition).
  swupdate's `bootloader` handler covers u-boot, EFI Boot Guard, GRUB.
  Per-machine bring-up has to pick one; new machines that need a different
  mechanism are a custom-handler problem, not a yoe problem.
- **Where signing keys live.** Specifically: project key in the project repo
  (under git, not committed plaintext), public key baked into the initramfs at
  build time. Mechanism for the private side TBD in the signing spec. The
  signing spec must also address key rotation under the bake-in constraint:
  replacing a compromised or expired key means delivering a new kernel image
  that the old key must verify — e.g. multiple key slots baked in, or a
  rotate-before-expiry policy. That constraint is an input requirement for the
  signing spec, not an implementation detail.

---

## References

- `yoe-init` source repo: <https://github.com/yoebuild/yoe-init>
- `updater.installer` source:
  <https://github.com/YoeDistro/yoe-distro/blob/master/sources/meta-yoe/recipes-support/updater/files/updater.installer>
- yoe-distro updater docs: <https://docs.yoedistro.org/updater.html>
- swupdate: <https://github.com/sbabic/swupdate>
- swupdate docs: <https://sbabic.github.io/swupdate/>
- Kernel `CONFIG_INITRAMFS_SOURCE`:
  <https://www.kernel.org/doc/Documentation/filesystems/ramfs-rootfs-initramfs.txt>
- Related yoe specs:
  [feed-server-and-deploy](2026-04-30-feed-server-and-deploy.md),
  [flash-command](2026-04-28-flash-command.md),
  [container-units](2026-04-04-container-units.md).
