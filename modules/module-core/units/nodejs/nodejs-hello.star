load("//classes/nodejs.star", "nodejs_app")

# Demo Node.js application packaged on top of nodejs_app. The class
# materialises an app directory at /usr/lib/node-apps/nodejs-hello, runs
# `npm install` against the package.json we ship below, and bundles our
# own hello.js next to the resulting node_modules tree. A /usr/bin/nodejs-
# hello wrapper lets users run the app like any system binary.
#
# package.json (and an optional package-lock.json) live next to this file
# under units/nodejs/nodejs-hello/ and declare the npm deps -- the same
# layout a developer's regular Node project uses, no yoe-specific schema.
nodejs_app(
    name = "nodejs-hello",
    version = "1.0.0",
    description = "Example Node.js app: prints an ASCII-art greeting via figlet",
    license = "MIT",
    # busybox supplies /bin/sh for the wrapper script below.
    runtime_deps = ["busybox"],
    tasks = [
        task("install-app", steps = [
            "mkdir -p $DESTDIR/usr/bin",
            install_file("package.json",
                         "$DESTDIR/usr/lib/node-apps/nodejs-hello/package.json",
                         mode = 0o644),
            install_file("hello.js",
                         "$DESTDIR/usr/lib/node-apps/nodejs-hello/hello.js",
                         mode = 0o644),
            "cat > $DESTDIR/usr/bin/nodejs-hello <<'__YOE_HELLO_WRAP__'\n" +
            "#!/bin/sh\n" +
            "exec node /usr/lib/node-apps/nodejs-hello/hello.js \"$@\"\n" +
            "__YOE_HELLO_WRAP__",
            "chmod 0755 $DESTDIR/usr/bin/nodejs-hello",
        ]),
    ],
)
