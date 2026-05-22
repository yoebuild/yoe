load("//classes/image.star", "image")
load("//classes/users.star", "user")
load("//units/base/base-files.star", "base_files")

# dev-image plus the build-host stack: yoe itself, Go, git, bwrap, Docker
# (engine + cli + containerd + runc + libseccomp + iptables pulled
# transitively by the alpine `docker` apk), and the first-boot rootfs
# grow service so /var/lib/docker has room to breathe.
#
# `user` joins the docker group so `docker run` and `yoe build` work
# without sudo. The docker group GID is baked into /etc/group at
# install time; docker-engine's later `addgroup -S docker` is a no-op.
base_files(
    name = "base-files-selfhost",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000, password = "password",
             groups = ["docker"]),
    ],
)

image(
    name = "selfhost-image",
    artifacts = [
        # dev-image baseline
        "base-files-selfhost", "busybox", "busybox-binsh", "musl", "kmod",
        "util-linux", "e2fsprogs", "eudev", "linux", "openrc",
        "network-config", "iproute2",
        "dhcpcd", "ntp-client", "mdnsd", "openssh", "ca-certificates",
        "curl", "simpleiot", "bash", "less", "file", "procps-ng",
        "htop", "strace", "apk-tools",
        "yazi", "zellij", "helix",
        # build-host additions
        "yoe", "go", "git", "bubblewrap",
        "docker", "docker-init",
        "grow-rootfs", "qemu-system-x86_64"
    ],
)
