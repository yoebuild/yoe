unit(
    name = "u-boot-beagleplay-r5",
    version = "2025.10",
    source = "https://github.com/beagleboard/u-boot.git",
    # BeagleBoard's u-boot fork, branch matches what meta-ti's
    # u-boot-bb.org_2025.10.bb pins for BeaglePlay.
    branch = "v2025.10-Beagle",
    license = "GPL-2.0-or-later",
    description = "U-Boot R5 SPL for BeaglePlay (AM625) — produces tiboot3.bin",
    # The R5 SPL targets Cortex-R5F (armv7-R), an ISA the aarch64 toolchain
    # in toolchain-musl cannot emit. The Alpine community repo ships
    # gcc-arm-none-eabi + binutils-arm-none-eabi for aarch64 — we pull them
    # in as apk-passthrough units and they land in /build/sysroot/usr/bin/
    # (already on PATH). ti-linux-firmware supplies the SYSFW + DM blob that
    # binman folds into tiboot3.bin.
    deps = [
        "toolchain-musl",
        "ti-linux-firmware",
        "gcc-arm-none-eabi",
        "binutils-arm-none-eabi",
        "newlib-arm-none-eabi",
        "python3",
        "python3-dev",
        "py3-setuptools",
        "py3-elftools",
        "py3-yaml",
        "py3-jsonschema",
        "py3-attrs",
        "py3-referencing",
        "py3-rpds-py",
        "py3-jsonschema-specifications",
        "py3-pathspec",
        "yamllint",
        "swig",
        # U-Boot's tools/ build (mkeficapsule + friends) wants gnutls and
        # the openssl bintool regardless of which SPL target is selected.
        "gnutls",
        "gnutls-dev",
        "nettle",
        "libtasn1",
        "libidn2",
        "p11-kit",
        "libffi",
        "gmp",
        "libunistring",
        "openssl",
    ],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            """
make CROSS_COMPILE=arm-none-eabi- \\
     ARCH=arm \\
     am62x_beagleplay_r5_defconfig
""",
            # SWIG_LIB / HOSTCFLAGS / HOSTLDFLAGS — see the
            # u-boot-beagleplay (A53) unit for the full rationale; they
            # route swig's runtime data and U-Boot's host-tool builds
            # (mkeficapsule and friends) at the merged yoe sysroot.
            """
export SWIG_LIB=$(echo /build/sysroot/usr/share/swig/*) && \\
make CROSS_COMPILE=arm-none-eabi- \\
     ARCH=arm \\
     HOSTCFLAGS=-I/build/sysroot/usr/include \\
     HOSTLDFLAGS=-L/build/sysroot/usr/lib \\
     BINMAN_INDIRS=/build/sysroot/lib/firmware \\
     -j$NPROC
""",
            # tiboot3.bin is the ROM-bootable blob: R5 SPL + TIFS + DM FW.
            # binman drops it at the build root.
            "install -D tiboot3.bin $DESTDIR/boot/tiboot3.bin",
        ]),
    ],
)
