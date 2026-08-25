load("//classes/go.star", "go_binary")
load("//classes/services.star", "service_gate")

go_binary(
    name = "simpleiot",
    version = "0.18.5",
    source = "https://github.com/simpleiot/simpleiot.git",
    tag = "v0.18.5",
    branch = "master",
    go_package = "./cmd/siot",
    binary = "siot",
    license = "Apache-2.0",
    description = "IoT application for sensor data, telemetry, configuration, and device management",
    services = ["simpleiot"],
    runtime_deps = ["openrc"],
    conffiles = ["/etc/default/simpleiot"],
    tasks = [
        # A unit is built once per distro but cannot know at Starlark time
        # which one, so it writes both descriptions of the service and
        # service_gate() below drops the set the target init does not read.
        # That is what lets `services` above enable the daemon on an OpenRC
        # rootfs and a systemd one alike.
        task("init-script", steps = [
            "mkdir -p $DESTDIR/etc/init.d $DESTDIR/etc/conf.d $DESTDIR/lib/systemd/system",
            install_file("simpleiot.init",
                         "$DESTDIR/etc/init.d/simpleiot", mode = 0o755),
            install_file("simpleiot.confd",
                         "$DESTDIR/etc/conf.d/simpleiot", mode = 0o644),
            install_file("simpleiot.service",
                         "$DESTDIR/lib/systemd/system/simpleiot.service", mode = 0o644),
            service_gate("simpleiot"),
        ]),
    ],
)
