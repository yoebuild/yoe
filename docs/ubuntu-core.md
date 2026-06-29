# Ubuntu Core for Embedded Devices

> **Status:** This page is background reference on how
> [Ubuntu Core](https://ubuntu.com/core) is built and customized for embedded
> hardware. It describes Canonical's system, not a `[yoe]` feature — `[yoe]`
> does not build Ubuntu Core images and has no roadmap to. It exists so readers
> evaluating the two can understand Ubuntu Core's customization model on its own
> terms. The closing section,
> [How `[yoe]` could assemble Ubuntu Core images](#how-yoe-could-assemble-ubuntu-core-images),
> is a forward-looking design exploration — it sketches a feasible
> implementation seam to make the boundary concrete, but no such code exists and
> the project has not committed to building it. For the shipped head-to-head
> comparison, see the
> [Ubuntu Core section of Comparisons](comparisons.md#vs-ubuntu-core).

Ubuntu Core is Canonical's snap-based, immutable, transactionally-updated
variant of Ubuntu, aimed at IoT, edge, and appliance devices. Customizing it for
a board is almost entirely about **assembling and signing your own set of
snaps** — there is no "edit the rootfs" step the way there is with a classic
distribution. Everything read-only on the device is a snap, and the image is
generated from a signed [model assertion](https://ubuntu.com/core/docs/models).
This page walks through the pieces people actually touch when bringing Ubuntu
Core up on custom hardware.

## The model assertion is the image

The model is a signed JSON document that _is_ the definition of an image. It
names the components that constitute a device:

- `architecture` — `amd64`, `arm64`, or `armhf`
- `base` — the root snap (`core20` / `core22` / `core24`)
- `kernel` — the kernel snap
- `gadget` — the board-support snap
- `snaps[]` — application and extra snaps, each pinned to a channel or revision

You sign the model with a **brand key**, then build the bootable image with
[`ubuntu-image`](https://github.com/canonical/ubuntu-image):

```console
$ ubuntu-image snap my-model.json
```

So "customizing Ubuntu Core" reduces to customizing the snaps the model points
at — gadget, kernel, and your apps — then re-signing the model. The base snap is
rarely touched.

## The four customization surfaces

### 1. Gadget snap — the board vehicle

The
[gadget snap](https://documentation.ubuntu.com/core/how-to-guides/image-creation/build-a-gadget-snap/)
is where almost all board-level customization lives. You typically fork a
reference gadget — [`pi-gadget`](https://github.com/snapcore/pi-gadget) for
Raspberry Pi, `pc-amd64-gadget` for PC-class hardware — and edit its
`gadget.yaml`:

- Partition and volume layout, sizes, filesystem types
- Bootloader choice and assets (`grub`, `u-boot`, `lk`, `piboot`)
- Kernel command-line additions
- **`defaults:`** — first-boot snap configuration keyed by snap-id (for example,
  disabling `console-conf`, or setting default system options)
- **`connections:`** — interface auto-connections wired in ahead of time

### 2. Kernel snap — kernel, DTBs, modules, initramfs

Board bring-up — a new SoC, a custom carrier board, extra peripherals — happens
in the kernel snap. You fork the reference kernel snap (for example
[`pi-kernel`](https://github.com/snapcore/pi-kernel)), adjust the kernel config
or defconfig, add patches and out-of-tree drivers, and ship your device-tree
blobs and overlays. It is built with `snapcraft`'s
[`kernel` plugin](https://snapcraft.io/docs/kernel-plugin).

### 3. Base snap — rarely customized

Most devices use the stock `core22` / `core24` base. A custom base is only
warranted when you need a non-standard libc or runtime layer, which is unusual.

### 4. Application snaps — your software

Apps ship as strictly confined snaps. To reach hardware you declare
`plugs`/`slots` against Ubuntu Core's
[interfaces](https://snapcraft.io/docs/supported-interfaces) (`gpio`,
`serial-port`, `network`, and so on), and use
[hooks](https://snapcraft.io/docs/supported-snap-hooks) (`install`, `configure`,
`prepare-device`) for lifecycle and configuration. Apps are listed in the model
so they are seeded into the image.

## First-boot and headless provisioning

Embedded devices are usually headless, so common customizations are:

- **Bypass `console-conf`** — the interactive first-boot setup is disabled via
  the gadget `defaults`.
- **Auto-create a user** — ship a
  [system-user assertion](https://ubuntu.com/core/docs/system-user) so an
  account is created at first boot without interaction.
- **Limited cloud-init** — Ubuntu Core restricts the available datasources, but
  a gadget can embed a `cloud.conf` for grub-based images.
- **Offline seeding** — pre-seed all snaps so the device boots fully provisioned
  with no network.

## Development versus production

- **Development:** build and sign with your own key in _dangerous_ mode, pull
  snapd with `--snapd-from-edge`, use unasserted or `devmode` snaps, and iterate
  with `snap try`. No store is required.
- **Production:** register a brand account, create a signing key
  (`snapcraft create-key` / `register-key`), and — for fleet delivery and
  controlled updates — set up a
  [brand store / dedicated snap store](https://documentation.ubuntu.com/core/explanation/stores/dedicated-snap-store/).
  Updates are transactional with automatic rollback; you control cadence with
  `refresh.timer` / `refresh.hold` and channel pinning in the model.

## Constraints on cost-sensitive hardware

Two properties matter most when weighing Ubuntu Core for low-end embedded:

1. **The store gate.** Serious fleet management — private snaps, controlled
   rollout, device assertions at scale — effectively wants a dedicated or brand
   store, which is a commercial relationship with Canonical.
2. **The size floor.** A minimum Ubuntu Core install lands around 2.5 GiB before
   any application code — see [below](#why-the-base-image-is-so-large) for the
   breakdown. That rules out the small-flash, 128–512 MiB class of device.

### Why the base image is so large

No single component is large. Ubuntu Core multiplies copies of the boot-critical
components to guarantee transactional rollback and factory recovery, and four
factors compound:

1. **Sealed snaps, no cross-snap sharing.** The base ships as four squashfs
   snaps — `core24` (~70 MB), the kernel snap (~100+ MB: kernel image, all
   modules, firmware, and the initramfs in one), `snapd` (~40 MB), and the
   gadget (a few MB). Each is self-contained, so a library shared between them
   is not deduplicated the way it would be in a shared FHS root.
2. **Revision retention, full copies not deltas.** snapd keeps
   `refresh.retain + 1` revisions of every snap (default `retain = 2`, so three
   copies) plus a temporary fourth during a refresh — roughly **4× per snap**.
   Each revision is a complete squashfs; there is no on-disk delta. The ~100 MB
   kernel snap is therefore budgeted at ~400 MB.
3. **A recovery seed that duplicates the boot snaps.** The `system-seed`
   partition holds a second complete copy of the kernel, base, snapd, and gadget
   snaps plus their assertions, so the device can reinstall or factory-reset —
   another full set of the core snaps.
4. **Several partitions, each with reserve.** The layout is four partitions,
   each carrying filesystem overhead and headroom snapd reserves for the next
   refresh's temporary copy. From Canonical's
   [partition-sizing guidance](https://documentation.ubuntu.com/core/how-to-guides/image-creation/calculate-partition-sizes/):

   | Partition     | Minimum  | Holds                                                 |
   | ------------- | -------- | ----------------------------------------------------- |
   | `system-seed` | ~457 MiB | recovery bootloader, recovery snap copies, assertions |
   | `system-boot` | ~160 MiB | kernel EFI image(s), boot state                       |
   | `system-save` | ~32 MiB  | device identity and recovery data                     |
   | `system-data` | variable | run-mode snaps, retained revisions, writable data     |

The structural reason is the architecture itself: everything is a read-only snap
that is never modified in place, so atomic update, rollback, and factory
recovery all require keeping _whole alternate copies_ — current, next, a few
old, and a recovery duplicate — rather than mutating files or storing deltas.
The size buys those guarantees.

These two points are exactly where `[yoe]` makes different choices — `.apk` /
`.deb` packages into a shared FHS root, self-hosted signed repositories, and
atomic image updates rather than snap revisions. The
[Comparisons](comparisons.md#vs-ubuntu-core) page covers that trade-off in full,
including the side-by-side table and the per-partition size breakdown.

## Where Ubuntu Core is the right call

Ubuntu Core is a strong fit when you want Canonical's 12-year LTS commitment,
when strict per-app confinement via snaps and AppArmor is a product requirement,
when your team already operates a Canonical stack (Landscape for fleet
management, a brand store for distribution), or when the device has ample
storage and the size floor is an acceptable trade for signed transactional
updates with rollback out of the box. If your target instead has tight flash, a
custom SoC, and a single-purpose image, a build system such as `[yoe]` or Yocto
is usually the better match.

## How `[yoe]` could assemble Ubuntu Core images

> This section is a design exploration, not a plan. `[yoe]` does not build
> Ubuntu Core images today. The point here is to show that the gap is a
> deliberate product choice rather than a technical wall: a snap-assembled,
> immutable image is files — squashfs blobs, partitions, a bootloader, and a
> chain of signed assertions — and `[yoe]` already builds partitioned, bootable
> disk images. What follows is the seam an implementation would most likely use.

### The precedent: orchestrate, don't reimplement

`[yoe]` does not reimplement `dpkg` to build a Debian rootfs — it shells out to
[`mmdebstrap`](https://gitlab.mister-muffin.de/josch/mmdebstrap), which drives
apt and dpkg in a single pass. The image assembler picks the rootfs builder by
distro family: `_is_apt_distro(effective_distro)` selects
`_assemble_debian_rootfs()` (mmdebstrap) over `_assemble_rootfs()` (apk), and
`_create_disk_image_debian()` over `_create_disk_image()`
(`modules/module-core/classes/image.star`).

An Ubuntu Core path would follow the same shape. The hard, UC-specific work —
fetching the snaps, validating them against the model, and writing the recovery
seed plus its assertion chain — is already implemented in
[`snapd`](https://github.com/canonical/snapd)'s `image` package
(`image.Prepare()`, backed by `seedwriter`), and Canonical's
[`ubuntu-image`](https://github.com/canonical/ubuntu-image) is a thin wrapper
over it. The implementation move is to **invoke `ubuntu-image` (or
`snap prepare-image`) as a subprocess**, not to re-derive the seed format —
exactly as the Debian path shells out to `mmdebstrap`.

The subprocess boundary is deliberate, and it is a licensing decision as much as
an architectural one. `[yoe]` is Apache-2.0; `snapd` and `ubuntu-image` are
GPL-3.0. Importing `snapd/image` as a Go library would static-link GPL-3 code
into the `[yoe]` binary and make the distributed binary a GPL-3 combined work —
incompatible with keeping `[yoe]` Apache-2.0. Running the GPL-3 tool as a
separate process is mere aggregation, so the copyleft does not reach into
`[yoe]`. This is the same firewall `[yoe]` already relies on when it shells out
to `mmdebstrap`, `mksquashfs`, and `apk-tools` rather than linking them, and a
from-source `.snap` emitter stays clean the same way (`mksquashfs` as a
subprocess plus a `meta/snap.yaml` it writes directly).

This is also `[yoe]`'s strongest piece of prior art for the idea. `ubuntu-image`
demonstrates the seam end to end: a single binary builds both snap-based (Ubuntu
Core) and classic deb-based images, and the two builders **share one
disk-assembly path** — load `gadget.yaml`, size the rootfs, populate the boot
filesystem, write the partitions — branching only on how the rootfs is filled
(seed snaps vs. a chroot of packages). That is exactly the `rootfs_fn`-varies /
disk-assembly-is-common split `[yoe]` already uses, which is why the UC path is
additive rather than a rewrite.

### The new pieces

Two new building blocks, both analogues of things `[yoe]` already has:

- **A `snap_feed(...)`** — the counterpart to `apt_feed(...)`. It pulls prebuilt
  snaps (base, kernel, gadget, snapd, apps) and their store assertions from a
  snap store, the same way `apt_feed` mirrors `.deb` packages and their signed
  `Packages` catalogs. Snaps that a project builds from source would come from
  units whose emitter writes a squashfs plus `meta/snap.yaml` (a `.snap`
  emitter, sibling to the existing `.deb` and `.apk` emitters).
- **A model input** — the project supplies a model assertion (or `[yoe]`
  generates an unsigned/"dangerous" one for development). This is the one
  genuinely new concept in the signing model: UC keys an image to a signed model
  and a brand key, where `[yoe]` today keys a repo to a project signing key.

### The parallel assembly path

With those in place, the image assembler gains a third branch alongside the apk
and apt paths. An image declared `distro = "ubuntu-core"` would resolve its
`rootfs_fn`/`disk_fn` to functions that:

1. Resolve the snap set from the `snap_feed` (and any from-source `.snap`
   units).
2. Invoke `ubuntu-image` (or `snap prepare-image`) as a subprocess in a
   container, the way `mmdebstrap` runs today, to fetch and validate the snaps
   and write the seed and its assertion chain — the `ubuntu-seed` /
   `ubuntu-boot` / `ubuntu-save` / `ubuntu-data` content and the managed
   bootloader.
3. Emit the disk image through `[yoe]`'s existing partition/image plumbing — the
   same step the apk and apt paths already use, since `ubuntu-image` shows the
   disk-assembly stage is independent of the packaging model.

The result slots into `[yoe]`'s model the way the Debian backend slots in beside
Alpine: a new distro family with its own assembler, sharing the unit graph,
content-addressed cache, and image front-end. The snap target is effectively
another distro/libc key in the existing fan-out (`target = core24`), so
from-source app snaps reuse `[yoe]`'s Ubuntu glibc backend to match the base
snap's ABI.

### What stays genuinely different

The mechanics are tractable; the reason `[yoe]` does not ship this is that the
path imports the parts of the snap model `[yoe]` was designed to avoid:

- **A second update model.** UC's read-only-base-plus-writable-overlay root,
  per-snap revision retention, and the resulting size floor are the whole point
  of snaps — and the thing `[yoe]`'s content-addressed packages plus atomic
  whole-image updates were built to do without. Adopting the assembly path means
  adopting that runtime model on the device.
- **Assertions and brand keys.** Production UC images are gated on a signed
  model and, for fleet delivery, a brand or dedicated store — a signing and
  distribution model distinct from `[yoe]`'s self-hosted project-key repository.
