load("//classes/go.star", "go_binary")

go_binary(
    name = "simpleiot",
    version = "0.18.5",
    source = "https://github.com/simpleiot/simpleiot.git",
    tag = "v0.18.5",
    go_package = "./cmd/siot",
    binary = "siot",
    license = "Apache-2.0",
    description = "IoT application for sensor data, telemetry, configuration, and device management",
    services = ["simpleiot"],
    runtime_deps = ["openrc"],
    tasks = [
        task("init-script", steps = [
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("simpleiot.init",
                         "$DESTDIR/etc/init.d/simpleiot", mode = 0o755),
        ]),
    ],
)
