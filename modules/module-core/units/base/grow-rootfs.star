unit(
    name = "grow-rootfs",
    version = "1.0.0",
    license = "Apache-2.0",
    description = "First-boot service that expands the rootfs partition to fill the disk",
    services = ["grow-rootfs"],
    # util-linux supplies sfdisk + partx; e2fsprogs supplies resize2fs.
    # Both are in dev-image already; we declare them as runtime_deps so
    # this unit is also usable in slimmer images.
    runtime_deps = ["openrc", "util-linux", "e2fsprogs"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps = [
            "mkdir -p $DESTDIR/etc/init.d $DESTDIR/var/lib",
            install_file("grow-rootfs.init",
                         "$DESTDIR/etc/init.d/grow-rootfs", mode = 0o755),
        ]),
    ],
)
