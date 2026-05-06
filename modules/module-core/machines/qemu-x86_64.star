machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    description = "QEMU x86_64 virtual machine (KVM)",
    kernel = kernel(
        unit = "linux",
        provides = "linux",
        defconfig = "x86_64_defconfig",
        cmdline = "console=ttyS0 root=/dev/vda2 rw",
    ),
    packages = ["syslinux"],
    partitions = [
        partition(label = "rootfs", type = "ext4", size = "600M", root = True),
    ],
    qemu = qemu_config(
        machine = "q35",
        cpu = "host",
        memory = "1G",
        firmware = "seabios",
        display = "none",
        ports = ["2222:22", "8080:80", "8118:8118"],
    ),
)
