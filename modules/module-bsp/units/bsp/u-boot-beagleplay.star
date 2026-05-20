unit(
    name = "u-boot-beagleplay",
    version = "2025.10",
    source = "https://github.com/beagleboard/u-boot.git",
    branch = "v2025.10-Beagle",
    license = "GPL-2.0-or-later",
    description = "U-Boot A53 (SPL + proper) for BeaglePlay (AM625) — produces tispl.bin + u-boot.img",
    # The A53 SPL embeds BL31 (TF-A), BL32 (OP-TEE), DM firmware, and TIFS
    # via binman. ti-linux-firmware + tfa-k3 + optee-k3 land their outputs
    # under /build/sysroot/lib/firmware/ where the make invocation below
    # picks them up.
    deps = [
        "toolchain-musl",
        "ti-linux-firmware",
        "tfa-k3",
        "optee-k3",
        "python3",
        "py3-setuptools",
        "py3-elftools",
        "swig",
        "python3-dev",
        "gnutls-dev",
        "gnutls",
        # libgnutls.so transitive deps (gnutls.pc "Requires.private")
        "nettle",
        "libtasn1",
        "libidn2",
        "p11-kit",
        "openssl",
    ],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "make ARCH=arm am62x_beagleplay_a53_defconfig",
            # BL31, TEE, TI_DM resolve through the merged sysroot — the
            # filenames and paths come straight from meta-ti's u-boot-ti.inc
            # PACKAGECONFIG (atf/optee/dm + BINMAN_INDIRS).
            #
            # SWIG_LIB: Alpine's swig binary has /usr/share/swig/<ver> baked
            # in at build time, but its data files land in the merged
            # sysroot at /build/sysroot/usr/share/swig/<ver>. Setting
            # SWIG_LIB redirects swig to the right location so pylibfdt
            # bindings generate.
            #
            # HOSTCFLAGS / HOSTLDFLAGS: U-Boot's host tools (mkeficapsule,
            # signing helpers, …) use pkg-config to find libraries from
            # unit deps like gnutls. The .pc files report prefix=/usr,
            # which pkg-config returns verbatim, missing the yoe sysroot
            # entirely. U-Boot's Makefile merges $(HOSTCFLAGS) into
            # KBUILD_HOSTCFLAGS (and same for HOSTLDFLAGS), so adding the
            # sysroot search paths here makes the host compile/link find
            # gnutls and any other dep-provided host-side headers/libs.
            """
export SWIG_LIB=$(echo /build/sysroot/usr/share/swig/*) && \\
make ARCH=arm \\
     HOSTCFLAGS=-I/build/sysroot/usr/include \\
     HOSTLDFLAGS=-L/build/sysroot/usr/lib \\
     BL31=/build/sysroot/lib/firmware/bl31.bin \\
     TEE=/build/sysroot/lib/firmware/bl32.bin \\
     TI_DM=/build/sysroot/lib/firmware/ti-dm/am62xx/ipc_echo_testb_mcu1_0_release_strip.xer5f \\
     BINMAN_INDIRS=/build/sysroot/lib/firmware \\
     -j$NPROC
""",
            # tispl.bin = A53 SPL + BL31 + BL32 + DM (FIT image, signed
            # downstream of ROM via tiboot3.bin's TIFS).
            "install -D tispl.bin   $DESTDIR/boot/tispl.bin",
            # u-boot.img = U-Boot proper, loaded by tispl.
            "install -D u-boot.img  $DESTDIR/boot/u-boot.img",
        ]),
    ],
)
