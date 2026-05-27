---
date: 2026-05-25
topic: module-debian
---

# Debian backend: module-debian, glibc toolchain, and apt-on-target

## Summary

Add a Debian backend to yoe — `module-debian` with a `debian_feed()`
synthetic-module builtin, a `toolchain-glibc` build container, a `.deb` artifact
path for project-built units, a signed Debian-format project repo, and
apt-on-target served from that mirror — written so the internal dpkg/apt
machinery is general enough that `module-ubuntu` later is a thin add rather than
a parallel implementation.

---

## Problem Frame

A real customer project wants embedded images on a Debian base, and the broader
industry expectation in this segment skews Debian/Ubuntu rather than Alpine. The
customer requires that deployed devices install and upgrade packages via `apt`
from the project repo after deployment, not only at flash time — so
apt-on-target (offline-capable, project-key-signed mirror) is load-bearing, not
a nice-to-have.

yoe today is Alpine-anchored end to end: the only build container is
`toolchain-musl` on `alpine:3.21`, all classes assume musl headers and Alpine
`apk` packaging, the project repo is an APK repo signed with a single project
key, the device's package manager is `apk`, and `module-alpine` is the only
prebuilt-package source the resolver knows about.

The earlier roadmap thinking pointed at "virtual units" for Alpine package
wrapping, but `docs/specs/2026-05-13-feeds-as-modules.md` landed on synthetic
modules instead and explicitly names `debian_feed()` / `ubuntu_feed()` as future
work that should follow the same shape. The yoe-core half of feeds-as-modules
has now shipped (see `docs/SPEC_PLAN_INDEX.md` for the row, and v0.10.13 in
`CHANGELOG.md` for the user-facing summary): `alpine_feed()` is a registered
Starlark builtin, `yoe update-feeds` is a working subcommand, synthetic modules
participate in resolver priority and `prefer_modules`, the TUI's Modules tab
groups feeds under their parent, and the supporting Go packages
(`internal/apkindex/`, `internal/feeds/alpine/`, `internal/starlark/synthetic_module.go`)
are in place. What remains on the Alpine side is the big-bang `module-alpine`
cutover (U13) replacing the generated per-package `.star` files with two
`alpine_feed()` calls plus `*-enable.star` companions. **That landed
infrastructure is the foundation `module-debian` builds on** — the synthetic-
module type, the loader hooks, the resolver-priority machinery, the closure
walk's lazy materialization, the `yoe update-feeds` command surface, and the
TUI feed-display layer all work format-agnostically and do not need to be
rebuilt for Debian.
`docs/specs/2026-05-18-mirror-alpine-keep-keys.md` separately moved Alpine's
trust model to "mirror verbatim, keep upstream signatures" — that posture
extends to Debian, but Debian's `.deb` files are not GPG-signed per-package
(unlike Alpine's `.apk`s), so the trust chain on the device runs entirely
through the project-key-signed `InRelease` and per-package SHA256 in `Packages`.
`docs/specs/2026-04-04-container-units.md` calls out `toolchain-glibc-gcc` as a
future variant.

The cost of staying Alpine-only is concrete: the customer can't ship on yoe, and
every adjacent Debian-or-Ubuntu opportunity is closed by the same single
decision rather than by N separate ones. The cost of getting the abstraction
wrong is also concrete: a Debian-shaped implementation that bakes in too much
Debian-specific framing makes `module-ubuntu` a second N-month project instead
of a small one.

---

## Actors

- A1. Module maintainer: writes and maintains `module-debian` (mirrors
  `module-alpine`'s role). Runs `yoe update-feeds` to refresh in-tree `Packages`
  files; reviews diffs; commits and pushes the module repo.
- A2. Project user: writes `PROJECT.star`, picks which modules to include,
  declares `distro` on each image, occasionally writes a shadow `.star` to
  override one feed entry.
- A3. Yoe resolver: walks the module list, ingests Starlark units and
  `debian_feed`-synthesized units uniformly, applies `prefer_modules` overrides.
- A4. Yoe image assembler: extracts mirrored debs and project-built debs into
  the rootfs, runs `dpkg --configure -a` under binfmt to execute maintainer
  scripts and let `deb-systemd-helper` handle service enablement per Debian
  preset machinery, before generating the disk image.
- A5. Yoe packager: produces `.deb` artifacts from project-built units' destdirs
  when those units are consumed by a Debian image; emits a signed Debian-format
  repo (per-suite, per-component, per-arch index plus signed `InRelease`
  carrying `Date` and `Valid-Until`).
- A6. apt-tools on the target: trusts the project key (scoped via `signed-by=`
  to the project repo), fetches and verifies `InRelease`, validates per-package
  SHA256 from `Packages`, installs without `--allow-unauthenticated`.

---

## Key Flows

- F1. Project resolves units for a Debian image
  - **Trigger:** `yoe build` runs against a project whose target image declares
    `distro = "debian"`.
  - **Actors:** A2, A3.
  - **Steps:** Project's `modules` list is walked. `debian_feed(...)` entries
    materialize one synthetic module per declared `(suite, component, arch)`
    triple, contributing one unit per `Packages` entry. The flattened module
    list is fed to the resolver, which applies `prefer_modules`, errors on
    unresolved conflicts. Image, class, and unit selection use `distro` to pick
    the glibc toolchain and the deb packaging path.
  - **Outcome:** A single unit graph keyed by name, every unit traceable to its
    source module; the build container the resolver hands to each unit is
    `toolchain-glibc`.
  - **Covered by:** R1, R2, R3, R7, R8.

- F2. Module maintainer refreshes the Debian feed
  - **Trigger:** New Debian point release, security update, or routine refresh.
  - **Actors:** A1.
  - **Steps:** Maintainer runs `yoe update-feeds` inside `module-debian`. Yoe
    fetches `InRelease` and the referenced `Packages` files per declared
    `(suite, component, arch)`, verifies `InRelease` against the trusted Debian
    release keyring committed in-module, rejects expired or
    `Valid-Until`-missing indices, writes the decompressed `Packages` files into
    the module's in-tree feed directory, leaves the diff staged. Maintainer
    reviews the diff, commits, and pushes the module repo.
  - **Outcome:** Module's in-tree `Packages` snapshot matches upstream as of the
    fetch moment; consumers pick up the change on next module sync.
  - **Covered by:** R10, R11, R24.

- F3. Image assembly stages debs, runs maintainer scripts, and bakes a Debian
  rootfs
  - **Trigger:** Build of a `distro = "debian"` image after dependency
    resolution.
  - **Actors:** A4.
  - **Steps:** Image assembler extracts each resolved deb (mirrored +
    project-built) into the staging rootfs. Runs `dpkg --configure -a` inside a
    binfmt container so every package's maintainer scripts execute against a
    fully-populated `/var/lib/dpkg/` state and `deb-systemd-helper` invocations
    honor the Debian preset machinery (`/lib/systemd/system-preset/*.preset`).
    Project-built units' `services = [...]` declarations are baked into their
    `.deb` at package time so the same configure pass enables them. Stages the
    project keyring into `/etc/apt/keyrings/` and writes a `signed-by=`-scoped
    sources.list entry pointing at the project repo over HTTPS.
  - **Outcome:** Bootable Debian rootfs with services enabled per Debian
    convention; apt works offline against the project repo with the project key
    scoped to that source.
  - **Covered by:** R12, R13, R14, R17, R18, R19, R20, R26.

- F4. Project-built unit ships as a deb in the project Debian repo
  - **Trigger:** A project-built unit lands in a `distro = "debian"` image.
  - **Actors:** A5.
  - **Steps:** Yoe packages the unit's destdir as a `.deb` with a control file
    derived from existing unit fields (name, version, runtime deps,
    description). Index pass emits per-arch `Packages` plus a top-level
    `Release` and `InRelease` signed with the project key. `InRelease` carries
    `Date` and `Valid-Until` for replay protection. Mirrored upstream debs are
    copied byte-for-byte; their hashes in the project's `Packages` match
    upstream.
  - **Outcome:** One signed Debian repo on the project host; both project-built
    and mirrored debs share the same `dists/<suite>/` tree.
  - **Covered by:** R12, R13, R15, R16, R24.

- F5. Target installs or upgrades a package from the project repo
  - **Trigger:** `apt-get install` or `apt-get upgrade` on a device pointed only
    at the project repo.
  - **Actors:** A6.
  - **Steps:** apt fetches `InRelease`, verifies its signature against the
    `signed-by=`-scoped project key in `/etc/apt/keyrings/`, rejects expired
    indices via `Valid-Until`. Resolves the target package to its entry in
    `Packages`, fetches the `.deb`, validates the SHA256 listed in `Packages`
    against the downloaded bytes. Installs and runs the package's maintainer
    scripts.
  - **Outcome:** Online or offline install/upgrade from the project's single
    host; trust roots entirely to the project key plus the SHA256 chain.
  - **Covered by:** R15, R16, R20, R24, R26.

---

## Requirements

**Module surface**

- R1. New built-in `debian_feed(...)` is available in `MODULE.star` and
  `PROJECT.star`. Its parameter shape parallels `alpine_feed(...)` but uses
  Debian terminology: feed name, base URL, suite, components, arches, signing
  keys, and in-tree index directory.
- R2. Each `debian_feed(...)` call materializes one synthetic module per
  `(suite, component)` tuple, named `<distro>.<suite>.<component>` (e.g.,
  `debian.bookworm.main`, `debian.bookworm.contrib`,
  `debian.bookworm-security.main`). A project that needs `bookworm-security` or
  `bookworm-updates` declares one `debian_feed` call per suite explicitly; the
  synthetic-module names do not encode snapshot timestamps — reproducibility
  comes entirely from the module repo's git SHA. Alpine's parallel decision is
  `<distro>.<component>` (e.g., `alpine.main`) — Debian inherits the dot-style
  separator with a suite axis inserted.
- R3. Synthetic modules from `debian_feed` integrate with the existing
  module-priority machinery: they rank below non-feed modules by default;
  `prefer_modules` is the targeted override.
- R4. A new module repo `module-debian` is established as a **separate git
  repository** parallel to `units-alpine`/`module-alpine` (sibling-repo pattern,
  not in-tree under `yoe/modules/`). It contains classes for Debian-flavored
  builds (where needed beyond what the generic classes already provide),
  checked-in `Packages` snapshots, the Debian release keyring used to verify
  upstream `InRelease`, and machine fragments for any Debian-default behavior
  that diverges from Alpine. A skeletal directory has been created at
  `/scratch4/yoe/module-debian/` (un-initialized) as a holding place; the first
  commit creating the actual git repo is part of v1 work, not this spec.
- R5. `module-debian`'s structure does not depend on Debian-specific code paths
  in yoe core. The same internal dpkg/apt machinery serves a future
  `module-ubuntu` without modification (within the carveouts named in Scope
  Boundaries).

**Internal dpkg/apt machinery (format-named, distro-agnostic)**

- R6. yoe gains internal support for parsing Debian package indices
  (`Packages`), parsing dpkg dependency syntax (including version constraints,
  virtual packages, and alternatives), extracting `.deb` files, building `.deb`
  files from a staged destdir + control metadata, and emitting and GPG-signing a
  Debian-format repo (per-arch `Packages` plus suite-level
  `Release`/`InRelease`). This machinery is named and located by format (`dpkg`,
  `deb`), not by distro, so future Debian-family backends reuse it as a library.
  The Alpine-side parallel has already landed and is the canonical reference for
  shape and depth: `internal/apkindex/` exposes `parse.go` (text-format parser),
  `deps.go` (dep tokens), `provides.go` (provides/virtuals table), `verify.go`
  (pure-Go signature verification, never touching the host keyring), and
  `materialize.go` (turn an index entry into a `*Unit` on first resolver
  reference). `internal/feeds/alpine/` is the consumer layer:
  `builtin.go` registers the Starlark builtin, `tarstream.go`/`peek.go` handle
  fetch and decompress, `update.go` drives `yoe update-feeds`, and
  `crossfeed_test.go` exercises the cross-feed providers wiring. Debian mirrors
  that layout: `internal/dpkg/` for the format-level parser, dep syntax, and
  `.deb` extract/build; `internal/feeds/debian/` for the `debian_feed` builtin,
  `Packages` fetch, `InRelease` verification, and the `yoe update-feeds`
  dispatch. `internal/repo/index.go`'s APK emitter gains a sibling Debian
  emitter rather than a rewrite.
- R7. The apt dependency resolver handles `Depends`, `Pre-Depends`, virtual
  `Provides`, `Conflicts`, `Replaces`, and the alternative-bar (`|`) syntax.
  `Recommends` and `Suggests` are not auto-installed at image build time
  (matches apt's default `--no-install-recommends` posture for predictable
  images); the decision is reversible per-image if needed.

**Toolchain split**

- R8. A new container unit `toolchain-glibc` is added alongside
  `toolchain-musl`, based on a pinned Debian release. It carries the equivalent
  native toolchain (gcc, binutils, headers) sourced from Debian rather than
  Alpine, plus the same yoe-side build helpers (`bwrap`, `dpkg`, `mkfs` tooling,
  etc.) that `toolchain-musl` ships. It also includes the binfmt machinery
  needed to run `dpkg --configure -a` against a foreign-arch staging rootfs (see
  R18).
- R9. Build classes (`autotools`, `cmake`, `go`, language-specific helpers) pick
  their toolchain from the consuming image's `distro`: `distro = "alpine"` →
  `toolchain-musl`, `distro = "debian"` → `toolchain-glibc`. The libc family is
  derived from `distro` in v1; no separate field on the image.

**Module index storage and refresh**

- R10. Each `module-debian` repo that declares a `debian_feed` checks the
  upstream `Packages` content (decompressed) into the module's git, one file per
  `(suite, component, arch)`. The module's checked-in ref pins the snapshot.
- R11. `yoe update-feeds` (run inside a module that declares Debian feeds)
  fetches `InRelease` for each declared suite, verifies the signature against
  the module's trusted release keyring committed in-tree (see R25), rejects
  `InRelease` whose `Valid-Until` is absent or expired (see R24), fetches and
  decompresses the referenced `Packages` files, and rewrites the in-tree files.
  The command writes changes but does not commit or push. The subcommand
  already exists for Alpine (`cmd/yoe/main.go` dispatches `case "update-feeds"`
  to `internal/feeds/alpine.UpdateFeeds`); the Debian work adds a sibling
  dispatch into `internal/feeds/debian.UpdateFeeds` keyed off which feed
  builtins the loaded module declared. The maintainer-facing flag surface
  (`--arch`, `--module-dir`) and the "write but do not stage/commit" contract
  carry over unchanged.

**Project-built debs and project repo**

- R12. Project-built units consumed by a `distro = "debian"` image are packaged
  as `.deb` by yoe's packager. Control metadata (`Package`, `Version`,
  `Architecture`, `Depends`, `Description`, `Maintainer`) is derived from
  existing unit fields; no new Starlark fields are required for the common case.
  Units carrying `services = [...]` declarations bake the corresponding systemd
  preset/wants symlinks into the `.deb` at package time, parallel to Alpine's
  handling — image-layer service-enablement scans are not used.
- R13. yoe emits a Debian-format project repo at the same conceptual location as
  today's APK repo (per-arch `Packages` under
  `dists/<suite>/<component>/binary-<arch>/`, a suite-level `Release` plus
  `InRelease` signed with the project key).
