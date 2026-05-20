# Raspberry Pi BSP (RPi4, RPi5)

yoe supports the Raspberry Pi 4 (BCM2711) and the Raspberry Pi 5 (BCM2712)
through the `raspberrypi4` and `raspberrypi5` machines. Both target the
64-bit application cores and share the same firmware unit, the same
config-file mechanism, and the same partition layout — they differ mainly
in which kernel image and DTB land on the boot partition.

Machine descriptors:

- `modules/module-bsp/machines/raspberrypi4.star`
- `modules/module-bsp/machines/raspberrypi5.star`

Units under `modules/module-bsp/units/bsp/`:

- `rpi-firmware` — shared GPU bootloader blobs
- `linux-rpi4`, `linux-rpi5` — per-board kernel builds
- `rpi4-config`, `rpi5-config` — per-board `config.txt` + `cmdline.txt`

## The Raspberry Pi boot chain

Raspberry Pi boards do not use a conventional CPU-side bootloader. The
boot sequence is GPU-first:

```
RPi4:  GPU ROM ─ reads ─→ bootcode.bin (SD) ─ loads ─→ start4.elf ─ reads ─→ config.txt + kernel + DTB ─ starts ARM cores
RPi5:  EEPROM  ────────────────────────────────────────────→ (same flow, no bootcode.bin, kernel_2712.img)
```

The VideoCore GPU is the first thing alive on the SoC. On RPi4, the
on-board ROM is minimal and reads `bootcode.bin` from the SD card to bring
the rest of the GPU firmware online. On RPi5, all that early code lives
in an EEPROM on the board, so there is no `bootcode.bin` on the SD —
the GPU goes straight from EEPROM to reading `config.txt`.

From there the flow is identical on both:

1. GPU firmware (`start4.elf` on RPi4, the EEPROM image on RPi5) parses
   `config.txt`.
2. It loads the kernel image named by `config.txt`'s `kernel=` line plus
   the matching DTB.
3. It reads `cmdline.txt` and passes it as the kernel command line.
4. It releases the ARM cores at the kernel entry point.

