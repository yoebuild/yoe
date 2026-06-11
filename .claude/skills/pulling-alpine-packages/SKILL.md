---
name: pulling-alpine-packages
description: >
  Use when a build needs a tool or library that Alpine Linux already packages
  (cross compilers, niche build tools, language runtimes without a source unit)
  and the instinct is to write a from-source unit. Triggers: missing-tool build
  errors, foreign-arch cross toolchain needs (e.g. armv7-R, riscv), questions
  about whether `module-alpine` already has package X, or "/pull-alpine".
---

# Pulling Alpine Packages Through module-alpine

When a build wants something Alpine `main` or `community` ships, pull it through
`module-alpine` rather than writing a from-source unit. This is the default; a
from-source unit is the fallback when Alpine doesn't ship the package or ships
the wrong version.

`module-alpine` exposes Alpine's repos as **feeds**, not per-package files. One
`alpine_feed(...)` call per repo section registers a synthetic module
(`alpine.main`, `alpine.community`) backed by a checked-in APKINDEX. Every
package in that index is available; the unit for a package **materializes
lazily** the first time a build's closure references it. You do not generate a
`.star` file to consume a package — you just name it.

## When this applies

- Cross toolchains for ISAs the native container can't emit (e.g. `armv7-R` from
  aarch64 — BeaglePlay's R5 SPL pulls `gcc-arm-none-eabi`,
  `binutils-arm-none-eabi`, `newlib-arm-none-eabi`).
- Niche build tools (formatters, code generators, helper utilities) used only to
  build something else.
- Language runtimes or libraries you don't otherwise want to maintain a source
  unit for.

Do **not** use this for components where we want git-source tracking and the
`yoe dev` patch workflow (kernels, U-Boot, OP-TEE, project libraries, anything
likely to need local patches), nor where the build _is_ the product (toolchain,
busybox, base-files, init).

## Workflow

In the common case there is **nothing to generate and nothing to push** — the
feed already exposes the package.

1. **Name the package in `deps`.** Add the Alpine package name directly to the
   consuming unit's `deps` (build-time) and/or `runtime_deps`. The synthetic
   feed module resolves it lazily; its apk data segment is extracted into
   `$DESTDIR` and thus the consumer's `/build/sysroot/`. Binaries land on the
   default `/build/sysroot/usr/bin` PATH.

   ```python
   deps = [
       "toolchain-musl",
       "gcc-arm-none-eabi",       # ← resolved from alpine.community, lazily
       "binutils-arm-none-eabi",
       "newlib-arm-none-eabi",
   ]
   ```

   No `prefer_modules` entry is needed when the package exists **only** in the
   feed: synthetic modules always rank below every real module, so a feed
   package with no source-unit competitor resolves automatically.

2. **Only override with `prefer_modules` when a source unit also claims the
   name.** If `module-core` builds `xz` from source but a particular image needs
   Alpine's prebuilt `xz` (e.g. for a shared `liblzma.so.5` soversion), route it
   in PROJECT.star. The map is **keyed by distro**:

   ```python
   prefer_modules = {
       "alpine": {
           "xz": "alpine.main",
           "curl": "alpine.main",
       },
   }
   ```

   Without an entry, the from-source unit wins. Reach for this only to flip a
   specific package to the feed, and leave a comment explaining why (the e2e
   project's PROJECT.star has worked examples).

3. **`testing` is never available.** Only `main` and `community` are wrapped.

## Refreshing the feed (only when you need a newer Alpine version)

The checked-in APKINDEX pins a point release. If you need a package version
Alpine has since published (a security bump, a new package), the APKINDEX must
be refreshed in the `module-alpine` repo:

```
yoe update-feeds                 # run in the module-alpine repo root
git diff feeds/                  # spot-check version bumps / new packages
```

`yoe update-feeds` fetches each feed's `APKINDEX.tar.gz`, verifies the RSA
signature against the feed's `keys=[...]`, and atomically rewrites
`feeds/<section>/<arch>/APKINDEX`. It writes only — it does not stage, commit,
or push.

The live `module-alpine` checkout is under
`testdata/<project>/cache/modules/module-alpine/` (for test builds,
`testdata/e2e-project/cache/modules/module-alpine/`). Any change there — a
refreshed APKINDEX, a new `*-enable.star` companion — must be **committed and
pushed upstream**: the next `yoe build` does
`git fetch && git checkout FETCH_HEAD` in the cache and silently discards
uncommitted or un-pushed local edits. Pause and confirm the push landed before
re-running anything that triggers a module sync. **Never do the upstream
commit/push yourself — the user manages those repos.**

## Enabling a service from a pulled package

A feed gives you the `.apk` but does not enable init scripts — Alpine ships them
disabled. To enable a service, add a one-line companion unit
(`<svc>-enable.star`) in `module-alpine` that depends on the package's `-openrc`
unit and sets `services = [...]` to bake the runlevel symlink. Do not scan the
rootfs for init scripts; explicit companion units are how a package's services
become enabled. (This is module-alpine repo work — same commit-and-push rule as
above.)

## Concrete win

BeaglePlay's R5 SPL is Cortex-R5F (armv7-R), an ISA the aarch64 build container
can't emit. Instead of building an entire cross toolchain from source, the SPL
unit lists Alpine's `gcc-arm-none-eabi`, `binutils-arm-none-eabi`, and
`newlib-arm-none-eabi` in `deps`. No file generated, no Dockerfile change, no
source build, no maintenance — the community feed materializes those three units
on demand.

## Common mistakes

- **Generating a `.star` file to consume a package.** Not needed — the feed
  exposes every `main`/`community` package lazily. Just name it in `deps`.
  (Maintaining module-alpine itself — refreshing the checked-in APKINDEX after
  Alpine ships a release — is `yoe update-feeds` run in that repo, not anything
  a consuming project does.)
- **Adding a `prefer_modules` entry for a feed-only package.** Unnecessary —
  synthetics already lose to real modules, so a package with no source-unit
  competitor resolves without routing. Use `prefer_modules` only to _override_ a
  source unit with the feed's build.
- **Forgetting to push after `yoe update-feeds` or adding a companion.** A
  local-only commit in the cache survives exactly until the next `yoe build`'s
  module sync, then vanishes.
- **Doing the commit/push yourself.** External-module repos are user-managed;
  surface the file paths and remind the user to push.
- **Reaching for this when we want patchable source.** If the package is
  something we will likely patch (kernel, bootloader, project library), write a
  from-source git-based unit instead.
