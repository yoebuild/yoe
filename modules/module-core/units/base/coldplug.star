# Coldplug: load modules for hardware that was already present at boot.
#
# The kernel emits a uevent when it registers a device, and a running device
# manager turns that into a modprobe. Devices on buses whose controllers are
# built into the kernel are registered during early boot, long before any
# userspace exists to hear about them — so a modular driver for a soldered-on
# peripheral is never loaded, and the device silently does not appear. The
# fix is a boot-time sweep of /sys for modalias files.
#
# Alpine's openrc package ships `hwdrivers`, which does exactly this sweep,
# but it is unusable here: its depend() has `need dev`, and nothing in a yoe
# image provides `dev` — `devfs` provides `dev-mount` and orders itself
# *before* `dev`, and yoe ships eudev's udevd binary without the OpenRC
# scripts Alpine keeps in a separate udev-init-scripts package. OpenRC's
# `rc_need` in /etc/conf.d only adds dependencies, so the `need dev` cannot
# be dropped from the outside. Rather than pull a whole device manager into
# every image's boot to satisfy one dependency, this unit ships an
# equivalent sweep whose dependencies a yoe image actually satisfies.
#
# Debian and Ubuntu need nothing: systemd's udev package ships
# systemd-udev-trigger.service already enabled via
# /usr/lib/systemd/system/sysinit.target.wants/, and that replays the coldplug
# uevents for everything in /sys. The build steps below are gated on $DISTRO
# so the apt images get an empty package rather than an OpenRC script their
# init would never read.
#
# The runlevel symlink is written directly instead of via `services = [...]`
# because that field always enables into the `default` runlevel, and coldplug
# has to run in `sysinit` — before localmount and network, so that storage and
# network drivers are loaded by the time those services need them. base-files
# lays down its own runlevel symlinks the same way.
unit(
    name = "coldplug",
    version = "1.0.0",
    license = "MIT",
    description = "Boot-time sweep of /sys that loads modules for already-present hardware",
    deps = ["toolchain"],
    # kmod supplies the real modprobe; busybox's applet does not honour the
    # -b (blacklist-aware) and -a (multiple aliases) flags the sweep relies on.
    distro_runtime_deps = {"alpine": ["openrc", "kmod"]},
    container = "toolchain",
    container_arch = "target",
    tasks = [
        # Alpine only. The systemd distros get an empty package: their udev
        # already replays the coldplug uevents, so there is nothing to add and
        # no OpenRC script their init would ever read.
        task("build", distros = ["alpine"], steps = [
            "mkdir -p $DESTDIR/etc/init.d $DESTDIR/etc/runlevels/sysinit",
            install_file("coldplug", "$DESTDIR/etc/init.d/coldplug", mode = 0o755),
            "ln -sf /etc/init.d/coldplug $DESTDIR/etc/runlevels/sysinit/coldplug",
        ]),
    ],
)
