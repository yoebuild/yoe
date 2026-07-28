# Arduino UNO Q flashing — Implementation Plan

> **Status:** Phase 1 landed (target-state reference doc). Phases 2–8 are
> outstanding — no flashing code exists yet. The `arduino-uno-q` machine
> definition and the `qcom.arduino` feed live in `module-qcom` and are the
> prerequisite this plan builds on.

## Problem

`yoe flash` writes a whole-disk image to a removable block device: it stats the
path, refuses anything that is not a block device, checks for mounted
partitions, and `dd`s one `.img` over the top. Every board yoe supports today
boots off an SD card, so that model has held.

The Arduino UNO Q breaks it in three independent ways:

1. **There is no removable medium.** The eMMC is soldered and is the only boot
   storage. There is no `/dev/sdX` to hand to `yoe flash`.
2. **yoe does not own the partition table.** The factory GPT has 69 entries; 66
   are signed Qualcomm firmware partitions (`xbl`, `tz`, `rpm`, `hyp`, `uefi`,
   `abl`, `devcfg`, `modemst`, `persist`, …) that a Linux image must not touch.
   yoe owns exactly two: `efi` (p67) and `rootfs` (p68). `userdata` (p69) is
   provisioned once and preserved across reflashes.
3. **The install path is a ROM-level USB protocol.** Provisioning goes through
   EDL (Emergency Download Mode) and the `qdl` tool, against a signed firehose
   programmer supplied by the vendor.

A second problem sits underneath the first: even with a flash path, yoe cannot
currently produce an artifact worth flashing. The image class populates a vfat
partition from `$DESTDIR/rootfs/boot/*` and unconditionally writes
`/boot/extlinux/extlinux.conf`. This board's ESP content lives at `/boot/efi`,
and it boots systemd-boot with Boot Loader Specification entries.

## Design summary

**Two regimes, and only one of them is flashing.**

- **Install and recovery — EDL + `qdl`.** The full-image path. Slow, requires a
  jumper on the JCTL header and a power cycle, and is the only way back from a
  bad image. Because EDL lives in masked ROM, it always works.
- **Iteration — `yoe deploy` over SSH.** Everything yoe builds for this board is
  a `.deb` in a Debian rootfs, and the board runs sshd.
  `yoe deploy <unit> <host>` already installs a unit onto a running device. This
  is the loop for package-level change; reflashing is for when the rootfs
  changes shape.

**The machine declares how it is provisioned, and the image class follows.** A
new `flash` block on `machine()` names the provisioning method. The image class
reads it to decide what to emit: a `qdl` machine gets per-partition images
(`efi.img`, `rootfs.img`), a block-device machine keeps today's single disk
image. One declaration drives both halves, so the artifact and the flash backend
cannot disagree — which two independent knobs would allow.

**`qdl` is a yoe unit, not a host prerequisite.** Telling users to
`apt install qdl` would reintroduce exactly the host dependency yoe exists to
remove: version skew between developers, a provisioning step in CI, and a fresh
checkout that does not just work. yoe pins its build inputs; a flashing tool
that writes a soldered eMMC deserves the same treatment. So `qdl` becomes a
**host-scoped unit** pulled from `debian.main` (`qdl 1.0+git20250319.30ac3a8`,
built from `github.com/linux-msm/qdl` — the same upstream, already packaged, so
no from-source unit is warranted).

**Host-scoped units are a new capability, and this is the first consumer.** yoe
has no way today to build a package for the _host_ architecture and make it
available to the build or to yoe itself. This plan adds one, because flashing
cannot be done hermetically without it, and it generalizes beyond flashing —
`Phase 5` covers it.

**The tool runs inside a container, not on the host.** A Debian-extracted binary
cannot be assumed to run on an arbitrary host: it wants glibc and `libusb` at
Debian's paths, and the host may be Alpine or Arch. Running it in a host-arch
container makes its closure the container's own, so it just works — and yoe's
containers already run `--privileged`, which grants access to host devices,
including `/dev/bus/usb`. That keeps the whole flash path hermetic: docker or
podman is the only host requirement, and yoe already requires one.

