load("@core//classes/image.star", "image")

# A minimal Debian image to exercise the full Debian backend end-to-end:
# distro="debian" routes the closure walk through R21a's visibility
# filter, the build executor's packageDeb branch handles each artifact,
# and Assemble's debian path extracts each .deb into the rootfs before
# running dpkg --configure -a in toolchain-glibc.
#
# Artifacts are pinned to a small set of essential bookworm packages —
# enough to bring up SSH on the target without the full systemd init
# stack. Adjust the list to match what the device needs at boot.
image(
    name = "debian-base-image",
    distro = "debian",
    artifacts = [
        "libc6",
        "coreutils",
        "bash",
        "dpkg",
        "apt",
        "openssh-server",
    ],
)
