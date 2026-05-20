machine(
    name = "raspberrypi4",
    arch = "arm64",
    description = "Raspberry Pi 4 Model B",
    kernel = kernel(
        unit = "linux-rpi4",
        provides = "linux",
        defconfig = "bcm2711_defconfig",
        cmdline = "console=ttyS0,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait rw",
    ),
    packages = ["rpi-firmware", "rpi4-config"],
    partitions = [
        partition(label = "boot", type = "vfat", size = "64M", contents = ["kernel", "dtbs", "firmware"]),
        partition(label = "rootfs", type = "ext4", size = "1G", root = True),
    ],
)
