unit(
    name = "linux-rpi5",
    version = "6.12",
    scope = "machine",
    source = "https://github.com/raspberrypi/linux.git",
    branch = "rpi-6.12.y",
    license = "GPL-2.0",
    description = "Raspberry Pi 5 kernel (BCM2712)",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            install_file("container.cfg", "$SRCDIR/.yoe-container.cfg"),
            # Generate defconfig, merge container-runtime CONFIG fragment
            # (overlayfs, netfilter, namespaces, eBPF cgroup support) so
            # dockerd/podman/runc work out of the box, then resolve deps.
            """
make ARCH=arm64 bcm2712_defconfig
scripts/kconfig/merge_config.sh -m -O . .config .yoe-container.cfg
make ARCH=arm64 olddefconfig
""",
            "make ARCH=arm64 -j$NPROC Image modules dtbs",
            # Install kernel as kernel_2712.img (RPi5 naming convention)
            "install -D arch/arm64/boot/Image $DESTDIR/boot/kernel_2712.img",
            # Install device trees
            "install -D arch/arm64/boot/dts/broadcom/bcm2712-rpi-5-b.dtb $DESTDIR/boot/bcm2712-rpi-5-b.dtb",
            # Install overlays directory
            "mkdir -p $DESTDIR/boot/overlays",
            "cp arch/arm64/boot/dts/overlays/*.dtbo $DESTDIR/boot/overlays/ 2>/dev/null || true",
            # Install modules into rootfs at /lib/modules/<kver>/.
            # DEPMOD=true skips depmod (not in build container); target runs it.
            "make ARCH=arm64 INSTALL_MOD_PATH=$DESTDIR DEPMOD=true modules_install",
            # Drop broken build/source symlinks pointing into the host build tree.
            "rm -f $DESTDIR/lib/modules/*/build $DESTDIR/lib/modules/*/source",
        ]),
    ],
)
