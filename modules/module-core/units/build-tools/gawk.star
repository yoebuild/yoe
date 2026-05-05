load("//classes/autotools.star", "autotools")

autotools(
    name = "gawk",
    version = "5.4.0",
    source = "https://git.savannah.gnu.org/git/gawk.git",
    tag = "gawk-5.4.0",
    license = "GPL-3.0-or-later",
    description = "GNU awk text processing language",
    configure_args = [
        "--disable-nls",
        "--disable-pma",
        "--without-mpfr",
        "--without-readline",
    ],
)
