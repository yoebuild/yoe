load("//classes/python.star", "python_venv")

# Demo Python application packaged on top of python_venv. The class
# materialises a venv at /usr/lib/python-venvs/python-hello with the listed
# pip packages installed; the extra install-app task ships our own script
# alongside it and exposes a /usr/bin/python-hello wrapper so users can run
# the app the same way they'd run any system binary.
python_venv(
    name = "python-hello",
    version = "1.0.0",
    description = "Example Python app: prints an ASCII-art greeting via pyfiglet",
    license = "MIT",
    pip_packages = ["pyfiglet==1.0.2"],
    # busybox supplies /bin/sh for the wrapper script below.
    runtime_deps = ["busybox"],
    tasks = [
        task("install-app", steps = [
            "mkdir -p $DESTDIR/usr/lib/python-hello $DESTDIR/usr/bin",
            install_file("hello.py", "$DESTDIR/usr/lib/python-hello/hello.py", mode = 0o644),
            "cat > $DESTDIR/usr/bin/python-hello <<'__YOE_HELLO_WRAP__'\n" +
            "#!/bin/sh\n" +
            "exec /usr/lib/python-venvs/python-hello/bin/python /usr/lib/python-hello/hello.py \"$@\"\n" +
            "__YOE_HELLO_WRAP__",
            "chmod 0755 $DESTDIR/usr/bin/python-hello",
        ]),
    ],
)
