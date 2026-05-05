load("//classes/autotools.star", "autotools")

autotools(
    name = "ncurses",
    version = "6.4",
    source = "https://github.com/mirror/ncurses.git",
    tag = "v6.4",
    license = "MIT",
    description = "Terminal handling library",
    configure_args = ["--with-shared", "--without-debug"],
)
