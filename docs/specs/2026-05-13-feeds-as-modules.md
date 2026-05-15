---
date: 2026-05-13
topic: feeds-as-modules
---

# Feeds as synthetic modules with auto-discovered services

## Summary

A module's `modules` field generalizes into a priority-ordered list whose
entries can be local directory paths, remote module references, or distro feeds
(`alpine_feed(...)`). Each `alpine_feed` materializes a synthetic module from a
checked-in APKINDEX; synthetic modules rank below non-feed modules by default,
so hand-written `.star` files shadow them by name with no extra ceremony.
Service enablement moves entirely off Starlark — image assembly scans the
assembled rootfs and wires runlevel or target symlinks for every init script and
systemd unit it finds.

---

## Problem Frame

Today the project's Alpine consumption pipeline lives in
`testdata/.../module-alpine/units/main/` — roughly a thousand cached
`alpine_pkg` `.star` files, each duplicating upstream metadata (`version`,
`runtime_deps`, `provides`, `replaces`) inline. `scripts/gen-unit.py`
regenerates them from APKINDEX on demand, and the project's maintainer
re-applies hand edits (services lists, custom overrides) after each
regeneration. The pipeline has known sharp edges: regenerator drops fields it
didn't enumerate, hand edits get lost on regen, every new Alpine package needs a
new checked-in `.star`, and the `services = [...]` field on each unit pushes
"enable this in any image that installs me" policy into the wrong place. See
`docs/apk-passthrough.md` for the current state and `docs/roadmap.md:189-211`
for an earlier overlap design.

Meanwhile, yoe's existing module-priority machinery is general enough to absorb
feeds directly: modules already have names, declare their units, and resolve
conflicts via `prefer_modules`. Treating an upstream feed as "just another
module" — synthesized from APKINDEX rather than git — lets all of that machinery
do work it's already shaped for, instead of inventing a parallel "virtual unit"
type system.

---

## Actors

- A1. Module maintainer: writes and maintains `module-alpine` (and future
  `module-debian`, `module-ubuntu`). Runs `yoe update-feeds` to refresh in-tree
  APKINDEX files; reviews diffs; commits and pushes.
- A2. Project user: writes `PROJECT.star`, picks which modules to include, pins
  Alpine release via module ref, occasionally writes a shadow `.star` to
  override one feed entry.
- A3. Yoe resolver: walks the recursive module list, flattens it, ingests
  Starlark units and APKINDEX-derived synthetic units uniformly, applies
  `prefer_modules` overrides.
- A4. Yoe image assembler: extracts packages into rootfs, scans for init scripts
  and systemd unit files, creates the appropriate runlevel/target symlinks
  before generating the disk image.

---

## Key Flows

- F1. Project resolves units across feeds and modules
  - **Trigger:** `yoe build` runs.
  - **Actors:** A2, A3.
  - **Steps:** Project's `modules` list is walked. Each entry contributes: a
    directory of `.star` files, a remote module (recursively), or a synthetic
    module from `alpine_feed`. Yoe flattens the tree to one ordered list,
    ingests units into the resolver, applies `prefer_modules` overrides, errors
    on unresolved name conflicts that `prefer_modules` doesn't cover.
  - **Outcome:** A single unit graph keyed by name, every unit traceable to its
    source module.
  - **Covered by:** R1, R2, R5, R6, R8, R9.

- F2. Module maintainer refreshes a feed
  - **Trigger:** New Alpine release or routine update.
  - **Actors:** A1.
  - **Steps:** Maintainer runs `yoe update-feeds` inside `module-alpine`. Yoe
    fetches each feed's `APKINDEX.tar.gz` per declared arch, writes the
    decompressed files to the module's in-tree feed directory, leaves the diff
    staged. Maintainer reviews the diff, commits, and pushes.
  - **Outcome:** Module's in-tree APKINDEX matches upstream as of the fetch
    moment. Consuming projects pick up the change on their next module sync.
  - **Covered by:** R10, R11.

- F3. Image assembly enables services
  - **Trigger:** `yoe build <image-unit>` completes package install into the
    rootfs.
  - **Actors:** A4.
  - **Steps:** Image assembler scans the assembled rootfs for service files
    (`/etc/init.d/*`, `/usr/lib/systemd/system/*.service`). Determines init
    system from the rootfs (which package owns `/sbin/init`, or explicit field
    on the image unit). For each discovered service, creates the appropriate
    runlevel symlink (`/etc/runlevels/default/<svc>` for OpenRC,
    `/etc/systemd/system/multi-user.target.wants/<svc>.service` for systemd).
    Skips template units and any package-shipped service explicitly excluded by
    the image's opt-out list.
  - **Outcome:** Every service whose package was installed will start at boot.
  - **Covered by:** R13, R14, R15.

---

## Requirements

**Module priority lists**

