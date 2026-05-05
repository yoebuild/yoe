load("//classes/autotools.star", "autotools")

# xz is currently built static-only (no -fPIC), so libmagic.so cannot link
# against liblzma — leave xz support disabled until xz ships shared libs.
autotools(
    name = "file",
    version = "5.46",
    source = "https://github.com/file/file.git",
    tag = "FILE5_46",
    license = "BSD-2-Clause",
    description = "File type identification utility",
    deps = ["zlib", "zstd"],
    runtime_deps = ["zlib", "zstd"],
    configure_args = [
        "--enable-zlib",
        "--enable-zstdlib",
        "--disable-xzlib",
        "--disable-bzlib",
        "--disable-libseccomp",
    ],
)
