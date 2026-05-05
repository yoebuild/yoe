load("//classes/autotools.star", "autotools")

autotools(
    name = "curl",
    version = "8.11.1",
    source = "https://github.com/curl/curl.git",
    tag = "curl-8_11_1",
    license = "MIT",
    description = "Command-line tool and library for transferring data with URLs",
    deps = ["openssl", "zlib", "zstd"],
    runtime_deps = ["openssl", "zlib", "zstd"],
    configure_args = ["--with-openssl", "--without-libpsl"],
)
