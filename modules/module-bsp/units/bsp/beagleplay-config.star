unit(
    name = "beagleplay-config",
    version = "1.0.0",
    scope = "machine",
    license = "MIT",
    description = "BeaglePlay U-Boot environment (uEnv.txt) — bootargs + load/booti for Image + DTB",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # uEnv.txt sits at the FAT boot partition root; U-Boot's
            # `boot` command on AM62x sources it (importenv), then runs
            # `uenvcmd` if present. We use that to load the kernel + DTB
            # and `booti` them with the right console/root command line.
            #
            # mmc 1 = on-board eMMC; partition 1 (FAT) holds the kernel,
            # partition 2 (ext4) holds rootfs (= /dev/mmcblk1p2 in Linux).
            # ttyS2 is the BeaglePlay debug UART; see meta-ti/am62xx.inc
            # `SERIAL_CONSOLES = "115200;ttyS2 ..."`.
            #
            # Subdir `extlinux/extlinux.conf` would be the more modern
            # alternative, but yoe's vfat partition assembly currently
            # flattens paths in the partition `contents` list — uEnv.txt
            # works without that limitation.
            """
mkdir -p $DESTDIR/boot
cat > $DESTDIR/boot/uEnv.txt << 'EOF'
bootargs=console=ttyS2,115200 earlycon=ns16550a,mmio32,0x02800000 root=/dev/mmcblk1p2 rootfstype=ext4 rootwait rw
loadaddr=0x82000000
fdt_addr_r=0x88000000
uenvcmd=load mmc 1:1 ${loadaddr} Image; load mmc 1:1 ${fdt_addr_r} k3-am625-beagleplay.dtb; booti ${loadaddr} - ${fdt_addr_r}
EOF
""",
        ]),
    ],
)
