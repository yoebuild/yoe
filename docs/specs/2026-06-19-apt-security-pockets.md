---
date: 2026-06-19
topic: apt-security-pockets
---

# apt security pockets: consuming `-security` / `-updates` alongside the base suite

## Summary

Teach `apt_feed(...)` to consume a Debian/Ubuntu release's **security and
updates pockets** (`trixie-security`, `resolute-updates`, …) in addition to the
base release, so a yoe image built against an apt feed picks up out-of-band
security fixes the same way a real Debian/Ubuntu box does. Pockets are merged
into the existing per-component catalog with **highest-version-wins** semantics;
they are not new modules and require no change on the consumer, image-assembly,
project-repo, or on-device side. The entire change lives at feed-refresh and
resolve time.

---

## Problem Frame

A stock Debian 13 (trixie) system has three apt sources, not one: the base
release (`trixie`, frozen except at point releases), `trixie-security`
(DSA-driven fixes on a dedicated archive,
`security.debian.org/debian-security`), and `trixie-updates` (non-security fixes
too urgent to wait for a point release, on the main mirror). apt pools all
enabled suites and installs the highest version of each package; the security
team crafts version strings (`…+deb13u1`) so the fixed package outsorts the base
one. Ubuntu is the same shape, except its security and updates pockets are
served from the _same_ hosts as the base (`archive.ubuntu.com` for amd64,
`ports.ubuntu.com` for arm64), not a separate security archive.

yoe's `apt_feed(...)` today consumes exactly one suite per distro. The base
`trixie` archive does not change between point releases, so a yoe image built
from a base-only feed misses every security fix published in the out-of-band
window — which can be ~2 months wide between point releases. `yoe update-feeds`
against the base suite only catches fixes once a point release (13.1, 13.2, …)
folds them back into base `trixie` and prunes them from `trixie-security`.

The current resolver actively forbids the obvious workaround. `SuiteForDistro`
(`internal/starlark/types.go`) enforces one suite per distro, on the reasoning
that the build toolchain pins a single release and glibc from a different
release cannot safely mix into the rootfs. That reasoning is correct for
_releases_ but overbroad for _pockets_: `trixie-security` is the same release as
`trixie`, just a different pocket of it, so there is no libc-mixing hazard. The
constraint conflates "one release per distro" (true, load-bearing) with "one
suite per distro" (too strong).

### Why this is cheaper than it looks

yoe does not point devices at Debian. Consumed `.deb`s are **republished into
the project's own content-addressed apt repo** (`internal/build/executor.go`
`PublishDeb`), which stamps a single suite and a single `main` component
regardless of where the upstream package came from. Image assembly
(`modules/module-core/classes/image.star`) runs
`mmdebstrap … "$SUITE" "deb [trusted=yes] copy:$REPO $SUITE main"` — it
bootstraps the rootfs **from the project repo**, never from a Debian mirror. The
on-device `sources.list` likewise points only at the project repo.

So the base/security/updates merge collapses entirely at resolve-and-republish
time. yoe picks the right (highest-version) package across the pockets, mirrors
that one `.deb` into its single-suite project pool, and the device and
image-assembly paths never learn that pockets exist. This is the central design
property: **the device side does not change.**

---

## Actors

- **Feed maintainer** — edits `module-debian` / `module-ubuntu` `MODULE.star` to
  declare pockets, and runs `yoe update-feeds` to refresh the checked-in
  `Packages` snapshots (including the security signing key on first use).
- **Project author** — unchanged. References `debian.main` / `ubuntu.universe`
  in `prefer_modules` exactly as today; pocket selection is automatic.
- **yoe resolver** — merges base + pocket catalogs per component and surfaces
  the highest-version entry, tagged with the originating pocket's mirror so the
  build downloads the `.deb` from the right host.
- **yoe build/republish** — unchanged below the feed: republishes the chosen
  `.deb` into the single-suite project repo.

---

## Key Flows

### Declaring a feed with pockets

