load("@core//classes/image.star", "image")
load("@core//classes/users.star", "user")
load("@core//units/base/base-files.star", "base_files")

# Dev image, one definition for every distro: the base-image closure plus a
# diagnostic + editor userland so the device is usable for real work over SSH.

# Minimal boot + SSH closure shared by the apt distros (see base-image).
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

# apt-specific dev tools — the apt names for roles whose package differs from
# Alpine's, plus apt-only additions.
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
    # Distro-neutral entries: the kernel (resolved per distro by the machine),
    # the shell, and the leaf CLI tools whose package name is identical on every
    # distro. Tools whose name differs (openssh vs openssh-server, procps-ng vs
    # procps) stay in the per-distro branches below.
    artifacts = [
        "linux", "bash",
        "ca-certificates", "curl", "less", "file", "htop", "strace", "iproute2",
        # Dormant on-device upstream-feed enabler (run
        # yoe-enable-upstream-feeds to opt in). Distro-neutral: each distro
        # module ships its own "upstream-feeds" companion, so the resolver
        # picks the right one per image. See docs/on-device-upstream-feeds.md.
        "upstream-feeds",
    ],
    distro_artifacts = {
        "alpine": [
            "base-files-dev", "busybox", "busybox-binsh", "musl", "kmod",
            "util-linux", "e2fsprogs", "eudev", "openrc",
            "network-config", "dhcpcd", "ntp-client", "mdnsd",
            "openssh", "simpleiot", "procps-ng", "apk-tools",
            "yazi", "zellij", "helix",
        ],
        "debian": _APT_BASE + _APT_DEV,
        "ubuntu": _APT_BASE + ["nm-manage-ethernet"] + _APT_DEV,
    },
)
