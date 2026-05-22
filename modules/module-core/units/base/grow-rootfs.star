unit(
    name = "grow-rootfs",
    version = "1.0.0",
    license = "Apache-2.0",
    description = "First-boot service that expands the rootfs partition to fill the disk",
    services = ["grow-rootfs"],
    # Alpine ships sfdisk and partx as separate top-level apks (NOT
    # subpackages of util-linux — util-linux-misc has fdisk but not
    # sfdisk). e2fsprogs supplies resize2fs.
    runtime_deps = ["openrc", "sfdisk", "partx", "e2fsprogs"],
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
