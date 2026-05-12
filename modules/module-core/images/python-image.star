load("//classes/image.star", "image")
load("//classes/users.star", "user")
load("//units/base/base-files.star", "base_files")

base_files(
    name = "base-files-python",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000, password = "password"),
    ],
)

# Boots into a userland that already has python3 and pip installed, so
# `pip install <pkg>` works on first login without an extra apk pull. The
# dev-image's diagnostic and networking userland is included so the device
# is usable for actual Python work, not just `python3 --version`.
image(
    name = "python-image",
    artifacts = [
        "base-files-python", "busybox", "busybox-binsh", "musl", "kmod",
        "util-linux", "e2fsprogs", "eudev", "linux", "openrc",
        "network-config", "iproute2",
        "dhcpcd", "ntp-client", "mdnsd", "openssh", "ca-certificates",
        "curl", "bash", "less", "file", "procps-ng",
        "htop", "strace", "apk-tools",
        "python3", "py3-pip",
    ],
)
