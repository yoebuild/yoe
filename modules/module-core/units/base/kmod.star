unit(
    name = "kmod",
    version = "34.2",
    source = "https://github.com/kmod-project/kmod.git",
    tag = "v34.2",
    license = "LGPL-2.1-or-later AND GPL-2.0-or-later",
    description = "tools for managing Linux kernel modules",
    # zlib and openssl come from the source-built units on both distros,
    # matching every other consumer in the tree (curl, coreutils,
    # ca-certificates). Mixing the debian feed's libssl-dev (3.0.x)
    # with the source-built openssl (3.4.x) — which the closure pulls in
    # anyway because it provides the libssl3 virtual — lands two
    # mismatched openssl header sets in one sysroot and breaks the
    # OPENSSL_API_COMPAT check. lzma is the one library that must stay
    # per-distro: the source-built xz is the musl/alpine build, so debian
    # uses the glibc feed's liblzma instead.
    deps = ["meson", "toolchain", "zlib", "openssl"],
    runtime_deps = ["zlib", "openssl"],
    distro_deps = {
        "alpine": ["xz"],
        "debian": ["liblzma-dev"],
    },
    distro_runtime_deps = {
        "alpine": ["xz"],
        "debian": ["liblzma5"],
    },
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "meson setup build --prefix=$PREFIX -Dzstd=disabled -Dmanpages=false -Ddocs=false -Dbashcompletiondir=no -Dfishcompletiondir=no -Dzshcompletiondir=no",
            "ninja -C build -j$NPROC",
            "DESTDIR=$DESTDIR ninja -C build install",
        ]),
    ],
)
