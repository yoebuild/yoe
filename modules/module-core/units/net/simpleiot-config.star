load("//classes/tasks.star", "merge_tasks")

# Simple IoT node configuration, applied by the built-in provisioning.
#
# Ships a configuration in the YAML form Simple IoT reads at start-up and
# whenever the file changes. The shipped configuration wires the three
# monitoring units together: a Database node writing to VictoriaMetrics, and
# the metrics nodes that collect host, application, and process data for
# Grafana to draw.
#
# The image must also carry exactly one of `simpleiot` or `simpleiot-bin` —
# they are alternatives that both own /usr/bin/siot. This unit names neither,
# because naming one would drag it into the closure of a project that chose
# the other and put both in the same rootfs.
unit(
    name = "simpleiot-config",
    version = "1.0.0",
    license = "MIT",
    description = "Simple IoT node configuration, applied by the built-in provisioning",
    # The configuration describes a Database node pointing at the local
    # VictoriaMetrics, so the store has to be there for it to mean anything.
    runtime_deps = ["victoria-metrics"],
    conffiles = ["/etc/simpleiot/provisioning/10-config.yml"],
    container = "toolchain",
    container_arch = "target",
    deps = ["toolchain"],
    tasks = [
        # Provisioning applies the files in this directory in lexical order,
        # so the numeric prefix leaves room to sequence other files around
        # this one.
        task("config", steps = [
            "mkdir -p $DESTDIR/etc/simpleiot/provisioning",
            install_file("10-config.yml",
                         "$DESTDIR/etc/simpleiot/provisioning/10-config.yml", mode = 0o644),
        ]),
        # Naming the directory is a drop-in rather than an edit to the
        # settings file simpleiot ships, so this package adds a setting
        # without owning the file an operator edits.
        task("provisioning-dir-openrc", distros = ["alpine"], steps = [
            "mkdir -p $DESTDIR/etc/conf.d/simpleiot.d",
            install_file("simpleiot-provisioning.confd",
                         "$DESTDIR/etc/conf.d/simpleiot.d/10-provisioning.conf", mode = 0o644),
        ]),
        task("provisioning-dir-systemd", distros = ["debian", "ubuntu"], steps = [
            "mkdir -p $DESTDIR/lib/systemd/system/simpleiot.service.d",
            install_file("simpleiot-provisioning.conf",
                         "$DESTDIR/lib/systemd/system/simpleiot.service.d/10-provisioning.conf",
                         mode = 0o644),
        ]),
    ],
)
