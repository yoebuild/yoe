# module-alpine — wrapping prebuilt Alpine packages

`module-alpine` is a yoe module that wraps prebuilt Alpine Linux `.apk` files as
yoe units. Where `module-core` builds packages from upstream source, units in
this module fetch a binary apk from a pinned Alpine release, verify its sha256,
and repack it as a yoe artifact. The unit's "build" is just extracting the apk
into `$DESTDIR`.

The module lives at <https://github.com/yoebuild/module-alpine>. Open it to
browse the cached `.star` files, the `gen-unit.py` generator, or to send a PR
adding a new package wrapper.

> **Implementation details:** how Alpine apks pass through yoe's pipeline
> (signature swap, noarch routing, `gen-unit.py`, the docker-openrc punt) live
> in [apk-passthrough.md](apk-passthrough.md). This doc is the "when to reach
> for it" rubric; the other is the "how it works end-to-end" reference.

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

## alpine_feed: declaring a whole repo as one module entry

`alpine_feed(...)` is a Starlark builtin that turns a checked-in directory of
APKINDEX files into a lazily-materialized synthetic module. Where `alpine_pkg`
is one unit per package, one `alpine_feed` call exposes thousands of packages
from an upstream Alpine repo (main, community, etc.) with a single declaration.
Units materialize on demand as an image's runtime closure references them, so a
project pulling 300 packages from a 60k-entry feed pays for 300 unit
allocations, not 60k.

A typical declaration in `module-alpine/MODULE.star`:

```python
module_info(name = "alpine")

alpine_feed(
    name    = "main",                                # synthetic module is alpine.main
    url     = "https://dl-cdn.alpinelinux.org/alpine",
    branch  = "v3.21",                               # Alpine release tag
    section = "main",                                # repo section
    index   = "feeds/main",                          # dir holding <arch>/APKINDEX
    keys    = ["keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub"],
)

alpine_feed(
    name    = "community",
    url     = "https://dl-cdn.alpinelinux.org/alpine",
    branch  = "v3.21",
    section = "community",
    index   = "feeds/community",
    keys    = ["keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub"],
)
```

The composed module name is `<parent>.<feed-name>` — `alpine.main`,
`alpine.community`. The resolver consults synthetic modules after every real
module so a from-source override (`module-core/units/openssl.star`,
say) wins against the feed automatically.

> **Layout migration (planned):** the current `module-alpine` repo still uses
> per-package `alpine_pkg.star` files under `units/main/` and
> `units/community/` (~3,751 files generated by `scripts/gen-unit.py`). The
> cutover to two `alpine_feed()` calls + a small companion layer of
> `*-enable.star` units is tracked as U13 in
> [docs/plans/2026-05-26-001-feat-feeds-as-modules-plan.md](plans/2026-05-26-001-feat-feeds-as-modules-plan.md).
> Until that lands, the builtin is wired and tested but module-alpine's
> on-disk layout is unchanged.

## Maintainer playbook: `yoe update-feeds`

When Alpine cuts a new release or ships a security patch, the module-alpine
maintainer refreshes the checked-in APKINDEX files with one command. Run it
inside the module repo:

```bash
cd path/to/module-alpine
yoe update-feeds                    # refresh every alpine_feed for every existing arch
yoe update-feeds --arch x86_64      # restrict to one arch
yoe update-feeds --module-dir ../some/other/module
```

What it does, per `alpine_feed()` call, per arch:

1. Fetch `<url>/<branch>/<section>/<arch>/APKINDEX.tar.gz` over HTTP.
2. Verify the RSA-SHA1 signature against the keys declared in
   `alpine_feed(keys=[...])`. Pure-Go verification — never consults
   `/etc/apk/keys/` on the maintainer's host, so the trust list the module
   declares is the one that's actually enforced.
3. Decompress the inner APKINDEX and atomically write it to
   `<module>/<index>/<alpine-arch>/APKINDEX`.

`yoe update-feeds` writes only — it does not stage, commit, or push. The
intended workflow is:

```bash
yoe update-feeds                # refresh every feed
git diff feeds/                 # spot-check version bumps, new packages, removals
git add feeds/
git commit -m "module-alpine: refresh feeds to Alpine v3.21.2"
git push                        # ships to consumers on next `yoe build`
```

### When the diff looks unexpected

- **Lots of new packages or removals**: confirm the Alpine release moved (a
  point release or branch flip).
- **A signature failure**: either Alpine rotated keys (see below) or the
  download was tampered. The failing key fingerprint is in the error message;
  cross-reference against Alpine's
  [release signing keys](https://wiki.alpinelinux.org/wiki/Release_signing_keys)
  before adding a new key.
- **HTTP 404**: the upstream mirror dropped the branch (very old release) or
  the section name in `alpine_feed` is wrong.

### Key rotation

When Alpine rotates its signing key (rare, but happens around major release
boundaries), commit the new public key alongside the old one and add it to
`alpine_feed(keys=[...])`:

```python
alpine_feed(
    name    = "main",
    # ... other fields ...
    keys    = [
        "keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub",  # old
        "keys/alpine-devel@lists.alpinelinux.org-5e69ca50.rsa.pub",  # new
    ],
)
```

Both keys verify during the transition period. Once every active Alpine release
the module ships has rotated to the new key, drop the old one in a follow-up
commit.

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
