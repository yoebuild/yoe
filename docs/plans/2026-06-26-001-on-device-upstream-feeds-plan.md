# On-device upstream feeds — Implementation Plan

> **Status:** Code landed, build/boot verification pending. Implements
> [docs/specs/2026-06-26-on-device-upstream-feeds.md](../specs/2026-06-26-on-device-upstream-feeds.md).
> Phases 1–3 are written (reference doc, the three distro-module
> `upstream-feeds` units + scripts, `dev-image` inclusion, and the `image.star`
> `copy:` cleanup); static checks pass (shellcheck, Starlark parse, `go build`,
> `prettier`). **The three distro-module units live in external modules and must
> be committed and pushed upstream before any build** — `yoe build` does
> `git fetch && git checkout FETCH_HEAD` and discards the un-pushed cache edits.
> Phase 4 (boottest) and the live networked install (Phase 5 verify) remain.

## Problem

A booted yoe device can install only from its project repo. To grab an
upstream-only package (`htop`, `strace`, a stray library) during development,
the loop today is add-a-unit → rebuild → republish → upgrade. The upstream
package already exists, prebuilt, for the exact branch/arch the image targets.
We want a one-command path to it that cannot disturb the source-built base and
that is trivially excluded from production.

## Design summary

A **self-contained `upstream-feeds` companion unit in each distro module**, no
shared class, no executor or image-assembly changes:

- **Per-distro units** in `module-alpine`, `module-debian`, `module-ubuntu` —
  each a plain `unit()` whose build task `install_file`s a committed enable
  script to `/usr/sbin/yoe-enable-upstream-feeds` and its key copy to a
  **non-trust** path (`/usr/share/yoe/upstream-keys/`), both **dormant**. This
  is the modules' existing `units/*-enable.star` companion pattern.
- Running the script activates the held-back feed and its key together (apk
  tag + key copy to `/etc/apk/keys/`; apt `signed-by` source + pin `100`);
  `--disable` reverses it. Excluding the unit removes everything — the
  production opt-out.
- A shared `module-core` class was considered and rejected as premature: only
  two distinct scripts, and yoe's install-step resolution (`templates.go`
  `resolveTemplatePath` rejects `..`) forbids a module-core file from shipping a
  distro module's keys — so a class couldn't own the unit anyway. The apt script
  duplicates across the two apt modules; acceptable, with feed-emission as the
  later dedup path if it becomes a problem.

