load("//classes/image.star", "image")
load("//classes/users.star", "user")
load("//units/base/base-files.star", "base_files")

base_files(
    name = "base-files-docker",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000, password = "password"),
    ],
)

# Ships the Docker userspace (engine, CLI, buildx, containerd, runc, tini,
# iptables) on top of the dev-image content. The kernel `container.cfg`
# fragment (overlayfs, bridge/veth, netfilter, cgroup v2, seccomp,
# namespaces) is merged into linux-rpi4/linux-rpi5/linux-beagleplay, so
# `dockerd` starts cleanly under OpenRC on those targets. For a complete
# build-host stack (yoe + Go + git + Docker on one image), see
# selfhost-image.star and docs/selfhost-rpi5.md.
image(
    name = "docker-image",
    artifacts = [
        "base-files-docker", "busybox", "busybox-binsh", "musl", "kmod",
        "util-linux", "e2fsprogs", "eudev", "linux", "network-config",
        "iproute2",
        "dhcpcd", "ntp-client", "mdnsd", "openssh", "ca-certificates",
        "curl", "simpleiot", "bash", "less", "file", "procps-ng",
        "htop", "strace", "apk-tools",
        "yazi", "zellij", "helix",
        "docker", "docker-init",
    ],
)
