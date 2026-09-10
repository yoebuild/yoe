load("//classes/autotools.star", "autotools")

autotools(
    name = "gawk",
    version = "5.4.0",
    # Use the GNU FTP tarball rather than the savannah git repo: savannah's
    # git server regularly stalls mid-clone and fails the build. The tarball
    # carries the same 5.4.0 release, and a project `source_mirrors` table can
    # redirect ftp.gnu.org to a faster mirror, which the git path cannot do.
    source = "https://ftp.gnu.org/gnu/gawk/gawk-5.4.0.tar.gz",
    sha256 = "df5756d50772212a8e3f26d903107ece3773c4037c6a9e0a59c2a0a8d7329f0d",
    license = "GPL-3.0-or-later",
    description = "GNU awk text processing language",
    configure_args = [
        "--disable-nls",
        "--disable-pma",
        "--without-mpfr",
        "--without-readline",
    ],
)