- R14. The project repo can host both APK and Debian content (one project,
  multiple image distros). Layout splits at the distro level inside the project
  repo directory: `repo/<project>/alpine/` keeps today's APK layout
  (`keys/`, `noarch/`, `<arch>/APKINDEX.tar.gz`) and `repo/<project>/debian/`
  carries the Debian-format tree (`dists/<suite>/<component>/binary-<arch>/`,
  suite-level `Release`/`InRelease`, `pool/<component>/<initial>/<pkg>/`, the
  project keyring next to it). A device's `sources.list` points only at the
  Debian subtree; an Alpine `/etc/apk/repositories` points only at the Alpine
  subtree. Migrating the existing `repo/<project>/<arch>/` layout to the
  `repo/<project>/alpine/<arch>/` layout is a one-time, in-place move; pre-1.0
  rules apply (no compat shim, no dual-path support).
- R14a. The build directory splits the same way at the same level:
  `build/<distro>/<unit>.<scope>/` (e.g. `build/alpine/openssl.x86_64/`,
  `build/debian/openssl.x86_64/`). `internal/build/sandbox.go`'s
  `UnitBuildDir(projectDir, scopeDir, unitName)` grows a `distro` parameter and
  emits the per-distro prefix; every caller (executor, sysroot stage,
  meta read/write, dry-run printer, TUI build view, clean) threads the value
  through. Existing on-disk `build/<unit>.<scope>/` directories are not
  preserved across the rename; the next `yoe build` after the migration
  rebuilds units the first time they're referenced under each distro.

  Why a directory-level split instead of a suffix on the unit name:
  - Composes consistently with R14's `repo/<project>/<distro>/...` shape.
  - Mirrors how `cache/modules/<module>/` already isolates per-source state.
  - Allows surgical reset (`rm -rf build/debian/`) without globbing into
    unrelated suffixes future scope dimensions might pick up.
  - Extends to `build/ubuntu/` (and any future distro) with no rename day.

  Untagged units (no `distro` field per R21a — the common case, e.g.
  `openssl`, `zlib`, `curl`) are visible to every distro's closure walk; an
  untagged unit reached from both an alpine and a debian image in the same
  project simply gets scheduled and built twice — once into
  `build/alpine/<unit>.<scope>/`, once into `build/debian/<unit>.<scope>/` —
  because the toolchain (and therefore the hash) differs. The build-dir
  split does not try to deduplicate those; the cost is a per-distro rebuild
  of those units, paid once per cache lifetime. Tagged units (R21a) are
  invisible to the wrong distro's closure and so only ever land in their
  matching subtree. Genuinely libc-neutral units (a `task`-class unit whose
  only work is `install_file` of a config and that emits a `noarch` apk/deb)
  still pick a container at build time and therefore still materialize under
  exactly one distro tree per closure that reaches them — same shape as
  every other untagged unit.

