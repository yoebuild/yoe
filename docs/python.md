# Python Workflows

This page covers how to ship Python apps with their pip dependencies on a yoe
image. yoe doesn't use pip as a package manager — pip-installed packages live in
a per-app **virtualenv** baked into a regular apk, so the on-device package
manager stays apk-only and rebuilding the image rebuilds the venv from a
declared list of pins.

## Packaging a Python app with pip dependencies

The `python_venv` class in `module-core/classes/python.star` creates a
virtualenv under `/usr/lib/python-venvs/<name>` on the target and pip-installs
the listed packages into it. The result is packaged as a regular `.apk`, gets
the same caching and signing as any other unit, and brings in `python3`
automatically via `runtime_deps`.

A minimal app:

```python
load("//classes/python.star", "python_venv")

python_venv(
    name = "python-hello",
    version = "1.0.0",
    description = "Greeter that renders ASCII art via pyfiglet",
    pip_packages = ["pyfiglet==1.0.2"],
    entry_points = {
        # /usr/bin/figlet runs `python -m pyfiglet "$@"` inside the venv
        "figlet": "pyfiglet",
    },
)
```

After `yoe build python-hello`, the resulting apk installs:

- `/usr/lib/python-venvs/python-hello/` — the venv (pip, pyfiglet, etc.)
- `/usr/bin/figlet` — a one-line `/bin/sh` wrapper that execs the venv's
  `python -m pyfiglet`

On the device, `figlet "hi"` works without the user knowing a venv is involved.

## Bundling app code alongside the venv

`python_venv` only manages the venv itself. For apps that have their own source
files, add an extra task that ships them via `install_file()` and points a
wrapper at the bundled script:

```python
load("//classes/python.star", "python_venv")

python_venv(
    name = "python-hello",
    version = "1.0.0",
    description = "Example Python app: ASCII-art greeting via pyfiglet",
    pip_packages = ["pyfiglet==1.0.2"],
    runtime_deps = ["busybox"],  # /bin/sh for the wrapper
    tasks = [
        task("install-app", steps = [
            "mkdir -p $DESTDIR/usr/lib/python-hello $DESTDIR/usr/bin",
            install_file("hello.py", "$DESTDIR/usr/lib/python-hello/hello.py",
                         mode = 0o644),
            "cat > $DESTDIR/usr/bin/python-hello <<'__WRAP__'\n" +
            "#!/bin/sh\n" +
            "exec /usr/lib/python-venvs/python-hello/bin/python " +
            "/usr/lib/python-hello/hello.py \"$@\"\n" +
            "__WRAP__",
            "chmod 0755 $DESTDIR/usr/bin/python-hello",
        ]),
    ],
)
```

`install_file()` resolves the source path relative to a sibling directory named
after the `.star` file — `units/python/python-hello.star` looks for `hello.py`
under `units/python/python-hello/`. See the
[`python-hello` example](https://github.com/yoebuild/yoe/tree/main/modules/module-core/units/python)
for the complete unit.

## How the venv stays runnable on the target

`python_venv` builds the venv inside `$DESTDIR` during the unit build, which
means every script the venv created has a build-time `$DESTDIR`-prefixed path
baked into its shebang or config. Before packaging, the class:

1. Strips every `__pycache__` so the apk doesn't ship stale bytecode that pip
   will regenerate on first import anyway.
2. Runs `grep -rIlF "$VENV_BUILD" | xargs sed -i` to rewrite every reference
   from the build-time `$DESTDIR/usr/lib/python-venvs/<name>` prefix back to the
   on-target `/usr/lib/python-venvs/<name>` prefix.
3. Re-creates `bin/python` and `bin/python3` as symlinks to `/usr/bin/python3`
   so the venv works against whatever python3 is installed on the target.

The toolchain container (`toolchain-musl`) ships the same Alpine `python3` the
target rootfs gets via `py3-pip`'s runtime-dep chain. Because the python version
(3.12.x) and its absolute path (`/usr/bin/python3`) match on both sides, the
venv carries over cleanly.

## Pure-Python wheels vs C extensions

Pure-Python wheels (`pyfiglet`, `flask`, `click`, `requests` and its
dependencies, etc.) install out of the box. Wheels with C extensions — `numpy`,
`cryptography`, `pydantic-core`, anything with `cffi` — need their build-time
libraries and headers in the toolchain container. Add them as `deps`:

```python
python_venv(
    name = "python-crypto-app",
    version = "1.0.0",
    pip_packages = ["cryptography==43.0.3"],
    deps = ["openssl", "libffi"],  # cryptography links these
)
```

If a wheel is published as `musllinux_*` (most popular packages now are), pip
will install the prebuilt binary and you can skip the headers.

## Customising the install path

By default the venv lives at `/usr/lib/python-venvs/<name>`. Override with
`install_path` when an app needs a different location — for example, when an
upstream config file points at a fixed path:

```python
python_venv(
    name = "myapp",
    version = "1.0.0",
    pip_packages = ["myapp==1.0.0"],
    install_path = "/opt/myapp/venv",
)
```

The wrapper script(s) emitted by `entry_points` follow the install path
automatically.

## `python-image`

`module-core/images/python-image.star` boots into a userland with `python3`,
`pip`, the dev-image diagnostic tools, and the `python-hello` demo
pre-installed. Run `yoe build python-image && yoe run python-image` to get a
QEMU VM where `python-hello "..."` renders an ASCII-art banner — useful as a
smoke test that `python_venv` works end-to-end on your machine before you spend
pip's download budget on a real app.
