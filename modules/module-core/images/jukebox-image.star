load("//classes/image.star", "image")
load("//classes/users.star", "user")
load("//units/base/base-files.star", "base_files")

base_files(
    name = "base-files-jukebox",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000, password = "password"),
    ],
)

# Boots straight into a running Navidrome (Subsonic/Airsonic-compatible
# music server) on its default port 4533. Drop music files under /music
# and the navidrome service scans them on first start, exposing the
# Subsonic API to any compatible client (DSub, Substreamer, Symfonium,
# Tempo, ...). Web UI on http://<host>:4533.
#
# The OpenRC init script ships in navidrome-openrc and is auto-enabled
# at boot by image assembly (per the "installed packages run their
# services" rule in CLAUDE.md). ffmpeg + libtag come along as
# navidrome's transcoding and tag-reading runtime deps.
#
# `hostname = "jukebox"` overrides the per-machine default so a fleet of
# identically-imaged devices is reachable at jukebox.local regardless of
# board (qemu-x86_64.local, raspberrypi4.local, beagleplay.local).
image(
    name = "jukebox-image",
    hostname = "jukebox",
    artifacts = [
        "base-files-jukebox", "busybox", "busybox-binsh", "musl", "kmod",
        "util-linux", "e2fsprogs", "eudev", "linux", "openrc",
        "network-config", "iproute2",
        "dhcpcd", "ntp-client", "mdnsd", "openssh", "ca-certificates",
        "curl", "bash", "less", "file", "procps-ng",
        "htop", "strace", "apk-tools",
        "navidrome", "navidrome-openrc",
    ],
)