**Mirrored deb trust and runtime apt trust model**

- R15. Mirrored upstream debs are copied byte-for-byte into the project repo (no
  re-archive, no re-sign). Their hashes in the project's `Packages` match
  upstream byte-for-byte.
- R16. The project's per-suite `Release`/`InRelease` is signed with the project
  key. The project-key-signed `InRelease` plus the per-package SHA256 in
  `Packages` is the **sole** runtime trust chain on the device for installed
  packages — apt does not consult per-`.deb` signatures (Debian `.deb`s are not
  per-package GPG-signed at all). The Debian release keyring on the device
  exists only for `yoe update-feeds`-style refresh operations (when those happen
  on-device at all), not for package install verification.
- R17. The image's rootfs ships the project signing key in
  `/etc/apt/keyrings/<project>.gpg`, referenced from the `signed-by=` field of
  the project's sources.list entry. The key is scoped to the project repo source
  only; it does not validate any other apt source the operator might add later.
  Debian release keys, when shipped on-device, similarly land in
  `/etc/apt/keyrings/` (not `/etc/apt/trusted.gpg.d/`) and are referenced only
  from the contexts that need them (module-refresh tooling, not apt's runtime
  install path).

**Image assembly**

- R18. Image assembly extracts staged debs into the rootfs and then runs
  `dpkg --configure -a` inside a binfmt container so every package's maintainer
  scripts execute at image-build time against a fully-populated `/var/lib/dpkg/`
  state. This replaces R18's earlier "scripts never run at build" stance:
  deferring postinsts to first boot left the dpkg status DB in `unpacked` state,
  breaking `Pre-Depends`, `dpkg-trigger`, `update-alternatives`, and any
  postinst that assumes a configured rootfs. Running `dpkg --configure -a` at
  build time gives images that boot into a fully-configured Debian state with no
  first-boot reconciliation pass.
- R19. Service enablement on Debian images is handled by `dpkg --configure -a`
  (R18). Each package's postinst invokes `deb-systemd-helper`, which honors
  `/lib/systemd/system-preset/*.preset` — services Debian's preset deliberately
  disables stay disabled, services enabled by the preset are enabled,
  debconf-gated services follow their answers. yoe's image assembler does NOT
  perform an independent scan-and-symlink pass over the staged rootfs; the
  policy lives in the packages themselves, matching the "units declare their own
  services" rule.
- R20. The image ships a `sources.list.d/<project>.sources` (deb822-style) or
  `sources.list` entry pointing at the project repo URL, scoped to the project's
  suite, with `signed-by=` referencing the project keyring at
  `/etc/apt/keyrings/<project>.gpg`. The URL must be HTTPS (see R26); yoe errors
  at image-build time if the project's configured repo URL uses plain HTTP.

