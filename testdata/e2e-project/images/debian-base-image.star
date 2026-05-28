load("@core//classes/image.star", "image")

# A minimal Debian image to exercise the full Debian backend end-to-end:
# distro="debian" routes the closure walk through the visibility
# filter, the build executor's packageDeb branch handles each artifact,
# and Assemble's debian path extracts each .deb into the rootfs before
# running dpkg --configure -a in toolchain-glibc. Followed by
# StageProjectAPTSource which lays down the keyring + deb822 sources,
# and a syslinux/extlinux bootloader install via the debian-specific
# disk path in image.star.
#
# Artifact set is the smallest closure that boots in QEMU and accepts
# an SSH login: kernel, init, networking, openssh-server. Expand as
# needed for the device's runtime requirements.
image(
    name = "debian-base-image",
    distro = "debian",
    artifacts = [
        "linux-image-amd64",
        "systemd-sysv",
        "systemd-resolved",
        "init",
        "libc6",
        "coreutils",
        "bash",
        "dpkg",
        "apt",
        "openssh-server",
        "isc-dhcp-client",
        "ifupdown",
    ],
)
