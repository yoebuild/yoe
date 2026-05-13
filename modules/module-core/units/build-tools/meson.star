unit(
    name = "meson",
    version = "1.10.2",
    source = "https://github.com/mesonbuild/meson.git",
    tag = "1.10.2",
    license = "Apache-2.0",
    description = "high performance build system for C/C++ and other languages",
    deps = ["samurai", "toolchain-musl", "python3", "py3-setuptools"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "python3 setup.py install --prefix=$PREFIX --root=$DESTDIR",
        ]),
    ],
)
