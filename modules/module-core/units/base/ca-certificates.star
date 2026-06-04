unit(
    name = "ca-certificates",
    version = "20250419",
    source = "https://deb.debian.org/debian/pool/main/c/ca-certificates/ca-certificates_20250419.tar.xz",
    sha256 = "33b44ef78653ecd3f0f2f13e5bba6be466be2e7da72182f737912b81798ba5d2",
    license = "MPL-2.0",
    description = "Mozilla CA certificates bundle for TLS verification",
    deps = ["openssl", "toolchain"],
    # Alpine ships /usr/bin/python3 in its python3 apk; Debian splits
    # the binary into python3.11-minimal (pulled transitively via the
    # python3.11 wrapper's runtime closure) and a /usr/bin/python3
    # symlink created by an update-alternatives postinst that yoe's
    # sysroot extraction doesn't run. Pull the package that owns the
    # binary, and rewrite the Makefile's literal `python3` call below.
    distro_deps = {
        "alpine": ["python3"],
        "debian": ["python3.11"],
        # Ubuntu (resolute) ships the interpreter as python3.14; the
        # build step below discovers whichever python3.NN is staged.
        "ubuntu": ["python3.14"],
    },
    runtime_deps = ["openssl"],
    # Source-built ca-certificates ships both the cert bundle (cert.pem,
    # certs/ca-certificates.crt) and the individual certs that Alpine
    # splits into a separate `ca-certificates-bundle` package. Declaring
    # the bundle here routes any package whose runtime_deps reach
    # `ca-certificates-bundle` (apk-tools, libcurl, libretls, …) back to
    # this unit, instead of pulling Alpine's bundle alongside and tripping
    # `apk add` on `trying to overwrite etc/ssl/cert.pem`.
    provides = ["ca-certificates-bundle"],
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # Remove cryptography dependency from certdata2pem.py — it is only
            # used for an optional expiry check; everything else is stdlib.
            "sed -i -e '/^import datetime$/d' -e '/^from cryptography/d' "
            + "-e '/cert = x509.load_der/,/Trusted but expired/{d}' "
            + "mozilla/certdata2pem.py",

            # mozilla/Makefile hardcodes `python3`. apt-family sysroots
            # ship python3.NN with no python3 symlink (update-alternatives
            # postinst doesn't run here), and the version differs per distro
            # (Debian python3.11, Ubuntu python3.14). Discover whichever
            # python3.NN is staged and rewrite the call to its basename.
            "if ! command -v python3 >/dev/null 2>&1; then "
            + "PY=''; for c in /build/sysroot/usr/bin/python3.[0-9] /build/sysroot/usr/bin/python3.[0-9][0-9]; do "
            + "[ -x \"$c\" ] && { PY=$(basename \"$c\"); break; }; done; "
            + "[ -n \"$PY\" ] || { echo 'ca-certificates: no python3 in build sysroot' >&2; exit 1; }; "
            + "sed -i \"s|\\bpython3\\b|$PY|g\" mozilla/Makefile; fi",

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
