load("//classes/image.star", "image")
load("//classes/users.star", "user")
load("//units/base/base-files.star", "base_files")

base_files(
    name = "base-files-nodejs",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000, password = "password"),
    ],
)

# Boots into a userland that already has node and npm installed, so
# `npm install <pkg>` works on first login without an extra apk pull. The
# dev-image's diagnostic and networking userland is included so the device
# is usable for actual Node.js work, not just `node --version`. Also
# bundles the nodejs-hello demo app so a fresh boot can show off an
# apk-packaged npm dependency with `nodejs-hello "..."`.
image(
    name = "nodejs-image",
    artifacts = [
        "base-files-nodejs", "busybox", "busybox-binsh", "musl", "kmod",
        "util-linux", "e2fsprogs", "eudev", "linux", "openrc",
        "network-config", "iproute2",
        "dhcpcd", "ntp-client", "mdnsd", "openssh", "ca-certificates",
        "curl", "bash", "less", "file", "procps-ng",
        "htop", "strace", "apk-tools",
        "nodejs", "npm",
        "nodejs-hello",
    ],
)
