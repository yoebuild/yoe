load("//classes/binary.star", "binary")
load("//classes/services.star", "service_gate")

# Grafana — dashboards and alerting over the metrics a gateway collects.
# Pairs with the `victoria-metrics` unit next to it: VictoriaMetrics answers
# Prometheus queries, so Grafana's bundled Prometheus data source reads it
# with no extra plugin.
#
# This installs the prebuilt OSS release. Grafana carries its web assets and
# bundled data-source plugins as files that the server resolves relative to
# its home directory, so the upstream layout is kept intact under
# /usr/share/grafana and /usr/bin/grafana is a symlink into it.
#
# SIZE. Grafana is large — roughly 930 MB installed at this version, and it
# grows with each release (the 13.x tarballs are more than twice the size of
# 11.x). Most of that is the server executable and the thirteen bundled data
# sources, each of which carries its own backend program. The `trim` task
# below removes what is only useful to someone developing Grafana itself:
# JavaScript source maps, the Swagger UI, the bundled documentation, and the
# packaging and container helper scripts. To go further, delete the
# data/plugins-bundled entries for data sources you do not query — each is
# 25-50 MB — but be aware that doing so is a per-image choice that costs the
# artifact sharing every other image gets from a single build.
#
# Upstream publishes Linux builds for x86_64 and arm64 only; a build for any
# other architecture stops with a clear error rather than reaching for an
# asset that does not exist.
binary(
    name = "grafana",
    version = "13.2.0",
    base_url = "https://dl.grafana.com/oss/release",
    asset = "grafana-{version}.linux-{arch}.tar.gz",
    sha256 = {
        "x86_64": "4669384cdb0bb5b4a3f804927e57490d17f4cc47258cd1698fc124e99ee58265",
        "arm64":  "28c0dfeedf4334e2170b423589224745ea5da6c709f2e401f11289f3381bcfb4",
    },
    install_tree = "$PREFIX/share/grafana",
    binaries = ["bin/grafana"],
    # Site configuration lives in /etc/grafana. grafana.ini overrides the
    # shipped conf/defaults.ini, which upstream expects to stay untouched.
    extras = [
        ("conf/sample.ini", "/etc/grafana/grafana.ini", 0o640),
    ],
    license = "AGPL-3.0-only",
    description = "Observability and data visualization platform",
    services = ["grafana"],
    runtime_deps = ["openrc", "ca-certificates"],
    conffiles = [
        "/etc/grafana/grafana.ini",
        "/etc/default/grafana",
    ],
    tasks = [
        task("trim", steps = [
            "cd $DESTDIR$PREFIX/share/grafana && rm -rf docs packaging tools Dockerfile README.md public/build-swagger public/test public/sass",
            "find $DESTDIR$PREFIX/share/grafana -name '*.js.map' -delete",
            # Upstream ships the server unstripped, which costs about
            # 140 MB of debug sections that nothing on a device reads.
            # Go keeps the symbol table its runtime needs in a loaded
            # section, so panic traces survive this.
            "strip $DESTDIR$PREFIX/share/grafana/bin/grafana",
        ]),
        # A unit is built once per distro but cannot know at Starlark time
        # which one, so it writes both descriptions of the service and
        # service_gate() below drops the set the target init does not read.
        # That is what lets `services` above enable the server on an OpenRC
        # rootfs and a systemd one alike.
        task("service", steps = [
            "mkdir -p $DESTDIR/etc/init.d $DESTDIR/etc/conf.d" +
            " $DESTDIR/lib/systemd/system $DESTDIR$PREFIX/lib/sysusers.d",
            install_file("grafana.init",
                         "$DESTDIR/etc/init.d/grafana", mode = 0o755),
            install_file("grafana.confd",
                         "$DESTDIR/etc/conf.d/grafana", mode = 0o644),
            install_file("grafana.service",
                         "$DESTDIR/lib/systemd/system/grafana.service", mode = 0o644),
            install_file("grafana.sysusers",
                         "$DESTDIR$PREFIX/lib/sysusers.d/grafana.conf", mode = 0o644),
            # Provisioning drop-in directories. Data sources and
            # dashboards described by files here are applied at start-up,
            # which is how an image ships a working dashboard set without
            # anyone clicking through the UI.
            "mkdir -p $DESTDIR/etc/grafana/provisioning/datasources" +
            " $DESTDIR/etc/grafana/provisioning/dashboards" +
            " $DESTDIR/etc/grafana/provisioning/plugins" +
            " $DESTDIR/etc/grafana/provisioning/alerting" +
            " $DESTDIR/etc/grafana/provisioning/notifiers" +
            " $DESTDIR/etc/grafana/provisioning/access-control",
            service_gate("grafana", sysusers = True),
        ]),
    ],
)
