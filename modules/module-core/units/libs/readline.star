load("//classes/autotools.star", "autotools")

autotools(
    name = "readline",
    version = "8.2",
    # Use the GNU FTP tarball rather than the savannah git repo: savannah's
    # git server regularly stalls mid-clone and fails the build. The tarball
    # carries the same 8.2 release, and a project `source_mirrors` table can
    # redirect ftp.gnu.org to a faster mirror, which the git path cannot do.
    source = "https://ftp.gnu.org/gnu/readline/readline-8.2.tar.gz",
    sha256 = "3feb7171f16a84ee82ca18a36d7b9be109a52c04f492a053331d7d1095007c35",
    license = "GPL-3.0-or-later",
    description = "GNU readline command-line editing library",
    deps = ["ncurses"],
    runtime_deps = ["ncurses"],
    configure_args = ["--with-curses"],
    tasks = [
        task("build", steps=[
            # The tarball ships a generated configure with its regeneration
            # rules commented out, so no autoreconf bootstrap is needed.
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
