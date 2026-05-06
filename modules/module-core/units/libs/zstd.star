unit(
    name = "zstd",
    version = "1.5.7",
    source = "https://github.com/facebook/zstd.git",
    tag = "v1.5.7",
    license = "BSD-3-Clause",
    description = "Zstandard fast real-time compression algorithm",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "cmake -B build -S build/cmake -DCMAKE_INSTALL_PREFIX=$PREFIX -DZSTD_BUILD_PROGRAMS=OFF -DZSTD_BUILD_TESTS=OFF",
            "cmake --build build -j$NPROC",
            "DESTDIR=$DESTDIR cmake --install build",
        ]),
    ],
)
