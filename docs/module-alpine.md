# module-alpine — wrapping prebuilt Alpine packages

`module-alpine` is a yoe module that wraps prebuilt Alpine Linux `.apk` files as
yoe units. Where `module-core` builds packages from upstream source, units in
this module fetch a binary apk from a pinned Alpine release, verify its sha256,
and repack it as a yoe artifact. The unit's "build" is just extracting the apk
into `$DESTDIR`.

## When to reach for it

The policy yoe follows:

1. **Yoe builds the easy stuff.** Small leaf libraries (`zlib`, `xz`, `expat`,
   `libffi`, `readline`, `ncurses`, …) and small userland tools (`less`, `htop`,
   `vim`, `procps-ng`, `iproute2`, …) stay in `module-core` even though Alpine
   ships them too. Their build is cheap, and keeping them in yoe preserves the
   option to retarget glibc or a different init system later.
2. **`module-alpine` ships Alpine-native and hard-to-build packages.**
   Alpine-native means `musl`, `apk-tools`, `alpine-keys`, `alpine-baselayout` —
   things that only make sense from Alpine. Hard-to-build means packages where
   Alpine's expertise (configure flags, security review, codec/license
   decisions, multi-language coupling) earns its keep: `openssl`, `openssh`,
   `curl`, eventually `python`, `llvm`, `qt6-qtwebengine`, and similar.
3. **Keep building from source anything where the build defines the product.**
   Toolchain, kernel, bootloader, `busybox`, init scripts, `base-files` — these
   are not packages, they are the distribution.

For the broader strategic context — why this rubric exists, where Alpine doesn't
fit (notably edge AI on Jetson), and how yoe expects to handle glibc/systemd
targets in the future — see [libc-and-init.md](libc-and-init.md).

## Alpine release coupling

The Alpine release pinned in `classes/alpine_pkg.star`
(`_ALPINE_RELEASE = "v3.21"` at the time of writing) **must** match the
`FROM alpine:<release>` line in
`@module-core//containers/toolchain-musl/Dockerfile`. Both currently point at
`v3.21`.

The coupling is not aesthetic. Three things tie them together:

1. **libc ABI.** Anything compiled in the toolchain container links against the
   toolchain's musl headers and libc. Anything you fetch via `alpine_pkg` was
   compiled against a specific Alpine release's musl. Mix versions and you
   produce images that compile and link cleanly, then crash on first run when
   the dynamic linker resolves a symbol whose layout has changed.
2. **Signing keys.** Every Alpine release ships with a build-host signing key.
   Prebuilt apks are signed by that key, and `apk-tools` inside the target image
   verifies signatures against the keyring baked into the toolchain container at
   build time. A version skew means the keyring doesn't recognise the signatures
   on the packages you're trying to install.
3. **Library co-versioning.** Many Alpine packages declare `D:so:libfoo.so.N`
   runtime dependencies pinned to specific minor versions. Pulling `package-A`
   from one release and `package-B` from another lands you with conflicting
   `so:` constraints that `apk` will refuse to install.

When bumping the Alpine release, do all three in lockstep across the yoe repo
and the [module-alpine repo](https://github.com/yoebuild/module-alpine):

1. Update `FROM alpine:<release>` in
   `modules/module-core/containers/toolchain-musl/Dockerfile` in the yoe repo.
2. Update `_ALPINE_RELEASE` in `classes/alpine_pkg.star` in the module-alpine
   repo.
3. Update `version` and `sha256` on every unit under `units/` in the
   module-alpine repo. The version comes from the new release's APKINDEX; the
   sha256 is the SHA-256 of the apk file itself.

## Writing a new alpine_pkg unit

```python
load("@module-alpine//classes/alpine_pkg.star", "alpine_pkg")

alpine_pkg(
    name = "sqlite-libs",
    version = "3.48.0-r4",
    license = "blessing",
    description = "SQLite shared library (Alpine v3.21)",
    runtime_deps = ["musl"],
    sha256 = {
        "x86_64": "...",
        "arm64":  "...",
    },
)
```

The `version` is Alpine's full pkgver (e.g., `3.48.0-r4`), not just the upstream
version. The sha256 dict keys are yoe canonical arches; the class maps them to
Alpine arch tokens (`arm64` → `aarch64`).

To find the version + sha256 for a package:

```bash
# 1. Find the version in the APKINDEX:
curl -sLO https://dl-cdn.alpinelinux.org/alpine/v3.21/main/x86_64/APKINDEX.tar.gz
tar -xzOf APKINDEX.tar.gz APKINDEX | awk -v RS= '/(^|\n)P:sqlite-libs(\n|$)/ { print; exit }'

# 2. Fetch the apk and sha256 it:
curl -sLO https://dl-cdn.alpinelinux.org/alpine/v3.21/main/x86_64/sqlite-libs-3.48.0-r4.apk
sha256sum sqlite-libs-3.48.0-r4.apk
```

Repeat for each architecture you target.

## Dependencies are not auto-imported

Alpine packages declare runtime dependencies via the `D:` field in APKINDEX. The
`alpine_pkg()` class **does not** read or follow those — it requires every
dependency to be listed explicitly in `runtime_deps`.

This is deliberate. Auto-following Alpine's dep closure would silently import
dozens of packages (busybox, openrc, ssl-client, …) that yoe either ships from
`module-core` already or doesn't want at all. Forcing explicit `runtime_deps`
keeps the imported surface visible and small. When you add a new alpine_pkg,
look at its `D:` line in APKINDEX and either declare the corresponding yoe units
in `runtime_deps`, or, for deps you don't need on the target image, just leave
them out.

## Override with a from-source unit

Because units in `module-alpine` use the bare names (`musl`, `sqlite-libs`, …),
any later-priority module — including the project itself — can override them by
defining a unit with the same name. See
[naming-and-resolution.md](naming-and-resolution.md#unit-replacement-via-name-shadowing).

```python
# PROJECT.star
modules = [
    module("https://github.com/yoebuild/module-alpine.git", ref = "main"),  # ships musl, sqlite-libs, …
    module("https://github.com/yoebuild/yoe.git", ref = "main", path = "modules/module-core"),  # source-built kernel, busybox, …
    module(..., path = "modules/my-overrides"),  # last → wins
]

# modules/my-overrides/units/musl.star
unit(name = "musl", source = "https://git.musl-libc.org/git/musl",
     tag = "v1.2.5", tasks = [...])
```

The override unit produces an apk under the same name. Consumers writing
`runtime_deps = ["musl"]` get the override automatically.
