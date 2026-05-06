load("//classes/autotools.star", "autotools")

# eudev provides /dev management (udevd, udevadm) for systems that need more
# than busybox mdev. The hwdb generator requires gperf, which we don't ship
# yet — disable it; busybox-style coldplug + udev rules cover the common case.
autotools(
    name = "eudev",
    version = "3.2.14",
    source = "https://github.com/eudev-project/eudev.git",
    tag = "v3.2.14",
    license = "GPL-2.0-or-later AND LGPL-2.1-or-later",
    description = "Standalone fork of systemd-udev for dynamic /dev management",
    deps = ["util-linux", "kmod", "gperf"],
    runtime_deps = ["util-linux", "kmod"],
    configure_args = [
        "--exec-prefix=",
        "--with-rootprefix=",
        "--with-rootlibexecdir=/lib/udev",
        "--sysconfdir=/etc",
        "--disable-manpages",
        "--disable-hwdb",
        "--disable-introspection",
        "--disable-static",
        "--disable-blkid",
    ],
)
