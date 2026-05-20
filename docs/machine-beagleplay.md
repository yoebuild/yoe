# BeaglePlay BSP

This page documents the BeaglePlay board support in yoe: which units make up the
BSP, how they cooperate at build time, and how the resulting artifacts are
arranged on the SD card or eMMC.

The hardware is BeagleBoard.org's BeaglePlay, built on TI's AM625 SoC (quad
Cortex-A53 application cores, a Cortex-R5F MCU island, and a Cortex-M4F dead-man
core). The cores run with help from TI's K3 security firmware ("TIFS" / "SYSFW")
and a small device-manager ("DM") payload — both shipped as signed blobs by TI.

The machine descriptor lives at `modules/module-bsp/machines/beagleplay.star`;
everything below is built by units under `modules/module-bsp/units/bsp/`.

## Boot chain at a glance

The AM625 boot ROM expects a multi-stage handoff. Each blob feeds the next, and
most of the blobs are themselves FIT images that bundle code from several
upstream projects.

```
ROM (AM625)
  └── tiboot3.bin                  ← U-Boot R5 SPL + TIFS + DM
        └── tispl.bin              ← U-Boot A53 SPL + BL31 (TF-A) + BL32 (OP-TEE) + DM
              └── u-boot.img       ← U-Boot proper (A53)
                    └── Image      ← Linux kernel (arm64) + DTB
                          └── init ← OpenRC (busybox PID 1)
```

Each arrow is "loads and jumps to". Two CPU clusters take turns: the R5F brings
the secure firmware up first, then hands the SoC over to the A53s for the rest
of the chain.

## The units

| Unit                   | Produces                                                     | Stage        | Notes                         |
| ---------------------- | ------------------------------------------------------------ | ------------ | ----------------------------- |
| `ti-linux-firmware`    | `/lib/firmware/{ti-sysfw,ti-dm,...}/`                        | binman input | TI blobs, no compile          |
| `u-boot-beagleplay-r5` | `boot/tiboot3.bin`                                           | ROM →        | R5F SPL, embeds TIFS + DM     |
| `tfa-k3`               | `/lib/firmware/bl31.bin`                                     | binman input | EL3 secure monitor            |
| `optee-k3`             | `/lib/firmware/bl32.bin`                                     | binman input | Trusted Execution Environment |
| `u-boot-beagleplay`    | `boot/tispl.bin`, `boot/u-boot.img`                          | tiboot3 →    | A53 SPL + U-Boot proper       |
| `linux-beagleplay`     | `boot/Image`, `boot/k3-am625-beagleplay.dtb`, kernel modules | u-boot.img → | Beagle's 6.12 fork            |
| `beagleplay-config`    | `boot/uEnv.txt`                                              | u-boot reads | bootargs + boot script        |

Sources and pinning:

- **`ti-linux-firmware`** — `git://git.ti.com/...` branch `ti-linux-firmware`,
  prebuilt blobs only. Cadence/PRU/etc. also ship through this unit so other
  rootfs units can pick what they need without re-cloning.
- **`tfa-k3`** — `git.trustedfirmware.org/TF-A` `master`. K3 platform support
  lives only on master (no per-release tag), and meta-ti pins to a master
  SRCREV; we follow the same branch so future syncs roll forward.
- **`optee-k3`** — upstream `OP-TEE/optee_os` at tag `4.9.0`, mirroring
  meta-ti's `optee-os-ti-version.inc`.
- **`u-boot-beagleplay` / `u-boot-beagleplay-r5`** — both build from
  `github.com/beagleboard/u-boot` branch `v2025.10-Beagle`. Same tree, two
  defconfigs (`_a53_` and `_r5_`), so they share the dep chain for build tools.
- **`linux-beagleplay`** — `github.com/beagleboard/linux` branch
  `v6.12.43-ti-arm64-r54`, the AM625 device tree + cape overlays that meta-
  beagle ships.
- **`beagleplay-config`** — local Starlark, generates `uEnv.txt` only.

