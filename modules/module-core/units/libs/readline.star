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
)
