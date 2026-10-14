load("@core//classes/image.star", "image")

# IoT gateway image — boot test for the monitoring stack.
#
# This lives in the project rather than in module-core because it exists to
# exercise the monitoring units -- simpleiot-bin, victoria-metrics, grafana,
# and the two provisioning units that wire them together -- on a running
# system, not to ship. It is the dev-image closure with SSH, plus the three units and
# the room grafana needs: the machine's default 2 GB rootfs does not hold a
# 930 MB Grafana alongside a dev userland.
image(
    name = "iot-gateway-image",
    artifacts = [
        "linux", "bash", "ca-certificates", "curl", "less", "iproute2",
    ],
    distro_artifacts = {
        "alpine": [
            "base-files", "busybox", "busybox-binsh", "musl", "kmod",
            "util-linux", "e2fsprogs", "eudev", "openrc",
            "network-config", "dhcpcd", "openssh", "procps-ng",
            "simpleiot-bin", "victoria-metrics", "grafana",
            "simpleiot-config", "grafana-config",
        ],
        "debian": [
            "systemd-sysv", "systemd-resolved", "init", "libc6", "libc-bin",
            "base-files", "base-passwd", "dash", "diffutils", "coreutils",
            "dpkg", "apt", "openssh-server", "network-manager", "procps",
            # systemd only Recommends timesyncd and yoe builds with Recommends
            # off, so name it or the image has no NTP client.
            "systemd-timesyncd",
            "simpleiot-bin", "victoria-metrics", "grafana",
            "simpleiot-config", "grafana-config",
        ],
    },
    # Keep the machine's layout (the Pi and BeaglePlay boot from a FAT
    # partition ahead of the rootfs) and only grow the root partition.
    # Listing a bare rootfs here replaces the machine layout outright and
    # leaves those boards with no boot partition.
    partitions = [
        partition(
            label = p.label,
            type = p.type,
            size = "6G" if p.root else p.size,
            root = p.root,
            contents = getattr(p, "contents", []),
        )
        for p in ctx.machine_config.partitions
    ],
)
