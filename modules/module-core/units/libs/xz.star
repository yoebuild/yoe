load("//classes/cmake.star", "cmake")

cmake(
    name = "xz",
    version = "5.6.3",
    source = "https://github.com/tukaani-project/xz.git",
    tag = "v5.6.3",
    license = "LGPL-2.1-or-later",
    description = "XZ Utils compression library and tools",
    replaces = ["busybox"],
)
