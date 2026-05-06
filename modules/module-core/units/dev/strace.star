unit(
    name = "strace",
    version = "6.9",
    source = "https://github.com/strace/strace.git",
    tag = "v6.9",
    license = "LGPL-2.1-or-later",
    description = "System call tracer for Linux",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "./bootstrap",
            "./configure --prefix=$PREFIX --enable-mpers=no --without-libdw",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
