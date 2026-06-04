# QEMU Machines

yoe ships two QEMU machines that serve as the default development and CI
targets: `qemu-arm64` and `qemu-x86_64`. Neither corresponds to physical
hardware — both target QEMU's emulated `virt`/`q35` machines and exist to let
you iterate on userspace and the kernel without booting real silicon each time.

This page covers what each machine ships, how the boot path differs between
them, and what `yoe qemu` actually does at run time.

Machine descriptors live at:

- `modules/module-core/machines/qemu-arm64.star`
- `modules/module-core/machines/qemu-x86_64.star`

Both lean entirely on `module-core` and `module-alpine` — no board-specific BSP
units.

## Comparison at a glance

| Aspect           | `qemu-arm64`                      | `qemu-x86_64`                |
| ---------------- | --------------------------------- | ---------------------------- |
| Arch             | `arm64`                           | `x86_64`                     |
| QEMU machine     | `virt`                            | `q35`                        |
| CPU              | `host`                            | `host`                       |
| Firmware         | none (direct kernel boot)         | `seabios` (QEMU default)     |
| Bootloader       | none — QEMU `-kernel`             | `syslinux` in the rootfs     |
| Console          | `ttyAMA0` (PL011 UART)            | `ttyS0` (16550 UART)         |
| Root device      | `/dev/vda1` (single part)         | `/dev/vda2`                  |
| Kernel unit      | `linux` (generic)                 | `linux` (`x86_64_defconfig`) |
| Extra packages   | none                              | `syslinux`                   |
| Default forwards | `2222:22`, `8080:80`, `8118:8118` | same                         |

Both default to 4 GB RAM and `display = "none"`; the `-nographic` flag sends
serial to the controlling terminal. 4 GB is the floor for memory-heavy unit
builds inside the guest — the kernel link step alone needs well over 1 GB, so a
self-hosted `yoe build` of `linux` is OOM-killed on a smaller VM.

Pass `--display` to `yoe run` (e.g. `yoe run qt-image --display`) to drop
`-nographic` and let QEMU open its native window for the guest framebuffer. The
launcher attaches a virtio-vga adapter for the DRM virtio-gpu driver and keeps
serial multiplexed onto host stdio so kernel logs still appear in the terminal
that started the run. The kernel's `graphics.cfg` fragment turns on the relevant
FB/DRM bits (`DRM_VIRTIO_GPU`, `DRM_BOCHS`, `FB_VESA`, `FB_EFI`,
`DRM_FBDEV_EMULATION`), so `/dev/fb0` is present from the first boot — needed by
linuxfb-backed UIs like the `qt-image` demo.

## qemu-arm64

```python
machine(
    name = "qemu-arm64",
    arch = "arm64",
    kernel = kernel(
        unit = "linux",
        defconfig = "defconfig",
        cmdline = "console=ttyAMA0 root=/dev/vda1 rw",
    ),
    partitions = [
        partition(label = "rootfs", type = "ext4", size = "512M", root = True),
    ],
    qemu = qemu_config(
        machine = "virt", cpu = "host", memory = "4G",
        display = "none",
        ports = ["2222:22", "8080:80", "8118:8118"],
    ),
)
```

There is no bootloader and no boot partition. `yoe qemu` invokes QEMU with
`-kernel <vmlinuz>` taken from the built image's own `/boot`, and passes the
machine's `cmdline` via `-append`. The kernel is whichever one the image ships:
Alpine installs `/boot/vmlinuz` and mounts the rootfs directly, while Debian
installs a versioned `/boot/vmlinuz-<ver>` alongside an `initrd.img-<ver>` that
QEMU also receives via `-initrd`. QEMU loads these straight into emulated DRAM
on the `virt` machine and starts the A53 cores at the kernel entry point.

This is the one place in yoe where direct-kernel boot is the correct path, not a
shortcut. The `virt` machine has no analog in physical silicon — there is no
ROM, no SPL, no need for U-Boot. (For physical aarch64 boards, see
[BeaglePlay](machine-beagleplay.md) for the full ROM → SPL → TF-A → U-Boot →
kernel chain.)

The single ext4 partition becomes `/dev/vda1` through QEMU's virtio-blk disk.
The disk is presented to the guest as a raw image file, attached with
`-drive file=...,format=raw,if=virtio`.

## qemu-x86_64

```python
machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    kernel = kernel(
        unit = "linux",
        defconfig = "x86_64_defconfig",
        cmdline = "console=ttyS0 root=/dev/vda2 rw",
    ),
    packages = ["syslinux"],
    partitions = [
        partition(label = "rootfs", type = "ext4", size = "600M", root = True),
    ],
    qemu = qemu_config(
        machine = "q35", cpu = "host", memory = "4G",
        firmware = "seabios",
        display = "none",
        ports = ["2222:22", "8080:80", "8118:8118"],
    ),
)
```

x86_64 goes through a real bootloader: SeaBIOS (QEMU's built-in legacy BIOS,
used by default on `q35`) reads the MBR off the virtio disk and jumps into
`syslinux`, which loads the kernel from the ext4 rootfs.

This mirrors how a physical x86 board with legacy BIOS boots, so the same image
will also boot on bare metal that lacks UEFI. (UEFI/OVMF support is set up in
`internal/device/qemu.go` — pass `firmware = "ovmf"` instead to swap SeaBIOS for
OVMF and boot via EFI.)

