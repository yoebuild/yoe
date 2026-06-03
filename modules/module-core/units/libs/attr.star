unit(
    name = "attr",
    version = "2.5.2",
    # Release tarball rather than git: attr's git tree ships no generated
    # configure, so a git build would have to autoreconf, which pulls in
    # autopoint/gettext just to bootstrap. The tarball carries a working
    # configure, so we build it directly — the same choice coreutils makes.
    source = "https://download.savannah.nongnu.org/releases/attr/attr-2.5.2.tar.gz",
    license = "LGPL-2.1-or-later",
    description = "Extended attribute (xattr) library — libattr + getfattr/setfattr",
    deps = ["toolchain"],
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # Extraction leaves the tarball's generated build system (configure,
            # *.in, aclocal.m4) older than their sources (configure.ac,
            # Makefile.am), so make's automake/autoconf rules would try to
            # regenerate them with automake-1.16 — absent in the container.
            # Touch the generated files newer than their sources so make
            # treats the shipped build system as up to date.
            "find . \\( -name configure -o -name aclocal.m4 -o -name '*.in' \\) -exec touch {} +",
            # --disable-nls keeps the build off gettext; libattr's headers
            # (<attr/libattr.h>) and libattr.so are what coreutils links
            # against to enable `cp --preserve=xattr`.
            "./configure --prefix=$PREFIX --disable-nls",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
