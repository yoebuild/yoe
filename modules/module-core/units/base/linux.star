unit(
    name = "linux",
    version = "6.6.87",
    release = 2,
    source = "https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git",
    tag = "v6.6.87",
    license = "GPL-2.0",
    description = "Linux kernel",
    deps = ["toolchain"],
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            install_file("container.cfg", "$SRCDIR/.yoe-container.cfg"),
            install_file("graphics.cfg", "$SRCDIR/.yoe-graphics.cfg"),
            # Use arch-appropriate defconfig and kernel image target.
            # ARCH is set by the build system (x86_64, arm64, riscv64).
            """
case $ARCH in
    x86_64)  KARCH=x86_64; DEFCONFIG=x86_64_defconfig; TARGET=bzImage; IMAGE=arch/x86/boot/bzImage ;;
    arm64)   KARCH=arm64;   DEFCONFIG=defconfig;         TARGET=Image;   IMAGE=arch/arm64/boot/Image ;;
    riscv64) KARCH=riscv;   DEFCONFIG=defconfig;         TARGET=Image;   IMAGE=arch/riscv/boot/Image ;;
    *)       echo "unsupported ARCH=$ARCH"; exit 1 ;;
esac
make ARCH=$KARCH $DEFCONFIG
# Merge in CONFIG fragments. `container.cfg` enables overlayfs / netfilter
# / namespaces / eBPF so dockerd/podman/runc work; `graphics.cfg` enables
# DRM + framebuffer (virtio-gpu, bochs, vesafb, efifb) so /dev/fb0 exists
# under `yoe run --display` and on real boards with the same GPUs. Both
# add a modest amount to the kernel and benefit every image, so they are
# always merged rather than gated per-image.
scripts/kconfig/merge_config.sh -m -O . .config .yoe-container.cfg .yoe-graphics.cfg
make ARCH=$KARCH olddefconfig
""",
            """
case $ARCH in
    x86_64)  KARCH=x86_64; TARGET=bzImage; IMAGE=arch/x86/boot/bzImage ;;
    arm64)   KARCH=arm64;   TARGET=Image;   IMAGE=arch/arm64/boot/Image ;;
    riscv64) KARCH=riscv;   TARGET=Image;   IMAGE=arch/riscv/boot/Image ;;
esac
make ARCH=$KARCH -j$NPROC $TARGET modules
""",
            """
case $ARCH in
    x86_64)  IMAGE=arch/x86/boot/bzImage ;;
    arm64)   IMAGE=arch/arm64/boot/Image ;;
    riscv64) IMAGE=arch/riscv/boot/Image ;;
esac
install -D $IMAGE $DESTDIR/boot/vmlinuz
""",
            # Install modules into rootfs at /lib/modules/<kver>/.
            # DEPMOD=true skips depmod (not in build container); target runs it.
            """
case $ARCH in
    x86_64)  KARCH=x86_64 ;;
    arm64)   KARCH=arm64  ;;
    riscv64) KARCH=riscv  ;;
esac
make ARCH=$KARCH INSTALL_MOD_PATH=$DESTDIR DEPMOD=true modules_install
rm -f $DESTDIR/lib/modules/*/build $DESTDIR/lib/modules/*/source
""",
        ]),
    ],
)
