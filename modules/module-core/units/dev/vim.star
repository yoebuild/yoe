unit(
    name = "vim",
    version = "9.1.0",
    source = "https://github.com/vim/vim.git",
    tag = "v9.1.0",
    license = "Vim",
    description = "Vi IMproved text editor",
    deps = ["ncurses", "toolchain-musl"],
    runtime_deps = ["ncurses"],
    replaces = ["busybox"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # Point directly at the sysroot ncurses and use static linking
            "vim_cv_tgetent=zero ./configure --prefix=$PREFIX --with-features=normal --disable-gui --without-x --with-tlib=ncurses LDFLAGS=\"$LDFLAGS -static\"",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
