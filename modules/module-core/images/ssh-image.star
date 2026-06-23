load("@core//classes/image.star", "image")
load("@core//classes/users.star", "user")
load("@core//units/base/base-files.star", "base_files")

# Minimal boot + SSH image, one definition for every distro — the apt branches
# match this module's base-image (already the smallest boot+SSH closure), the
# Alpine branch is its busybox/openrc/apk equivalent. See base-image for why the
# dpkg-configure essentials are listed explicitly and how NetworkManager comes up.
_APT_SSH = [
    "systemd-sysv",
    "systemd-resolved",
    "init",
    "libc6",
    "libc-bin",
    "base-files",
    "base-passwd",
    "dash",
    "diffutils",
    "coreutils",
    "dpkg",
    "apt",
    "openssh-server",
    "network-manager",
]

# Alpine seeds its users via a custom base-files unit; the apt distros get users
# from base-passwd + maintainer scripts, so this unit is Alpine-only.
base_files(
    name = "base-files-ssh",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000, password = "password"),
    ],
)

image(
    name = "ssh-image",
    artifacts = ["linux", "bash"],
    distro_artifacts = {
        "alpine": [
            "base-files-ssh", "busybox", "busybox-binsh", "musl",
            "kmod", "util-linux", "e2fsprogs", "eudev",
            "openrc", "apk-tools", "network-config", "dhcpcd", "openssh",
        ],
        "debian": _APT_SSH,
        "ubuntu": _APT_SSH + ["nm-manage-ethernet"],
    },
)
