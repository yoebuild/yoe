load("//classes/binary.star", "binary")
load("//classes/services.star", "service_gate")

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
    runtime_deps = ["openrc", "ca-certificates"],
    conffiles = ["/etc/default/simpleiot"],
    tasks = [
        # A unit is built once per distro but cannot know at Starlark time
        # which one, so it writes both descriptions of the service and
        # service_gate() below drops the set the target init does not read.
        # That is what lets `services` above enable the daemon on an OpenRC
        # rootfs and a systemd one alike.
        task("service", steps = [
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
