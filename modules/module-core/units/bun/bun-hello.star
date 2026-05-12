load("//classes/bun.star", "bun_app")

# Demo Bun application packaged on top of bun_app. The class materialises
# an app directory at /usr/lib/bun-apps/bun-hello, runs `bun install`
# against the package.json we ship below, and bundles our own hello.ts
# next to the resulting node_modules tree. A /usr/bin/bun-hello wrapper
# lets users run the app like any system binary — and since bun runs
# TypeScript natively, the .ts file is the actual entry point with no
# compile step.
#
# package.json (and an optional bun.lockb) live next to this file under
# units/bun/bun-hello/ and declare the npm deps -- the same layout a
# developer's regular Bun project uses, no yoe-specific schema.
bun_app(
    name = "bun-hello",
    version = "1.0.0",
    description = "Example Bun app: prints an ASCII-art greeting via figlet",
    license = "MIT",
    # busybox supplies /bin/sh for the wrapper script below.
    runtime_deps = ["busybox"],
    tasks = [
        task("install-app", steps = [
            "mkdir -p $DESTDIR/usr/bin",
            install_file("package.json",
                         "$DESTDIR/usr/lib/bun-apps/bun-hello/package.json",
                         mode = 0o644),
            install_file("hello.ts",
                         "$DESTDIR/usr/lib/bun-apps/bun-hello/hello.ts",
                         mode = 0o644),
            "cat > $DESTDIR/usr/bin/bun-hello <<'__YOE_HELLO_WRAP__'\n" +
            "#!/bin/sh\n" +
            "exec bun /usr/lib/bun-apps/bun-hello/hello.ts \"$@\"\n" +
            "__YOE_HELLO_WRAP__",
            "chmod 0755 $DESTDIR/usr/bin/bun-hello",
        ]),
    ],
)
