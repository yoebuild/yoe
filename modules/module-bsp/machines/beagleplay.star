machine(
    name = "beagleplay",
    arch = "arm64",
    description = "BeagleBoard.org BeaglePlay (TI AM625, quad Cortex-A53)",
    kernel = kernel(
        unit = "linux-beagleplay",
        provides = "linux",
        defconfig = "defconfig",
        cmdline = "console=ttyS2,115200 root=/dev/mmcblk1p2 rootfstype=ext4 rootwait rw",
    ),
    # The boot chain (ROM → tiboot3.bin → tispl.bin → u-boot.img → Image)
    # is assembled from these packages. Order matters only for human
    # readability — yoe resolves the actual dep DAG via each unit's deps.
    packages = [
        "u-boot-beagleplay-r5",   # tiboot3.bin (R5 SPL stage)
        "u-boot-beagleplay",      # tispl.bin + u-boot.img (A53 stages)
        "ti-linux-firmware",      # TIFS + DM blobs needed at runtime
        "beagleplay-config",      # uEnv.txt
    ],
    partitions = [
        # FAT boot partition holds every file ROM/U-Boot/distro_bootcmd
        # looks for. The `contents` patterns glob against $rootfs/boot/.
        partition(label = "boot",   type = "vfat", size = "128M",
                  contents = ["tiboot3.bin", "tispl.bin", "u-boot.img",
                              "Image", "k3-am625-beagleplay.dtb",
                              "uEnv.txt"]),
        partition(label = "rootfs", type = "ext4", size = "1G", root = True),
    ],
)
