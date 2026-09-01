# Service enablement for iiod, the IIO network daemon.
#
# The daemon binary comes from `libiio`; this unit ships only the description
# its init reads and declares the service, the same division `docker-init` uses
# for dockerd. Keeping the two apart means an image can install libiio for
# iio_info and the rest of the tools without also standing up a network daemon
# — which matters here, because iiod has no authentication and grants any
# client on the network read *and write* access to this board's IIO devices,
# including driving DAC outputs. Add this unit to an image only when serving
# sensors to other machines is the intent, and only on a network you trust.
unit(
    name = "iiod-init",
    version = "1.0.0",
    license = "MIT",
    description = "Service definition that runs iiod, the IIO network daemon, at boot",
    services = ["iiod"],
    deps = ["toolchain"],
    runtime_deps = ["libiio"],
    distro_runtime_deps = {"alpine": ["openrc"]},
    container = "toolchain",
    container_arch = "target",
    tasks = [
        # Upstream ships a SysVinit script, an upstart job and a systemd unit;
        # none is an OpenRC service, and installing all three would put files
        # in every image that its init never reads. Describe the service once
        # per init system and let `distros` pick. Both read $IIOD_OPTS, so the
        # settings file is the same KEY=VALUE content at the path each base
        # expects.
        task("service-openrc", distros = ["alpine"], steps = [
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("iiod", "$DESTDIR/etc/init.d/iiod", mode = 0o755),
        ]),
        task("service-systemd", distros = ["debian", "ubuntu"], steps = [
            "mkdir -p $DESTDIR/lib/systemd/system",
            install_file("iiod.service",
                         "$DESTDIR/lib/systemd/system/iiod.service", mode = 0o644),
        ]),
    ],
)
