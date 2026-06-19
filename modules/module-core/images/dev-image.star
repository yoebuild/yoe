load("@core//classes/image.star", "image")
load("@core//classes/users.star", "user")
load("@core//units/base/base-files.star", "base_files")

# Dev image, one definition for every distro: the base-image closure plus a
# diagnostic + editor userland so the device is usable for real work over SSH.
# Each distro's leaf tools differ by name (procps-ng vs procps, helix/yazi/zellij
# on Alpine vs vim-tiny on apt), which is exactly what distro_artifacts expresses.
_APT_BASE = [
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

# Tools whose package name is identical on every distro, so they live once in
# the shared artifacts list rather than being repeated per distro_artifacts branch.
_COMMON_DEV = [
    "strace",
    "ca-certificates",
    "curl",
    "less",
    "file",
    "htop",
    "iproute2",
]

# Apt-only leaf tools (names differ from or are absent on Alpine).
_APT_DEV = [
    "procps",
    "iputils-ping",
    "vim-tiny",
]

base_files(
    name = "base-files-dev",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000, password = "password"),
    ],
)

image(
    name = "dev-image",
    artifacts = ["linux", "bash"] + _COMMON_DEV,
    distro_artifacts = {
        "alpine": [
            "base-files-dev", "busybox", "busybox-binsh", "musl", "kmod",
            "util-linux", "e2fsprogs", "eudev", "openrc",
            "network-config", "dhcpcd", "ntp-client", "mdnsd",
            "openssh", "simpleiot",
            "procps-ng", "apk-tools", "yazi", "zellij", "helix",
        ],
        "debian": _APT_BASE + _APT_DEV,
        "ubuntu": _APT_BASE + ["nm-manage-ethernet"] + _APT_DEV,
    },
)
