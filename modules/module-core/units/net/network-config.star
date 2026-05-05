unit(
    name = "network-config",
    version = "1.0.0",
    license = "MIT",
    description = "DHCP networking on eth0 — uses dhcpcd if installed, else busybox udhcpc",
    services = ["S10network"],
    runtime_deps = ["busybox"],
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps = [
            "mkdir -p $DESTDIR/usr/share/udhcpc $DESTDIR/etc/init.d",
            install_file("udhcpc-default.script",
                         "$DESTDIR/usr/share/udhcpc/default.script", mode = 0o755),
            install_file("S10network", "$DESTDIR/etc/init.d/S10network", mode = 0o755),
        ]),
    ],
)
