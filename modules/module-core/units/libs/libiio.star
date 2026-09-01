load("//classes/cmake.star", "cmake")

# libiio talks to the kernel's industrial I/O subsystem: ADCs, DACs,
# accelerometers, gyros, magnetometers, light and pressure sensors — the usual
# sensor path on the boards yoe targets. The command-line tools are the
# immediate value: iio_info enumerates devices and channels, iio_attr reads and
# writes attributes, iio_readdev/iio_writedev stream buffers, and iiod serves a
# board's sensors to a host over the network.
#
# The 0.x line is pinned deliberately rather than tracking the default branch:
# libiio 1.x reworks the API and is not source compatible. v0.26 is the newest
# 0.x release.
#
# Backends are configure-time switches, and only the ones with a dependency
# already in reach are on. Local (sysfs) and network cover the common cases;
# USB and serial would pull in libusb and libserialport, and neither has a unit.
# The XML backend is not optional here despite the name — libiio ships a
# context as XML over the wire, so the network backend refuses to configure
# without it, which is why libxml2 is a hard dependency rather than a choice.
#
# This unit ships the library, the command-line tools and the iiod binary, but
# does not start anything: the daemon's init script and its `services = [...]`
# declaration live in the `iiod-init` companion. That split is deliberate.
# iiod has no authentication and grants any client on the network read *and
# write* access to the board's IIO devices, including driving DAC outputs, so
# turning it on is a decision an image should make on purpose. Installing
# libiio for iio_info should not make that decision by accident.
#
# DNS-SD is off. Upstream's zeroconf support is written against Avahi, and yoe
# uses mdnsd for mDNS; leaving it on would add an Avahi dependency to get a
# service-discovery path yoe would not use anyway. Clients reach iiod by
# address or by the hostname mdnsd already advertises.
cmake(
    name = "libiio",
    version = "0.26",
    source = "https://github.com/analogdevicesinc/libiio.git",
    tag = "v0.26",
    license = "LGPL-2.1-or-later",
    description = "Library and command-line tools for the Linux industrial I/O (IIO) subsystem",
    deps = ["toolchain"],
    # libxml2 supplies the XML backend, and iiod's command parser is generated
    # from a lex/yacc grammar, so flex and bison have to be present at build
    # time. All three are packaged by every backend distro under the same
    # names, so take them from the distro feed rather than building them.
    distro_deps = {
        "alpine": ["libxml2-dev", "flex", "bison"],
        "debian": ["libxml2-dev", "flex", "bison"],
        "ubuntu": ["libxml2-dev", "flex", "bison"],
    },
    distro_runtime_deps = {
        "alpine": ["libxml2"],
        "debian": ["libxml2"],
        "ubuntu": ["libxml2"],
    },
    cmake_args = [
        # CMake does not read $CPPFLAGS/$LDFLAGS, and pkg-config alone is not
        # enough to redirect it: the apt libxml-2.0.pc reports prefix=/usr, so
        # the HINTS that CMake's FindLibXml2 derives from it point at the
        # container's own /usr rather than the yoe sysroot, and the subsequent
        # find_path/find_library come up empty. Naming the sysroot as a prefix
        # is what makes find_* search it — CMake appends the multiarch
        # subdirectory itself on the apt distros, so one entry covers both
        # libc bases. Same class of problem as the HOSTCFLAGS workaround the
        # kernel units carry.
        "CMAKE_PREFIX_PATH=/build/sysroot/usr",
        # Alpine's libxml2 ships a CMake config package that hardcodes
        # /usr/include/libxml2 and /usr/lib and then searches with
        # NO_DEFAULT_PATH, so it ignores the prefix above and reports the
        # library as NOTFOUND from inside the sysroot. It also sets
        # LIBXML2_VERSION_STRING, which is the variable libiio tests to decide
        # between config mode and CMake's own FindLibXml2 module — so config
        # mode wins and the module never runs. Refusing the config package
        # sends libiio down the module path on both bases, and that one does
        # honour CMAKE_PREFIX_PATH. Debian ships no config package, so this is
        # a no-op there.
        "CMAKE_DISABLE_FIND_PACKAGE_LibXml2=ON",
        "WITH_LOCAL_BACKEND=ON",
        "WITH_NETWORK_BACKEND=ON",
        "WITH_XML_BACKEND=ON",
        "WITH_USB_BACKEND=OFF",
        "WITH_SERIAL_BACKEND=OFF",
        "HAVE_DNS_SD=OFF",
        # The command-line tools live in tests/ upstream, so this is what
        # ships iio_info and friends — not a test-suite switch.
        "WITH_TESTS=ON",
        "WITH_IIOD=ON",
        # iiod's own sub-features, all default-on upstream. Async I/O wants
        # libaio, which nothing else here needs; the gain is throughput on
        # large streaming buffers, which is not what a board's handful of
        # sensor channels asks for. USB gadget and UART serving are separate
        # from the client-side backends switched off above — they would have
        # iiod listen on a USB FunctionFS gadget and on a UART, neither of
        # which is how a yoe board is reached.
        "WITH_AIO=OFF",
        "WITH_IIOD_USBD=OFF",
        "WITH_IIOD_SERIAL=OFF",
        "WITH_HWMON=ON",
        "WITH_ZSTD=OFF",
        "WITH_EXAMPLES=OFF",
        # Upstream's own service files are declined in favour of the ones this
        # unit ships; see the install-service task below.
        "WITH_SYSTEMD=OFF",
        "WITH_SYSVINIT=OFF",
        "WITH_UPSTART=OFF",
        "WITH_DOC=OFF",
        "WITH_MAN=OFF",
        "CPP_BINDINGS=OFF",
        "CSHARP_BINDINGS=OFF",
        "PYTHON_BINDINGS=OFF",
    ],
)
