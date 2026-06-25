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
        "toolchain",
        "ti-linux-firmware",
        "tfa-k3",
        "optee-k3",
        # Common-named build tools (same package name on every backend).
        # python3-dev pulls its full closure per distro — Alpine's
        # self-contained headers, or the apt python3.<minor>-dev /
        # libpython3.<minor>-dev that the metapackage depends on (the
        # build resolver materializes the whole dependency closure).
        "python3",
        "python3-dev",
        "swig",
        "openssl",
        "p11-kit",
    ],
    # Same build-time roles, distro-specific package names. Alpine bundles
    # headers+lib and uses py3-*; the apt distros split out -dev and use
    # python3-*. libgnutls28-dev pulls gnutls' runtime closure (nettle,
    # libtasn1, libidn2, …) automatically.
    distro_deps = {
        "alpine": [
            "py3-setuptools", "py3-elftools",
            "gnutls", "gnutls-dev",
            # libgnutls.so transitive deps (gnutls.pc "Requires.private")
            "nettle", "libtasn1", "libidn2",
        ],
        "debian": [
            "python3-setuptools", "python3-pyelftools",
            "libgnutls28-dev", "nettle-dev", "libtasn1-6-dev", "libidn2-dev",
        ],
        "ubuntu": [
            "python3-setuptools", "python3-pyelftools",
            "libgnutls28-dev", "nettle-dev", "libtasn1-6-dev", "libidn2-dev",
        ],
    },
    container = "toolchain",
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
            # KBUILD_HOSTCFLAGS (and same for HOSTLDFLAGS), so passing yoe's
            # own $CPPFLAGS/$LDFLAGS makes the host compile/link find gnutls
            # and other dep-provided host-side headers/libs. We use the env
            # vars (not a hardcoded -L/build/sysroot/usr/lib) because the apt
            # distros install libs under the multiarch dir
            # /usr/lib/<triplet>/, which $LDFLAGS already covers.
            """
export SWIG_LIB=$(echo /build/sysroot/usr/share/swig/*) && \\
make ARCH=arm \\
     HOSTCFLAGS="$CPPFLAGS" \\
     HOSTLDFLAGS="$LDFLAGS" \\
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