A component feed gains an optional `pockets` list. Each pocket names a suite of
the **same release**, an optional mirror override (Debian security lives on a
different host; Ubuntu pockets do not), and its own checked-in index directory.

```python
# module-debian/MODULE.star
_DEBIAN_MIRROR   = "https://deb.debian.org/debian"
_DEBIAN_SECURITY = "https://security.debian.org/debian-security"
_DEBIAN_SUITE    = "trixie"

apt_feed(
    name      = "main",
    distro    = "debian",
    url       = _DEBIAN_MIRROR,
    suite     = _DEBIAN_SUITE,          # base release
    component = "main",
    arches    = ["amd64", "arm64"],
    index     = "feeds/main",
    keyring   = "keys/debian-archive-keyring.gpg",
    pockets   = [
        {"suite": "trixie-security", "url": _DEBIAN_SECURITY, "index": "feeds/main-security"},
        {"suite": "trixie-updates",                            "index": "feeds/main-updates"},
    ],
)
```

```python
# module-ubuntu/MODULE.star — pockets share the base's split mirrors
apt_feed(
    name      = "main",
    distro    = "ubuntu",
    url       = _UBUNTU_MIRROR,
    arch_urls = {"arm64": _UBUNTU_PORTS},
    suite     = "resolute",
    component = "main",
    arches    = ["amd64", "arm64"],
    index     = "feeds/main",
    keyring   = "keys/ubuntu-archive-keyring.gpg",
    pockets   = [
        {"suite": "resolute-security", "index": "feeds/main-security"},
        {"suite": "resolute-updates",  "index": "feeds/main-updates"},
    ],
)
```

A pocket with no `url`/`arch_urls` inherits the base feed's mirrors.

### Refreshing pockets

`yoe update-feeds` walks every `apt_feed` and, for each, fetches the base suite
plus each pocket. For a pocket it fetches
`dists/<pocket-suite>/<component>/binary-<arch>/Packages.gz` from the pocket's
mirror, verifies the pocket's `InRelease` signature, and writes
`<pocket-index>/<arch>/Packages`. The Debian security archive is signed by the
"Debian Security Archive Automatic Signing Key" — a different key than the main
archive but already present in `debian-archive-keyring.gpg`; its fingerprint is
added to `keys/allowed-fingerprints` once (the existing `--allow-key-update`
flow handles the first sighting). Ubuntu pockets are signed by the same archive
key as the base.

### Resolving a package

On `Lookup(name)`, the feed merges its base catalog with every pocket catalog
for the active arch and returns the entry with the highest `dpkg` version
(`internal/dpkg.CompareVersions`). The returned unit's `Source` URL is built
from the **winning entry's pocket mirror** (so a security-only fix downloads
from `security.debian.org`), and its `SHA256` is the winning entry's hash. The
provides table the resolver consults is rebuilt from the merged winners, so a
dependency satisfied only by a security build resolves correctly.

### Building and shipping

Unchanged. The chosen `.deb` is fetched (SHA256-verified), republished into the
project pool under the base release suite + `main`, and the rootfs is assembled
from the project repo. Neither `mmdebstrap`, the repo emitter, nor the device
`sources.list` is aware pockets were involved.

---

## Requirements

**Feed declaration**

- R1. `apt_feed(...)` accepts an optional `pockets` kwarg: a list of mappings,
  each with `suite` (required), `index` (required, in-tree dir holding
  `<arch>/Packages`), and optional `url` / `arch_urls` mirror overrides. Absent
  `pockets`, behavior is identical to today.
- R2. Every pocket suite must belong to the same release as the base `suite`:
  validated by codename prefix (`<base>` or `<base>-<pocket>`, e.g. base
  `trixie` admits `trixie-security`, `trixie-updates`, `trixie-backports`). A
  pocket whose codename does not share the base prefix is a load-time error that
  names both suites.
