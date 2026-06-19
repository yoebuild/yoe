load("@core//classes/image.star", "image")

# Minimal bootable image, one definition for every distro. The smallest closure
# that boots in QEMU and accepts an SSH login. `distro_artifacts` carries each
# distro's package set; only the branch matching the build's effective distro is
# consulted. The kernel is referenced as the virtual name `"linux"` and resolved
# per (machine, distro) by the machine's kernel config.
#
# The apt branches list the dpkg-configure essentials (dash, diffutils, libc-bin,
# base-files, base-passwd) explicitly because mmdebstrap --variant=custom installs
# no implicit Essential/Priority base. NetworkManager self-enables via postinst
# and auto-DHCPs unmanaged ethernet; Ubuntu additionally needs the
# nm-manage-ethernet drop-in (its NM leaves wired ethernet to netplan, which yoe
# images don't carry). See docs/specs/2026-06-03-debian-device-networking.md.
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
    "bash",
    "dpkg",
    "apt",
    "openssh-server",
    "network-manager",
]

image(
    name = "base-image",
    artifacts = ["linux"],
    distro_artifacts = {
        "alpine": [
            "musl", "base-files", "busybox", "busybox-binsh",
            "apk-tools", "openrc", "network-config",
        ],
        "debian": _APT_BASE,
        "ubuntu": _APT_BASE + ["nm-manage-ethernet"],
    },
)
