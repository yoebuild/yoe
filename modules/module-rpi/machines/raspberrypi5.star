machine(
    name = "raspberrypi5",
    arch = "arm64",
    description = "Raspberry Pi 5",
    kernel = kernel(
        unit = "linux-rpi5",
        provides = "linux",
        defconfig = "bcm2712_defconfig",
        cmdline = "console=ttyAMA10,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait rw",
    ),
    packages = ["rpi-firmware", "rpi5-config"],
    partitions = [
        partition(label = "boot", type = "vfat", size = "64M", contents = ["kernel", "dtbs", "firmware"]),
        partition(label = "rootfs", type = "ext4", size = "1G", root = True),
    ],
)
