load("//classes/autotools.star", "autotools")

autotools(
    name = "ncurses",
    version = "6.4",
    source = "https://github.com/mirror/ncurses.git",
    tag = "v6.4",
    license = "MIT",
    description = "Terminal handling library",
    # The C++ binding (libncurses++.a) leaks its NCURSES_BOOL macro into the
    # libstdc++ headers and fails to compile under GCC 15 (Ubuntu). Nothing in
    # the closure consumes it, so disable it for a single consistent artifact
    # across all distros.
    configure_args = ["--with-shared", "--without-debug", "--without-cxx-binding"],
)
