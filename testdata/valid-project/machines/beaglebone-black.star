machine(
    name = "beaglebone-black",
    arch = "arm64",
    description = "BeagleBone Black (AM3358)",
    kernel = kernel(
        unit = "linux-beaglebone",
        provides = "linux",
        defconfig = "bb.org_defconfig",
        cmdline = "console=ttyS0,115200 root=/dev/mmcblk0p2 rootwait rw",
    ),
)