**Distro selector and propagation**

- R20a. `PROJECT.star` MAY declare `default_distro = "alpine" | "debian"` at
  the project level. This is the fallback used by any image that does not
  declare `distro` itself. Today's all-Alpine projects set
  `default_distro = "alpine"` once in PROJECT.star and no image-level
  declarations need touching; mixed-distro projects override per image.
  `default_distro` is explicit at the project level (not a hidden default in
  yoe core), preserving the "explicit over implicit" CLAUDE.md rule.
  `internal/init.go`'s template grows the field; `testdata/e2e-project/
  PROJECT.star` adopts it in the same commit.
- R21. Each image's **effective distro** is resolved as: image's own `distro`
  field if set; else the project's `default_distro`; else an evaluation error
  ("image X has no distro and project has no default_distro"). The effective
  distro is what flows through R9 (toolchain selection), R12/R13 (packaging
  format and repo subtree), R14/R14a (on-disk split), and the closure walk
  (R21a's visibility filter). The libc family is derived from the effective
  distro (alpine → musl, debian → glibc); no separate `libc` field is
  required in v1. When a future hybrid case (a glibc-built unit shimmed into
  an Alpine rootfs via gcompat, or similar) materializes, a `libc` override
  field can be added then — the v1 design does not foreclose it.
- R21a. Any non-image unit MAY declare `distro = "alpine" | "debian"`. On a
  non-image unit, `distro` is a **compatibility tag** — it does not drive the
  toolchain, the build steps, the packaging format, or the build-output
  location (all of those still come from the consuming image's effective
  distro). It drives one thing only: closure-walk visibility. During the
  closure walk for an image of effective distro X, a unit is visible iff its
  `distro` field is unset OR equals X. A unit tagged for the wrong distro is
  invisible — exactly as if it did not ship — so a dependency reference to
  it from the wrong distro's closure fails with the normal "unit not found"
  error. Untagged units are visible to all distros and build once per
  distro that reaches them (per R14a's split). Feed-materialized units
  auto-inherit their feed's distro: `alpine_feed` lookups tag units with
  `distro = "alpine"`, `debian_feed` lookups tag with `distro = "debian"`, no
  Starlark author writes this manually. The `distro` field does NOT enter
  `internal/resolve/hash.go`'s hash key — it affects visibility only, not
  build output — so adding the field to existing units with the field unset
  is cache-neutral. Collision rules: a tagged and an untagged unit of the
  same name both visible to a distro X resolve via the existing tagged-wins-
  inside-its-distro rule (the more specific declaration wins); two tagged
  units of the same name for the same distro fall back to the existing
  `prefer_modules` machinery.
- R22. Foreign-arch builds for Debian use arch-tagged variants of
  `toolchain-glibc` running under QEMU user-mode, the same model
  `toolchain-musl` uses today. No cross-compile toolchain is introduced.

**Documentation**

- R23. `docs/` gains a `module-debian.md` companion to `docs/module-alpine.md`,
  the `distro` image field plus `default_distro` project field (R20a) and the
  per-unit compatibility-tag `distro` field (R21a) are documented next to the
  existing image-class docs (with the driver-vs-tag distinction called out
  explicitly), and `docs/apk-passthrough.md` either gains a Debian sibling or
  grows a Debian section covering the parallel deb path. The `CHANGELOG.md`
  entry leads with the user-visible win (build Debian images, ship signed
  Debian repos, apt-on-target). `docs/SPEC_PLAN_INDEX.md` gets a row for this
  spec in the same commit as the spec lands. `internal/init.go`'s template
  and `testdata/e2e-project/PROJECT.star` both adopt `default_distro` in the
  same commit that ships the field, per the "yoe init mirrors the
  e2e-project template" CLAUDE.md rule.

**Replay protection and index validity**

- R24. The project-emitted `InRelease` carries both `Date` and `Valid-Until`
  fields. The staleness window between them is a project-configurable value with
  a sensible default (e.g., 30 days, suitable for typical fleet update
  cadences). `yoe update-feeds` rejects any upstream `InRelease` where
  `Valid-Until` is absent or has passed, logging both field values and refusing
  to rewrite in-tree `Packages` until the operator confirms or the upstream feed
  publishes a fresh index. Target-side `Valid-Until` enforcement by apt is
  standard apt behavior and is documented as a Dependencies/Assumptions item —
  yoe relies on it rather than reimplementing it.

**Bootstrap trust anchor**

- R25. `module-debian` ships an initial Debian release keyring committed
  directly to the module repo (not fetched via the feed it bootstraps), with
  each key's fingerprint documented in `module-debian`'s README and verified
  against `https://ftp-master.debian.org/keys.html` or the distro keyserver
  out-of-band by the module maintainer at first setup. Subsequent
  `yoe update-feeds` runs refuse to replace the in-tree keyring unless every new
  key's fingerprint appears on a pinned allow-list maintained inside the module
  repo. This breaks the chicken-and-egg cycle in which the keyring would
  otherwise be fetched via the feed it must verify.

**Network transport**

- R26. The project repo URL baked into the image's sources.list entry MUST be
  HTTPS. yoe issues an error at image-build time if a project configures an HTTP
  project repo URL. TLS certificate trust on the device is operator-managed
  (root CA store, certificate pinning, or `verify-peer=no` for closed networks
  at operator discretion) and is orthogonal to GPG trust on the repo metadata.

---

## Acceptance Examples

- AE1. **Covers R1, R2, R3.** Given `module-debian` declares
  `debian_feed("bookworm-main", ...)` and a project includes `module-debian`
  plus a hand-written `module-core` with a source-built `openssl`, when the
  resolver runs against an image that lists `openssl` in artifacts, the resolver
  picks `module-core`'s source-built openssl (non-feed beats feed by default, no
  `prefer_modules` entry needed).

- AE2. **Covers R3.** Given the same setup, when `PROJECT.star` declares
  `prefer_modules = {"openssl": "debian.bookworm.main"}`, the resolver picks the
  Debian prebuilt instead.

- AE3. **Covers R2.** Given a project that needs security updates,
  `module-debian` declares three separate `debian_feed` calls —
  `debian.bookworm.main`, `debian.bookworm-security.main`,
  `debian.bookworm-updates.main` — and the project lists those module names in
  `prefer_modules` for the packages where it wants the security/updates variant.
  The synthetic-module name does not include the snapshot date; pinning the
  module repo's git SHA pins the snapshot.

- AE4. **Covers R7.** Given a `Packages` entry whose `Depends` line includes
  `libssl3 (>= 3.0.0) | libssl1.1`, when the resolver walks the dep, it consults
  the provides table built from the same feed, picks `libssl3` if available at
  the requested version, falls back to `libssl1.1` only if `libssl3` is not
  present in the resolved module set.

- AE5. **Covers R8, R21.** Given an image declares `distro = "debian"` and
  includes a unit built via the `autotools` class, when yoe schedules that
  unit's build, it runs in `toolchain-glibc` (not `toolchain-musl`). No `libc`
  field is required on the image.

