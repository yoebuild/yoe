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
    },
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # Run setup.py under whichever interpreter the sysroot
            # actually has. Alpine ships /usr/bin/python3; Debian
            # ships /usr/bin/python3.11 with no python3 symlink
            # (update-alternatives postinst doesn't run here).
            # `command -v` picks the first one present.
            #
            # --single-version-externally-managed bypasses debian's
            # _distutils_hack/distutils-precedence patch that
            # otherwise redirects --prefix=/usr installs to
            # /usr/local to protect dpkg-managed paths. Without it
            # meson lands at /usr/local/bin/meson, which isn't on
            # the build sysroot's PATH, and downstream consumers
            # (kmod's `meson setup build` step) fail with
            # `meson: not found`. The flag is a no-op on alpine
            # where there's no such redirect.
            "if command -v python3 >/dev/null 2>&1; then PY=python3; else PY=python3.11; fi; "
            + "INSTALL_OPTS='--prefix=$PREFIX --root=$DESTDIR'; "
            + "if [ \"$PY\" = python3.11 ]; then INSTALL_OPTS=\"$INSTALL_OPTS --install-layout=deb --single-version-externally-managed --record /dev/null\"; fi; "
            + "\"$PY\" setup.py install $INSTALL_OPTS",
        ]),
    ],
)
