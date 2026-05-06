# Perfect hash function generator. Used by eudev (and others) at build time
# to generate static lookup tables; runs on the build host.
#
# gperf 3.1 ships pre-generated autotools files but the macros are old enough
# that re-running autoreconf produces a broken Makefile (unsubstituted
# @INSTALL_PROGRAM@). Skip autoreconf and use the shipped configure.
unit(
    name = "gperf",
    version = "3.1",
    source = "https://ftp.gnu.org/gnu/gperf/gperf-3.1.tar.gz",
    sha256 = "588546b945bba4b70b6a3a616e80b4ab466e3f33024a352fc2198112cdbb3ae2",
    license = "GPL-3.0-or-later",
    description = "Perfect hash function generator (build tool)",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    sandbox = True,
    shell = "bash",
    tasks = [
        task("build", steps = [
            "./configure --prefix=$PREFIX",
            "make -j$NPROC MAKEINFO=true",
            "make DESTDIR=$DESTDIR install MAKEINFO=true",
        ]),
    ],
)
