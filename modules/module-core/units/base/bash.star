load("//classes/autotools.star", "autotools")

# bash needs --without-bash-malloc on musl; the bundled malloc assumes glibc
# internals. ncurses is required by bash's bundled readline.
# Use the GNU FTP tarball: the savannah git repo only carries `bash-5.2`
# tagged releases plus the bash-5.2-testing branch — patch-level releases
# (5.2.37) are distributed only as rolled-up tarballs.
autotools(
    name = "bash",
    version = "5.2.37",
    source = "https://ftp.gnu.org/gnu/bash/bash-5.2.37.tar.gz",
    sha256 = "9599b22ecd1d5787ad7d3b7bf0c59f312b3396d1e281175dd1f8a4014da621ff",
    license = "GPL-3.0-or-later",
    description = "GNU Bourne-Again SHell",
    deps = ["ncurses"],
    runtime_deps = ["ncurses"],
    configure_args = [
        "--without-bash-malloc",
        "--disable-nls",
        "--enable-readline",
    ],
)
