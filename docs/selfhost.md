# Self-Host Builds

`selfhost-image` turns a QEMU (and soon Raspberry Pi 5) into a standalone
`[yoe]` build host. Flash it to a microSD or NVMe SSD, power on, log in, and you
can `yoe-init` or `git clone` a yoe project and run `yoe build` on the device —
no workstation in the loop. The image includes the yoe CLI, Go, Docker, git,
bubblewrap, and the dev-image tool set (helix, yazi, zellij, openssh, bash,
htop, strace).

This is the developer's dogfood path. The same yoe build flow that runs on an
x86_64 workstation runs on the RPi5 on top of a yoe image, only natively: ARM64
builds without QEMU emulation.

_Note, eventually we plan to deploy individual units to a remote ARM native
runner. For now, running the `yoe build` on an ARM system is the fastest way to
do ARM builds._

## What's in the image

- **`yoe` CLI** — the build tool itself, installed as an apk. Upgrade later with
  `apk upgrade yoe` once a feed is reachable.
- **Docker** — engine, CLI, buildx, containerd, runc, libseccomp, iptables,
  ca-certificates. `dockerd` starts at boot via OpenRC.
- **Go toolchain** — for building yoe and any Go-based units.
- **git, bubblewrap** — clone modules and sources; bwrap is what yoe uses to
  sandbox per-unit builds inside the build container.
- **Developer tools** — helix (editor), yazi (file browser), zellij (terminal
  multiplexer), bash, htop, strace, less, file, curl, ssh.
- **First-boot rootfs grow** — partition 2 expands to fill the SD/NVMe on the
  first boot, then disables itself.

## Recommended hardware

- **Raspberry Pi 5**, 8 GB or 16 GB. 4 GB will work but feels cramped once
  Docker images start filling memory.
- **NVMe boot via the official PCIe HAT** is the happy path. Builds run 10–20×
  faster than from a microSD card. The image enables `dtparam=nvme` in
  `config.txt` so an NVMe HAT enumerates automatically.
- **microSD** works for a quick demo. Plan to wait for builds.
- **A 27 W USB-PD power supply** is required for NVMe HAT users.

## First flash

Build the image from a workstation (the cross-arch QEMU path makes this
straightforward):

```
yoe build --machine raspberrypi5 selfhost-image
```

Flash it:

```
yoe flash --machine raspberrypi5 selfhost-image /dev/sdN
```

Or via the TUI: open `yoe`, highlight `selfhost-image`, press `f`, pick the
target device.

## First boot

1. Insert the card / NVMe, connect serial + power, power on.
2. The serial console shows the kernel boot, then `dockerd` starting, then a
   login prompt.
3. Log in as `user` / `password`. **Change the password immediately**, add an
   SSH public key, and disable password auth before connecting the device to any
   network you don't fully control. The same security note applies as for
   [the other RPi images](machine-rpi.md#first-boot).
4. The rootfs grows to fill the device on first boot. `df -h /` shows the new
   size.

## Booting from NVMe

After flashing to an NVMe drive (with the SSD installed via the PCIe HAT):

1. Edit `/boot/cmdline.txt` on the NVMe's boot partition, change
   `root=/dev/mmcblk0p2` to `root=/dev/nvme0n1p2`.
2. Boot. The first-boot grow service expands the NVMe's rootfs partition to fill
   the SSD.

`dtparam=nvme` is already in `config.txt`, so the PCIe controller is enabled
regardless of boot medium.

## Day-to-day workflow

```
# In ~/projects on the RPi5
yoe init my-product
cd my-product
# Edit PROJECT.star and unit files with helix
yoe build base-image
```

The first build pulls module clones into `cache/modules/` and the toolchain
Docker image (~1 GB). Subsequent builds hit the Docker image cache and the
per-unit content-addressed cache.

`yoe build yoe` rebuilds the yoe binary on-device; `apk upgrade yoe` from the
project's own feed replaces the baked-in copy without a reboot.

`yoe flash` and `yoe deploy` work from the RPi5 the same way they work from a
workstation — flash an SD to a separate target device, or `yoe deploy` to a
running yoe device on the same network.

## Storage and resource notes

- The rootfs partition is 2 GB at flash time, then grown to fill the underlying
  device on first boot. (Kept tight at flash time so `yoe flash` doesn't have to
  write 4 GB of mostly-zero blocks to a microSD card — `flash_write.go` is not
  sparse-aware yet.)
- Docker's storage (`/var/lib/docker`) lives on the rootfs. No separate data
  partition.
- The project tree, build cache, and per-unit Docker images all live on the same
  partition. An 8 GB SD will run out of room on non-trivial projects; an NVMe
  SSD with 64+ GB is the realistic target.
- RAM matters more than people expect — concurrent unit builds (kernel, llvm)
  push past 4 GB. 8 GB is the comfortable minimum; 16 GB is generous.

## What's not in v1

- No multi-arch builds **from** the RPi5. To build x86_64 or RISC-V on the
  device, install `qemu-user-static` and register `binfmt` handlers yourself;
  baking this into the image is a follow-up.
- No A/B partitions or read-only rootfs.
- No headless installer or kiosk variant — serial console + ssh only.
- The same image is not yet validated on the RPi4, though the manifest should
  work with a `linux-rpi4` swap.

## Related docs

- [Raspberry Pi BSP](machine-rpi.md) — boot chain, flashing, serial console
  wiring, the underlying RPi4/RPi5 BSP details.
- [Containers on yoe Images](containers.md) — kernel config prerequisites and
  the future source-built container stack design.
- [Feed Server and yoe deploy](feed-server.md) — how `apk upgrade yoe` works
  once a feed is reachable.
- Specification:
  [2026-05-21-selfhost-rpi5-builds](specs/2026-05-21-selfhost-rpi5-builds.md)
