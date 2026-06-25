load("//classes/autotools.star", "autotools")

autotools(
    name = "libffi",
    version = "3.4.6",
    source = "https://github.com/libffi/libffi.git",
    tag = "v3.4.6",
    license = "MIT",
    description = "Foreign function interface library",
    configure_args = ["--disable-docs"],
    # libffi 3.4.6's configure.ac calls LT_SYS_SYMBOL_USCORE, removed in
    # libtool 2.5.x, so autoreconf fails on newer host toolchains (e.g.
    # Ubuntu 26.04). The probe only defines the unused SYMBOL_UNDERSCORE,
    # so the patch drops it — no built output changes.
    patches = ["libffi/0001-libtool-2.5-drop-uscore-probe.patch"],
    tasks = [
        task("build", steps=[
            "test -f configure || autoreconf -fi",
            "./configure --prefix=$PREFIX --disable-docs",
            "make -j$NPROC all",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
