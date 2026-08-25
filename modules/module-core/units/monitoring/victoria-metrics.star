load("//classes/binary.star", "binary")
load("//classes/services.star", "service_gate")

# VictoriaMetrics — a time series database that speaks the Prometheus remote
# write and query protocols, which makes it a convenient metrics store for a
# gateway collecting from devices on the local network. Pairs with the
# `grafana` unit next to it for dashboards.
#
# This is the single-node release. The tarball holds one executable,
# victoria-metrics-prod, with no enclosing directory. `victoria-metrics` is
# a symlink to it so the command has the name people expect while the
# upstream filename stays visible in process listings.
#
# Upstream publishes Linux builds for x86_64 and arm64 only; a build for any
# other architecture stops with a clear error rather than reaching for an
# asset that does not exist.
binary(
    name = "victoria-metrics",
    version = "1.150.0",
    base_url = "https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/v{version}",
    asset = "victoria-metrics-linux-{arch}-v{version}.tar.gz",
    sha256 = {
        "x86_64": "22bfe77be3de1ad03f214a005129312536d77ed4e293b66c186df417ee40a61d",
        "arm64":  "fdb9e272f5e4d49cb506991b7293ed53bccbff821a62653dea7c463ae1980da5",
    },
    binaries = {"victoria-metrics-prod": "victoria-metrics-prod"},
    symlinks = {
        "$PREFIX/bin/victoria-metrics": "victoria-metrics-prod",
    },
    license = "Apache-2.0",
    description = "Fast, cost effective and scalable time series database",
    services = ["victoria-metrics"],
    runtime_deps = ["openrc"],
    conffiles = ["/etc/default/victoria-metrics"],
    tasks = [
        # A unit is built once per distro but cannot know at Starlark time
        # which one, so it writes both descriptions of the service and
        # service_gate() below drops the set the target init does not read.
        # That is what lets `services` above enable the database on an OpenRC
        # rootfs and a systemd one alike.
        task("service", steps = [
            "mkdir -p $DESTDIR/etc/init.d $DESTDIR/etc/conf.d" +
            " $DESTDIR/lib/systemd/system $DESTDIR$PREFIX/lib/sysusers.d",
            install_file("victoria-metrics.init",
                         "$DESTDIR/etc/init.d/victoria-metrics", mode = 0o755),
            install_file("victoria-metrics.confd",
                         "$DESTDIR/etc/conf.d/victoria-metrics", mode = 0o644),
            install_file("victoria-metrics.service",
                         "$DESTDIR/lib/systemd/system/victoria-metrics.service", mode = 0o644),
            install_file("victoria-metrics.sysusers",
                         "$DESTDIR$PREFIX/lib/sysusers.d/victoria-metrics.conf", mode = 0o644),
            service_gate("victoria-metrics", sysusers = True),
        ]),
    ],
)
