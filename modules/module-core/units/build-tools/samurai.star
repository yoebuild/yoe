unit(
    name = "samurai",
    version = "1.2",
    source = "https://github.com/michaelforney/samurai.git",
    tag = "1.2",
    license = "ISC AND Apache-2.0 AND MIT",
    description = "ninja-compatible build tool written in C",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "make -j$NPROC PREFIX=$PREFIX",
            "make PREFIX=$PREFIX DESTDIR=$DESTDIR install",
            "ln -s samu $DESTDIR$PREFIX/bin/ninja",
        ]),
    ],
)
