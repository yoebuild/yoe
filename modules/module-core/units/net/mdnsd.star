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
    # Upstream commit a654c19 (post-v0.12): rebuild A/AAAA records every
    # time the interface address changes. Without this, mdnsd publishes only
    # the addresses present at startup — on DHCP boxes that race the init
    # script, the IPv4 A record never appears and <host>.local resolves to
    # the IPv6 link-local only.
    patches = ["mdnsd/0001-Update-the-records-when-the-iface-has-changed.patch"],
    services = ["mdnsd"],
    runtime_deps = ["musl", "busybox", "openrc"],
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
            install_file("mdnsd", "$DESTDIR/etc/init.d/mdnsd", mode = 0o755),
        ]),
    ],
)