- AE5a. **Covers R20a, R21.** Given a project declares
  `default_distro = "alpine"` in PROJECT.star and lists three images, two
  with no `distro` field and one declaring `distro = "debian"`, when yoe
  evaluates the project the first two images resolve their effective distro
  to `"alpine"` and the third to `"debian"`. No image-level field is required
  on the alpine images; the debian image's per-image override wins over the
  project default. Removing `default_distro` from PROJECT.star and the two
  images' implicit reliance on it surfaces an evaluation error pointing at
  the offending images, not a silent fall-through.

- AE5b. **Covers R14a, R21a.** Given a project that builds both an alpine and
  a debian image, both pulling in an untagged `openssl` unit and the
  alpine-tagged `apk-tools` unit, when yoe schedules builds: `openssl`
  appears twice in the schedule — once as
  `build/alpine/openssl.x86_64/` (toolchain-musl, hash includes alpine
  container), once as `build/debian/openssl.x86_64/` (toolchain-glibc, hash
  includes debian container); `apk-tools` appears once, only under
  `build/alpine/`, because the debian image's closure walk filtered it out as
  invisible. A reference to `apk-tools` from the debian image's units fails
  with "unit not found `apk-tools`", same shape as any other missing-unit
  error.

- AE6. **Covers R12, R13, R16.** Given a project-built unit `foo` ships in a
  Debian image, when the publish step runs, the project repo gains
  `dists/<suite>/main/binary-<arch>/Packages` listing `foo` with the
  project-key-signed `InRelease` carrying `Valid-Until`. Mirrored Debian
  packages in the same `Packages` carry SHA256 values matching upstream
  byte-for-byte; apt verifies the project's `InRelease` signature against the
  keyring referenced by `signed-by=` in sources.list and validates each `.deb`
  by its `Packages`-listed SHA256.

- AE7. **Covers R17, R20, R24, R26.** Given a Debian image is flashed and booted
  with no network route to debian.org, when an operator runs
  `apt-get install <mirrored-pkg>` on the device, apt fetches `InRelease` from
  the project repo URL over HTTPS, verifies its signature against the project
  key at `/etc/apt/keyrings/<project>.gpg` (scoped via `signed-by=` to the
  project source only), enforces `Valid-Until`, validates the deb's SHA256 from
  `Packages`, and installs without `--allow-unauthenticated`. No Debian release
  key is consulted in the runtime install path.

- AE8. **Covers R18, R19.** Given a Debian image installs `openssh-server` and
  `chrony`, when image assembly runs `dpkg --configure -a` under binfmt,
  `openssh-server`'s postinst generates host keys and `deb-systemd-helper`
  enables `ssh.service`; `chrony`'s preset is `disable` so its postinst leaves
  `chronyd.service` disabled. The booted image has sshd starting at boot and
  chronyd available but not auto-started, matching Debian convention exactly.

- AE9. **Covers Success Criterion — upgrade flow.** Given a device installed at
  project-repo version N, when the project repo is updated to version N+1
  carrying a newer mirrored Debian deb (with maintainer scripts) and a newer
  project-built deb, and the operator runs `apt-get upgrade`, apt fetches the
  new `InRelease`, verifies and enforces `Valid-Until`, downloads both debs,
  validates each by SHA256, runs each package's `preinst`/`postinst`, restarts
  services as the maintainer scripts dictate, and completes without
  `--allow-unauthenticated`.

- AE10. **Covers R11, R24, R25.** Given the module maintainer runs
  `yoe update-feeds` inside `module-debian`, yoe verifies upstream `InRelease`
  against the in-tree Debian release keyring, rejects the run if `Valid-Until`
  is missing or expired, refuses to replace the in-tree keyring unless any new
  keys match the pinned allow-list fingerprints, then rewrites `Packages` files
  and leaves them staged. The maintainer reviews the diff and pushes via normal
  git workflow.

---

## Success Criteria

- A project building a Debian image lands one bootable disk image whose rootfs
  is composed entirely of debs (mirrored + project-built), with
  `dpkg --configure -a` having already run against the staging rootfs at image
  build, so every service is enabled or disabled per Debian convention at first
  boot.
- A device pointed only at the project repo URL installs both project-built and
  upstream Debian packages offline, without `--allow-unauthenticated` and
  without a route to debian.org. Trust roots through the `signed-by=`-scoped
  project key plus per-package SHA256 in `Packages`.
- A device installed at project-repo version N **upgrades** cleanly to N+1 via
  `apt-get upgrade`, including maintainer-script execution and service restarts,
  without `--allow-unauthenticated`. (The upgrade path is the load-bearing
  reason the customer asks for apt-on-target rather than a base tarball.)
- Adding a new Debian package to a project requires zero file edits in
  `module-debian` — listing the package name in an image's `artifacts` is
  enough, provided the package exists in the pinned Debian snapshot.
- A source-built unit can target either `distro = "alpine"` or
  `distro = "debian"` without changes to its `.star` file; the class chooses the
  right toolchain.
- A future `module-ubuntu` PR reuses `internal/dpkg`, `internal/deb`, and
  `internal/repo`'s Debian emitter unchanged (within the carveouts named in
  Scope Boundaries); the new code is the `ubuntu_feed()` builtin, a new
  `module-ubuntu` repo, and the keyring/policy wiring named in those carveouts.
- A downstream agent or implementer reading this document can plan
  implementation without inventing product behavior, scope boundaries, or trust
  posture.

---

## Scope Boundaries

- `module-ubuntu` and `ubuntu_feed()` — design accommodates them; implementation
  is a follow-on. **Ubuntu carveouts** (concrete things NOT covered by the "thin
  add" claim and that will require new code or new module data when
  `module-ubuntu` lands):
  - `restricted` and `multiverse` components carry licensing flags Debian
    doesn't have; mirror policy and user-facing license signaling may need
    extension.
  - Ubuntu's per-release rotating keyring with `ubuntu-keyring`'s
    auto-derivation behavior — not a one-shot keyring snapshot like Debian's.
  - `Phased-Update-Percentage:` in `Packages` — apt honors phased updates; yoe's
    resolver must either replicate or disable.
  - Snap-stub packages in 22.04+ (`chromium`, `firefox` etc. are transitional
    `.deb`s that call `snap`) — silently broken on a no-snapd rootfs.
  - PPA signing posture differs from main archive.
  - debhelper preset behavior differs in subtle ways from Debian's.
- Distro abstraction layer (refactor the Alpine path through an internal
  `distro` interface before building Debian) — deferred until at least one of
  Ubuntu, SUSE, or Yocto surfaces real interface pressure. The format-named code
  organization (`internal/dpkg`, `internal/deb`) carries most of the abstraction
  benefit at a fraction of the design cost.
- Debian base-tarball + delta product shape (consume upstream Debian as a
  black-box rootfs and install project units into `/opt`) — rejected because the
  customer requires apt-on-target for in-field upgrades (anchored in Problem
  Frame); a base-tarball + `/opt` shape cannot deliver that. The base-tarball +
  delta shape remains the right answer for any future flash-once customer whose
  deployments never run `apt-get upgrade`.
