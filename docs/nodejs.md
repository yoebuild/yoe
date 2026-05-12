# Node.js Workflows

This page covers how to ship Node.js apps with their npm dependencies on a yoe
image. yoe doesn't use npm as a package manager — npm-installed packages live in
a per-app `node_modules` tree baked into a regular apk, so the on-device package
manager stays apk-only and rebuilding the image rebuilds `node_modules` from
your `package.json` (and `package-lock.json` if present).

## Packaging a Node.js app with npm dependencies

The `nodejs_app` class in `module-core/classes/nodejs.star` creates an app
directory under `/usr/lib/node-apps/<name>` on the target, runs `npm install`
against your `package.json` so the listed packages land in `node_modules/` next
to your code, and ships the whole tree as a regular `.apk`. It gets the same
caching and signing as any other unit and brings in `nodejs` automatically via
`runtime_deps`.

Each app lives in its own source directory next to the unit's `.star` file and
uses a normal Node.js project layout — `package.json` is the source of truth for
deps, exactly like a developer would use locally.

A minimal app:

```python
load("//classes/nodejs.star", "nodejs_app")

nodejs_app(
    name = "nodejs-hello",
    version = "1.0.0",
    description = "Greeter that renders ASCII art via figlet",
    runtime_deps = ["busybox"],  # /bin/sh for the wrapper
    tasks = [
        task("install-app", steps = [
            install_file("package.json",
                         "$DESTDIR/usr/lib/node-apps/nodejs-hello/package.json",
                         mode = 0o644),
            install_file("hello.js",
                         "$DESTDIR/usr/lib/node-apps/nodejs-hello/hello.js",
                         mode = 0o644),
            "cat > $DESTDIR/usr/bin/nodejs-hello <<'__WRAP__'\n" +
            "#!/bin/sh\n" +
            "exec node /usr/lib/node-apps/nodejs-hello/hello.js \"$@\"\n" +
            "__WRAP__",
            "chmod 0755 $DESTDIR/usr/bin/nodejs-hello",
        ]),
    ],
)
```

With a `package.json` like this next to the unit:

```json
{
  "name": "nodejs-hello",
  "version": "1.0.0",
  "private": true,
  "dependencies": {
    "figlet": "1.7.0"
  }
}
```

After `yoe build nodejs-hello`, the resulting apk installs:

- `/usr/lib/node-apps/nodejs-hello/` — `package.json`, `hello.js`, and
  `node_modules/` (figlet and its transitive deps)
- `/usr/bin/nodejs-hello` — a one-line `/bin/sh` wrapper that runs the app via
  `node`

On the device, `nodejs-hello "hi"` works without the user knowing node_modules
is involved.

## How the task order works

`nodejs_app` wraps the user-supplied tasks between two class-owned tasks:

1. **`nodejs-setup`** — creates `$DESTDIR/<install_path>` so install_file steps
   have a target directory.
2. **(your tasks)** — copy `package.json` (and optionally `package-lock.json`),
   then any JS/asset files, into `$APP_BUILD` using `install_file()`. Emit your
   `/usr/bin` wrapper here too.
3. **`nodejs-install`** — runs `npm ci` if a lockfile is present, otherwise
   `npm install`, against the staged `package.json`. Then rewrites any
   build-time path baked into `node_modules` back to the on-target absolute path
   and writes the `entry_points` wrappers.

If you ship a `package-lock.json` alongside `package.json`, `npm ci` makes the
install fully reproducible — recommended for production units. Without a
lockfile you get whatever satisfies the version ranges in `dependencies{}` at
build time.

## entry_points shortcut

For apps whose main entry point is just "run a binary from `node_modules/.bin`"
or "run a script from a package," skip the manual wrapper script and use
`entry_points`:

```python
nodejs_app(
    name = "myapp",
    version = "1.0.0",
    entry_points = {
        # /usr/bin/myapp runs `node_modules/.bin/myapp`
        "myapp": "myapp",
        # /usr/bin/lint runs `node node_modules/eslint/bin/eslint.js`
        "lint": "eslint:bin/eslint.js",
    },
    tasks = [
        task("install-app", steps = [
            install_file("package.json",
                         "$DESTDIR/usr/lib/node-apps/myapp/package.json"),
        ]),
    ],
)
```

`"pkg"` resolves to `node_modules/.bin/pkg`. `"pkg:script"` resolves to
`node node_modules/pkg/script`.

## Pure-JS packages vs native bindings

Pure-JavaScript packages (`figlet`, `commander`, `chalk`, `express` and its
deps, etc.) install out of the box. Packages with native bindings (anything
using `node-gyp`, `prebuild`, or a `binding.gyp`) need their build-time
libraries and headers in the toolchain container. Add them as `deps`:

```python
nodejs_app(
    name = "sqlite-app",
    version = "1.0.0",
    deps = ["sqlite"],  # better-sqlite3 links libsqlite
    tasks = [
        task("install-app", steps = [
            install_file("package.json",
                         "$DESTDIR/usr/lib/node-apps/sqlite-app/package.json"),
        ]),
    ],
)
```

When a package ships musl-compatible prebuilt binaries, npm will use those and
you can skip the headers.

## Customising the install path

By default the app lives at `/usr/lib/node-apps/<name>`. Override with
`install_path` when an app needs a different location — for example, when an
upstream config or service file points at a fixed path:

```python
nodejs_app(
    name = "myapp",
    version = "1.0.0",
    install_path = "/opt/myapp",
    tasks = [
        task("install-app", steps = [
            install_file("package.json", "$DESTDIR/opt/myapp/package.json"),
        ]),
    ],
)
```

The wrapper script(s) emitted by `entry_points` follow the install path
automatically.

## `nodejs-image`

`module-core/images/nodejs-image.star` boots into a userland with `node`, `npm`,
the dev-image diagnostic tools, and the `nodejs-hello` demo pre-installed. Run
`yoe build nodejs-image && yoe run nodejs-image` to get a QEMU VM where
`nodejs-hello "..."` renders an ASCII-art banner — useful as a smoke test that
`nodejs_app` works end-to-end on your machine before you spend npm's download
budget on a real app.