All units build in the `toolchain-musl` container with
`container_arch = "target"`, i.e. the aarch64 Alpine container under QEMU
user-mode. There is no cross-compilation in the conventional sense — the build
sees the target ISA as native. The R5 SPL is the one exception (Cortex-R5F is
armv7-R, an ISA the aarch64 toolchain can't emit) and pulls Alpine's
`gcc-arm-none-eabi` cross toolchain via `module-alpine`.

## Stage-by-stage walkthrough

### Stage 0: ROM

The AM625 ROM is masked silicon. On power-up it reads `tiboot3.bin` from the
configured boot media (SD MMC0 / eMMC MMC1 / OSPI, selected by SYSBOOT straps).
The file is a FIT image: the ROM walks it, verifies signatures against TI's keys
(or, on a GP — General-Purpose — part, accepts a self-signed blob), and starts
execution on the R5F.

### Stage 1: R5F SPL — `tiboot3.bin`

Built by `u-boot-beagleplay-r5`. The output `tiboot3.bin` packs three things via
binman:

1. **R5 SPL** — early U-Boot, runs on the Cortex-R5F.
2. **TIFS / SYSFW** — `ti-sysfw/ti-fs-firmware-am62x-gp-acl.bin` from
   `ti-linux-firmware`. The R5 SPL hands the SoC over to TIFS; from that point
   on, every privileged operation (power, clock, security) is brokered through
   TIFS via the TI SCI mailbox protocol.
3. **DM firmware** — `ti-dm/am62xx/...xer5f` from `ti-linux-firmware`. The
   "Device Manager" runs alongside TIFS on the R5 cluster and handles non-secure
   resource management once Linux is up.

The R5 SPL initializes DDR through the K3 DDR driver, sets up the first SD/eMMC
controller, and loads the next stage off the FAT partition.

### Stage 2: A53 SPL — `tispl.bin`

Built by `u-boot-beagleplay`. Another binman-assembled FIT, this time holding:

1. **A53 SPL** — second-stage U-Boot, runs on Cortex-A53.
2. **BL31** — `tfa-k3`'s `bl31.bin`, the Arm TF-A secure monitor (runs at EL3,
   owns SMCs, dispatches to OP-TEE).
3. **BL32** — `optee-k3`'s `bl32.bin` (= `tee-pager_v2.bin`), the OP-TEE OS
   running in the secure world (S-EL1).
4. **DM firmware** — same `ti-dm` payload, reused here because the A53 SPL
   re-loads it once it has full access to system memory.

These resolve through the merged sysroot — `tfa-k3` and `optee-k3` install their
outputs to `/lib/firmware/bl{31,32}.bin`, and `u-boot-beagleplay`'s make line
names them with `BL31=` / `TEE=` / `TI_DM=` / `BINMAN_INDIRS=`. binman then
sucks them into the FIT.

After loading, the A53 SPL parks BL31 at EL3 and BL32 in the secure world, then
jumps to U-Boot proper at EL2.

### Stage 3: U-Boot proper — `u-boot.img`

Same `u-boot-beagleplay` build, second output. This is the full U-Boot shell —
environment, distro_bootcmd, network, USB, FAT/EXT drivers. By default it
sources `uEnv.txt` from the FAT partition and runs `uenvcmd`.

### Stage 4: Linux — `Image` + DTB

`linux-beagleplay` builds the BeagleBoard fork of 6.12 with `defconfig` plus a
small fragment that turns on container runtime support (cgroups v2, overlay,
namespaces — same fragment used by the Raspberry Pi units, kept in
`linux-beagleplay/container.cfg`).

The kernel and the BeaglePlay-specific DTB (`k3-am625-beagleplay.dtb`) land on
the FAT partition; modules go to the rootfs at `/lib/modules/<ver>/`.

### Stage 5: userspace

The kernel pivots to `/dev/mmcblk1p2`, busybox `init` reads `/etc/inittab`,
which fires OpenRC's `sysinit` → `boot` → `default` runlevels. See
[libc, init, and the Rootfs Base](libc-and-init.md) for how that base shapes up.

## Build-time dependency layering

Within the BSP, dep order matters because binman pulls inputs out of the merged
per-unit sysroot:

```
ti-linux-firmware ─┐
                   ├─→ u-boot-beagleplay-r5  → tiboot3.bin
                   │
                   ├─→ u-boot-beagleplay    → tispl.bin, u-boot.img
tfa-k3 ────────────┤      (also embeds bl31, bl32, DM)
optee-k3 ──────────┘
linux-beagleplay   (independent — kernel + DTB)
beagleplay-config  (independent — generates uEnv.txt)
```

`u-boot-beagleplay` declares `ti-linux-firmware`, `tfa-k3`, and `optee-k3` as
deps so their `/lib/firmware/...` outputs are visible in its sysroot at build
time. The R5 SPL declares only `ti-linux-firmware` (it doesn't embed BL31 or
BL32).

Both U-Boot units also pull a substantial Python/host-tools chain through
`module-alpine` — binman is a Python program with optional dependencies on
`pyelftools`, `pyyaml`, `jsonschema`, `yamllint`, and friends. Which ones get
exercised depends on the defconfig: the A53 build uses a smaller subset, the R5
build's binman config invokes the `ti-board-config` entry type which drags in
the full schema-validation stack. The dep lists in the two `.star` files reflect
that — they intentionally differ.

## Image assembly

The machine descriptor declares two partitions:

```python
partitions = [
    partition(label = "boot",   type = "vfat", size = "128M",
              contents = ["tiboot3.bin", "tispl.bin", "u-boot.img",
                          "Image", "k3-am625-beagleplay.dtb",
                          "uEnv.txt"]),
    partition(label = "rootfs", type = "ext4", size = "1G", root = True),
]
```

