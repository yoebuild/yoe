# dhcpcd has its own hand-written configure script (not autoconf), so use a
# custom unit. Privsep is disabled because it requires a dedicated dhcpcd
# user/group; busybox-style usage is plenty for now.
unit(
    name = "dhcpcd",
    version = "10.2.4",
    source = "https://github.com/NetworkConfiguration/dhcpcd.git",
    tag = "v10.2.4",
    license = "BSD-2-Clause",
    description = "Full-featured DHCPv4/DHCPv6 client",
    deps = ["toolchain-musl"],
    runtime_deps = [],
    container = "toolchain-musl",
    container_arch = "target",
    sandbox = True,
    shell = "bash",
    tasks = [
        task("build", steps = [
            "./configure --prefix=/usr --sysconfdir=/etc " +
                "--libexecdir=/lib/dhcpcd --dbdir=/var/lib/dhcpcd " +
                "--rundir=/run --without-dev --without-udev " +
                "--disable-privsep",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
