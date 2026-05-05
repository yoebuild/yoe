load("//classes/autotools.star", "autotools")

autotools(
    name = "mdnsd",
    version = "0.12",
    source = "https://github.com/troglobit/mdnsd.git",
    tag = "v0.12",
    license = "ISC",
    description = "Small mDNS responder daemon — advertises the host as <hostname>.local without dbus/glib",
    # --sysconfdir=/etc moves mdnsd's default config search path from
    # /usr/etc/mdns.d (autotools default with prefix=/usr) to /etc/mdns.d.
    configure_args = ["--without-systemd", "--sysconfdir=/etc"],
    services = ["S30mdnsd"],
    runtime_deps = ["musl", "busybox"],
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        # mdnsd ignores its own host A record unless at least one service
        # record exists in /etc/mdns.d. Shipping the SSH advertisement is
        # the cheapest way to ensure <hostname>.local resolves; it also
        # gives Bonjour-aware tools a service to find.
        task("install-config", steps = [
            "mkdir -p $DESTDIR/etc/mdns.d $DESTDIR/etc/init.d",
            install_file("ssh.service", "$DESTDIR/etc/mdns.d/ssh.service", mode = 0o644),
            install_file("S30mdnsd", "$DESTDIR/etc/init.d/S30mdnsd", mode = 0o755),
        ]),
    ],
)
