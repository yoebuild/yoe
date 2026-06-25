unit(
    name = "u-boot-beagleplay-r5",
    version = "2025.10",
    source = "https://github.com/beagleboard/u-boot.git",
    # BeagleBoard's u-boot fork, branch matches what meta-ti's
    # u-boot-bb.org_2025.10.bb pins for BeaglePlay.
    branch = "v2025.10-Beagle",
    license = "GPL-2.0-or-later",
    description = "U-Boot R5 SPL for BeaglePlay (AM625) — produces tiboot3.bin",
    # The R5 SPL targets Cortex-R5F (armv7-R), an ISA the native aarch64
    # toolchain cannot emit, so we pull a bare-metal arm-none-eabi cross
    # toolchain from the build distro's feed (gcc-arm-none-eabi +
    # binutils-arm-none-eabi, with newlib). It lands in the sysroot and on
    # PATH. ti-linux-firmware supplies the SYSFW + DM blob that binman
    # folds into tiboot3.bin.
    deps = [
        "toolchain",
        "ti-linux-firmware",
        # Common-named build tools (same package name on every backend).
        "gcc-arm-none-eabi",
        "binutils-arm-none-eabi",
        "python3",
        "python3-dev",
        "yamllint",
        "swig",
        "p11-kit",
        "libffi",
        "openssl",
    ],
    # Same build-time roles, distro-specific package names. Alpine bundles
    # headers+lib and uses py3-*; the apt distros split out -dev and use
    # python3-*. python3-dev pulls its full per-distro closure (Alpine's
    # self-contained headers, or apt's libpython3.<minor>-dev). U-Boot's
    # tools/ build (mkeficapsule + friends) wants the gnutls/openssl
    # bintools; libgnutls28-dev pulls gnutls' link closure on the apt side.
    distro_deps = {
        "alpine": [
            "newlib-arm-none-eabi",
            "py3-setuptools", "py3-elftools", "py3-yaml", "py3-jsonschema",
            "py3-attrs", "py3-referencing", "py3-rpds-py",
            "py3-jsonschema-specifications", "py3-pathspec",
            "gnutls", "gnutls-dev", "nettle", "libtasn1", "libidn2",
            "gmp", "libunistring",
        ],
        "debian": [
            "libnewlib-arm-none-eabi",
            "python3-setuptools", "python3-pyelftools", "python3-yaml",
            "python3-jsonschema", "python3-attr", "python3-referencing",
            "python3-rpds-py", "python3-jsonschema-specifications",
            "python3-pathspec",
            "libgnutls28-dev", "nettle-dev", "libtasn1-6-dev", "libidn2-dev",
            "libgmp-dev", "libunistring-dev",
        ],
        "ubuntu": [
            "libnewlib-arm-none-eabi",
            "python3-setuptools", "python3-pyelftools", "python3-yaml",
            "python3-jsonschema", "python3-attr", "python3-referencing",
            "python3-rpds-py", "python3-jsonschema-specifications",
            "python3-pathspec",
            "libgnutls28-dev", "nettle-dev", "libtasn1-6-dev", "libidn2-dev",
            "libgmp-dev", "libunistring-dev",
        ],
    },
    container = "toolchain",
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
     HOSTCFLAGS="$CPPFLAGS" \\
     HOSTLDFLAGS="$LDFLAGS" \\
     BINMAN_INDIRS=/build/sysroot/lib/firmware \\
     -j$NPROC
""",
            # tiboot3.bin is the ROM-bootable blob: R5 SPL + TIFS + DM FW.
            # binman drops it at the build root.
            "install -D tiboot3.bin $DESTDIR/boot/tiboot3.bin",
        ]),
    ],
)
