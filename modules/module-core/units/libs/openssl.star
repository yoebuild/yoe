unit(
    name = "openssl",
    version = "3.4.1",
    release = 1,
    source = "https://github.com/openssl/openssl.git",
    tag = "openssl-3.4.1",
    license = "Apache-2.0",
    description = "TLS/SSL and crypto library",
    deps = ["zlib", "toolchain-musl"],
    runtime_deps = ["zlib"],
    # Source-built openssl owns the same /usr/lib/libcrypto.so.3,
    # /usr/lib/libssl.so.3, /etc/ssl/* paths and the same SONAMEs as
    # Alpine's prebuilt libcrypto3 / libssl3. Declaring the virtuals
    # routes consumers' runtime_deps to this unit so the Alpine packages
    # aren't pulled in alongside, which would cause `apk add` to abort
    # with "trying to overwrite ... owned by openssl".
    provides = ["libcrypto3", "libssl3"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "./Configure --prefix=$PREFIX --libdir=lib --openssldir=/etc/ssl shared zlib",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install_sw install_ssldirs",
        ]),
    ],
)
