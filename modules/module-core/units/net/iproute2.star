# iproute2 has a hand-written configure script and Makefile-driven install
# paths, so it doesn't fit the autotools class. Build directly.
unit(
    name = "iproute2",
    version = "6.13.0",
    source = "https://github.com/iproute2/iproute2.git",
    tag = "v6.13.0",
    license = "GPL-2.0-or-later",
    description = "Full ip(8)/tc(8) suite for advanced network configuration",
    deps = ["util-linux", "toolchain-musl"],
    runtime_deps = ["util-linux"],
    replaces = ["busybox"],
    container = "toolchain-musl",
    container_arch = "target",
    sandbox = True,
    shell = "bash",
    tasks = [
        task("build", steps = [
            # configure auto-detects libelf via pkg-config and unconditionally
            # appends -DHAVE_ELF and -lelf to CFLAGS/LDLIBS in config.mk, so
            # `make HAVE_ELF=n` cannot undo it. Strip those lines after
            # configure so ip(8) does not link against libelf.so.1 — we don't
            # ship elfutils in images.
            "./configure",
            "sed -i '/^HAVE_ELF:=/d; /-DHAVE_ELF/d; /-lelf/d' config.mk",
            "make CC=cc HAVE_CAP=n HAVE_SELINUX=n -j$NPROC " +
                "CONFDIR=/etc/iproute2",
            "make DESTDIR=$DESTDIR PREFIX=/usr SBINDIR=/sbin " +
                "CONFDIR=/etc/iproute2 install",
        ]),
    ],
)
