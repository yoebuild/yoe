load("//classes/binary.star", "binary")

# Bun — fast all-in-one JavaScript runtime, package manager, and bundler
# (https://bun.sh/). Upstream ships static-ish musl builds as zip archives
# with a single `bun` binary at `bun-linux-<arch>-musl/bun`; the source
# workspace strips the leading directory automatically, so the install
# just copies that one binary into $PREFIX/bin and symlinks `bunx` to it
# (bunx is bun's `npx`-equivalent runner, an alias the bun CLI dispatches
# on argv[0]).
#
# Bun's release filenames use kernel-style arch tokens — x64 / aarch64 —
# not Go-style amd64/arm64, so we override arch_map.
binary(
    name = "bun",
    version = "1.1.43",
    base_url = "https://github.com/oven-sh/bun/releases/download/bun-v{version}",
    asset = "bun-linux-{arch}-musl.zip",
    arch_map = {
        "x86_64": "x64",
        "arm64":  "aarch64",
    },
    sha256 = {
        "x86_64": "b6333cd8665d3099c5f57663774c264a79eaf78127733689113bd39dc7a522c8",
        "arm64":  "3f7a1f058f759bfce2a773a235e5f2b02af47169c2e53219c7bbd0cec5b43a5d",
    },
    binaries = {"bun": "bun"},
    symlinks = {
        "$PREFIX/bin/bunx": "bun",
    },
    license = "MIT",
    description = "Fast all-in-one JavaScript runtime, package manager, and bundler",
)
