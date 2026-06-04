unit(
    name = "strace",
    version = "6.19",
    source = "https://github.com/strace/strace.git",
    tag = "v6.19",
    license = "LGPL-2.1-or-later",
    description = "System call tracer for Linux",
    deps = ["toolchain"],
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "./bootstrap",
            "./configure --prefix=$PREFIX --enable-mpers=no --without-libdw --enable-bundled=yes",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
