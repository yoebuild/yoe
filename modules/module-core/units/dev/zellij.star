load("//classes/binary.star", "binary")

# Zellij — terminal workspace / multiplexer (https://zellij.dev/). Upstream
# ships statically-linked musl builds as tar.gz archives containing just the
# `zellij` binary at the top level — direct install, no install_tree needed.
#
# Zellij's release filenames use kernel-style arch tokens (x86_64 / aarch64),
# not Go-style amd64/arm64, so we override arch_map.
binary(
    name = "zellij",
    version = "0.44.1",
    base_url = "https://github.com/zellij-org/zellij/releases/download/v{version}",
    asset = "zellij-{arch}-unknown-linux-musl.tar.gz",
    arch_map = {
        "x86_64": "x86_64",
        "arm64":  "aarch64",
    },
    sha256 = {
        "x86_64": "669825021d529fca5d939888263c9d2a90762145191fa07581a15250e8af2b49",
        "arm64":  "6f028bb569d29be968c961249c5f80d5336ad4ad4b3cd79af8e32afab57b0948",
    },
    license = "MIT",
    description = "Terminal workspace and multiplexer written in Rust",
)
