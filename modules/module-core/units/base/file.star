load("//classes/autotools.star", "autotools")

# Debian splits libraries across runtime (libfoo1) and dev
# (libfoo-dev) packages: configure needs the headers + unversioned .so
# symlink that live in -dev, while runtime needs only the versioned
# .so in the lib package. Alpine bundles both shapes into one apk by
# convention, so the same name works for build and runtime. The split
# is per-consumer-distro, expressed via distro_deps / distro_runtime_deps.
#
# xz is currently built static-only (no -fPIC), so libmagic.so cannot link
# against liblzma — leave xz support disabled until xz ships shared libs.
autotools(
    name = "file",
    version = "5.46",
    source = "https://github.com/file/file.git",
    tag = "FILE5_46",
    license = "BSD-2-Clause",
    description = "File type identification utility",
    distro_deps = {
        "alpine": ["zlib", "zstd"],
        "debian": ["zlib1g-dev", "libzstd-dev"],
    },
    distro_runtime_deps = {
        "alpine": ["zlib", "zstd"],
        "debian": ["zlib1g", "libzstd1"],
    },
    configure_args = [
        "--enable-zlib",
        "--enable-zstdlib",
        "--disable-xzlib",
        "--disable-bzlib",
        "--disable-libseccomp",
    ],
)
