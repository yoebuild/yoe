unit(
    name = "rpi-firmware",
    version = "2025.03.24",
    source = "https://github.com/raspberrypi/firmware.git",
    tag = "1.20250305",
    license = "Broadcom-RPi",
    description = "Raspberry Pi GPU firmware blobs (boot partition)",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # No compilation — install prebuilt firmware blobs
            "install -D boot/start4.elf $DESTDIR/boot/start4.elf",
            "install -D boot/start4x.elf $DESTDIR/boot/start4x.elf",
            "install -D boot/start4cd.elf $DESTDIR/boot/start4cd.elf",
            "install -D boot/start4db.elf $DESTDIR/boot/start4db.elf",
            "install -D boot/fixup4.dat $DESTDIR/boot/fixup4.dat",
            "install -D boot/fixup4x.dat $DESTDIR/boot/fixup4x.dat",
            "install -D boot/fixup4cd.dat $DESTDIR/boot/fixup4cd.dat",
            "install -D boot/fixup4db.dat $DESTDIR/boot/fixup4db.dat",
            # bootcode.bin needed for RPi4 (RPi5 has firmware in EEPROM)
            "install -D boot/bootcode.bin $DESTDIR/boot/bootcode.bin",
        ]),
    ],
)