Separately, a **pre-existing bug** the work surfaced: the device's
`/etc/apt/sources.list` carries a build-machine `copy:$REPO` line that is
invalid on-device and errors on every `apt update`. It is fixed in image
assembly (the only piece that can't live in the unit) and stands alone.

## Phase Overview

- **Phase 1** — Target-state reference doc.
- **Phase 2** — Per-distro `upstream-feeds` units + scripts; `dev-image`
  inclusion.
- **Phase 3** — Separate cleanup: strip the build-machine apt source.
- **Phase 4** — Tests.
- **Phase 5** — Verify; changelog.

## Phase 1: Target-state docs

### Task 1.1: New reference doc

Write `docs/on-device-upstream-feeds.md` in final (implemented) voice — no
`(planned)` flags. Cover: the one-unit model (script + dormant keys, run to
enable, exclude to keep out of production); the held-back idiom per backend
(`apk add foo@upstream` vs. `apt install -t <suite> foo`); branch-must-match;
`apk update` / `apt update` fetching every configured source once enabled
(cost + connectivity + the targeted-refresh escape hatch); the subpackage-split
/ missing-`-dev` failure mode as a known limit. Keep plan vocabulary out (no U/R
numbers, no commit hashes), per the docs-stand-alone rule.

### Task 1.2: Index + cross-links

Add the page to `docs/SUMMARY.md`. Add a cross-link from `docs/on-device-apk.md`
("for upstream-distro packages during development, see …"). The
`SPEC_PLAN_INDEX.md` row's Plan column already points here.

## Phase 2: Per-distro units + scripts + image inclusion

### Task 2.1: Companion units (distro modules — external, must be pushed)

Add `units/upstream-feeds.star` + a `units/upstream-feeds/` asset dir to each
distro module. The unit is a plain `unit()` (model on the existing
`units/*-enable.star` companions), **`distro = "<distro>"`** (required — all
three units share the name `upstream-feeds`; an untagged unit is distro-neutral
and the three would collide under module priority, resolving one variant into
every image, e.g. Ubuntu's `dpkg`-based script onto Alpine — the tag scopes each
to its distro, like the feed packages), `container = "toolchain"`,
`container_arch = "target"`, one `task("build", steps=[…])` of `install_file`
steps (no shell — `install_file` auto-creates parent dirs):

- `install_file("yoe-enable-upstream-feeds", "$DESTDIR/usr/sbin/yoe-enable-upstream-feeds", mode = 0o755)`
- one `install_file` per key into `$DESTDIR/usr/share/yoe/upstream-keys/`

Asset dir contents per module:

- **module-alpine** — `yoe-enable-upstream-feeds` (apk script) + copies of the
  two feed keys (`alpine-devel@…-6165ee59.rsa.pub`, `…-616ae350.rsa.pub`).
  Values: url `https://dl-cdn.alpinelinux.org/alpine`, branch `v3.21`, sections
  `main community`.
- **module-debian** — apt script + copy of `debian-archive-keyring.gpg`. Values:
  url `https://deb.debian.org/debian`, suite `trixie`, component `main`.
- **module-ubuntu** — apt script + copy of `ubuntu-archive-keyring.gpg`. Values:
  suite `resolute`, components `main universe`, and the **per-arch mirror
  split** — `http://archive.ubuntu.com/ubuntu` for amd64,
  `http://ports.ubuntu.com/ubuntu-ports` for arm64 (the script picks by
  `dpkg --print-architecture`).

The key file must live in the unit's asset dir (install steps can't escape it
with `..`), so each module keeps a copy of its feed key beside the unit. Values
are written into the script directly, with a comment to keep them in sync with
the module's `*_feed(...)` call (same module, same change). These are **external
modules** — edits land in the cached copies under
`testdata/e2e-project/cache/modules/…` and must be committed **and pushed**
upstream before any build that triggers a module sync (the sync discards
un-pushed edits).

### Task 2.2: Enable/disable script behavior

Each `yoe-enable-upstream-feeds` (POSIX sh), idempotent, default action =
enable, `--disable` = revert; runs nothing at install time:

- **apk** (module-alpine) — `enable`: append one held-back tagged line per
  section to `/etc/apk/repositories`, and copy each `*.rsa.pub` from
  `/usr/share/yoe/upstream-keys/` into `/etc/apk/keys/` (apk has no per-repo key
  scoping):
  ```
  @upstream https://dl-cdn.alpinelinux.org/alpine/v3.21/main
  @upstream https://dl-cdn.alpinelinux.org/alpine/v3.21/community
  ```
  Print: `apk update && apk add <pkg>@upstream`. `--disable`: remove the tagged
  lines and the copied keys.
- **apt** (module-debian / module-ubuntu) — `enable`: write
  `/etc/apt/sources.list.d/yoe-upstream.list` with the key scoped to that source
  via `signed-by` (no global trust anchor), and
  `/etc/apt/preferences.d/yoe-upstream.pref` pinning the mirror origin host to
  `Pin-Priority: 100`:
  ```
  deb [signed-by=/usr/share/yoe/upstream-keys/<keyring>.gpg] <url> <suite> main [universe]
  ```
  Print: `apt update && apt install -t <suite> <pkg>`. `--disable`: remove the
  source and the pin (the key in `/usr/share` stays, untrusted).

### Task 2.3: Include in `dev-image` (in-tree)

Add `upstream-feeds` to `dev-image.star`'s distro-neutral `artifacts` list
(alongside `curl`/`htop`/…) — each distro module ships its own `upstream-feeds`,
so the resolver picks the right one per image; no need to repeat it in each
`distro_artifacts` branch. Base/ssh images omit it (production opt-out).

