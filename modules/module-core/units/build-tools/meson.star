# Alpine packages setuptools as `py3-setuptools`; Debian as
# `python3-setuptools`. Pick the right name based on the project's
# effective distro at evaluation time. Per-project rather than
# per-consumer, which is fine since meson is a build tool — a project
# building both alpine and debian images would need both feeds to
# expose one of these names.
_setuptools = "python3-setuptools" if (ctx.default_distro_override or ctx.default_distro) == "debian" else "py3-setuptools"

unit(
    name = "meson",
    version = "1.10.2",
    source = "https://github.com/mesonbuild/meson.git",
    tag = "1.10.2",
    license = "Apache-2.0",
    description = "high performance build system for C/C++ and other languages",
    deps = ["samurai", "toolchain", "python3", _setuptools],
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "python3 setup.py install --prefix=$PREFIX --root=$DESTDIR",
        ]),
    ],
)
