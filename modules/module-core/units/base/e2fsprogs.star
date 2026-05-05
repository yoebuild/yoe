# util-linux already provides libblkid, libuuid, libmount, fsck — disable
# the duplicate copies in e2fsprogs to avoid conflicting headers/libs in the
# sysroot.
#
# Use the kernel.org tarball: it ships a pre-baked configure with AX_PTHREAD
# already expanded. configure.ac references AX_PTHREAD from autoconf-archive
# which isn't in the container, so we must NOT run autoreconf — the autotools
# class would do that, so we define our own build task instead.
unit(
    name = "e2fsprogs",
    version = "1.47.2",
    source = "https://www.kernel.org/pub/linux/kernel/people/tytso/e2fsprogs/v1.47.2/e2fsprogs-1.47.2.tar.xz",
    sha256 = "08242e64ca0e8194d9c1caad49762b19209a06318199b63ce74ae4ef2d74e63c",
    license = "GPL-2.0-only AND LGPL-2.0-only AND BSD-3-Clause AND MIT",
    description = "ext2/ext3/ext4 filesystem utilities (mkfs, fsck, tune2fs)",
    deps = ["util-linux", "toolchain-musl"],
    runtime_deps = ["util-linux"],
    replaces = ["busybox"],
    container = "toolchain-musl",
    container_arch = "target",
    sandbox = True,
    shell = "bash",
    tasks = [
        task("build", steps = [
            "./configure --prefix=$PREFIX " +
                "--enable-elf-shlibs " +
                "--disable-libblkid " +
                "--disable-libuuid " +
                "--disable-uuidd " +
                "--disable-fsck " +
                "--disable-nls",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
