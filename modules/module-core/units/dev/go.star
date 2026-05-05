load("//classes/binary.star", "binary")

# Go toolchain — installed as a prebuilt bundle from go.dev. The default
# arch_map (x86_64→amd64, arm64→arm64) matches Go's own asset naming.
#
# install_tree puts the entire toolchain (bin/, pkg/, src/, lib/, etc.)
# under /usr/lib/go and creates /usr/bin/{go,gofmt} symlinks pointing
# back into it. This mirrors how Go expects to be deployed: the binary
# resolves $GOROOT relative to its own location, so the symlink target
# being one of the toolchain's own bin/<tool> files keeps that working.
binary(
    name = "go",
    version = "1.26.2",
    base_url = "https://go.dev/dl",
    asset = "go{version}.linux-{arch}.tar.gz",
    sha256 = {
        "x86_64": "990e6b4bbba816dc3ee129eaeaf4b42f17c2800b88a2166c265ac1a200262282",
        "arm64":  "c958a1fe1b361391db163a485e21f5f228142d6f8b584f6bef89b26f66dc5b23",
    },
    install_tree = "$PREFIX/lib/go",
    binaries = ["bin/go", "bin/gofmt"],
    license = "BSD-3-Clause",
    description = "The Go programming language toolchain",
)
