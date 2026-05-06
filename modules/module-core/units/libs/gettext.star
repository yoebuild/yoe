load("//classes/autotools.star", "autotools")

# Tarball source (not git) because gettext needs autopoint to run autoreconf,
# but autopoint comes from gettext — circular dependency. The release tarball
# ships a pre-generated configure script.
autotools(
    name = "gettext",
    version = "0.26",
    source = "https://ftp.gnu.org/pub/gnu/gettext/gettext-0.26.tar.xz",
    license = "GPL-3.0-or-later",
    description = "GNU internationalization utilities and library",
    deps = ["ncurses"],
    configure_args = [
        "--enable-relocatable",
        "--disable-java",
        "--disable-native-java",
        "--disable-openmp",
        "--without-emacs",
        "--without-cvs",
        "--without-git",
        "--without-bzip2",
    ],
)
