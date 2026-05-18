# Raspberry Pi BSP Module (module-rpi)

**Date:** 2026-03-31 **Status:** Draft

## Problem

Yoe-NG has no support for physical hardware — only QEMU virtual machines.
Raspberry Pi 4 and 5 are the most common ARM64 boards for edge products. Adding
a BSP module enables building bootable SD card images for real hardware.

## Solution

A new `module-rpi` module providing machine definitions, a Raspberry Pi kernel
fork, GPU firmware, boot configuration, and a bootable image definition for RPi
4 and RPi 5.

## Design Decisions

- **RPi 4 and RPi 5 only** — the two most relevant current boards. RPi 3, Zero,
  and older boards can be added later.
- **Direct kernel boot (no u-boot)** — the GPU loads the kernel directly from
  the FAT32 boot partition. Simpler and matches how most RPi deployments work.
  U-Boot can be added later for A/B update workflows.
- **RPi kernel fork** — `github.com/raspberrypi/linux` has the most complete
  hardware support, device trees, and defconfigs for these boards. Mainline
  kernel support can be added as an alternative later.
- **WiFi/Bluetooth deferred** — Ethernet works without firmware blobs. WiFi/BT
  firmware units can be added later without changing the core BSP.
- **Separate kernel units per board** — `linux-rpi4` and `linux-rpi5` rather
  than one kernel with runtime selection. Different defconfigs
  (`bcm2711_defconfig` vs `bcm2712_defconfig`) and different device trees.

## Module Structure

```
modules/module-rpi/
├── MODULE.star
├── machines/
│   ├── raspberrypi4.star
│   └── raspberrypi5.star
├── units/
│   └── bsp/
│       ├── linux-rpi4.star
│       ├── linux-rpi5.star
│       ├── rpi-firmware.star
│       └── rpi-config.star
└── images/
    └── rpi-image.star
```

## Units

### linux-rpi4

Raspberry Pi 4 kernel from the RPi foundation fork.

- **Source:** `https://github.com/raspberrypi/linux.git`
- **Branch/tag:** `rpi-6.12.y` (or latest stable)
- **Defconfig:** `bcm2711_defconfig`
- **Arch:** arm64 (`ARCH=arm64`)
- **Image:** `arch/arm64/boot/Image`
- **Device trees:** `bcm2711-rpi-4-b.dtb`, `bcm2711-rpi-400.dtb`,
  `bcm2711-rpi-cm4.dtb`
- **Install:** kernel Image to `$DESTDIR/boot/kernel8.img`, DTBs to
  `$DESTDIR/boot/`

Build steps:

```sh
make ARCH=arm64 bcm2711_defconfig
make ARCH=arm64 -j$NPROC Image dtbs
install -D arch/arm64/boot/Image $DESTDIR/boot/kernel8.img
install -D arch/arm64/boot/dts/broadcom/bcm2711-rpi-4-b.dtb $DESTDIR/boot/bcm2711-rpi-4-b.dtb
# ... additional DTBs
```

### linux-rpi5

Raspberry Pi 5 kernel from the RPi foundation fork.

- **Source:** `https://github.com/raspberrypi/linux.git`
- **Branch/tag:** `rpi-6.12.y` (same repo, same branch as rpi4)
- **Defconfig:** `bcm2712_defconfig`
- **Arch:** arm64 (`ARCH=arm64`)
- **Image:** `arch/arm64/boot/Image`
- **Device trees:** `bcm2712-rpi-5-b.dtb`, `bcm2712-rpi-cm5-cm5io.dtb`
- **Install:** kernel Image to `$DESTDIR/boot/kernel_2712.img`, DTBs to
  `$DESTDIR/boot/`

Build steps:

```sh
make ARCH=arm64 bcm2712_defconfig
make ARCH=arm64 -j$NPROC Image dtbs
install -D arch/arm64/boot/Image $DESTDIR/boot/kernel_2712.img
install -D arch/arm64/boot/dts/broadcom/bcm2712-rpi-5-b.dtb $DESTDIR/boot/bcm2712-rpi-5-b.dtb
```

### rpi-firmware

GPU firmware blobs required for the RPi to boot. The GPU reads these from the
FAT32 boot partition before the ARM core starts.

- **Source:** `https://github.com/raspberrypi/firmware.git` (or release tarball)
- **Files:** `start4.elf`, `start4x.elf`, `fixup4.dat`, `fixup4x.dat`,
  `bootcode.bin` (RPi4 only — RPi5 has firmware in EEPROM)
- **Install:** all firmware files to `$DESTDIR/boot/`

Build steps (no compilation — just install prebuilt blobs):

