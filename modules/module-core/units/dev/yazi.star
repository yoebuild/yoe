load("//classes/binary.star", "binary")

# Yazi — terminal file manager (https://yazi-rs.github.io/). Upstream
# ships statically-linked musl builds as zip archives containing the
# `yazi` and `ya` binaries plus shell completions.
#
# Yazi's release filenames use the kernel-style arch tokens
# (x86_64 / aarch64), not Go-style amd64/arm64, so we override arch_map.
# After zip extraction (with top-level dir stripped), the binaries sit at
# $SRCDIR/yazi and $SRCDIR/ya — direct install, no install_tree needed.
binary(
    name = "yazi",
    version = "26.1.22",
    base_url = "https://github.com/sxyazi/yazi/releases/download/v{version}",
    asset = "yazi-{arch}-unknown-linux-musl.zip",
    arch_map = {
        "x86_64": "x86_64",
        "arm64":  "aarch64",
    },
    sha256 = {
        "x86_64": "b977351968206c0b78d2ef5bf21351685cc191b58a4c7e1c98c37db5d0a381f8",
        "arm64":  "91a37cdb3aa49f903aab6af57bca708935acb6def1d9f218716ab414f0a3a8b1",
    },
    binaries = ["yazi", "ya"],
    extras = [
        ("LICENSE", "$PREFIX/share/licenses/yazi/LICENSE"),
    ],
    license = "MIT",
    description = "Blazing fast terminal file manager written in Rust",
)
