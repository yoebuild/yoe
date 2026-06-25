unit(
    name = "tfa-k3",
    version = "2.14",
    source = "https://git.trustedfirmware.org/TF-A/trusted-firmware-a.git",
    # TF-A doesn't tag the K3 platform releases — meta-ti pins to a master
    # SRCREV (currently 76500ceaeefcda9...). We follow the same branch so
    # future syncs roll forward by re-cloning.
    branch = "master",
    license = "BSD-3-Clause",
    description = "TF-A BL31 (secure monitor) for TI K3 AM62x — input to U-Boot A53 SPL",
    deps = ["toolchain"],
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # Native build inside the aarch64 (target) container — point
            # TF-A's per-tool variables at Alpine's unadorned `gcc`/`ld`/
            # etc. rather than letting it search for the non-existent
            # `aarch64-none-elf-gcc` / `aarch64-linux-gnu-gcc` defaults.
            # TF-A reads CC/CPP/AS/LD/AR/OC/OD as the aarch64 toolchain
            # parameters when their make origin is "command line"
            # (see make_helpers/toolchains/aarch64.mk). Without these
            # overrides the toolchain-detection loop fails, the
            # generated $(call MAKE_LIB,c) expansion produces malformed
            # conditionals, and the build dies with
            # "invalid syntax in conditional".
            #
            # CFLAGS= / CPPFLAGS= override yoe's env exports
            # (-I/build/sysroot/usr/include) — TF-A's cflags.mk merges
            # them into its compile line, and -Wmissing-include-dirs
            # makes the empty sysroot dir a hard build error.
            #
            # PLAT=k3 TARGET_BOARD=lite matches meta-ti's am62xx.inc
            # (TFA_BOARD = "lite"). SPD=opteed wires OP-TEE as the secure
            # payload. K3_PM_SYSTEM_SUSPEND=1 keeps system-suspend support
            # parity with TI's BSP. BL31's load address is set by the K3
            # platform port — no need to override.
            """
make ARCH=aarch64 \\
     CC=gcc \\
     CPP="gcc -E" \\
     AS=gcc \\
     LD=gcc \\
     AR=gcc-ar \\
     OC=objcopy \\
     OD=objdump \\
     CFLAGS= \\
     CPPFLAGS= \\
     PLAT=k3 \\
     TARGET_BOARD=lite \\
     SPD=opteed \\
     K3_PM_SYSTEM_SUSPEND=1 \\
     -j$NPROC \\
     bl31
""",
            # Stage bl31.bin where downstream units find it via the merged
            # sysroot: /build/sysroot/lib/firmware/bl31.bin for U-Boot's
            # `make BL31=...` argument.
            "install -D build/k3/lite/release/bl31.bin $DESTDIR/lib/firmware/bl31.bin",
        ]),
    ],
)
