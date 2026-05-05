load("//classes/autotools.star", "autotools")

# Uses tarball because util-linux's autoreconf from git requires gtkdocize
# and gettext macros not in the build container. Configure must be run with
# bash and CONFIG_SHELL=bash — the #!/bin/sh shebang invokes busybox sh
# which lacks $LINENO, triggering autoconf's configure.lineno fallback that
# floods expr calls until file descriptors are exhausted.
autotools(
    name = "util-linux",
    version = "2.41.3",
    source = "https://www.kernel.org/pub/linux/utils/util-linux/v2.41/util-linux-2.41.3.tar.xz",
    license = "GPL-2.0-or-later AND LGPL-2.1-or-later AND BSD-3-Clause",
    description = "essential system utilities for mounting, partitioning, and device management",
    deps = ["ncurses", "zlib"],
    runtime_deps = ["ncurses", "zlib"],
    replaces = ["busybox"],
    tasks = [
        task("build", steps=[
            "test -f configure || autoreconf -fi",
            "CONFIG_SHELL=bash bash ./configure --prefix=$PREFIX " +
            "--disable-all-programs " +
            "--enable-mount " +
            "--enable-losetup " +
            "--enable-fsck " +
            "--enable-lsblk " +
            "--enable-sfdisk " +
            "--enable-findmnt " +
            "--enable-wipefs " +
            "--enable-flock " +
            "--enable-dmesg " +
            "--enable-agetty " +
            "--enable-switch_root " +
            "--enable-hwclock " +
            "--disable-hwclock-gplv3 " +
            "--enable-nsenter " +
            "--enable-unshare " +
            "--enable-logger " +
            "--enable-libblkid " +
            "--enable-libfdisk " +
            "--enable-libmount " +
            "--enable-libsmartcols " +
            "--enable-libuuid " +
            "--disable-nls " +
            "--disable-asciidoc " +
            "--disable-poman " +
            "--disable-makeinstall-chown " +
            "--without-python " +
            "--without-systemd " +
            "--without-udev " +
            "--without-selinux " +
            "--without-audit " +
            "--without-readline " +
            "--without-econf",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
