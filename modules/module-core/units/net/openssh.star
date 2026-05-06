load("//classes/autotools.star", "autotools")

autotools(
    name = "openssh",
    version = "9.9p1",
    source = "https://github.com/openssh/openssh-portable.git",
    tag = "V_9_9_P1",
    license = "BSD-2-Clause",
    description = "OpenSSH secure shell client and server",
    deps = ["openssl", "zlib"],
    runtime_deps = ["openssl", "zlib"],
    configure_args = [
        "--sysconfdir=/etc/ssh",
        "--without-openssl-header-check",
    ],
    services = ["S40sshd"],
    tasks = [
        task("install-init", steps = [
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("S40sshd", "$DESTDIR/etc/init.d/S40sshd", mode = 0o755),
        ]),
    ],
)