**The programmer blob is fetched, not vendored and not hand-supplied.**
`prog_firehose_ddr.elf` is signed and not ours to redistribute, but it is
published, and meta-qcom shows the pattern: a recipe fetches the vendor
bootloader archive from a pinned URL with a `sha256sum` and unpacks the
programmer from it. That is exactly a yoe unit with a source URL and hash, and
it removes a manual setup step. Confirm Arduino publishes the UNO Q bundle at a
stable URL first; fall back to a user-supplied path if not.

**The `rawprogram` description is data, checked in, not generated.** It maps
files to partition offsets, and those offsets are fixed by the factory GPT.
meta-qcom corroborates this: the XML and GPT binaries are not produced by the
image build at all — they come from a separate repo (`qcom-ptool`), versioned
per platform and per storage type (`partitions/qrb2210-rb1/emmc`), and the image
step merely `install`s them. A static hand-authored XML per machine is
reviewable and matches the project's rule against generating intermediate
artifacts that a tool then consumes.

**Open gate — fastboot.** U-Boot occupies the final firmware stage on this
board, and U-Boot's `fastboot` command writes GPT partitions by name. If it is
compiled into Arduino's build, `fastboot flash rootfs rootfs.img` needs no
jumper, no power cycle, and no signed programmer, and it becomes the primary
path with EDL demoted to recovery. This is unverified and is Phase 2's only job.
Phases 3–5 are independent of the answer; Phase 6 branches on it.

## Prior art: `meta-qcom`

