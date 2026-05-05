load("//classes/autotools.star", "autotools")

autotools(
    name = "libffi",
    version = "3.4.6",
    source = "https://github.com/libffi/libffi.git",
    tag = "v3.4.6",
    license = "MIT",
    description = "Foreign function interface library",
    configure_args = ["--disable-docs"],
    tasks = [
        task("build", steps=[
            "test -f configure || autoreconf -fi",
            "./configure --prefix=$PREFIX --disable-docs",
            "make -j$NPROC all",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
