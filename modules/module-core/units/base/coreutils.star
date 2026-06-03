load("//classes/autotools.star", "autotools")

autotools(
    name = "coreutils",
    version = "9.6",
    source = "https://ftp.gnu.org/gnu/coreutils/coreutils-9.6.tar.xz",
    license = "GPL-3.0-or-later",
    description = "GNU core utilities (ls, cp, mv, cat, etc.)",
    # attr (libattr) is required for xattr support: coreutils' cp uses
    # libattr's attr_copy_file() for `--preserve=xattr`. Without it,
    # configure prints "GNU coreutils will be built without xattr support"
    # and cp fails any copy that requests xattrs — which breaks Debian's
    # update-initramfs (dracut-install copies kernel modules with
    # `cp --preserve=...,xattr`), producing an initramfs with no modules
    # and an unbootable image. Runtime dep too: cp links libattr.so.
    deps = ["openssl", "attr"],
    runtime_deps = ["openssl", "attr"],
    configure_args = [
        "--disable-nls",
        "--without-selinux",
        "--with-openssl",
        "--enable-single-binary=symlinks",
        "--enable-no-install-program=hostname,su,kill,uptime",
    ],
)
