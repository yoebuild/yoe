machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    description = "QEMU x86_64 virtual machine (KVM)",
    kernel = kernel(
        # Per-distro kernel: the from-source `linux` unit on Alpine, the stock
        # feed kernel meta-package on the apt distros. image() resolves the
        # "linux" provides-name to the entry for the build's effective distro.
        distro_unit = {
            "alpine": "linux",
            "debian": "linux-image-amd64",
            "ubuntu": "linux-image-generic",
        },
        provides = "linux",
        defconfig = "x86_64_defconfig",
        cmdline = "console=ttyS0 root=/dev/vda1 rw",
    ),
    packages = ["syslinux"],
    partitions = [
        partition(label = "rootfs", type = "ext4", size = "2G", root = True),
    ],
    qemu = qemu_config(
        machine = "q35",
        cpu = "host",
        memory = "4G",
        firmware = "seabios",
        display = "none",
        ports = ["2222:22", "8080:80", "8118:8118"],
    ),
)
