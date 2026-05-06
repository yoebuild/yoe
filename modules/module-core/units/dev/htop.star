load("//classes/autotools.star", "autotools")

autotools(
    name = "htop",
    version = "3.4.0",
    source = "https://github.com/htop-dev/htop.git",
    tag = "3.4.0",
    license = "GPL-2.0-or-later",
    description = "Interactive process viewer",
    deps = ["ncurses"],
    runtime_deps = ["ncurses"],
    configure_args = [
        # ncurses unit currently builds without --enable-widec, so disable
        # unicode support in htop. Re-enable here once ncurses ships ncursesw.
        "--disable-unicode",
        "--disable-hwloc",
        "--disable-static",
        "--disable-affinity",
        "--disable-capabilities",
        "--disable-sensors",
    ],
)
