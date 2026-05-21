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
   signing/encryption, optional Hawkbit/OTA. Pulled from `module-alpine` as a
   binary, not built from source.
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
- swupdate consumed as an Alpine binary via `module-alpine`, not built from
  source.
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
- yoe's signing story: `.swu` bundles are signed with a project key, swupdate
  verifies on the target.

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
                 │     2. .swu present on USB/SD?               → update
                 │     3. rootfs partition present and clean?    → boot
                 │     else                                     → rescue
                 │
                 ├── (update path)
                 │     mount media RO, locate newest .swu by lexicographic
                 │     version, spawn `swupdate -i <path>`, attach to
                 │     progress socket, stream phase/percent to console.
                 │     on success: optionally reboot or fall through to boot.
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

### Image transaction layer: swupdate

The `.swu` bundle is a cpio archive containing:

```
sw-description           # libconfig-style manifest
sw-description.sig       # CMS or RSA-PKCS1 signature (optional v1, required v2)
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
the architectural commitment is: signing is built into v1's binary layer
(swupdate is compiled with `CONFIG_SIGNED_IMAGES=y`), and the v1 cut ships with
verification disabled by configuration. Enabling it is a sw-description and
key-deploy change, not a code change.

### Orchestration layer: Zig init

A single static binary,
`zig build -Dtarget=<arch>-linux-musl -Doptimize=ReleaseSmall`. Lives in the
initramfs at `/init`. Responsibilities:

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

### Initramfs contents

```
/init                    # Zig binary, statically linked
/bin/sh                  # busybox (from module-alpine)
/bin/busybox             # busybox itself
/usr/sbin/swupdate       # from module-alpine
/etc/hwrevision          # machine identifier (templated per-image)
/lib/...                 # only if a binary genuinely needs it; goal is none
/dev, /proc, /sys, /run  # empty mount points
/newroot                 # mount point for final rootfs
```

Target size: under 4 MiB compressed (xz). swupdate's binary is the heaviest item
(~1–2 MiB depending on configured handlers).

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

| Unit       | Module                        | Purpose                                                                                                       |
| ---------- | ----------------------------- | ------------------------------------------------------------------------------------------------------------- |
| `zig`      | `module-core` (toolchain)     | The Zig compiler, fetched from Alpine binary (Zig is packaged) or upstream tarball if Alpine's version trails |
| `yoe-init` | `module-core`                 | The Zig init source tree; builds with `zig build` against `zig`. Produces a single static binary `init`       |
| `swupdate` | `module-alpine` (passthrough) | Already in Alpine community. No source unit                                                                   |
| `busybox`  | `module-alpine` (passthrough) | Already in Alpine                                                                                             |

`yoe-init` is the first non-trivial Zig unit; expect a `zig.star` class to
emerge from it (`zig_build()` wrapping the `zig build` invocation, vendor dir
handling, target triple selection). v1 of this spec just inlines the build steps
in the unit.

### New image type: `initramfs`

A new image kind that assembles a cpio archive instead of a partitioned disk.
Inputs:

- A list of packages (`yoe-init`, `swupdate`, `busybox`) to install into a
  staging root.
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

A new build step (likely a `package_swu()` builtin invoked from a machine or
project task) takes:

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

- Land `swupdate` as a passthrough Alpine unit; add to initramfs.
- Generate `sw-description` from Starlark for the qemu-x86 machine.
- Build a `package_swu()` step; produce a `.swu` from the qemu-x86 image.
- Extend Zig init: search USB for `.swu`, spawn swupdate, watch progress,
  succeed-and-reboot.

**Exit criterion:** boot the QEMU machine with `-drive` pointing at a
`.swu`-bearing virtual USB; the initramfs detects it, runs swupdate, writes the
rootfs, reboots, and comes up on the new rootfs.

### Phase 4 — signing, rescue UX, real hardware

- Enable signing in swupdate config; document key generation/deploy.
- Polish rescue path: rescue-trigger file, console banner.
- Bring up one real board (likely a Raspberry Pi from `module-rpi`).
- Move the documentation out of `(planned)`.

---

## Open Questions

- **Zig version pin.** Zig is pre-1.0; std-lib shape changes between releases.
  Pin to a specific Zig version per branch; bump deliberately. Decide whether
  the `zig` unit pulls Alpine's `zig` (which version?) or pins upstream
  tarballs.
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
  build time. Mechanism for the private side TBD in the signing spec.

---

## References

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
