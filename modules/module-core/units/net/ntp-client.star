unit(
    name = "ntp-client",
    version = "1.0.0",
    license = "MIT",
    description = "One-shot busybox NTP sync at boot for boards without an RTC",
    services = ["ntp-client"],
    runtime_deps = ["busybox", "openrc"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps = [
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("ntp-client", "$DESTDIR/etc/init.d/ntp-client", mode = 0o755),
        ]),
    ],
)
