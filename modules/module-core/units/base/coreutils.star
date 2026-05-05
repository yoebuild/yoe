load("//classes/autotools.star", "autotools")

autotools(
    name = "coreutils",
    version = "9.6",
    source = "https://ftp.gnu.org/gnu/coreutils/coreutils-9.6.tar.xz",
    license = "GPL-3.0-or-later",
    description = "GNU core utilities (ls, cp, mv, cat, etc.)",
    deps = ["openssl"],
    runtime_deps = ["openssl"],
    configure_args = [
        "--disable-nls",
        "--without-selinux",
        "--with-openssl",
        "--enable-single-binary=symlinks",
        "--enable-no-install-program=hostname,su,kill,uptime",
    ],
)