- Mixing Debian and Alpine packages in one rootfs — single-distro per image is a
  hard rule in v1.
- **Multi-suite-per-project on the Debian side.** A project may declare at most
  one Debian suite (e.g., bookworm). yoe errors at evaluation if two Debian
  images target different suites. Supporting multiple suites in one project
  requires a suite axis in the toolchain cache key, which is future work, not
  v1. (Note: this is different from declaring multiple `debian_feed` entries
  within one suite — `bookworm` + `bookworm-security` + `bookworm-updates` are
  the same suite and remain in scope per R2.)
- A generic `feed(format=…)` builtin — preserves the existing `feeds-as-modules`
  Key Decision.
- Cross-compile toolchains — CLAUDE.md project rule (native-only under QEMU).
- Pointing the device's `apt` at debian.org directly — the project-repo mirror
  is the canonical `sources.list` entry; debian.org is not an alternative target
  in v1.
- Snap, flatpak, debootstrap-on-target, live-build, deb-src/buildd integration —
  out of scope.
- Bumping module-alpine semantics or `RepackAPK` behavior — this work does not
  touch the Alpine side beyond what shared format-named helpers naturally bring
  along during the move out of `internal/artifact/apk.go` (if any move happens).
- Project key rotation flow (unchanged from `docs/signing.md`); Debian keyring
  lifecycle rides Debian release bumps analogously to how Alpine keys ride
  Alpine release bumps.
- Init systems other than `systemd` on Debian and `OpenRC` on Alpine. sysvinit
  on Debian and systemd on Alpine are out.
- Non-amd64/arm64 first arches on the Debian side. The first ship targets the
  arches the customer needs; broader arch matrix follows demand.
- `Recommends`/`Suggests` auto-installation policy beyond apt's default
  `--no-install-recommends` posture — left as a planning-time decision per-image
  only if a real need surfaces.

---

## Key Decisions

- **Synthetic modules over virtual units.** Inherits the `feeds-as-modules` Key
  Decision. The user's brief used "virtual unit" wording; the substantive
  mechanism is the same (synthesize one unit per index entry, no `.star` file
  per package), but the integration shape is "synthetic module," reusing every
  existing piece of resolver and module machinery rather than introducing a
  parallel "virtual unit" type. Why: reuse existing resolver, no new concept in
  the type system, `prefer_modules` works unchanged. The
  `internal/starlark/synthetic_module.go` type that backs this already shipped
  with Alpine's work and explicitly calls out `debian_feed(...)` as a
  registered consumer alongside `alpine_feed(...)` — `debian_feed` just calls
  `Engine.RegisterSyntheticModule` with a Debian-shaped `Lookup`/`Names`
  closure pair.
- **Per-distro Starlark builtins (`debian_feed`), shared internal machinery.**
  Preserves the `feeds-as-modules` rejection of a generic `feed(format=…)`
  discriminator while keeping the internal code distro-agnostic. Each distro's
  index format, dep syntax, signing scheme, and package layout differ enough
  that the user-facing shape must be per-distro; the internal dep parser, deb
  extractor, and repo emitter share without leakage.
- **Internal code named by format, not by distro.** `internal/dpkg`,
  `internal/deb`, `internal/repo`'s Debian emitter — not `internal/debian`. When
  `module-ubuntu` lands, it imports the same packages with no rename day. Cheap
  insurance, easy to do correctly once. The Alpine half of feeds-as-modules
  already adopted this convention: index parsing lives in `internal/apkindex/`
  (not `internal/alpine/`), with `internal/feeds/alpine/` as the thin
  Starlark-facing wrapper. Debian follows the same split: format work in
  `internal/dpkg/` and `internal/deb/`, distro-facing wrapper in
  `internal/feeds/debian/`.
- **On-disk distro split lives one level down from the top, in `build/` and
  `repo/`.** A project that ships both Alpine and Debian images grows
  `build/alpine/`, `build/debian/`, `repo/<project>/alpine/`,
  `repo/<project>/debian/` rather than tagging the libc family into individual
  unit-build directory names. The split happens once at the top level of each
  output tree (R14, R14a), composes consistently across `build/` and `repo/`,
  and extends to `build/ubuntu/` without renaming day. Considered and rejected:
  a `build/<unit>.<scope>.<libc>/` suffix scheme — smaller code-touch but
  shape-asymmetric with the repo split, harder to reset surgically, and noisier
  to scan visually. The directory-level split also matches how the existing
  `cache/modules/<module>/` already isolates per-source state — same mental
  model.

  `cache/sources/` stays flat — files are SHA-256-keyed, collisions impossible
  across distros. `cache/modules/<module>/` stays flat — `module-debian` is just
  another sibling of `module-alpine`. Language caches (`cache/go/`, future
  language toolchains) are deferred to planning: if a glibc Go toolchain and a
  musl Go toolchain ever want to share `GOMODCACHE` cleanly, splitting `cache/
  go/<distro>/` follows the same shape; if they're already isolated by the way
  the Go toolchain hashes its inputs, no split is needed.
- **`distro` is the only image-level distro selector; libc is derived.** Image
  declares `distro = "alpine" | "debian"`. Classes pick the toolchain from the
  image's effective distro (alpine→musl, debian→glibc). v1 does not require a
  separate `libc` field because in v1 the mapping is one-to-one and any
  mismatch would be an error anyway. If a future hybrid case (glibc binary on
  Alpine via gcompat, or similar) materializes, a `libc` override field
  becomes a targeted extension rather than v1-day cost.
- **`distro` means "driver" on images, "compatibility tag" on non-image units.**
  The same field name carries two distinct semantics by context (R21 vs R21a),
  and the spec is explicit about which is which. On an image-class unit,
  `distro` is the **driver**: it selects the toolchain, packaging format, repo
  subtree, and build-dir prefix for every unit reached through that image's
  closure. On a non-image unit, `distro` is a **compatibility tag**: it gates
  visibility during the closure walk and nothing else. A tagged source unit
  does not independently pick a different toolchain or land in a different
  build subtree — those are driven by the consuming image. The tag is purely
  "this unit is part of the alpine world / debian world; filter me out of the
  wrong-world closures." Considered and rejected: separate field names
  (`distro` for images, `compatible_distros = [...]` for units) — would have
  been more precise but at the cost of API surface and a name users would
  have to remember. One field, two clearly-documented meanings, beats two
  fields here. Considered and rejected: per-unit `distro` as a build-driver
  override (would have forced units to declare their own toolchain
  independently of the consuming image) — that direction leads to hybrid-
  rootfs land, which Scope Boundaries defers.
- **Project-level `default_distro` fallback, image-level override.** R20a +
  R21 give a cascade: image's `distro` → project's `default_distro` → error.
  This keeps the "explicit over implicit" rule (default_distro must still be
  set somewhere — yoe core has no built-in fallback) while letting today's
  all-Alpine projects declare it once in PROJECT.star instead of repeating
  `distro = "alpine"` on every image. The cascade reads top-down: project
  states the common case, images override the exceptions.