Qualcomm's own Yocto layer
([qualcomm-linux/meta-qcom](https://github.com/qualcomm-linux/meta-qcom))
supports this exact SoC — `conf/machine/rb1-core-kit.conf` targets the QRB2210 —
and it independently arrived at most of the design above. What it corroborates
and what it adds:

**Corroborated.** U-Boot is the bootloader for QRB2210 there too
(`PREFERRED_PROVIDER_virtual/bootloader = "u-boot"`), flashed as the payload of
the slot Qualcomm's UEFI would otherwise occupy — `image_types_qcom.bbclass`
substitutes `u-boot-*.mbn` for `uefi.elf` when the provider is U-Boot. Note that
the _slot_ differs between the two boards: meta-qcom writes U-Boot to `uefi` for
RB1, whereas on the UNO Q the `uefi` partitions are zeroed and the ELF sits in
`abl` (see Task 2.1). Do not assume RB1's `rawprogram` mapping transfers.
`clk_ignore_unused pd_ignore_unused` appear as `KERNEL_CMDLINE_EXTRA` at
SoC-family level in `qcom-qcm2290.inc`, confirming they are a QCM2290
requirement rather than board tuning. `root=PARTLABEL=rootfs` is their default
too (`QCOM_BOOTIMG_ROOTFS`). The flash deliverable is a bundle of per-partition
images (`rootfs.img`, `efi.bin`, `dtb.bin`) plus the GPT binaries and XML — not
a whole-disk image.

**Added: A/B slot blessing.** `rb1-core-kit.conf` pulls in `qbootctl` with the
comment "tell the android bootloader to mark the boot as successful — the boot
firmware will switch to slot B and fail to boot otherwise." The stock UNO Q
image does ship it, from Debian trixie main, with `qbootctl.service` enabled and
active, running `qbootctl -m` after `boot-complete.target`. This was missing
from the `arduino-uno-q` machine and has been added; an image without it stops
booting several reboots after the fact, which presents as a brick.

Running `qbootctl` on a stock board confirms the mechanism directly: slot `_a`
reports `Active 1 / Successful 1 / Bootable 1` while `_b` reports `Successful 0`
— and that `Successful` flag is exactly what `qbootctl -m` sets. It also warns
`Couldn't find cmdline arg: 'slot_suffix'`, so the stock cmdline carries no
`androidboot.slot_suffix` and qbootctl falls back to reading the slot from the
control block. A yoe cmdline therefore does not need to supply one.

**Added: partition data belongs in its own versioned source.** The GPT binaries
and `rawprogram`/`patch` XML come from a separate repo, `qcom-ptool`, organized
per platform and storage type. Worth checking whether the UNO Q's layout can be
described there rather than hand-maintained in `module-qcom`.

**Added: the UKI approach** (see Task 4.2) and the **ModemManager conflict**
(see Task 6.2).

## Phase Overview

- **Phase 1** — Target-state reference doc. _(landed)_
- **Phase 2** — Decision gate: probe U-Boot for fastboot.
- **Phase 3** — Per-partition image output.
- **Phase 4** — A bootable ESP: UKI (or BLS entry) + DTB staging.
- **Phase 5** — Host-scoped units (new capability; `qdl` is the first consumer).
- **Phase 6** — Flash backend selected by the machine.
- **Phase 7** — Tests.
- **Phase 8** — Verify docs and code agree; changelog.

## Phase 1: Target-state docs _(landed)_

### Task 1.1: Rewrite `docs/machine-uno-q.md`

Done in this change. The doc now reads in final voice: no `(planned)` heading
suffixes, no `> Status:` blockquote. Corrections made against a stock board
(Debian 13.1 trixie, kernel `7.0.0-g122c2c22d838`):

- **Boot chain.** The previous text described `ABL → U-Boot → extlinux/sysboot`
  with a boot script setting `0xC0000000` load addresses and calling `booti` —
  that is the Armbian arrangement, not Arduino's stock image. U-Boot replaces
  Qualcomm's ABL as the final firmware stage and presents itself as UEFI 2.11;
  it chainloads systemd-boot from the ESP, which reads Boot Loader Specification
  type#1 entries. The device tree comes from U-Boot off the ESP, not from a BLS
  `devicetree` line.
- **Partition table.** 69 entries, 66 of them vendor firmware. `userdata` (p69)
  is mounted at `/home/arduino` and holds user data — not the empty tail the
  previous text described.
- **Kernel and device tree.** Arduino ships `linux-image-7.0.0-g122c2c22d838`
  (source package `linux-upstream`) carrying `qrb2210-arduino-imola*` device
  trees. The previous text named a `qcom-v6.19.0-unoq` branch and
  `qrb2210-rb1.dtb`.
- **BSP mapping.** The "A yoe BSP for this board (planned)" section is replaced
  by a description of what `module-qcom` provides.

### Task 1.2: Index

Append a row to `docs/SPEC_PLAN_INDEX.md` pointing at this plan, status
**Partial** (docs landed, code outstanding).

## Phase 2: Decision gate — does U-Boot expose fastboot?

Everything downstream is cheaper if the answer is yes, so resolve it before
building the EDL path.

### Task 2.1: Probe over the bootloader control block — no serial needed

The GPT carries `misc` (p34), the conventional home for the bootloader control
block. Write a fastboot request into it from the running system over ssh,
reboot, and watch whether the host enumerates a fastboot device
(`fastboot devices`, then `fastboot getvar partition-size:rootfs`). That answers
the question with no serial adapter and no jumper, and if it works it also
establishes the reboot-to-bootloader path a flash backend would use.

**Static inspection was tried and is inconclusive**, so do not spend more time
on it. On a stock board, `abl_a`/`abl_b` hold an ELF with no readable `U-Boot`
or `fastboot` strings — a compressed or signed payload — and `uefi_a`/`uefi_b`
are entirely zeroed, while `boot_a`/`boot_b` hold Android boot images. Grepping
the partitions cannot answer whether `CONFIG_CMD_FASTBOOT` is set.

Fall back to a serial console only if the BCB probe is inconclusive: interrupt
U-Boot at its prompt and run `help fastboot`, then `fastboot usb 0`. That needs
a **1.8 V** adapter on the JCTL header — a 3.3 V adapter can damage the SoC (see
the reference doc).

Incidental finding worth keeping: the empty `uefi` partitions plus an ELF in
`abl` corroborate that U-Boot occupies the `abl` slot on this board, rather than
being flashed as `uefi.elf` the way meta-qcom does it for RB1.

### Task 2.2: Record the answer

Write the result into the reference doc's flashing section. If fastboot works,
Phase 5 gains a `fastboot` backend and EDL becomes the recovery path; if not,
EDL is the only path and the doc says so plainly.

**Deliverable:** a documented yes/no. Do not start Phase 6 without it.

## Phase 3: Per-partition image output

### Task 3.1: A `flash` block on `machine()`

Add to `internal/starlark/builtins.go` (`fnMachine`) and `types.go`:

```python
flash = flash_config(
    method     = "qdl",                      # or "block" (today's default)
    programmer = "prog_firehose_ddr.elf",    # user-supplied, path or ""
    program_xml = "flash/rawprogram-yoe.xml",
)
```

Parse it the way `qemu_config` is parsed — a struct kwarg lifted into a
`FlashConfig` on `Machine`. `method = "block"` is the default and preserves
current behavior for every existing machine.

**Cache neutrality.** If any of this reaches the image unit's input hash
(`internal/resolve/hash.go`), gate each `fmt.Fprintf` on a non-empty check, per
the project's content-addressed-caching rule. An unconditional write invalidates
every unit's hash the moment it lands and forces a full rebuild.

### Task 3.2: Emit partition images

In `modules/module-core/classes/image.star`, `_create_disk_image_debian` already
builds each partition as a standalone file (`$DESTDIR/<name>.img.<label>.part`)
before `dd`-ing it into the disk and deleting it. For a `qdl` machine, keep
those files as `$DESTDIR/<label>.img` and skip the disk assembly, the MBR
`sfdisk` call, and the syslinux install.

### Task 3.3: Fix the multi-ext4 bug

The loop runs `mkfs.ext4 -d $DESTDIR/rootfs` for _every_ ext4 partition, so a
machine declaring two ext4 partitions gets a second full copy of the rootfs.
Only the partition marked `root = True` should be populated from the rootfs;
others should be created empty. This is why `arduino-uno-q` does not currently
model `userdata`, and it must be fixed before any machine can.

**Verification:** `yoe build base-image --machine arduino-uno-q` produces
`efi.img` and `rootfs.img` and no whole-disk `.img`. Existing machines are
byte-identical to before (compare input hashes).

## Phase 4: A bootable ESP

Flashing an ESP that boots nothing is not progress. This phase is what makes the
artifact meaningful, and it is the largest piece of work.

### Task 4.1: Populate the ESP from `/boot/efi`

Both disk creators hard-code `mcopy … $DESTDIR/rootfs/boot/*`. For a machine
whose ESP is a mounted `/boot/efi`, the source is `$DESTDIR/rootfs/boot/efi/*`.

### Task 4.2: Replace `extlinux.conf` with something systemd-boot can boot

`_write_debian_extlinux_conf` runs unconditionally on the apt path. Two ways to
give systemd-boot something to find, and meta-qcom's choice is the better one:

**Option A — a UKI (preferred).** Bundle kernel, initrd, and cmdline into one
Unified Kernel Image at `EFI/Linux/linux-<machine>.efi`. systemd-boot discovers
it with no configuration file at all: no machine-id directory, no
`loader/entries`, no `kernel-install` hooks to replicate. Debian ships
`systemd-ukify`. This is what meta-qcom does for every EFI Qualcomm machine
(`esp-qcom-image` inherits `uki` / `uki-esp-image`), and its ESP image is built
as a separate image recipe whose rootfs contains only `EFI/` — the same shape as
Task 3.2's per-partition output. SecureBoot is disabled on this board
(`SecureBoot` efivar reads 0), so the UKI needs no signing.

**Option B — BLS type#1 entries.** What the stock Arduino image uses:
`loader/entries/<machine-id>-<version>.conf` plus kernel and initrd under
`<machine-id>/<version>/`, generated on-device by Debian's `kernel-install`
hooks. Reproducing that at image-assembly time means either invoking
`kernel-install` inside the toolchain container or hand-writing the layout and
keeping the machine-id consistent with the rootfs.

Option A is less machinery and fewer moving parts, and it keeps multi-kernel
rollback (drop in a second UKI). Option B matches the stock image, which is
worth something when debugging against Arduino's own builds. Prefer A; note that
either coexists with Task 4.3, because the device tree comes from U-Boot rather
than from the kernel image.

### Task 4.3: Stage the device trees

Copy `qcom/qrb2210-arduino-imola*` from the kernel package into
`/boot/efi/dtb/qcom/`, where U-Boot and `arduino-linux-config` expect them. The
stock image mirrors the whole arm64 DTB tree; the qcom subtree is smaller and
sufficient.

### Task 4.4: `contents` on `partition()`

`partition(contents = [...])` is parsed, hashed, and exposed to Starlark but
read by neither disk creator — the vfat partition is always filled from
`rootfs/boot/*`. Either honor it (which would express Tasks 4.1 and 4.3
declaratively) or delete it. Leaving a field that silently does nothing is the
worse option.

## Phase 5: Host-scoped units

A general capability, not a flashing detail. yoe can build a package for the
target architecture and stage it into an image or a build sysroot. It cannot
build one for the **host** architecture and make it available to a build step or
to yoe itself. Every host tool today is either baked into a build container or
assumed present on the developer's machine, and neither is acceptable for a tool
that writes a board's eMMC.

### Task 5.1: `scope = "host"`

`Unit.Scope` is already the cache-keying axis — `"arch"` (default), `"machine"`,
`"noarch"`. Add `"host"`, meaning: this unit resolves, builds, and is keyed at
the **host** architecture regardless of the target. A host-scoped unit implies
`container_arch = "host"` and lands in `build/<distro>/<unit>.<host-arch>/`,
which the existing per-arch build-directory layout already accommodates.

### Task 5.2: Resolve feed lookups at the host arch

This is the one genuinely invasive piece. `Engine.activeArch` is a single value
set once per evaluation pass from the machine (`loader.go`, `SetActiveArch`),
and every feed lookup reads it (`apt/builtin.go`, `alpine/builtin.go`). A
host-scoped unit needs its lookups served at a different arch **within the same
pass** — an arm64 target with an x86_64 host must get the x86_64 `qdl`.

The feed side is already prepared for this: `archState` caches per-arch in a
`byArch` map and `cacheFor(arch)` is arch-parameterized. Only the _selection_ is
globally pinned. So the change is threading a resolution arch through `lookup()`
instead of reading the global — contained, but it touches both feed backends and
the resolver, and it needs tests proving a single pass can serve two arches.

### Task 5.3: A host sysroot, and how a build reaches it

Host-scoped deps stage into a host sysroot, separate from the target sysroot
that `AssembleSysroot` builds. A unit declares them explicitly:

```python
host_deps = ["qdl"]
```

The host sysroot's `bin` is prepended to `PATH` in the build environment. Keep
this separate from `deps` — silently mixing host and target binaries in one
sysroot is how cross-builds acquire mysterious "exec format error" failures.

**Cache neutrality.** Gate the new fields in `internal/resolve/hash.go` on
non-empty, per the content-addressed-caching rule, or every unit's hash changes
the moment this lands.

### Task 5.4: `units/qdl.star` in `module-qcom`

A host-scoped unit pulling `qdl` from `debian.main`. No build steps — it is a
prebuilt feed package; the unit exists to pin the version and stage it.

### Task 5.5: Running a host tool

yoe invokes a host tool by running it in a host-arch container with the host
sysroot mounted. Containers already run `--privileged`, so `/dev/bus/usb` is
reachable with no new plumbing. Verify two things that the design assumes rather
than proves:

- **USB re-enumeration mid-flash.** The board changes identity when the firehose
  programmer loads. Confirm the container sees the new device node — `/dev` is a
  bind mount of the host's, so it should, but this is exactly the kind of
  assumption that fails quietly.
- **Rootless podman.** Raw USB access may not survive a rootless runtime. If it
  does not, detect it and say so rather than failing inside `qdl`.

## Phase 6: Flash backend selected by the machine

### Task 6.1: Restructure `Flash()`

`internal/device/flash.go` mixes request validation with block-device I/O.
Split: resolve the image, read `machine.Flash.Method`, dispatch. The
mounted-partition and system-disk guards belong to the block path only.

### Task 6.2: The `qdl` backend

New `internal/device/flash_qdl.go`:

- Locate `qdl` on `PATH`; when absent, name the package (`apt install qdl` on
  Debian/Ubuntu) rather than reporting a bare "not found."
- Detect the board in EDL by USB VID:PID `05c6:9008`. When it is missing, say so
  in the board's terms — jumper JCTL pins 1–2, power-cycle — rather than
  reporting a generic "device not found."
- Detect the missing udev rule and print the rule to install, since
  `qdl: unable to open USB device` otherwise reads as a hardware fault.
- **Detect a running ModemManager and refuse with an explanation.** It claims
  the 9008 device and makes the flash fail for reasons that look nothing like
  the cause; meta-qcom's flashing guide leads with "make sure ModemManager is
  not running." This is the kind of failure that costs an hour if undiagnosed,
  and a one-line preflight check removes it.
- Support `--serial=<sn>` when more than one board is attached, reporting the
  serials found (`lsusb -v -d 05c6:9008`) instead of picking one arbitrarily.
- Resolve the programmer via the fetch-with-hash unit; fail clearly when unset.
- Invoke `qdl --storage emmc <programmer> <rawprogram> <patch>`, adding
  `--allow-missing` because yoe supplies only the two partitions it owns and not
  the full vendor image set.

### Task 6.3: Confirmation prompt

Overwriting a soldered eMMC deserves a prompt that names what changes. List the
partitions to be written (`efi`, `rootfs`) and state explicitly that vendor
firmware and `userdata` are untouched. Honor `--yes` and `--dry-run` as the
block path does.

### Task 6.4: `flash/rawprogram-yoe.xml` in `module-qcom`

Hand-authored, two entries, referencing `efi.img` and `rootfs.img` at the
factory GPT's offsets. Checked in next to the machine.

### Task 6.5: The fastboot backend, if Phase 2 says yes

`fastboot flash efi efi.img` / `fastboot flash rootfs rootfs.img`, with EDL
retained as the recovery path.

## Phase 7: Tests

- `fnMachine` parses `flash_config`; `method` defaults to `"block"`; existing
  machines' input hashes are unchanged.
- A single evaluation pass resolves one feed at two arches — a host-scoped unit
  gets the host-arch package while target units get the target-arch one. This is
  the regression test for Task 5.2 and the one most worth writing first.
- `scope = "host"` keys the build directory by host arch, and existing units'
  input hashes are unchanged by the new fields.
- Backend dispatch picks the right path per method.
- EDL detection and the udev-rule diagnostic, against a faked USB enumeration.
- Argument construction for `qdl` (no live device).
- `--dry-run` writes nothing.

`internal/device/boottest.go` cannot cover this board — there is no QEMU model
for the QRB2210, so end-to-end verification is manual on hardware. Say so in the
plan rather than leaving a coverage gap that looks like an oversight.

## Phase 8: Verify and close

- Flash a yoe-built image to a real board over the Phase 2 path; confirm it
  boots to a login prompt on the serial console.
- Confirm `userdata` survived and `/home/arduino` still mounts.
- Re-read `docs/machine-uno-q.md` against the shipped behavior and close any gap
  Phases 2–6 opened.
- Changelog entry: user-facing and short — what a user can now do, not the
  mechanism.
- Flip this plan's row in `docs/SPEC_PLAN_INDEX.md` to **Done**.

## Risks and open questions

- **Fastboot is unresolved** (Phase 2). It changes the shape of Phase 6 but
  nothing before it.
- **A stable URL for Arduino's programmer.** The fetch-with-hash approach needs
  one. meta-qcom pins Qualcomm's RB1 bootloader archive on
  `artifacts.codelinaro.org`, but that archive is for RB1, not the UNO Q, and
  Arduino's bundle may only be reachable through `arduino-flasher-cli`. If no
  stable URL exists, fall back to a user-supplied path.
- **A 10 GB rootfs over USB is slow.** If it proves painful, a smaller default
  rootfs partition with a first-boot resize is the usual answer — but the
  reference doc notes that resize tooling misbehaves against this layout,
  because the rootfs is followed by a populated `userdata`.
- **Phase 4 is the long pole**, and it is image-assembly work in `@module-core`,
  not board work. It benefits every UEFI/systemd-boot target, not just this one,
  which argues for doing it properly rather than special-casing one machine.
- **Phase 5 is the most invasive**, because per-arch resolution within one
  evaluation pass touches the engine, both feed backends, and the resolver. It
  is also the most reusable thing in this plan: every future host tool —
  `fastboot`, `mkbootimg`, a vendor signing utility — depends on it, so it is
  worth building as a general capability rather than a flashing special case.
- **Rootless podman may not pass through raw USB.** If it does not, the
  container execution model needs a documented fallback, and the failure has to
  be detected and explained rather than surfacing from inside `qdl`.
- **Arduino kernel updates rename the kernel package.**
  `linux-image-7.0.0-g<hash>` is pinned by the machine; `yoe update-feeds`
  surfaces the new name and the machine must move with it.
