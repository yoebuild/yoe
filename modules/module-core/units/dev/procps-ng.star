# Use the upstream release tarball: the gitlab repo runs autopoint via
# autoreconf, which requires the full gettext-tools chain (not in the
# container). The release tarball ships pre-generated configure.
#
# ncurses is built without pkg-config files (`--enable-pc-files` is off in the
# ncurses unit), so procps-ng's pkg-config probe fails. Pass NCURSES_CFLAGS
# and NCURSES_LIBS explicitly to skip the probe.
unit(
    name = "procps-ng",
    version = "4.0.5",
    source = "https://downloads.sourceforge.net/project/procps-ng/Production/procps-ng-4.0.5.tar.xz",
    sha256 = "c2e6d193cc78f84cd6ddb72aaf6d5c6a9162f0470e5992092057f5ff518562fa",
    license = "GPL-2.0-or-later AND LGPL-2.1-or-later",
    description = "ps, top, free, vmstat and friends — full procps tools",
    deps = ["ncurses", "toolchain-musl"],
    runtime_deps = ["ncurses"],
    replaces = ["busybox"],
    container = "toolchain-musl",
    container_arch = "target",
    sandbox = True,
    shell = "bash",
    tasks = [
        task("build", steps = [
            "NCURSES_CFLAGS='-I$DESTDIR_DEPS/usr/include' " +
            "NCURSES_LIBS='-lncurses' " +
            "./configure --prefix=$PREFIX --exec-prefix= " +
                "--disable-static --without-systemd --without-elogind " +
                "--disable-nls",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