- R1. A module's `MODULE.star` (and a project's `PROJECT.star`) accepts a
  `modules = [...]` field whose entries are one of: a bare string (local
  directory path relative to the declaring module), a `module(...)` call (remote
  module reference), or an `alpine_feed(...)` call (future: `debian_feed(...)`,
  `ubuntu_feed(...)`).
- R2. The list is order-significant. Earlier entries take precedence over later
  entries when units of the same name appear in both. (Verify current direction
  against existing resolver code; document the chosen direction in user-facing
  docs.)
- R3. Modules may nest recursively: a module's `modules` list may itself contain
  module references whose own `MODULE.star` declares further modules. Yoe walks
  the tree depth-first and flattens to a single ordered list at project-eval
  time.
- R4. Cycles in the module graph are detected and reported as a clear error with
  the cycle path. Modules are deduplicated by canonical identity (resolved git
  URL + ref, or absolute filesystem path).
- R5. Synthetic modules produced by `alpine_feed` (and future feed builtins)
  rank below all non-feed modules in the flattened priority list.
  `prefer_modules` is the targeted override when this default is wrong for a
  specific name.

**`alpine_feed` builtin**

- R6. `alpine_feed(name, url, branch, section, index, keys)` registers a feed
  for the declaring module. `index` names the in-tree directory containing
  per-arch APKINDEX files. `keys` lists in-tree paths to the signing key(s)
  trusted for this feed.
- R7. At project-eval, yoe materializes one synthetic module per `alpine_feed`
  call, named `<parent-module-name>.<feed-name>` (e.g., `alpine.main`). The
  synthetic module contributes one unit per APKINDEX entry whose arch matches an
  arch in active use by the project.
- R8. Synthetic units are first-class — same `Unit{}` struct, same resolver
  entry points, same input-hash treatment — and carry metadata identifying their
  source feed for TUI display and diagnostics.
- R9. The apk dependency parser handles `name`, `name<rel>ver`,
  `so:<soname>[=ver]`, `cmd:<binary>[=ver]`, `pc:<pcname>[=ver]`, `/file/path`,
  and `!<conflict>`. A provides table built from the synthesized module's
  APKINDEX resolves so/cmd/pc/file-path lookups during name resolution.

**APKINDEX as in-tree lockfile**

- R10. Each module that declares an `alpine_feed` checks the upstream
  `APKINDEX.tar.gz` content (decompressed) into the module's git, one file per
  `(section, arch)`. The module's checked-in ref pins the snapshot; consuming
  projects pick up snapshot changes via routine module sync.
- R11. `yoe update-feeds` (run inside a module) re-fetches each declared feed's
  APKINDEX from upstream and rewrites the in-tree files. The command writes
  changes but does not commit or push; the maintainer reviews and commits via
  normal git workflow.
- R12. A synthetic unit's input hash folds in the entry's name, version,
  release, the APKINDEX `C:` checksum, and the resolved `runtime_deps` /
  `provides` / `replaces`. At fetch time, the apk's control-segment checksum is
  verified against `C:`.

**Service auto-discovery and enablement**

- R13. After package extraction into the rootfs and before disk-image
  generation, yoe's image assembler scans the rootfs for init scripts
  (`/etc/init.d/<svc>`) and systemd unit files
  (`/usr/lib/systemd/system/<svc>.service` and
  `/etc/systemd/system/<svc>.service`).
- R14. For each discovered service, the assembler creates the appropriate
  runlevel or target symlink so the service starts at boot: OpenRC images get
  `/etc/runlevels/default/<svc>`, systemd images get
  `/etc/systemd/system/multi-user.target.wants/<svc>.service`.
- R15. Template service files (those whose name contains `@`, e.g.
  `getty@.service`) are not auto-enabled; instantiation is the user's explicit
  choice.
- R16. Unit definitions no longer accept a `services = [...]` field. The field
  is removed from the `Unit{}` struct and from every cached `alpine_pkg` `.star`
  file. Image units do not gain an `enable_services = [...]` field.

**TUI display**

- R17. The TUI surface that lists modules shows the flattened, recursive module
  list in resolved priority order, with each entry tagged by source (directory,
  remote module, feed) and (for synthetics) the parent module that declared the
  feed.

**Migration**

- R18. The transition removes the auto-generated `alpine_pkg` `.star` files from
  `module-alpine`. The exact rollout strategy is deferred to ce-plan.
  Hand-edited overrides survive — migrated to thin `.star` files in a
  higher-priority module — but the bulk of the cached unit directory is deleted.