There is no U-Boot, no SPL, no TF-A in this chain by default. (You can
chain-load U-Boot from the GPU firmware if you want EFI semantics, but
yoe doesn't.)

## Shared units

### `rpi-firmware`

```python
unit(
    name = "rpi-firmware",
    source = "https://github.com/raspberrypi/firmware.git",
    tag = "1.20250305",
)
```

Prebuilt blobs only — no compilation. Installs the GPU firmware files the
RPi family needs on the FAT boot partition:

| File              | Used by | Purpose                                |
| ----------------- | ------- | -------------------------------------- |
| `bootcode.bin`    | RPi4    | first-stage GPU loader (RPi5 in EEPROM) |
| `start4.elf`      | RPi4    | main GPU firmware (also `start4x.elf`, `start4cd.elf`, `start4db.elf` variants) |
| `fixup4.dat`      | RPi4    | memory split / DRAM tuning (matching `4x`, `4cd`, `4db` variants) |

RPi5 doesn't need any of these on the SD card (the EEPROM ships them),
but yoe stages them anyway — installed packages are uniform across both
boards and the extra ~10 MB on the FAT partition is harmless.

### `linux-rpi4` / `linux-rpi5`

Both kernels build from `github.com/raspberrypi/linux` on branch
`rpi-6.12.y` — the Raspberry Pi Foundation's downstream tree carrying
Broadcom GPU drivers, the wireless stack, and out-of-tree patches that
aren't yet in mainline.

The two units differ in defconfig and output naming:

| Aspect           | `linux-rpi4`              | `linux-rpi5`               |
| ---------------- | ------------------------- | -------------------------- |
| SoC              | BCM2711                   | BCM2712                    |
| `defconfig`      | `bcm2711_defconfig`       | `bcm2712_defconfig`        |
| Kernel filename  | `kernel8.img`             | `kernel_2712.img`          |
| DTBs installed   | `bcm2711-rpi-4-b.dtb`, `bcm2711-rpi-400.dtb`, `bcm2711-rpi-cm4.dtb` | `bcm2712-rpi-5-b.dtb` |

Both run a defconfig merge step that folds in `container.cfg` — a small
fragment that enables overlayfs, cgroups v2, netfilter, namespaces, and
the eBPF cgroup hooks needed to make Docker / Podman / runc work out of
the box. The same fragment is also used by `linux-beagleplay`; see
[BeaglePlay](machine-beagleplay.md) for the parallel.

Overlays go to `/boot/overlays/*.dtbo`. Kernel modules install into the
rootfs under `/lib/modules/<kver>/`, with `DEPMOD=true` skipping depmod
at build time (the build container doesn't have it; the target runs
`depmod -a` at first boot via OpenRC).

### `rpi4-config` / `rpi5-config`

Two-file boot config: `config.txt` for the GPU firmware, `cmdline.txt`
for the kernel.

**`config.txt` (RPi4 / RPi5 differences):**

```
# RPi4
arm_64bit=1
enable_uart=1
kernel=kernel8.img
dtoverlay=vc4-kms-v3d
disable_splash=1
```

```
# RPi5
arm_64bit=1
enable_uart=1
kernel=kernel_2712.img
dtoverlay=vc4-kms-v3d-pi5
disable_splash=1
```

- `arm_64bit=1` flips the GPU firmware into 64-bit kernel mode (it
  defaults to 32-bit for legacy compatibility).
- `enable_uart=1` brings up the mini-UART on RPi4 / the PL011 on the GPIO
  header so you get a serial console.
- `kernel=` matches what the per-board kernel unit installed.
- `dtoverlay=vc4-kms-v3d` selects the modern KMS DRM driver for the
  VideoCore GPU (the `-pi5` variant on RPi5 targets VC6 / RP1).
- `disable_splash=1` skips the rainbow boot logo.

**`cmdline.txt`:**

```
RPi4: console=ttyS0,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait rw
RPi5: console=ttyAMA10,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait rw
```

- RPi4 uses the BCM2711 mini-UART, exposed as `ttyS0`.
- RPi5 uses a different UART (PL011 routed differently in the BCM2712
  GPIO multiplexer), exposed as `ttyAMA10`.
- Both root from `/dev/mmcblk0p2` — second partition on the SD card.

## Image assembly

Both machines use the same partition layout:

```python
partitions = [
    partition(label = "boot",   type = "vfat", size = "64M",
              contents = ["kernel", "dtbs", "firmware"]),
    partition(label = "rootfs", type = "ext4", size = "1G", root = True),
]
```

The `contents` patterns are name-based selectors that map to file globs
under `/boot/` in the assembled rootfs:

| Selector   | Matches                                        |
| ---------- | ---------------------------------------------- |
| `kernel`   | `kernel8.img` / `kernel_2712.img` / etc.       |
| `dtbs`     | `*.dtb`, `*.dtbo` (including `overlays/`)      |
| `firmware` | `bootcode.bin`, `start4*.elf`, `fixup4*.dat`   |

The `config.txt` and `cmdline.txt` written by the per-board config unit
land in `/boot/` too and are matched by the firmware/dtbs/kernel
selectors as appropriate.

SD card layout the GPU expects:

| Partition | Type | Contents                                                       |
| --------- | ---- | -------------------------------------------------------------- |
| 1         | vfat | Firmware blobs, kernel, DTBs, overlays, `config.txt`, `cmdline.txt` |
| 2         | ext4 | Linux rootfs (musl + busybox + OpenRC + apps + modules)        |

The GPU firmware doesn't care about partition labels or GPT — it reads
the first FAT partition off the MMC. Linux mounts the same FAT at
`/boot` and uses partition 2 as `/`.

## What's the same and what differs across boards

**Shared:**

- Same upstream kernel tree, same branch, same container.cfg fragment.
- Same `rpi-firmware` package (RPi5 ignores the SD copies but they're
  harmless).
- Same partition layout and root device.
- Same OpenRC / busybox / apk userspace.

**Per-board:**

- `linux-rpi4` vs `linux-rpi5` (defconfig, kernel image name, DTBs).
- `rpi4-config` vs `rpi5-config` (kernel image name in `config.txt`, KMS
  overlay variant, serial console device).
- The machine descriptor (which kernel unit to use, which config unit).

If you're adding a Raspberry Pi 3 or Pi Zero 2 W, the work is mostly
mechanical: clone the per-board kernel + config unit, swap defconfig and
DTB names, and add a machine descriptor. The firmware unit and the
partition layout don't need to change.

## When something fails

- **Rainbow screen, no kernel boot.** GPU firmware loaded but couldn't
  find the kernel. Check `config.txt`'s `kernel=` line and confirm the
  named file is on the FAT partition.
- **Black screen, never sees UART.** `enable_uart=1` missing from
  `config.txt`, or the wrong `console=` in `cmdline.txt` for the board.
- **Kernel boots but no rootfs.** SD card not the only block device the
  kernel sees, or `rootwait` not in the cmdline — partition probing can
  race the kernel.
- **WiFi / Bluetooth missing.** The Foundation kernel pulls in
  `brcmfmac` firmware blobs that aren't yet in this BSP. Add them via a
  separate unit if needed; the `linux-firmware` tree on the Foundation
  GitHub has them under `brcm/`.