Why `root=/dev/vda2` when there's only one partition declared? syslinux
installation inserts its own boot sector ahead of the data partition, so the
visible partition index starts at 2 once the image is on disk. The rootfs is
still that single ext4 — it just lives at `vda2` from Linux's view.

## What runs inside the guest

Both machines pick up the generic `linux` unit from `module-core`, not a
board-specific kernel. That unit builds `arch/<arch>/boot/{Image,bzImage}` plus
the standard module set; no out-of-tree drivers, no custom defconfig fragment.

The userspace stack is whatever the project includes via its package list plus
the rootfs base — busybox, OpenRC, apk-tools, and any apks pulled through
`module-alpine`. See [libc, init, and the Rootfs Base](libc-and-init.md) for the
userspace layout.

## Networking

`yoe qemu` wires a single virtio-net device through QEMU's user-mode networking
(SLIRP). The default forwards in the machine descriptor land SSH on host port
2222 and a couple of HTTP ports for app dev. Extra forwards can be passed on the
CLI (`yoe run --port 9000:9000`, repeatable). A `--port` entry whose **guest**
port matches a machine forward replaces that forward; an entry with a new guest
port is appended.

That replace-on-match behavior is what makes `--port` usable for qemu-in-qemu —
see [Running inside a QEMU guest](#running-inside-a-qemu-guest-qemu-in-qemu)
below.

## Tuning at run time

Three knobs override the machine descriptor for a given developer without
editing checked-in `.star` files:

| Knob     | Local override        | CLI flag      | Persisted by       |
| -------- | --------------------- | ------------- | ------------------ |
| RAM      | `qemu_memory = "8G"`  | `--memory 8G` | `--memory` and TUI |
| Display  | `qemu_display = "on"` | `--display`   | TUI                |
| Forwards | `qemu_ports = [...]`  | `--port h:g`  | TUI                |

All three live in `local.star` and apply the next time you run the same image.
The TUI editor is on **Setup → QEMU settings** (press `s`, move down to **QEMU
settings**, press Enter). Local-override forwards layer over the machine's
defaults with the same replace-on-guest-port rule that `--port` uses; the order
at run time is machine ← `local.star` ← CLI, so a one-off `--port` still beats a
saved entry for the same guest port.

## How `yoe qemu` runs

The launcher in `internal/device/qemu.go`:

1. Picks the binary by arch: `qemu-system-aarch64`, `qemu-system-x86_64`, or
   `qemu-system-riscv64`.
2. Builds the arg list: `-machine`, `-cpu`, `-m`, `-nographic` by default (or
   `-device virtio-vga -serial mon:stdio` when `yoe run --display` is set, which
   lets QEMU open its native window and still leaves the serial console muxed
   onto host stdio), the virtio-blk drive, the virtio-net device with port
   forwards, and `-bios` if a firmware (OVMF/AAVMF) is set. On a same-arch host
   it adds `-enable-kvm` when `/dev/kvm` is present; when it is not (notably
   qemu-in-qemu without nested virtualization) it drops KVM, downgrades a `host`
   CPU to `max`, and runs under TCG software emulation instead — slower, but it
   still boots.
3. If the machine has no `firmware`, appends
   `-kernel <vmlinuz> -append <cmdline>` for the direct-boot path (this is what
   qemu-arm64 uses), taking the kernel from the built image's `/boot` and adding
   `-initrd <initrd.img>` when the image ships one (e.g. Debian).
4. Tries host QEMU first; falls back to running QEMU inside the `toolchain-musl`
   container with the project bind-mounted at `/project` if the host doesn't
   have it installed.

The image yoe passes is whatever the assembly step produced, attached
read-write. Restart-and-iterate workflows: rebuild, then re-run `yoe qemu` — the
image is regenerated, the guest starts clean.

## When to use which

- **qemu-x86_64** is the right default for most development. KVM acceleration on
  an x86 host is essentially native speed; the boot path matches legacy-BIOS
  bare metal so what you debug here is what runs on similar hardware.
- **qemu-arm64** is for catching arch-specific bugs (byte ordering, alignment,
  ARM64-only paths in code) without finding a board. It runs under TCG (software
  emulation) on x86 hosts, which is slow but faithful. On aarch64 hosts (an
  Apple Silicon Mac, an Ampere server) it uses KVM and is fast.
- For anything physical-board-shaped — secure boot, vendor blobs, display, real
  I/O — use the actual board's machine descriptor.

## Running inside a QEMU guest (qemu-in-qemu)

`yoe run` works from within a guest that is itself running under QEMU — useful
for exercising a self-hosted `yoe` build. Two things differ from a run on the
bare host, and `yoe run` handles both:

1. **Port forwards collide.** The outer guest already holds the machine's
   default host forwards (`2222`, `8080`, `8118`), so a nested run cannot bind
   them. Remap the host side with `--port`; an entry whose guest port matches a
   default forward replaces it:

   ```sh
   yoe run base-image --port 12222:22 --port 18080:80 --port 18118:8118
   ```

2. **No KVM.** A guest has no `/dev/kvm` unless its host was started with nested
   virtualization. `yoe run` detects this and falls back to TCG software
   emulation automatically — it prints `using TCG software emulation (slower)`
   and boots. No flag is needed; expect roughly a 10–20× slowdown versus KVM.

To get full-speed nested runs instead of TCG, enable nested virtualization on
the bare-metal host (`kvm_intel`/`kvm_amd` module option `nested=1`) and start
the outer guest with a passthrough CPU so `/dev/kvm` appears inside it.
