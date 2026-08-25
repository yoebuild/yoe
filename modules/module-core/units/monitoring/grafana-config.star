# Grafana data source for the local VictoriaMetrics.
#
# Grafana ships with the provisioning directories but nothing in them, so a
# fresh install has no data source and every dashboard has to be pointed at
# one by hand. This unit fills that in for the pairing the monitoring units
# are built around, so an image comes up able to draw what VictoriaMetrics
# has stored.
#
# meta-iot has no counterpart to this: its Grafana recipe creates the same
# empty directories and leaves configuring a data source to the operator.
unit(
    name = "grafana-config",
    version = "1.0.0",
    license = "MIT",
    description = "Grafana data source for the local VictoriaMetrics instance",
    runtime_deps = ["grafana", "victoria-metrics"],
    conffiles = ["/etc/grafana/provisioning/datasources/10-victoria-metrics.yml"],
    container = "toolchain",
    container_arch = "target",
    deps = ["toolchain"],
    tasks = [
        task("datasource", steps = [
            "mkdir -p $DESTDIR/etc/grafana/provisioning/datasources",
            install_file("10-victoria-metrics.yml",
                         "$DESTDIR/etc/grafana/provisioning/datasources/10-victoria-metrics.yml",
                         mode = 0o644),
        ]),
    ],
)