- R3. Declaring pockets does **not** change a feed's module identity. The
  synthetic module stays `<distro>.<component>` (e.g. `debian.main`); pockets
  are an internal catalog dimension, never separately referenceable in
  `prefer_modules`. Pocket selection is automatic and not consumer-visible.

**Resolution / merge**

- R4. A component feed's per-arch catalog is the union of its base index and all
  pocket indices, deduplicated by package name, keeping the entry with the
  highest `dpkg` version under Debian version ordering
  (epoch:upstream-revision). Ties (identical version present in two pockets)
  resolve deterministically to the base, then pockets in declaration order.
- R5. Each retained entry records the mirror (`url` + `arch_urls`) of the pocket
  it came from. `populateBuildFields` builds `Unit.Source` from that mirror, not
  from the base feed's mirror, so a package present only in `-security`
  downloads from the security archive.
- R6. The feed's provides table is rebuilt from the merged winning set, so
  Provides/virtual-package resolution and cross-package dependency resolution
  see the same versions that will actually be republished. The existing
  cross-feed sibling resolution (`multiFeedProviders`) is unaffected; pocket
  merge happens inside a single feed's primary table before siblings are
  consulted.

**Release-identity guard**

- R7. `SuiteForDistro` is redefined to return the **base release** suite for a
  distro and to enforce **one base release per distro** across component feeds
  (all of `debian.main`, `debian.contrib`, … must agree on the base `suite`).
  Pockets are not base suites and never trip this guard. The error message
  distinguishes "multiple releases" (still forbidden) from pocket suites
  (allowed).
- R8. The project repo emitter, image-assembly `$SUITE`, and on-device
  `sources.list` continue to use the single base release suite and the `main`
  component. No device-visible or image-visible change. (This is a constraint to
  preserve, not new behavior to add.)

**Refresh**

- R9. `yoe update-feeds` fetches and verifies the base suite plus every declared
  pocket for each `apt_feed`, writing `<pocket-index>/<arch>/Packages`
  atomically. Each pocket's `InRelease` is verified against the feed's keyring;
  a new signing key (e.g. the Debian security key) is admitted only through the
  existing fingerprint allow-list / `--allow-key-update` path.
- R10. `apt.PeekFeedDecls` records pockets on `FeedDecl` so `update-feeds` can
  run inside a module repo with no project loaded, matching today's base-feed
  flow.

---

## Acceptance Examples

1. **Out-of-band security fix.** `debian.main` declares a `trixie-security`
   pocket. `openssl` is `3.5.0-1` in base `trixie` and `3.5.0-1+deb13u1` in
   `trixie-security`. Resolving `openssl` yields version `3.5.0-1+deb13u1`, its
   `Source` points at `security.debian.org/debian-security/…`, and the
   republished project-repo `.deb` is the `+deb13u1` build.

2. **Base-only package.** A package present only in base `trixie` (no security
   update) resolves to its base version and downloads from `deb.debian.org`,
   exactly as today.

3. **Multiple components, each with a security pocket.** `debian.main` and
   `debian.contrib` both declare `trixie-security` pockets.
   `SuiteForDistro("debian")` returns `trixie` with no error; an image builds
   and assembles from the project repo under suite `trixie`, `main`.

4. **Refresh.** `yoe update-feeds` in `module-debian` writes
   `feeds/main/<arch>/Packages`, `feeds/main-security/<arch>/Packages`, and
   `feeds/main-updates/<arch>/Packages`, having verified three `InRelease`
   signatures (the security one against the security signing key).

5. **Release mismatch is rejected.** A pocket declared as
   `{"suite": "bookworm-security", …}` under a `trixie` base fails at load with
   an error naming both `trixie` and `bookworm-security`.

6. **Ubuntu split mirror.** `ubuntu.main` with a `resolute-security` pocket and
   no pocket `url` inherits `arch_urls = {"arm64": ports.ubuntu.com}`; an arm64
   security `.deb` downloads from `ports.ubuntu.com`, an amd64 one from
   `archive.ubuntu.com`.

---

## Success Criteria

