unit(
    name = "openssl",
    # Latest release on the libssl.so.3 ABI. Staying on the 3.x series
    # is deliberate: Debian trixie's libssl3t64 provides libssl3 and
    # declares `Breaks: libssl3 (< 3.5.6-1~deb13u1)`, so the version
    # this unit provides must be >= 3.5.6 or the Debian rootfs solve
    # rejects it. 4.0 would bump the SONAME to libssl.so.4, breaking the
    # provides = ["libssl3"] mapping below and diverging from every
    # Debian package that links libssl.so.3 — not an option here.
    version = "3.6.2",
    release = 1,
    source = "https://github.com/openssl/openssl.git",
    tag = "openssl-3.6.2",
    license = "Apache-2.0",
    description = "TLS/SSL and crypto library",
    deps = ["zlib", "toolchain"],
    runtime_deps = ["zlib"],
    # Source-built openssl owns the same /usr/lib/libcrypto.so.3,
    # /usr/lib/libssl.so.3, /etc/ssl/* paths and the same SONAMEs as
    # Alpine's prebuilt libcrypto3 / libssl3. Declaring the virtuals
    # routes consumers' runtime_deps to this unit so the Alpine packages
    # aren't pulled in alongside, which would cause `apk add` to abort
    # with "trying to overwrite ... owned by openssl".
    provides = ["libcrypto3", "libssl3"],
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            "./Configure --prefix=$PREFIX --libdir=lib --openssldir=/etc/ssl shared zlib",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install_sw install_ssldirs",
        ]),
    ],
)
