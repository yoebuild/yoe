machine(
    name = "qemu-arm64",
    arch = "arm64",
    description = "QEMU ARM64 virtual machine",
    kernel = kernel(
        # Per-distro kernel: the from-source `linux` unit on Alpine, the stock
        # feed kernel meta-package on the apt distros. image() resolves the
        # "linux" provides-name to the entry for the build's effective distro.
        distro_unit = {
            "alpine": "linux",
            "debian": "linux-image-arm64",
            "ubuntu": "linux-image-generic",
        },
        provides = "linux",
        defconfig = "defconfig",
        cmdline = "console=ttyAMA0 root=/dev/vda1 rw",
    ),
    partitions = [
        partition(label = "rootfs", type = "ext4", size = "2G", root = True),
    ],
    qemu = qemu_config(
        machine = "virt",
        cpu = "host",
        memory = "4G",
        display = "none",
        ports = ["2222:22", "8080:80", "8118:8118"],
    ),
)
