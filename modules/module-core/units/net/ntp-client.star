unit(
    name = "ntp-client",
    version = "1.0.0",
    license = "MIT",
    description = "One-shot busybox NTP sync at boot for boards without an RTC",
    services = ["S20ntp"],
    runtime_deps = ["busybox"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps = [
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("S20ntp", "$DESTDIR/etc/init.d/S20ntp", mode = 0o755),
        ]),
    ],
)
