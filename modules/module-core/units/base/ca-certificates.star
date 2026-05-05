unit(
    name = "ca-certificates",
    version = "20250419",
    source = "https://deb.debian.org/debian/pool/main/c/ca-certificates/ca-certificates_20250419.tar.xz",
    sha256 = "33b44ef78653ecd3f0f2f13e5bba6be466be2e7da72182f737912b81798ba5d2",
    license = "MPL-2.0",
    description = "Mozilla CA certificates bundle for TLS verification",
    deps = ["openssl", "toolchain-musl"],
    runtime_deps = ["openssl"],
    # Source-built ca-certificates ships both the cert bundle (cert.pem,
    # certs/ca-certificates.crt) and the individual certs that Alpine
    # splits into a separate `ca-certificates-bundle` package. Declaring
    # the bundle here routes any package whose runtime_deps reach
    # `ca-certificates-bundle` (apk-tools, libcurl, libretls, …) back to
    # this unit, instead of pulling Alpine's bundle alongside and tripping
    # `apk add` on `trying to overwrite etc/ssl/cert.pem`.
    provides = ["ca-certificates-bundle"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # Remove cryptography dependency from certdata2pem.py — it is only
            # used for an optional expiry check; everything else is stdlib.
            "sed -i -e '/^import datetime$/d' -e '/^from cryptography/d' "
            + "-e '/cert = x509.load_der/,/Trusted but expired/{d}' "
            + "mozilla/certdata2pem.py",

            # Generate individual .crt files from certdata.txt
            "make -C mozilla",

            # Install individual certs
            "mkdir -p $DESTDIR/usr/share/ca-certificates",
            "cp mozilla/*.crt $DESTDIR/usr/share/ca-certificates/",

            # Generate concatenated CA bundle
            "mkdir -p $DESTDIR/etc/ssl/certs",
            "cat mozilla/*.crt > $DESTDIR/etc/ssl/certs/ca-certificates.crt",
            "ln -sf certs/ca-certificates.crt $DESTDIR/etc/ssl/cert.pem",

            # Generate ca-certificates.conf
            "cd $DESTDIR/usr/share/ca-certificates && find . -name '*.crt' | sort | sed 's,^\\./,,' > $DESTDIR/etc/ca-certificates.conf",

            # Create hash symlinks for OpenSSL cert lookup
            "openssl rehash $DESTDIR/etc/ssl/certs",
        ]),
    ],
)