Image assembly scans the assembled rootfs (built from all `packages` apks) and
matches each `contents` glob against `/boot/`. Every file listed lands in the
FAT partition at the root level (yoe's vfat assembly flattens paths currently —
that's why `uEnv.txt` is at the partition root, not under `extlinux/`).

The MMC layout the AM625 ROM expects:

| Partition | Type | Contents                                                           |
| --------- | ---- | ------------------------------------------------------------------ |
| 1         | vfat | `tiboot3.bin`, `tispl.bin`, `u-boot.img`, `Image`, DTB, `uEnv.txt` |
| 2         | ext4 | Linux rootfs (musl + busybox + OpenRC + apps)                      |

The kernel command line in `uEnv.txt` resolves `root=/dev/mmcblk1p2`, which is
the ext4 partition on the on-board eMMC (`mmc 1` in U-Boot, `mmcblk1` in Linux —
`mmc 0` is the SD slot).

## U-Boot environment

`beagleplay-config` writes `uEnv.txt` to `/boot/`:

```
bootargs=console=ttyS2,115200 earlycon=ns16550a,mmio32,0x02800000 \
  root=/dev/mmcblk1p2 rootfstype=ext4 rootwait rw
loadaddr=0x82000000
fdt_addr_r=0x88000000
uenvcmd=load mmc 1:1 ${loadaddr} Image; \
  load mmc 1:1 ${fdt_addr_r} k3-am625-beagleplay.dtb; \
  booti ${loadaddr} - ${fdt_addr_r}
```

`ttyS2` is BeaglePlay's debug UART (the same one meta-ti's `am62xx.inc` names in
`SERIAL_CONSOLES`). The DRAM load addresses keep the kernel and DTB clear of the
SPL/U-Boot's own footprint.

## Notable build choices

A few things are non-obvious and worth knowing if you go to change a unit:

- **`ARCH=arm`, not `ARCH=arm64`, for U-Boot and OP-TEE.** Both upstreams keep
  all ARM code (32- and 64-bit) under `arch/arm/` in their source tree. The
  64-bit secure world / kernel selection happens via `CONFIG_*` or
  `CFG_ARM64_core=y`, not via the directory layout. yoe exports `ARCH=arm64` as
  the target arch in the build env, so each unit's make line overrides it
  explicitly.
- **No `CROSS_COMPILE` prefix in the A53 builds.** Inside the `target` Alpine
  container, plain `gcc`/`ld`/`ar` already target aarch64; there is no
  `aarch64-linux-musl-` triplet binary. The R5 SPL is the exception because it
  needs `arm-none-eabi-` to emit armv7-R code.
- **TF-A overrides every per-tool variable.** TF-A's toolchain-detection
  searches for `aarch64-none-elf-gcc` / `aarch64-linux-gnu-gcc` by default. The
  `tfa-k3` unit passes `CC=gcc LD=gcc AS=gcc AR=gcc-ar OC=objcopy OD=objdump` on
  the make line so detection finds the Alpine native tools. It also passes
  `CFLAGS= CPPFLAGS=` empty, because TF-A's `cflags.mk` merges the env values
  into its compile line and yoe's `-I/build/sysroot/usr/include` would trip
  `-Werror=missing-include-dirs`.
- **OP-TEE TAs restricted to 64-bit.** `CFG_USER_TA_TARGETS=ta_arm64` skips
  building 32-bit Trusted Applications. With `CFG_ARM64_core=y` the default
  would be both 32 and 64, which requires a separate `arm-linux-gnueabihf-`
  toolchain that we don't carry. AM62x runs a 64-bit secure world so 32-bit TAs
  aren't needed.
- **U-Boot's host tools want a sysroot.** `mkeficapsule` (and other
  signing/binman helpers) link against gnutls/openssl. yoe's env doesn't reach
  U-Boot's `HOSTCC`/`HOSTLD` path, so both U-Boot units pass
  `HOSTCFLAGS=-I/build/sysroot/usr/include` and
  `HOSTLDFLAGS=-L/build/sysroot/usr/lib` on the make command line. They also
  export `SWIG_LIB` to redirect Alpine's swig binary at the merged sysroot for
  pylibfdt generation.

## When something fails

The boot chain is long and each stage is opinionated about exactly what it
accepts from the previous one. Common breakage modes:

- **ROM rejects `tiboot3.bin`.** Wrong TIFS variant for the silicon (GP vs HS-FS
  vs HS-SE), or the FIT signature is bad. Confirm the `ti-sysfw/...-gp-acl.bin`
  selection in `u-boot-beagleplay-r5`'s binman config matches the part you have.
- **`tispl.bin` loads but jumps into nothing.** Usually BL31/BL32 didn't land in
  the FIT — check that `tfa-k3` and `optee-k3` actually built and their outputs
  exist under `/build/sysroot/lib/firmware/`.
- **DM firmware "not found".** binman's `BINMAN_INDIRS` resolution: make sure
  the path matches `/build/sysroot/lib/firmware`. The TI_DM filename embeds the
  silicon revision (`am62xx`) — if you bump SoC variant, that filename changes
  too.
- **Kernel boots but no console.** `console=ttyS2` and the matching `earlycon=`
  in `uEnv.txt` must reach the kernel. Older BeaglePlay docs use
  `ttyAMA0`/`ttymxc*`/etc. — those are different SoCs.
