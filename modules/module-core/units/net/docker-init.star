unit(
    name = "docker-init",
    version = "1.0.0",
    license = "MIT",
    description = "OpenRC service script for dockerd",
    services = ["docker"],
    runtime_deps = ["docker", "openrc"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps = [
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("docker", "$DESTDIR/etc/init.d/docker", mode = 0o755),
        ]),
    ],
)
