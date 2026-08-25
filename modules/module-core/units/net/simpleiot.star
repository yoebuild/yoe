load("//classes/go.star", "go_binary")

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
    # openrc is the Alpine service manager; naming it unconditionally pulls
    # Debian's openrc package into an apt image, where it conflicts with
    # systemd-sysv and the rootfs solve fails.
    distro_runtime_deps = {"alpine": ["openrc"]},
    conffiles = ["/etc/default/simpleiot"],
    tasks = [
        # The service, described once per init system. A unit is built once
        # per distro but evaluated once for all of them, so `distros` is what
        # picks the right one; the package that reaches a device carries only
        # the description its init reads. The settings file is the same either
        # way -- plain KEY=VALUE lines, which OpenRC sources directly and
        # systemd reads through EnvironmentFile -- and only its path differs.
        task("service-openrc", distros = ["alpine"], steps = [
            "mkdir -p $DESTDIR/etc/init.d $DESTDIR/etc/conf.d",
            install_file("simpleiot.init",
                         "$DESTDIR/etc/init.d/simpleiot", mode = 0o755),
            install_file("simpleiot.confd",
                         "$DESTDIR/etc/conf.d/simpleiot", mode = 0o644),
        ]),
        task("service-systemd", distros = ["debian", "ubuntu"], steps = [
            "mkdir -p $DESTDIR/lib/systemd/system $DESTDIR/etc/default",
            install_file("simpleiot.service",
                         "$DESTDIR/lib/systemd/system/simpleiot.service", mode = 0o644),
            install_file("simpleiot.confd",
                         "$DESTDIR/etc/default/simpleiot", mode = 0o644),
        ]),
    ],
)
