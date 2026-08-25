load("@core//classes/image.star", "image")

# Gateway image — boot test for the monitoring stack.
#
# This lives in the project rather than in module-core because it exists to
# exercise simpleiot-bin, victoria-metrics, and grafana on a running system,
# not to ship. It is the dev-image closure with SSH, plus the three units and
# the room grafana needs: the machine's default 2 GB rootfs does not hold a
# 930 MB Grafana alongside a dev userland.
image(
    name = "gateway-image",
    artifacts = [
        "linux", "bash", "ca-certificates", "curl", "less",
    ],
    distro_artifacts = {
        "alpine": [
            "base-files", "busybox", "busybox-binsh", "musl", "kmod",
            "util-linux", "e2fsprogs", "eudev", "openrc",
            "network-config", "dhcpcd", "openssh", "procps-ng",
            "simpleiot-bin", "victoria-metrics", "grafana",
        ],
        "debian": [
            "systemd-sysv", "systemd-resolved", "init", "libc6", "libc-bin",
            "base-files", "base-passwd", "dash", "diffutils", "coreutils",
            "dpkg", "apt", "openssh-server", "network-manager", "procps",
            "simpleiot-bin", "victoria-metrics", "grafana",
        ],
    },
    partitions = [
        partition(label = "rootfs", type = "ext4", size = "6G", root = True),
    ],
)
