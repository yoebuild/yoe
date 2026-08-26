load("//classes/binary.star", "binary")

# Simple IoT, installed from the executable upstream publishes rather than
# built from source. The `simpleiot` unit next to this one builds the same
# program with the Go toolchain; this one trades that for a much shorter
# build. Pick one or the other in an image's artifacts list — both install
# /usr/bin/siot and /etc/init.d/simpleiot, and `replaces` records that this
# unit takes ownership of those paths where a rootfs already carries them.
#
# Upstream ships the release asset as a bare executable rather than an
# archive, so the source workspace copies it into the build tree unchanged
# and the install step renames it to `siot`. The asset names use the kernel
# arch spelling on x86_64 and riscv64, so the arch map is identity there
# rather than the Go-style default.
binary(
    name = "simpleiot-bin",
    version = "0.25.1",
    base_url = "https://github.com/simpleiot/simpleiot/releases/download/v{version}",
    asset = "simpleiot-v{version}-linux-{arch}",
    arch_map = {
        "x86_64":  "x86_64",
        "arm64":   "arm64",
        "riscv64": "riscv64",
    },
    sha256 = {
        "x86_64":  "09244fe809df324dc21cd32155c84cc8e5f38b63acfde52c3aeaba6cf1fbcf44",
        "arm64":   "6463c761cd85e459514b5dfa71efbc7fc46343e4af3a494a465378b5514bebbf",
        "riscv64": "21a4d15639097fe15177586b3d3db4ba4a34da966e6737bb2dce92ecc13f5a7b",
    },
    binaries = {"siot": "simpleiot-v{version}-linux-{arch}"},
    license = "Apache-2.0",
    description = "IoT application for sensor data, telemetry, configuration, and device management",
    replaces = ["simpleiot"],
    services = ["simpleiot"],
    runtime_deps = ["ca-certificates"],
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
