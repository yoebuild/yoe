unit(
    name = "rpi5-config",
    version = "1.0.0",
    scope = "machine",
    license = "MIT",
    description = "Raspberry Pi 5 boot configuration (config.txt, cmdline.txt)",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            """
mkdir -p $DESTDIR/boot
cat > $DESTDIR/boot/config.txt << 'EOF'
# Raspberry Pi 5 boot configuration
arm_64bit=1
enable_uart=1
kernel=kernel_2712.img
dtoverlay=vc4-kms-v3d-pi5
# Disable RPi logo on boot
disable_splash=1
EOF
""",
            """
cat > $DESTDIR/boot/cmdline.txt << 'EOF'
console=ttyAMA10,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait rw
EOF
""",
        ]),
    ],
)