- **Closure-walk visibility, not constraint-and-error.** R21a is implemented
  by filtering the walker, not by post-walk validation. A unit tagged for the
  wrong distro is invisible — exactly as if it did not ship — so a wrong-
  distro reference produces the normal "unit not found" error path, not a
  separate "distro mismatch" path. One failure mode, not two.
- **Typed `distro` field, not a generic `tags = [...]` system.** Considered
  and rejected: generalize R21a's per-unit `distro` into a generic tag
  mechanism (`tags = ["alpine"]`, `tags = ["gpu"]`, etc.) that closure-walk
  visibility could filter against. The rejection is anchored in the
  observation that distro carries two distinct responsibilities — closure-
  walk **visibility filtering** (cheap, walker-local, generalizes easily to
  tags) AND **build-output partitioning** (R14a's `build/<distro>/...`
  split, the hash key's `container` axis, the runtime installer's repo-
  subtree lookup; expensive, threaded through executor / sysroot / meta /
  TUI / clean). A tag system handles the first cleanly but breaks on the
  second: yoe would have to know, per tag, whether the tag warrants a build-
  dir dimension, which is either cache-exploding (every tag forks the cache)
  or a registry of "partitioning tags vs metadata tags" that is just typed
  fields wearing a costume. On-disk shapes like
  `build/distro=debian,arch=x86_64,gpu/<unit>/` invite order-sensitive
  drift; today's `build/<distro>/<unit>.<scope>/` is parseable at a glance.
  We also have only one example today (distro); CLAUDE.md's "three similar
  lines is better than a premature abstraction" applies. When a second
  visibility-only need surfaces (e.g., a GPU-only unit), the right move at
  that point may still be another typed field
  (`requires_features = ["gpu"]`) rather than untyped tags — typed fields
  read cleanly in `.star`, produce specific error messages, and resist the
  partitioning-vs-metadata conflation. Revisit then, not now.
- **Toolchain container is a unit, not a Dockerfile bake.** `toolchain-glibc`
  follows `toolchain-musl`'s pattern from
  `docs/specs/2026-04-04-container-units.md`: a `container()` unit with a
  Dockerfile in `containers/toolchain-glibc/`. No bypass, no special-case.
- **Trust roots through `InRelease` + per-package SHA256 — no per-deb GPG
  verification on the device.** Debian `.deb` files are not GPG-signed
  per-package by upstream, so the runtime trust chain on the device runs
  entirely through the project-key-signed `InRelease` plus the per-package
  SHA256 in `Packages`. The Debian release keyring is used only for module-side
  `yoe update-feeds` (verifying upstream `InRelease`), not for any device-side
  install-time check. This diverges intentionally from the Alpine
  `mirror-alpine-keep-keys` shape (where `.apk`s carry per-package signatures
  verified at install time on the device) — the divergence is forced by Debian's
  archive format, not chosen.
- **Mirror upstream debs verbatim.** Mirroring is a copy; no re-archive, no
  re-sign. The `Packages` index lists upstream SHA256 byte-identical so the
  project repo's index addresses the same bytes upstream does. Avoids the
  fragile re-sign path entirely on the Debian side from day one — Debian doesn't
  have the legacy `RepackAPK` to remove.
- **Bootstrap-keyring is committed, not fetched.** R25 commits the initial
  Debian release keyring directly to `module-debian` and pins fingerprints so
  subsequent keyring refreshes are anchored to known-good keys. Breaks the
  chicken-and-egg where the keyring would otherwise be fetched via the feed it
  must verify.
- **Project key scoped via `signed-by=`, not via `/etc/apt/trusted.gpg.d/`.**
  The project key lives in `/etc/apt/keyrings/` and is referenced from the
  sources.list entry's `signed-by=` field, scoping its trust to the project repo
  source only. Modern apt's per-source scoping prevents the project key from
  validating any third-party apt source the operator might later add.
  `/etc/apt/trusted.gpg.d/` (global trust) was the older default; this design
  adopts the newer, narrower posture from day one.
- **Maintainer scripts run at image build via `dpkg --configure -a` under
  binfmt, not deferred to first boot.** R18 commits to the debootstrap model:
  image assembly stages debs, then runs `dpkg --configure -a` inside a binfmt
  container so every postinst executes against a fully-configured
  `/var/lib/dpkg/` state. Deferring postinsts to first boot leaves the dpkg DB
  in `unpacked` state, breaking Pre-Depends, triggers, and any postinst that
  assumes a configured rootfs. Running them at build time gives images that boot
  into a fully-configured Debian state with no first-boot reconciliation pass.
- **Service enablement lives in the packages, not in the image assembler.** R19
  drops the assembler-scan-and-symlink approach. Mirrored debs get service
  enablement from their own `dpkg --configure -a` invocation of
  `deb-systemd-helper` honoring Debian preset machinery. Project-built debs
  carry `services = [...]` declarations baked into their `.deb` at package time,
  parallel to Alpine and consistent with CLAUDE.md's "units declare their own
  services" rule.
- **systemd is the only init system for Debian images in v1.** Debian's default.
- **Dot-style synthetic-module names with a suite axis for Debian.**
  `<distro>.<suite>.<component>` (e.g. `debian.bookworm.main`). Alpine uses the
  simpler `<distro>.<component>` (`alpine.main`). Resolves the open separator
  question in `feeds-as-modules` in the same direction; matches Python/Starlark
  attribute feel and reads cleanly inside `prefer_modules` dict literals.
- **One Debian suite per project in v1.** A project may declare at most one
  Debian suite across all its images, because the single `toolchain-glibc`
  container is pinned to a single Debian release and the binary-reuse rule would
  otherwise risk ABI mismatch across suites. Suite-per-project is enforced at
  evaluation; supporting multiple suites in one project requires a suite axis in
  the toolchain cache key, deferred to future work.
- **Approach A over Approach B (distro abstraction).** Approach B's interface
  boundaries are unverified with only one distro instance; format-named internal
  code captures most of the abstraction benefit without the design risk. Revisit
  when Ubuntu or another distro reveals real interface pressure.
- **Approach A over Approach C (base-tarball + delta).** The customer requires
  apt-on-target for in-field upgrades (anchored in Problem Frame); a
  base-tarball + `/opt` shape cannot deliver upgrades cleanly. C remains the
  right answer for any future flash-once customer.

---

## Dependencies / Assumptions

- The `feeds-as-modules` spec (`docs/specs/2026-05-13-feeds-as-modules.md`) has
  substantially landed on the yoe-core side as of 2026-05-26 — see
  `docs/SPEC_PLAN_INDEX.md` for the row marked
  "Partial — yoe-core done, awaiting module-alpine cutover". The pieces
  `module-debian` depends on (synthetic-module type and registration hooks,
  recursive module walking with cycle detection, closure walk in Go, lazy
  materialization, `prefer_modules` preflight, `yoe update-feeds` dispatch,
  TUI feed display, cross-feed providers, companion service-only apk
  emission, build-transport fields on synthetic units) are all in place.
  What is still outstanding on the Alpine side is the in-place `module-alpine`
  cutover (U13: replace generated per-package `.star` files with two
  `alpine_feed()` calls plus `*-enable.star` companions) and the e2e validation
  pass that follows it (U15). Implementing `debian_feed` independently of
  `alpine_feed` would duplicate the synthetic-module plumbing; this design
  assumes they share it, and as of today they can.