- R19. The existing `prefer_modules` entries that point at `module-alpine` (such
  as `testdata/e2e-project`'s `xz` pin) update mechanically to point at the
  synthesized feed module name (e.g., `alpine.main`).

---

## Acceptance Examples

- AE1. **Covers R2, R5.** Given `module-core` declares `openssl` and
  `alpine_feed("main", ...)` synthesizes an `openssl` from APKINDEX, when the
  project's image artifacts include `openssl`, the resolver picks
  `module-core`'s source-built openssl (non-feed beats feed by default, no
  `prefer_modules` line needed).

- AE2. **Covers R5.** Given the same setup, when `PROJECT.star` declares
  `prefer_modules = {"openssl": "alpine.main"}`, the resolver picks the Alpine
  prebuilt instead.

- AE3. **Covers R3, R4.** Given `module-A`'s `modules` list references
  `module-B` and `module-B`'s `modules` list references `module-A`, yoe errors
  at project-eval with a cycle path `A → B → A`. Build does not proceed.

- AE4. **Covers R9.** Given an APKINDEX entry whose `D:` line includes
  `so:libcrypto.so.3=3.5.4-r0`, when the resolver walks the dep, it consults the
  provides table built from the same feed, finds the package whose `p:` line
  declares `so:libcrypto.so.3=<v>`, and adds that package as the runtime dep.

- AE5. **Covers R13, R14, R15.** Given an image installs `docker` (which ships
  `/etc/init.d/docker`), `openssh` (which ships `/etc/init.d/sshd`), and
  `agetty` (which ships `/usr/lib/systemd/system/getty@.service`) into an OpenRC
  rootfs, when image assembly runs, it creates `/etc/runlevels/default/docker`
  and `/etc/runlevels/default/sshd` but does not touch the `getty@` template.

- AE6. **Covers R11.** Given a maintainer runs `yoe update-feeds` inside
  `module-alpine`, yoe rewrites the in-tree `feeds/main/x86_64/APKINDEX` (and
  other declared arches) from upstream, leaves them staged, and exits. The
  maintainer then runs normal `git diff`, `git commit`, and `git push`.

---

## Success Criteria

- A project that today consumes Alpine packages via the cached
  `module-alpine/units/main/` directory builds an equivalent image after the
  change, with all of the cached `.star` files removed.
- Adding a new Alpine package to a project requires zero file edits in
  `module-alpine` — listing the package name in an image's `artifacts` is
  enough.
- Hand-written overrides for `module-alpine` units are migrated cleanly to
  shadow units in a higher-priority module; their behavior is preserved.
- The TUI module list shows the resolved priority order, lets the maintainer see
  which module supplied each unit, and surfaces feed membership at a glance.
- `yoe update-feeds` produces a reviewable diff in module-alpine's git that
  captures one Alpine snapshot update in one commit.
- A downstream agent or implementer reading this document can begin planning
  without needing to invent product behavior or override semantics.

---

## Scope Boundaries

- The monolithic-vs-split package conflict (yoe's source-built `util-linux` vs
  Alpine's split `libuuid` / `libmount` / `libblkid` — see
  `docs/apk-passthrough.md`) is unchanged. This design lets the resolver see
  Alpine packages cheaply; it does not arbitrate path- ownership conflicts
  between source-built and prebuilt providers.
- Install scripts (`.pre-install`, `.post-install`) and triggers (`.trigger`)
  are not executed at image build time today, and this design preserves that
  limitation. Image assembly remains a `tar`-extract pass plus the service
  auto-enable scan.
- Build-time consumption of synthetic units beyond what destdir extraction
  already covers — e.g., automatic surfacing of an Alpine `-dev` subpackage's
  headers when a source-built unit lists the meta-package in `deps` — is not in
  scope. Roadmap item 44 ("alpine should have unit deps, not just runtime deps")
  tracks the broader need.
- A generic `feed(...)` builtin with a `format=` discriminator is rejected in
  favor of per-distro builtins (`alpine_feed`, `debian_feed`, `ubuntu_feed`).
  Discriminator-style would force every parameter to be either distro-specific
  or shared, producing a leaky shape.
- The roadmap's "module-alpine units as deltas over upstream PKGINFO" proposal
  (`_extra` / `_drop` / `_override` suffix fields on `runtime_deps`, `provides`,
  etc. — `docs/roadmap.md:189-211`) is decoupled. Override-by-shadowing is the
  canonical mechanism in this design. The suffix syntax may still land later as
  a typing-saver for large overrides; if so, it should apply to any unit
  shadowing a lower-priority counterpart, not specifically to `alpine_pkg`.
- `debian_feed` and `ubuntu_feed` are mentioned for naming-convention purposes
  only — no implementation work in this spec. They follow when someone needs
  them, reusing all downstream machinery.

---

## Key Decisions

- **Synthetic modules over virtual units.** Feeds materialize as full modules in
  the priority list, not as a parallel "virtual" unit type. Rationale: reuses
  every existing piece of resolver and module machinery; introduces no new
  concept in the type system; makes `prefer_modules` work unchanged.
