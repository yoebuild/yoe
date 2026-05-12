# Bun Workflows

This page covers how to ship [Bun](https://bun.sh/) apps with their npm
dependencies on a yoe image. yoe doesn't use bun (or npm) as a system package
manager — bun-installed packages live in a per-app `node_modules` tree baked
into a regular apk, so the on-device package manager stays apk-only and
rebuilding the image rebuilds `node_modules` from your `package.json` (and
`bun.lockb` if present).

Bun is a single binary that bundles a JavaScript runtime, a package manager, and
a bundler. It runs TypeScript directly with no separate compile step, so the
entry point of a bun app can be a plain `.ts` file.

## Packaging a Bun app with npm dependencies

The `bun_app` class in `module-core/classes/bun.star` creates an app directory
under `/usr/lib/bun-apps/<name>` on the target, runs `bun install --production`
against your `package.json` so the listed packages land in `node_modules/` next
to your code, and ships the whole tree as a regular `.apk`. It gets the same
caching and signing as any other unit and brings in `bun` automatically via
`runtime_deps`.

Each app lives in its own source directory next to the unit's `.star` file and
uses a normal Bun project layout — `package.json` is the source of truth for
deps, exactly like a developer would use locally.

A minimal app:

```python
load("//classes/bun.star", "bun_app")

bun_app(
    name = "bun-hello",
    version = "1.0.0",
    description = "Greeter that renders ASCII art via figlet",
    runtime_deps = ["busybox"],  # /bin/sh for the wrapper
    tasks = [
        task("install-app", steps = [
            install_file("package.json",
                         "$DESTDIR/usr/lib/bun-apps/bun-hello/package.json",
                         mode = 0o644),
            install_file("hello.ts",
                         "$DESTDIR/usr/lib/bun-apps/bun-hello/hello.ts",
                         mode = 0o644),
            "cat > $DESTDIR/usr/bin/bun-hello <<'__WRAP__'\n" +
            "#!/bin/sh\n" +
            "exec bun /usr/lib/bun-apps/bun-hello/hello.ts \"$@\"\n" +
            "__WRAP__",
            "chmod 0755 $DESTDIR/usr/bin/bun-hello",
        ]),
    ],
)
```

With a `package.json` like this next to the unit:

```json
{
  "name": "bun-hello",
  "version": "1.0.0",
  "private": true,
  "type": "module",
  "dependencies": {
    "figlet": "1.7.0"
  }
}
```

And `hello.ts`:

```typescript
import figlet from "figlet";

const argv = process.argv.slice(2);
const text = argv.length > 0 ? argv.join(" ") : "Hello, yoe!";
console.log(figlet.textSync(text, { font: "Slant" }));
console.log(`(bun ${Bun.version})`);
```

After `yoe build bun-hello`, the resulting apk installs:

- `/usr/lib/bun-apps/bun-hello/` — `package.json`, `hello.ts`, and
  `node_modules/` (figlet and its transitive deps)
- `/usr/bin/bun-hello` — a one-line `/bin/sh` wrapper that runs the app via
  `bun`

On the device, `bun-hello "hi"` works without the user knowing node_modules is
involved.

## How the task order works

`bun_app` wraps the user-supplied tasks between two class-owned tasks:

1. **`bun-setup`** — creates `$DESTDIR/<install_path>` so install_file steps
   have a target directory.
2. **(your tasks)** — copy `package.json` (and optionally `bun.lockb`), then any
   JS/TS/asset files, into `$APP_BUILD` using `install_file()`. Emit your
   `/usr/bin` wrapper here too.
3. **`bun-install`** — runs `bun install --production` against the staged
   `package.json`, then rewrites any build-time path baked into `node_modules`
   back to the on-target absolute path and writes the `entry_points` wrappers.

If you ship a `bun.lockb` alongside `package.json`, bun resolves dependencies
from the lockfile — recommended for production units. Without a lockfile you get
whatever satisfies the version ranges in `dependencies{}` at build time.

## entry_points shortcut

For apps whose main entry point is a single script or a binary from
`node_modules/.bin`, skip the manual wrapper and use `entry_points`:

```python
bun_app(
    name = "myapp",
    version = "1.0.0",
    entry_points = {
        # /usr/bin/myapp runs `bun /usr/lib/bun-apps/myapp/main.ts`
        "myapp": "main.ts",
        # /usr/bin/serve runs `node_modules/.bin/serve`
        "serve": "serve",
        # /usr/bin/lint runs `bun node_modules/eslint/bin/eslint.js`
        "lint": "eslint:bin/eslint.js",
    },
    tasks = [
        task("install-app", steps = [
            install_file("package.json",
                         "$DESTDIR/usr/lib/bun-apps/myapp/package.json"),
            install_file("main.ts",
                         "$DESTDIR/usr/lib/bun-apps/myapp/main.ts"),
        ]),
    ],
)
```

Entry forms:

- `"file.ts"` / `"file.js"` / `"file.mjs"` — exec `bun <install_path>/<file>`.
- `"pkg"` — exec `node_modules/.bin/pkg` directly.
- `"pkg:script"` — exec `bun node_modules/pkg/script`.

## Why Bun is a useful default for new JS/TS apps

A few practical differences from Node:

- **TypeScript runs as-is.** No `tsc`, no `ts-node`, no separate build step. The
  entry point of a `bun_app` can be a `.ts` file and it works.
- **`bun install` is fast.** The install task in a typical app build is much
  shorter than the `npm install` equivalent.
- **Single binary.** The runtime, package manager, test runner, and bundler are
  all the same `bun` executable, so the toolchain footprint is one apk.

Node is still available via `module-alpine`'s `nodejs` unit if you have an
existing Node app or a dep that depends on Node-specific behavior.

## Customising the install path

By default the app lives at `/usr/lib/bun-apps/<name>`. Override with
`install_path` when an app needs a different location:

```python
bun_app(
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

## `bun-image`

`module-core/images/bun-image.star` boots into a userland with `bun`, `bunx`,
the dev-image diagnostic tools, and the `bun-hello` demo pre-installed. Run
`yoe build bun-image && yoe run bun-image` to get a QEMU VM where
`bun-hello "..."` renders an ASCII-art banner — useful as a smoke test that
`bun_app` works end-to-end on your machine before you spend bun's download
budget on a real app.
