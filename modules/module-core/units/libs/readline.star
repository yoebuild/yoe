load("//classes/autotools.star", "autotools")

autotools(
    name = "readline",
    version = "8.2",
    source = "https://git.savannah.gnu.org/git/readline.git",
    tag = "readline-8.2",
    license = "GPL-3.0-or-later",
    description = "GNU readline command-line editing library",
    deps = ["ncurses"],
    runtime_deps = ["ncurses"],
    configure_args = ["--with-curses"],
    tasks = [
        task("build", steps=[
            "test -f configure.ac && autoreconf -fi || true",
            "./configure --prefix=$PREFIX --with-curses",
            # readline's own build links libreadline.so against nothing but
            # libc: --with-curses only sets TERMCAP_LIB for static consumers,
            # while the shared-library link uses SHLIB_LIBS, which upstream
            # leaves empty on Linux. The result is a libreadline.so.8 with
            # undefined tgetent/tputs/tgoto/BC/PC/UP. glibc hides this via
            # lazy binding, but musl resolves every relocation at load time
            # and aborts with exit 127, so any process that pulls in
            # libreadline (Alpine's python3, for one) dies before main().
            # Link the shared library against ncurses explicitly.
            "make -j$NPROC SHLIB_LIBS=-lncurses",
            "make DESTDIR=$DESTDIR install SHLIB_LIBS=-lncurses",
        ]),
    ],
)
