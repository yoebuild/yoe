unit(
    name = "linux-rpi5",
    version = "6.12",
    scope = "machine",
    source = "https://github.com/raspberrypi/linux.git",
    branch = "rpi-6.12.y",
    license = "GPL-2.0",
    description = "Raspberry Pi 5 kernel (BCM2712)",
    deps = ["toolchain"],
    # The kernel's certs/extract-cert host tool needs OpenSSL/libcrypto
    # headers. Package name differs by backend: Alpine bundles them in
    # openssl-dev, the apt distros split them into libssl-dev.
    distro_deps = {
        "alpine": ["openssl-dev"],
        "debian": ["libssl-dev"],
        "ubuntu": ["libssl-dev"],
    },
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            install_file("container.cfg", "$SRCDIR/.yoe-container.cfg"),
            # Generate defconfig, merge container-runtime CONFIG fragment
            # (overlayfs, netfilter, namespaces, eBPF cgroup support) so
            # dockerd/podman/runc work out of the box, then resolve deps.
            """
make ARCH=arm64 bcm2712_defconfig
scripts/kconfig/merge_config.sh -m -O . .config .yoe-container.cfg
make ARCH=arm64 olddefconfig
""",
            # Audit the resolved .config for the CONFIG options Docker /
            # containerd / runc require. Fails fast (before the expensive
            # `make Image` step) if container.cfg drifts out of sync with
            # what container runtimes actually need.
            """
required="CONFIG_NAMESPACES CONFIG_PID_NS CONFIG_NET_NS CONFIG_IPC_NS \
CONFIG_UTS_NS CONFIG_USER_NS CONFIG_CGROUPS CONFIG_MEMCG CONFIG_CPUSETS \
CONFIG_OVERLAY_FS CONFIG_BRIDGE CONFIG_VETH CONFIG_NF_TABLES \
CONFIG_NF_NAT CONFIG_IP_NF_IPTABLES CONFIG_SECCOMP CONFIG_SECCOMP_FILTER \
CONFIG_KEYS CONFIG_POSIX_MQUEUE CONFIG_BPF_SYSCALL"
missing=""
for opt in $required; do
    grep -qE "^${opt}=(y|m)" .config || missing="$missing $opt"
done
if [ -n "$missing" ]; then
    echo "container-config check FAILED — missing CONFIGs:$missing" >&2
    exit 1
fi
echo "container-config check passed"
""",
            # HOSTCFLAGS/HOSTLDFLAGS carry the sysroot include/lib paths to the
            # kernel's host tools (e.g. certs/extract-cert, which #includes
            # <openssl/bio.h>). Host tools do not inherit CFLAGS/CPPFLAGS.
            "make ARCH=arm64 HOSTCFLAGS=\"$CPPFLAGS\" HOSTLDFLAGS=\"$LDFLAGS\" -j$NPROC Image modules dtbs",
            # Install kernel as kernel_2712.img (RPi5 naming convention)
            "install -D arch/arm64/boot/Image $DESTDIR/boot/kernel_2712.img",
            # Install device trees
            "install -D arch/arm64/boot/dts/broadcom/bcm2712-rpi-5-b.dtb $DESTDIR/boot/bcm2712-rpi-5-b.dtb",
            # Install overlays directory
            "mkdir -p $DESTDIR/boot/overlays",
            "cp arch/arm64/boot/dts/overlays/*.dtbo $DESTDIR/boot/overlays/ 2>/dev/null || true",
            # Install modules into rootfs at /lib/modules/<kver>/.
            # DEPMOD=true skips depmod (not in build container); target runs it.
            "make ARCH=arm64 INSTALL_MOD_PATH=$DESTDIR DEPMOD=true modules_install",
            # Drop broken build/source symlinks pointing into the host build tree.
            "rm -f $DESTDIR/lib/modules/*/build $DESTDIR/lib/modules/*/source",
        ]),
    ],
)
