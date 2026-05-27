load("//classes/image.star", "image")
load("//classes/users.star", "user")
load("//units/base/base-files.star", "base_files")

base_files(
    name = "base-files-qt",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000, password = "password"),
    ],
)

# qt-image boots straight into a small Qt 6 Widgets demo rendered to
# /dev/fb0 — a quick "yes, the graphical stack works" target useful on
# both real boards with a virtio-gpu/Bochs/VESA framebuffer and QEMU
# under `yoe run --display`. The demo binary is shipped by the qt-demo
# unit, which also ships the OpenRC init script and enables it via
# `services = ["qt-demo"]`.
#
# `hostname = "qt"` keeps a fleet of identically-imaged devices
# reachable at qt.local regardless of board.
#
# The closure (Qt 6 base + fontconfig + liberation fonts + the demo
# binary) lands at roughly 150 MiB on x86_64, so the default 2 GiB
# rootfs partition has comfortable headroom.
image(
    name = "qt-image",
    hostname = "qt",
    # No `eudev` in the artifact list: Qt 6 pulls in libinput-libs, whose
    # so:libudev.so.1 closure picks `libudev-zero` (the no-op libudev
    # stub Alpine ships alongside Qt). Explicit `eudev` then trips apk's
    # so:libudev.so.1 conflict at image-assembly time because eudev and
    # libudev-zero own the same SONAME. The qt-image doesn't need real
    # hotplug management — the kernel's devtmpfs populates /dev/fb0,
    # /dev/dri/, and friends on its own — so accepting the stub is the
    # cheapest path. Images that DO need eudev (jukebox-image,
    # dev-image) avoid the conflict by not pulling Qt in the first
    # place.
    artifacts = [
        "base-files-qt", "busybox", "busybox-binsh", "musl", "kmod",
        "util-linux", "e2fsprogs", "linux", "openrc",
        "network-config", "iproute2",
        "dhcpcd", "mdnsd", "openssh", "ca-certificates",
        "bash", "less", "file",
        "qt-demo",
    ],
)