- A yoe Debian image built between point releases contains the security-fixed
  version of a package that has an open DSA, verified by comparing the
  republished pool version against the base `trixie` version.
- No change to image size, boot, or on-device apt behavior versus a base-only
  build (the device repo is byte-for-byte the same shape, just possibly carrying
  newer package versions).
- `module-debian` and `module-ubuntu` declare `main` (and other components, as
  desired) with `-security` and `-updates` pockets; `yoe update-feeds` refreshes
  all of them in one run.

---

## Scope Boundaries

- **Still one release per distro.** This spec adds pockets _within_ a release,
  not multiple releases. Mixing `trixie` and `bookworm` for one distro remains
  forbidden (toolchain/libc hazard).
- **No device-side multi-suite.** The device continues to consume only the
  single-suite project repo. This spec deliberately does not add Debian suites
  to the on-device `sources.list`.
- **`-backports` is out of scope.** Backports are admissible under R2's prefix
  rule, but real apt pins backports below the base (priority 100) so they are
  not auto-installed; yoe's version-wins merge would instead always prefer a
  backport. Supporting backports correctly needs a per-pocket "do not
  auto-prefer" flag and an explicit opt-in at the consumer; that is a separate
  change. Until then, declaring a backports pocket is discouraged and not
  validated for correctness.
- **No new project-repo layout.** The project repo stays single-suite / `main`.

---

## Key Decisions

- **Pockets, not per-suite modules.** An earlier design sketch
  (`docs/specs/2026-05-25-module-debian.md`) proposed per-suite synthetic
  modules named `<distro>.<suite>.<component>` (e.g.
  `debian.bookworm-security.main`). That was not implemented, and it is the
  wrong model here: separate modules push pocket selection onto the consumer's
  `prefer_modules`, which breaks apt's automatic highest-version-wins behavior
  and forces every project to know which pocket a given fix landed in. Merging
  pockets inside one component feed keeps selection automatic and matches how a
  real system behaves.
- **Merge at resolve time, not on the device.** Because yoe republishes into its
  own single-suite repo and assembles from it, the merge has exactly one correct
  home: the feed. This keeps the repo emitter, `mmdebstrap`, and the device
  untouched, and means the property "build picks the security version" is
  testable without booting anything.
- **Release identity, not suite identity.** Reframing `SuiteForDistro`'s
  invariant from "one suite" to "one release, many pockets" is the minimal
  change that removes the false constraint while preserving the real one.
- **Reuse the existing signing trust path.** The Debian security key is already
  in `debian-archive-keyring.gpg`; no new keyring, just an allow-list entry on
  first sighting via the existing `--allow-key-update` flow.

---

## Dependencies / Assumptions

- `internal/dpkg.CompareVersions` implements Debian version ordering correctly
  (epoch, `~`, etc.); the merge relies on it.
- The Debian security archive serves the same
  `dists/<suite>/<component>/binary-<arch>/Packages` layout as the main archive
  (it does), so no new parser is needed.
- `debian-archive-keyring.gpg` / `ubuntu-archive-keyring.gpg` contain the
  signing keys for their respective security archives (they do).
- Checked-in pocket `Packages` snapshots are thin (security/updates carry only
  packages with fixes), so the added repo size is small relative to the base
  component index.

---

## Outstanding Questions

### Resolve before planning

- Pocket descriptor shape: a list of dict literals (as sketched) versus a typed
  `pocket(...)` helper builtin. Dicts avoid a new builtin and match yoe's
  explicit-config leaning; confirm before implementing.
- Index directory convention: `feeds/main-security/` (sibling dirs, as sketched)
  versus `feeds/main/security/` (nested under the component). Sibling dirs keep
  the existing `<index>/<arch>/Packages` shape unchanged per pocket.

### Deferred to planning

- Whether `update-feeds` should fetch pockets concurrently with the base.
- Whether to surface, in the TUI Modules tab, that a component carries pockets
  (informational only; identity is unchanged).