## Phase 3: Separate cleanup — strip the build-machine apt source

> Independent of upstream feeds; could land as its own change. Included here
> because the same investigation surfaced it.

### Task 3.1: Rewrite the device `/etc/apt/sources.list` post-mmdebstrap

In `_assemble_debian_rootfs` (`modules/module-core/classes/image.star`, after
the dpkg "broken packages" gate), overwrite
`$DESTDIR/rootfs/etc/apt/sources.list` with a commented template mirroring
`base-files`' apk `repositories` file (explain that the project feed is added
via `yoe device repo add`, the upstream feed via `yoe-enable-upstream-feeds`).
This removes mmdebstrap's `deb [trusted=yes] copy:$REPO $SUITE main`, a
build-host path invalid on-device. The rewrite must happen here, not in a unit:
mmdebstrap writes the source after every package is installed, so a unit-shipped
`sources.list` would be clobbered.

No apk-side change — `apk add -X $REPO` never persists the local repo, so the
device's `/etc/apk/repositories` is already `base-files`' template.

### Task 3.2: Confirm nothing depends on the old source

Grep the assembly path and first-boot units for any consumer of the baked
`copy:` source (there should be none — image-time installs go through
mmdebstrap's own `--include`, not the persisted sources.list). The boottest
asserts a clean `apt update`.

## Phase 4: Tests

### Task 4.1: Offline unit-output checks

Assert the built `upstream-feeds` package for each distro contains the script at
`/usr/sbin/yoe-enable-upstream-feeds` and the keys under
`/usr/share/yoe/upstream-keys/`, and that neither lands in a trust dir at
install time. A focused Starlark/loader test (or a destdir inspection in the e2e
harness) confirms the per-distro instantiation resolves with the right feed
values.

### Task 4.2: Assembly + behavior (boottest)

Extend the dev-image boottest matrix (alpine/debian/ubuntu, both arches):

- `apt update` exits 0 with no `copy-stat` / `copy:` error (Phase 3).
- `/usr/sbin/yoe-enable-upstream-feeds` exists and is executable; the keys are
  in `/usr/share/yoe/upstream-keys/` and **not** yet in `/etc/apk/keys/` /
  `/etc/apt/trusted.gpg.d/` (dormant).
- Running the script writes the expected held-back config and activates the key
  (apk: key copied + tagged line; apt: `signed-by` source + pin); idempotent on
  a second run; `--disable` reverts it (source/pin/copied key gone). Offline-
  checkable — assert the generated files; do not require a live upstream fetch.

A live `apk add foo@upstream` / `apt install -t <suite> foo` needs network and
is out of scope for CI; note it in the doc as a manual check.

## Phase 5: Verify and changelog

- `go test ./internal/... ./cmd/...` green on a branch rebased on `main`.
- Boottest matrix green; distro-module edits committed **and pushed** before any
  run that triggers a module sync.
- `CHANGELOG.md`: one user-facing `[Unreleased]` entry — `dev-image` devices can
  now reach the upstream distro feed for experimentation via
  `yoe-enable-upstream-feeds`, and `apt update` no longer errors on a stale
  build-time source.
- Re-read `docs/on-device-upstream-feeds.md` against the shipped behavior; close
  any gaps. Changed Markdown passes `prettier --check`.

## Verification

- A freshly built `dev-image` boots; `apt update` is clean; the enable script
  and the dormant keys are present but inactive (no upstream source, no trust
  anchor).
- Running the enable script then pulling a known upstream-only package by tag
  succeeds (manual, networked); a plain `apk upgrade` / `apt upgrade` leaves the
  source-built base untouched (held-back verified); `--disable` removes the
  source and trust.
- A base/ssh image (no `upstream-feeds`) carries neither the script nor the
  keys.
- The `copy:` source no longer appears in the device's `/etc/apt/sources.list`.
