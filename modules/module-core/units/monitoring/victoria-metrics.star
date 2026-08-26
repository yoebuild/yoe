load("//classes/binary.star", "binary")

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
    # openrc is the Alpine service manager; naming it unconditionally pulls
    # Debian's openrc package into an apt image, where it conflicts with
    # systemd-sysv and the rootfs solve fails.
    distro_runtime_deps = {"alpine": ["openrc"]},
    conffiles = ["/etc/default/victoria-metrics"],
    tasks = [
        # The service, described once per init system. A unit is built once
        # per distro but evaluated once for all of them, so `distros` is what
        # picks the right one; the package that reaches a device carries only
        # the description its init reads. The settings file is the same either
        # way -- plain KEY=VALUE lines, which OpenRC sources directly and
        # systemd reads through EnvironmentFile -- and only its path differs.
        task("service-openrc", distros = ["alpine"], steps = [
            "mkdir -p $DESTDIR/etc/init.d $DESTDIR/etc/conf.d",
            install_file("victoria-metrics.init",
                         "$DESTDIR/etc/init.d/victoria-metrics", mode = 0o755),
            install_file("victoria-metrics.confd",
                         "$DESTDIR/etc/conf.d/victoria-metrics", mode = 0o644),
        ]),
        task("service-systemd", distros = ["debian", "ubuntu"], steps = [
            "mkdir -p $DESTDIR/lib/systemd/system $DESTDIR/etc/default",
            install_file("victoria-metrics.service",
                         "$DESTDIR/lib/systemd/system/victoria-metrics.service", mode = 0o644),
            install_file("victoria-metrics.confd",
                         "$DESTDIR/etc/default/victoria-metrics", mode = 0o644),
            "mkdir -p $DESTDIR$PREFIX/lib/sysusers.d",
            install_file("victoria-metrics.sysusers",
                         "$DESTDIR$PREFIX/lib/sysusers.d/victoria-metrics.conf", mode = 0o644),
        ]),
    ],
)
