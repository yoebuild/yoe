unit(
    name = "kmod",
    version = "34.2",
    source = "https://github.com/kmod-project/kmod.git",
    tag = "v34.2",
    license = "LGPL-2.1-or-later AND GPL-2.0-or-later",
    description = "tools for managing Linux kernel modules",
    deps = ["meson", "zlib", "openssl", "xz", "toolchain-musl"],
    runtime_deps = ["zlib", "openssl", "xz"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "meson setup build --prefix=$PREFIX -Dzstd=disabled -Dmanpages=false -Ddocs=false -Dbashcompletiondir=no -Dfishcompletiondir=no -Dzshcompletiondir=no",
            "ninja -C build -j$NPROC",
            "DESTDIR=$DESTDIR ninja -C build install",
        ]),
    ],
)
