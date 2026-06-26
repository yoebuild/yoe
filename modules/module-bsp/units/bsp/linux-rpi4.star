unit(
    name = "linux-rpi4",
    version = "6.12",
    scope = "machine",
    source = "https://github.com/raspberrypi/linux.git",
    branch = "rpi-6.12.y",
    license = "GPL-2.0",
    description = "Raspberry Pi 4 kernel (BCM2711)",
    deps = ["toolchain"],
    # The kernel's certs/extract-cert host tool needs OpenSSL/libcrypto
    # headers. Package name differs by backend: Alpine bundles them in
    # openssl-dev, the apt distros split them into libssl-dev.
    distro_deps = {
        "alpine": ["openssl-dev"],
        "debian": ["libssl-dev"],
        "ubuntu": ["libssl-dev"],
    },
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            install_file("container.cfg", "$SRCDIR/.yoe-container.cfg"),
            # Generate defconfig, merge container-runtime CONFIG fragment
            # (overlayfs, netfilter, namespaces, eBPF cgroup support) so
            # dockerd/podman/runc work out of the box, then resolve deps.
            """
make ARCH=arm64 bcm2711_defconfig
scripts/kconfig/merge_config.sh -m -O . .config .yoe-container.cfg
make ARCH=arm64 olddefconfig
""",
            # HOSTCFLAGS/HOSTLDFLAGS carry the sysroot include/lib paths to the
            # kernel's host tools (e.g. certs/extract-cert, which #includes
            # <openssl/bio.h>). Host tools do not inherit CFLAGS/CPPFLAGS.
            "make ARCH=arm64 HOSTCFLAGS=\"$CPPFLAGS\" HOSTLDFLAGS=\"$LDFLAGS\" -j$NPROC Image modules dtbs",
            # Install kernel as kernel8.img (RPi4 64-bit naming convention)
            "install -D arch/arm64/boot/Image $DESTDIR/boot/kernel8.img",
            # Install device trees
            "install -D arch/arm64/boot/dts/broadcom/bcm2711-rpi-4-b.dtb $DESTDIR/boot/bcm2711-rpi-4-b.dtb",
            "install -D arch/arm64/boot/dts/broadcom/bcm2711-rpi-400.dtb $DESTDIR/boot/bcm2711-rpi-400.dtb",
            "install -D arch/arm64/boot/dts/broadcom/bcm2711-rpi-cm4.dtb $DESTDIR/boot/bcm2711-rpi-cm4.dtb",
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