```sh
install -D boot/start4.elf $DESTDIR/boot/start4.elf
install -D boot/start4x.elf $DESTDIR/boot/start4x.elf
install -D boot/fixup4.dat $DESTDIR/boot/fixup4.dat
install -D boot/fixup4x.dat $DESTDIR/boot/fixup4x.dat
```

### rpi-config

Generates `config.txt` and `cmdline.txt` for the boot partition.

No source — a shell script that writes the files based on board type. The
`$MACHINE` environment variable (or a board-specific env var) determines which
config to generate.

**config.txt** (RPi 4 example):

```
# RPi 4 boot config
arm_64bit=1
enable_uart=1
dtoverlay=vc4-kms-v3d
kernel=kernel8.img
```

**config.txt** (RPi 5 example):

```
# RPi 5 boot config
arm_64bit=1
enable_uart=1
dtoverlay=vc4-kms-v3d-pi5
kernel=kernel_2712.img
```

**cmdline.txt:**

```
console=ttyS0,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait rw
```

(RPi 5 uses `ttyAMA10` instead of `ttyS0`)

Since `rpi-config` needs to know which board it targets, and both RPi 4 and 5
use arm64, the `$ARCH` variable alone is insufficient. Options:

- **Two config units:** `rpi4-config` and `rpi5-config` (simplest, matches the
  two-kernel approach)
- **Single unit with a MACHINE variable:** requires plumbing a new env var

Recommendation: **two config units** (`rpi4-config`, `rpi5-config`) for
consistency with the kernel units.

## Machines

### raspberrypi4

```python
machine(
    name = "raspberrypi4",
    arch = "arm64",
    description = "Raspberry Pi 4 Model B",
    kernel = kernel(
        unit = "linux-rpi4",
        defconfig = "bcm2711_defconfig",
        cmdline = "console=ttyS0,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait rw",
    ),
)
```

### raspberrypi5

```python
machine(
    name = "raspberrypi5",
    arch = "arm64",
    description = "Raspberry Pi 5",
    kernel = kernel(
        unit = "linux-rpi5",
        defconfig = "bcm2712_defconfig",
        cmdline = "console=ttyAMA10,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait rw",
    ),
)
```

## Image

### rpi-image

Bootable SD card image with two partitions:

```python
image(
    name = "rpi-image",
    version = "1.0.0",
    description = "Minimal bootable Raspberry Pi image",
    artifacts = [
        "base-files",
        "busybox",
        "musl",
    ] + (["linux-rpi4", "rpi4-config"] if MACHINE == "raspberrypi4"
         else ["linux-rpi5", "rpi5-config"])
    + ["rpi-firmware"],
    hostname = "yoe-rpi",
    partitions = [
        partition(label="boot", type="vfat", size="64M",
                 contents=["rpi-firmware", "linux-rpi4", "rpi4-config"]),
        partition(label="rootfs", type="ext4", size="256M", root=True),
    ],
)
```

Note: The `contents` field on the boot partition is new — the image assembly
code currently doesn't support populating non-root partitions. This requires a
Go code change to the image assembly (`internal/image/disk.go`).

### Partition Contents (New Concept)

The `contents` field on a partition lists units whose `$DESTDIR/boot/` files
should be copied to that partition. For the FAT32 boot partition:

1. Image assembly creates the FAT32 filesystem
2. For each unit in `contents`, copies files from
   `build/<arch>/<unit>/destdir/boot/` into the FAT32 partition
3. This populates the boot partition with firmware, kernel, DTBs, and config

This is the main Go code change required beyond the Starlark module files.

## Go Code Changes Required

### 1. Partition contents support in image assembly

`internal/image/disk.go` needs to support the `contents` field on partitions:

- When creating a non-root partition with `contents`, copy files from each
  listed unit's `destdir/boot/` into the partition after formatting
- For `vfat` partitions, use `mcopy` (from `mtools`, already in the container)
  to copy files into the FAT32 image without mounting

### 2. MACHINE variable in Starlark

Add `MACHINE` as a predeclared Starlark variable (like `ARCH`), set from the
default machine name. This allows image definitions to conditionally include
board-specific units.

### 3. Partition type in types.go

The `Partition` struct already has a `Type` field and `Contents` field. Verify
`Contents` is wired through to the image assembly code. If not, add it.

## Module Reference

Projects reference the module in `PROJECT.star`:

```python
module("https://github.com/YoeDistro/yoe-ng.git",
      ref = "main",
      path = "modules/module-rpi")
```

## What's Not Included (Future Work)

- WiFi/Bluetooth firmware (BCM43455/BCM4345c0)
- U-Boot bootloader support
- RPi 3, Zero, Compute Module support
- Device tree overlays (audio HATs, displays, etc.)
- EEPROM update tooling (RPi4/5)
- GPU userland libraries
- Camera support (libcamera, imx219/imx477/imx708)
