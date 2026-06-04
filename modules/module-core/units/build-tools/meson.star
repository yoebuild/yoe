unit(
    name = "meson",
    version = "1.10.2",
    source = "https://github.com/mesonbuild/meson.git",
    tag = "1.10.2",
    license = "Apache-2.0",
    description = "high performance build system for C/C++ and other languages",
    # Build needs samurai (ninja-compatible), a C toolchain, and the
    # Python interpreter setup.py runs under. python3 + setuptools
    # name differs between distros — express both shapes here and
    # let the closure walker pick the right one per consumer.
    deps = ["samurai", "toolchain"],
    distro_deps = {
        "alpine": ["python3", "py3-setuptools"],
        # Debian's `python3` apt package is a transitional wrapper
        # (pdb3 only). The actual interpreter binary ships in
        # python3.11. Reference it explicitly so the build step
        # below can shell out to `python3.11` and find it.
        "debian": ["python3.11", "python3-setuptools"],
        # Ubuntu (resolute) ships the interpreter as python3.14; the
        # build step globs for python3.NN in the sysroot, so only the
        # package name differs from Debian.
        "ubuntu": ["python3.14", "python3-setuptools"],
    },
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # Run setup.py under the interpreter shipped in the build
            # sysroot — never the toolchain container's own python3.
            # Several Debian build packages (cmake, dpkg-dev, …) pull
            # /usr/bin/python3 into the container, so a bare
            # `command -v python3` finds the *container* python, which
            # has no setuptools and no path into the sysroot's
            # dist-packages. Search /build/sysroot/usr/bin explicitly:
            # Alpine ships python3 there; Debian ships python3.NN with
            # no python3 symlink (update-alternatives postinst doesn't
            # run here).
            "PY=''; "
            + "for c in /build/sysroot/usr/bin/python3 /build/sysroot/usr/bin/python3.[0-9] /build/sysroot/usr/bin/python3.[0-9][0-9]; do "
            + "[ -x \"$c\" ] && { PY=\"$c\"; break; }; done; "
            + "[ -n \"$PY\" ] || { echo 'meson: no python3 in build sysroot' >&2; exit 1; }; "
            # $PREFIX/$DESTDIR are expanded by distutils itself, not the
            # shell — the single quotes are intentional.
            + "INSTALL_OPTS='--prefix=$PREFIX --root=$DESTDIR'; "
            # Debian's setuptools (identified by its dist-packages
            # sys.path layout) needs --install-layout=deb plus
            # --single-version-externally-managed to bypass the
            # _distutils_hack/distutils-precedence patch that otherwise
            # redirects --prefix=/usr installs to /usr/local. Without
            # it meson lands at /usr/local/bin/meson, which isn't on
            # the build sysroot's PATH, and downstream consumers
            # (kmod's `meson setup build` step) fail with
            # `meson: not found`. No-op on Alpine, which has no such
            # redirect and uses the site-packages layout.
            + "if \"$PY\" -c 'import sys; sys.exit(0 if any(\"dist-packages\" in p for p in sys.path) else 1)'; then "
            + "INSTALL_OPTS=\"$INSTALL_OPTS --install-layout=deb --single-version-externally-managed --record /dev/null\"; fi; "
            + "\"$PY\" setup.py install $INSTALL_OPTS",
        ]),
    ],
)
