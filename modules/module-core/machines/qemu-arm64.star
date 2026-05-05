machine(
    name = "qemu-arm64",
    arch = "arm64",
    description = "QEMU ARM64 virtual machine",
    kernel = kernel(
        unit = "linux",
        provides = "linux",
        defconfig = "defconfig",
        cmdline = "console=ttyAMA0 root=/dev/vda1 rw",
    ),
    partitions = [
        partition(label = "rootfs", type = "ext4", size = "512M", root = True),
    ],
    qemu = qemu_config(
        machine = "virt",
        cpu = "host",
        memory = "1G",
        display = "none",
        ports = ["2222:22", "8080:80", "8118:8118"],
    ),
)
