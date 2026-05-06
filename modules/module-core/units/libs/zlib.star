unit(
    name = "zlib",
    version = "1.3.1",
    source = "https://github.com/madler/zlib.git",
    tag = "v1.3.1",
    license = "Zlib",
    description = "Compression library",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # zlib has its own configure (not autoconf-based)
            "./configure --prefix=$PREFIX",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