- The `mirror-alpine-keep-keys` spec
  (`docs/specs/2026-05-18-mirror-alpine-keep-keys.md`) lands before or alongside
  this work — the verbatim-mirror posture is the same on both sides, even though
  the per-package verification semantics differ (Alpine: per-`.apk` signatures
  verified on device; Debian: per-package SHA256 in `Packages` plus
  `InRelease`).
- Debian's `InRelease` (clear-signed) is the supported signed-index format;
  `Release` + detached `Release.gpg` is not required to be supported as a
  separate path in v1.
- Target-side apt enforces `InRelease`'s `Valid-Until` field by default — yoe
  relies on this rather than reimplementing it. Confirm against the apt version
  used by the chosen Debian release.
- The pinned Debian `debian-archive-keyring` package ships the active build-host
  signing keys for the suite the project targets. Note that R25 ships the
  initial keyring as committed bytes in `module-debian` rather than fetched via
  the feed; the `debian-archive-keyring` package may still be installed in the
  runtime image for refresh-on-device contexts but is not the bootstrap source.
- `apt-tools` on the target validates `InRelease` against the
  `signed-by=`-scoped project key and validates each `.deb` against its
  `Packages`-listed SHA256. Per-`.deb` GPG signature verification
  (`debsig-verify`) is NOT relied on — Debian's archive does not produce
  per-package signatures.
- Today's project repo emitter (`internal/repo/index.go`) is structurally
  extensible to a Debian-format emitter, or a sibling emitter is straightforward
  to introduce; verified at planning time.
- The toolchain-glibc container's pinned Debian release is the same release used
  for the runtime rootfs at v1 — same coupling rule as `toolchain-musl`'s alpine
  release / `module-alpine` coupling. A future split (toolchain pinned to one
  release, rootfs to another, or multiple suites in one project) is out of scope
  and tracked under Scope Boundaries.
- Foreign-arch (e.g., `arm64`) debs can be mirrored and extracted into the
  rootfs without the host running them at extract time. However,
  `dpkg --configure -a` (R18) DOES execute postinsts under binfmt against the
  foreign-arch rootfs, which is a real runtime dependency on QEMU user-mode +
  binfmt_misc for the build host.
- TLS certificate trust for the project repo URL is operator-managed (root CA
  store, certificate pinning, or `verify-peer=no` for closed networks at
  operator discretion). It is orthogonal to GPG trust on the repo metadata. yoe
  does not bundle or manage TLS certificates.

---

## Outstanding Questions

### Resolve Before Planning

_(All Resolve-Before-Planning items resolved during the brainstorm and the
round-1 doc review.)_

### Deferred to Planning

- [Affects R23][Technical] CHANGELOG/docs commit phasing: one entry per ship
  milestone (debian_feed builtin, deb artifact, project repo emit,
  apt-on-target) vs a batched entry on first end-to-end demo. Planner picks
  based on how the work actually decomposes into PRs.
- [Affects R6, R7][Technical] dpkg dep-syntax parser scope: which corner cases
  of `Depends` (e.g., architecture qualifiers `:any`, `:native`, `:<arch>`;
  build profiles) must v1 handle, vs surface as a clear error and defer?
  Real-world Debian `main` exercises most of them; v1 may not need all.
- [Affects R6, R12][Technical] `.deb` builder scope: minimum control fields
  required, handling of `debian/changelog`-style metadata yoe doesn't naturally
  have, whether to support per-unit `postinst` script generation (and if so, how
  it interacts with the build-time `dpkg --configure -a` rule).
- [Affects R13][Technical] Debian-side `Release` field detail set: which hash
  algorithms to publish (SHA256 is the modern minimum; whether to also emit
  SHA512), whether to emit `Description`/`Origin`/`Label`/`Suite`/`Codename`
  fields beyond the minimum apt requires, and `pool/` layout fan-out
  (`pool/<component>/<initial>/<pkg>/` vs flat). R14/R14a already pin the
  top-level path split.
- [Affects R7][Technical, Needs research] Virtual package handling: how Debian's
  `Provides:` interacts with the synthetic-module's provides table, particularly
  for multi-provider virtual packages where apt's resolver has heuristics yoe
  must either replicate or pin via `prefer_modules`.
- [Affects R8, R22][Technical] `toolchain-glibc` arch matrix: which arches the
  container ships for v1, what binfmt/QEMU coupling looks like for
  `aarch64-via-glibc-Debian-container` versus today's
  `aarch64-via-musl-Alpine-container`. Note R18's `dpkg --configure -a` step
  also depends on binfmt being available for the target arch on the build host.
- [Affects R10, R11][Technical] In-tree `Packages` storage: decompressed
  all-arches (largest), decompressed active-arches only, or `Packages.gz`
  (smallest, less diff-friendly). Tradeoff is diff-readability vs repo size;
  same tradeoff `feeds-as-modules` flagged for APKINDEX.
- [Affects R20][Technical] sources.list format: legacy single-line vs
  `deb822`-style `.sources` files. R20 leans toward deb822 but planner picks
  based on apt version on the pinned Debian release.
- [Affects R14, R14a][Technical] `cache/go/` (and any other language-toolchain
  caches that currently live flat under `cache/`) — confirm at planning time
  whether they need a per-distro split (`cache/go/<distro>/`) to keep a glibc
  Go build and a musl Go build from poisoning each other's module cache, or
  whether the toolchain's own input hashing already isolates them. The on-disk
  split for `build/` and `repo/` is settled by R14/R14a; the language-cache
  question is the residue.
- [Affects R7][Technical] `Recommends` policy: confirm v1 ships
  `--no-install-recommends` posture or expose it as an image-level toggle.
- [Affects R18][Technical] Maintainer-script audit + privilege context. R18 runs
  every package's maintainer script at image build under root in binfmt — the
  script-execution surface is real even though it doesn't run on the device.
  Planning should decide: does yoe emit a per-image manifest (script name,
  package, hash) so the image author has a reviewable record before publish?
  Should a future "no-scripts" or stripped-script image mode be in scope for
  locked-down deployments? Privilege context for the build-side execution should
  be documented in Dependencies/Assumptions.
- [Affects R24][Technical] Default `Valid-Until` staleness window — typical
  fleet update cadences (security advisories, point releases) should inform the
  default. 30 days is a reasonable starting guess but worth validating against
  the customer's deployment shape.
- [Affects ongoing maintenance][User decision] Maintenance ownership of
  `module-debian`, `toolchain-glibc`, and the Debian keyring lifecycle was
  raised by round-1 review and explicitly skipped in this spec. Planning should
  revisit once customer engagement clarifies whether the customer or the yoe
  team carries the ongoing load.
