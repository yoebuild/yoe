load("//classes/autotools.star", "autotools")

autotools(
    name = "expat",
    version = "2.6.4",
    source = "https://github.com/libexpat/libexpat.git",
    tag = "R_2_6_4",
    license = "MIT",
    description = "XML parsing C library",
    tasks = [
        task("build", steps=[
            "cd expat && autoreconf -fi",
            "cd expat && ./configure --prefix=$PREFIX",
            "cd expat && make -j$NPROC",
            "cd expat && make DESTDIR=$DESTDIR install",
        ]),
    ],
)
