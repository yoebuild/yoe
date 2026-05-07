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
# iptables) on top of the dev-image content. The kernel currently lacks the
# CONFIG fragment Docker needs (overlayfs, bridge/veth, netfilter, cgroup v2,
# seccomp, namespaces) and busybox init does not supervise dockerd, so
# `dockerd` will not actually start a container on this image yet — that is
# the next milestone. See docs/containers.md.
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
        "docker",
    ],
    hostname = "yoe-docker",
)
