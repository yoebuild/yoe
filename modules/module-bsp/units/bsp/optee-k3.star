unit(
    name = "optee-k3",
    version = "4.9.0",
    source = "https://github.com/OP-TEE/optee_os.git",
    # Mirror meta-ti optee-os-ti-version.inc; this is the upstream tag that
    # matches their SRCREV f2a7ad0... (4.9.0+git).
    tag = "4.9.0",
    license = "BSD-2-Clause",
    description = "OP-TEE OS BL32 for TI K3 AM62x — input to U-Boot A53 SPL",
    deps = [
        "toolchain-musl",
        "python3",
        "py3-cryptography",
        "py3-cffi",
        "libffi",
        "py3-elftools",
    ],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # Native build inside the aarch64 (target) container, so no
            # CROSS_COMPILE prefix — the unadorned `gcc` already targets
            # the secure world's ISA. ARCH=arm is OP-TEE's source-tree
            # convention (everything K3 lives under core/arch/arm/plat-k3
            # regardless of bit width); CFG_ARM64_core=y selects the
            # 64-bit secure world on the A53 cores. Without the override,
            # yoe's env exports ARCH=arm64 and OP-TEE looks for the
            # non-existent core/arch/arm64/plat-k3/conf.mk.
            # The default O=out/arm-plat-k3 places artifacts under out/.
            """
make ARCH=arm \\
     PLATFORM=k3-am62x \\
     CFG_ARM64_core=y \\
     CFG_USER_TA_TARGETS=ta_arm64 \\
     CFG_TEE_CORE_LOG_LEVEL=1 \\
     -j$NPROC \\
     all
""",
            # bl32.bin = tee-pager_v2.bin (paged TEE core image). Stage it
            # at /lib/firmware/bl32.bin so U-Boot's `make TEE=...` finds it
            # through the sysroot.
            "install -D out/arm-plat-k3/core/tee-pager_v2.bin $DESTDIR/lib/firmware/bl32.bin",
        ]),
    ],
)
