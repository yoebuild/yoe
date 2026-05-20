unit(
    name = "linux-beagleplay",
    version = "6.12",
    scope = "machine",
    source = "https://github.com/beagleboard/linux.git",
    # Matches meta-ti/meta-beagle's linux-bb.org_6.12.bb 64-bit pin
    # (v6.12.43-ti-arm64-r54 → SRCREV 84c4b4613a852db269620a3fdfed65de90569fa1).
    # BeagleBoard's fork carries the AM625 BeaglePlay device tree plus the
    # cape/overlay collection meta-beagle ships.
    branch = "v6.12.43-ti-arm64-r54",
    license = "GPL-2.0",
    description = "BeaglePlay kernel (TI AM625, BeagleBoard.org fork)",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            install_file("container.cfg", "$SRCDIR/.yoe-container.cfg"),
            # `bb.org_defconfig` is the BeagleBoard fork's curated arm64
            # config — it covers the AM62x SoC, eMMC, USB, networking, and
            # DRM, and (unlike the upstream arm64 `defconfig`) does not
            # enable DRM_MSM/NOUVEAU/TEGRA whose header generators require
            # python3 at build time. Merge in yoe's container-runtime
            # fragment so dockerd/podman/runc work out of the box.
            """
make ARCH=arm64 bb.org_defconfig
scripts/kconfig/merge_config.sh -m -O . .config .yoe-container.cfg
make ARCH=arm64 olddefconfig
""",
            "make ARCH=arm64 -j$NPROC Image modules dtbs",
            # U-Boot's distro_bootcmd loads `Image` by default.
            "install -D arch/arm64/boot/Image $DESTDIR/boot/Image",
            # AM625 device trees live under arch/arm64/boot/dts/ti/.
            # Only ship the BeaglePlay DTB — the sk/cape overlays are
            # optional and add a couple of MB to the boot partition.
            "install -D arch/arm64/boot/dts/ti/k3-am625-beagleplay.dtb $DESTDIR/boot/k3-am625-beagleplay.dtb",
            # Modules into rootfs; same DEPMOD trick as the rpi units.
            "make ARCH=arm64 INSTALL_MOD_PATH=$DESTDIR DEPMOD=true modules_install",
            "rm -f $DESTDIR/lib/modules/*/build $DESTDIR/lib/modules/*/source",
        ]),
    ],
)
