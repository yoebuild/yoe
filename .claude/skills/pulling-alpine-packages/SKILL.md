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

When a build wants something Alpine main or community ships, pull it through
`module-alpine` rather than writing a from-source unit. This is the default; a
from-source unit is the fallback when Alpine doesn't ship the package or ships
the wrong version.

## When this applies

- Cross toolchains for ISAs the native container can't emit (e.g. `armv7-R` from
  aarch64 — BeaglePlay R5 SPL pulls `gcc-arm-none-eabi`,
  `binutils-arm-none-eabi`, `newlib-arm-none-eabi`).
- Niche build tools (formatters, code generators, helper utilities) used only to
  build something else.
- Language runtimes or libraries you don't otherwise want to maintain a source
  unit for.

Do **not** use this for components where we want git-source tracking and the
`yoe dev` patch workflow (kernels, U-Boot, OP-TEE, project libraries, anything
likely to need local patches).

## Workflow

1. Locate the live `module-alpine` checkout under
   `testdata/<project>/cache/modules/module-alpine/`. For test builds that's
   `testdata/e2e-project/cache/modules/module-alpine/`. Any older
   `units-alpine/` checkout is legacy — do not edit it.

2. From the `module-alpine` repo root, run:

   ```
   scripts/gen-unit.py <pkgname> [<pkgname>...]
   ```

   The script:
   - Auto-picks `main` vs `community`.
   - Fetches APKINDEX.
   - Computes `apk_checksum` per arch.
   - Writes `units/<repo>/<pkgname>.star` with the canonical header
     `load("@alpine//classes/alpine_pkg.star", "alpine_pkg")`.

3. Add the new unit name to the downstream unit's `deps`. The apk's data segment
   is extracted into `$DESTDIR` and thus into the consumer's `/build/sysroot/`;
   binaries land on the default `/build/sysroot/usr/bin` PATH.

4. Commit **and push** the new files in the `module-alpine` repo upstream. The
   next `yoe build` does `git fetch && git checkout FETCH_HEAD` in the cache and
   silently discards any uncommitted or un-pushed local edits. Pause and confirm
   the push landed before re-running anything that triggers a module sync.
   **Never do the upstream commit/push yourself — the user manages those
   repos.**

## Concrete win

BeaglePlay's R5 SPL is Cortex-R5F (armv7-R), an ISA the aarch64 build container
can't emit. Instead of building an entire cross toolchain from source, the SPL
unit depends on Alpine's `gcc-arm-none-eabi`, `binutils-arm-none-eabi`, and
`newlib-arm-none-eabi`. No Dockerfile change, no source build, no maintenance.

## Common mistakes

- **Editing the legacy `units-alpine/` tree.** It is not the live module. Edits
  there have no effect on builds.
- **Forgetting to push upstream.** A local-only commit in the cache survives
  exactly until the next `yoe build`'s module sync, then vanishes.
- **Doing the commit/push yourself.** External-module repos are user-managed;
  surface the file paths and remind the user to push.
- **Reaching for this when we want patchable source.** If the package is
  something we will likely patch (kernel, bootloader, project library), write a
  from-source git-based unit instead.