- **Synthetic modules rank low by default.** Hand-written wins over
  feed-synthesized for the same name without requiring per-name `prefer_modules`
  entries. The default matches the common case (use source-built when
  available); `prefer_modules` handles exceptions.
- **Per-distro builtin (`alpine_feed`) over generic `feed(format=...)`.**
  Matches yoe's class-per-build-system convention. Each distro's index format,
  dep syntax, signing scheme, and package layout differ enough that a generic
  shape leaks.
- **APKINDEX checked into module git, not synthesized at build.** The module ref
  already pins the snapshot. No separate lockfile, no project-side bookkeeping.
- **Decompressed APKINDEX in tree.** Diff-readability on `yoe update-feeds` is
  the point. Size cost (~40MB for Alpine main+community across three arches) is
  one-time per module and git-deltas well.
- **Service enablement is rootfs-derived, not Starlark-declared.** Installing a
  package means the service runs. The decoupled install-vs-enable model adds
  knobs without value for the common case; the explicit-opt-out for the rare
  exception (install but don't enable) is a follow-up decision if it ever comes
  up.

---

## Dependencies / Assumptions

- The apk-tools control-segment SHA1 (`C:` in APKINDEX, `Q1`-prefixed base64) is
  acceptable as an input-hash component. Trust chain runs through the (signed)
  APKINDEX checked into the module repo plus yoe's project-key re-signing on the
  install side, so SHA1 collision-strength is not the load-bearing link.
- The current module-priority resolution direction (first-declared wins, or
  last-declared wins) needs to be confirmed against
  `internal/starlark/builtins.go` before user-facing docs commit to a direction.
  The choice is structural; whichever direction yoe uses today should be
  preserved and made explicit.
- The `internal/source/fetch.go` and cache-validity machinery extend naturally
  to apks fetched against an APKINDEX entry — fetch URL derived from
  `feed.url + branch + section + arch + filename`, sha pinned to `C:`. No new
  fetcher type expected.
- Existing `prefer_modules` syntax (`internal/starlark/builtins.go:472`- ish)
  accepts string module names that today match declared module names. Synthetic
  module names like `alpine.main` should pass through unchanged; this needs
  verification.

---

## Outstanding Questions

### Resolve Before Planning

- [Affects R2, R17][User decision] Should the flattened-list priority direction
  surface in the TUI as "earlier = higher priority" (matches PROJECT.star
  reading order) or "later = higher priority" (matches override-style mental
  model)? Pick one before docs commit.
- [Affects R7][User decision] Separator for synthetic module names:
  `alpine.main` (dot, matches Starlark/Python attribute style) or `alpine-main`
  (hyphen, matches Alpine's own repo naming)? Affects `prefer_modules`
  ergonomics.
- [Affects R18][User decision] Migration strategy: big-bang delete in the
  cutover commit, gradual deletion with feed-as-module shipped inert first, or
  kept-as-overrides indefinitely? Affects rollout staging.

### Deferred to Planning

- [Affects R10][Technical] APKINDEX in-tree storage: decompressed-all- arches
  (largest), decompressed-active-arches-only, or compressed `APKINDEX.tar.gz`.
  Tradeoff is diff-readability vs repo size.
- [Affects R12][Technical] Hash anchor implementation: use `C:` (Q1-SHA1)
  directly in `internal/resolve/hash.go`, or compute SHA256 at apk fetch and
  store both. Existing apks-from-tarballs path uses SHA256; consistency vs
  simplicity.
- [Affects R13][Technical] Init system detection: explicit
  `init = "openrc" | "systemd"` field on the image unit (matches "explicit over
  implicit" project rule), or detect from rootfs (which package owns
  `/sbin/init`, or systemd-tmpfiles presence). Project rule argues explicit;
  ergonomics argue detection.
- [Affects R15][Technical] Service-template filter rules beyond `@` in the
  filename — e.g., are there OpenRC scripts that should be excluded by
  convention? Probably none, but verify with a real rootfs scan.
- [Affects R7][Technical, Needs research] APKINDEX entries occasionally list
  multiple versions of the same package (rare in stable Alpine, common in
  edge/testing). Tiebreaker rule: newest only, or expose all and require
  `prefer_modules` to disambiguate?
- [Affects R4][Technical] Cycle detection canonical-identity rule when a remote
  module is fetched via two different URLs that resolve to the same commit
  (e.g., `https://` vs `git@`).
- [Affects R2][Technical] Verify current resolution direction in
  `internal/starlark/builtins.go` and confirm it matches the chosen user-facing
  direction. If not, decide whether to change the code or the docs.
- [Affects R8][Technical] Cache layer for parsed APKINDEX: parsing ~5MB per arch
  on every `yoe build` may be slow. Probably needs an in-memory or on-disk cache
  keyed by file hash; design at plan time.
