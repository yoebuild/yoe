---
date: 2026-05-13
topic: feeds-as-modules
---

# Feeds as synthetic modules with auto-discovered services

## Summary

A module's `modules` field generalizes into a priority-ordered list whose
entries can be local directory paths, remote module references, or distro feeds
(`alpine_feed(...)` in this spec; `debian_feed(...)` follows in a separate spec,
`ubuntu_feed(...)` after that). Each `*_feed` call materializes a synthetic
module backed by a checked-in upstream index (APKINDEX for alpine, `Packages`
for debian); synthetic modules rank below non-feed modules by default, so
hand-written `.star` files shadow them by name with no extra ceremony. A
feed-providing module ships a **companion layer** of hand-written units
alongside its feed indices — service-enabler units (`*-enable.star`) plus any
explicit overrides — which compose with the synthetic feed under the same
priority rule. Service enablement continues to use yoe's existing
`services = [...]` field on `Unit{}` (per CLAUDE.md "units declare their own
services"); the `*-enable.star` companion units make that opt-in explicit and
project-controllable.

**This is the single mechanism going forward** for distro-package consumption in
yoe. After this work lands, the auto-generated `alpine_pkg.star` directory in
`module-alpine` is gone; the synthetic-module-from-feed machinery is the only
path, used unchanged by Debian (`docs/specs/2026-05-25-module-debian.md`,
`docs/plans/2026-05-25-001-feat-module-debian-debian-backend-plan.md`) and any
future distro. The mechanism is therefore designed format-agnostic from day one
and to scale to Debian-class catalogs (60k+ packages per arch) via lazy
synthesis and on-disk parsed-index caching, not just to alpine's ~10k.

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
- A4. Yoe image assembler: extracts packages into rootfs. Service enablement
  happens at apk/deb package time (via the unit's `services = [...]` field,
  baked into the package), not via rootfs scanning at image-assembly time.

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
  the cycle path. Modules are deduplicated by **resolved commit SHA** — two
  module references resolve to the same module iff their resolved commit SHA is
  identical, regardless of URL form (`https://` vs `git@`) or ref form (tag,
  branch, or SHA). Local-filesystem modules dedupe by absolute path after
  symlink resolution.
- R5. Synthetic modules produced by `alpine_feed` (and future feed builtins)
  rank below all non-feed modules in the flattened priority list.
  `prefer_modules` is the targeted override when this default is wrong for a
  specific name. Worked example: given module list
  `[A (no feeds), B (declares alpine_feed), C (no feeds)]`, the flattened
  priority is `[A, B, C, alpine.main]` — all non-feed modules (in declaration
  order) come first, then all synthetic modules. Synthetics are NOT interleaved
  with their declaring parent; the rank-below-all-non-feed rule is global, not
  parent-local.

**`alpine_feed` builtin**

- R6. `alpine_feed(name, url, branch, section, index, keys)` registers a feed
  for the declaring module. Parameter semantics:
  - `name`: feed name used to compose the synthetic module name
    (`<parent-module-name>.<name>`, e.g., `alpine.main`).
  - `url`: mirror base URL (e.g., `https://dl-cdn.alpinelinux.org/alpine`).
  - `branch`: release branch path segment (e.g., `v3.21`, `edge`).
  - `section`: Alpine repository section — `main`, `community`, or `testing`
    (`testing` exists only on `edge`).
  - `index`: in-tree directory containing per-arch APKINDEX files (e.g.,
    `feeds/main/x86_64/APKINDEX` under the module root).
  - `keys`: list of in-tree paths to the RSA public key(s) trusted to sign
    upstream APKINDEX/apks for this feed.

  Minimal example:

  ```
  alpine_feed(
      name = "main",
      url = "https://dl-cdn.alpinelinux.org/alpine",
      branch = "v3.21",
      section = "main",
      index = "feeds/main",
      keys = ["keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub"],
  )
  ```

- R7. At project-eval, yoe materializes one synthetic module per `alpine_feed`
  call, named `<parent-module-name>.<feed-name>` (e.g., `alpine.main`). The
  synthetic module contributes one unit per APKINDEX entry whose arch matches an
  arch in active use by the project. **Multi-version tiebreaker:** when an
  APKINDEX lists multiple versions of the same package name (rare in stable
  Alpine, common in `edge`), the synthetic module exposes only the **newest**
  version per name (matching `apk add` host-system behavior). Edge/testing
  projects that need a specific older version pin via shadow `.star` in a
  higher-priority module.
- R8. Synthetic units are first-class — same `Unit{}` struct, same resolver
  entry points, same input-hash treatment — and carry metadata identifying their
  source feed for TUI display and diagnostics.
- R9. The apk dependency parser handles `name`, `name<rel>ver`,
  `so:<soname>[=ver]`, `cmd:<binary>[=ver]`, `pc:<pcname>[=ver]`, `/file/path`,
  and `!<conflict>`. A provides table built from the synthesized module's
  APKINDEX resolves so/cmd/pc/file-path lookups during name resolution.
  **Version-constraint behavior:** version operators (`>=`, `=`, etc.) and
  versioned-soname tails (`so:libcrypto.so.3=3.5.4-r0`) are parsed for syntax
  validity at yoe-load time but stripped before resolver lookup — yoe-side
  resolution operates on the name dimension only. Version enforcement is
  deferred to `apk` / `apt` at install time on the device, matching today's
  behavior. The multi-version tiebreaker (R7) ensures yoe-side resolution picks
  one concrete provider per name regardless of constraint.

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

**Service enablement: companion `*-enable.star` units**

- R13. Service enablement continues to use yoe's existing `services = [...]`
  field on `Unit{}` (per CLAUDE.md "Units declare their own services"). The
  field's effect is unchanged: at apk/deb package time, the runlevel symlinks
  (OpenRC `/etc/runlevels/default/<svc>` or systemd
  `/etc/systemd/system/multi-user.target.wants/<svc>.service`) are baked into
  the package. Installing the package on the device (or extracting it during
  image assembly) materializes the symlinks. No rootfs scanning, no image-level
  `enable_services` field.
- R14. **Companion `*-enable.star` units** are the canonical pattern for opting
  an image into service auto-start. A feed-providing module (today
  `module-alpine`; later `module-debian`) ships hand-written companion units
  alongside its feed indices. An enable unit is a normal yoe unit with no
  source/build steps, a `runtime_deps` pointing at the upstream package that
  ships the init script, and `services = [...]` declaring what to enable:

  ```
  # module-alpine/units/docker-enable.star
  unit(
      name = "docker-enable",
      version = "1",
      runtime_deps = ["docker-openrc"],
      services = ["docker"],
  )
  ```

  The build pipeline produces a tiny apk whose only content is the runlevel
  symlinks. Projects opt into auto-start by listing the enable unit in their
  image's artifacts; projects that want only the binaries (no auto-start) omit
  the enable unit. No new yoe class is required — `unit(...)` directly is
  sufficient.

- R15. Init system mapping (which target/runlevel the `services = [...]` field
  resolves to) is determined by the image unit's `init = "openrc" | "systemd"`
  field, which the image unit MUST declare explicitly. Missing `init` is an
  evaluation error, matching CLAUDE.md "explicit over implicit."

**TUI display**

- R17. The TUI surface that lists modules shows the flattened, recursive module
  list in resolved priority order, with each entry tagged by source (directory,
  remote module, feed) and (for synthetics) the parent module that declared the
  feed.

**Migration**

- R18. The transition removes the auto-generated `alpine_pkg` `.star` files from
  `module-alpine` in a **big-bang cutover commit** — the migration is the whole
  point of "one mechanism going forward." Hand-edited overrides survive by being
  lifted into thin `.star` files in a higher-priority module before cutover;
  cutover deletes the bulk of the cached unit directory. No gradual rollout
  where `alpine_pkg.star` and `alpine_feed` coexist long-term — that's exactly
  the two-mechanism trap this spec exists to avoid.
- R19. The existing `prefer_modules` entries that point at `module-alpine` (such
  as `testdata/e2e-project`'s `xz` pin) update to point at the specific
  synthesized feed module name. The package's upstream location determines the
  target: a package shipped from `main` resolves via `alpine.main`; a package
  shipped from `community` resolves via `alpine.community`. When a name is
  present in both feeds (rare in Alpine but real), the default resolution is
  `alpine.main` > `alpine.community` by feed declaration order in
  `module-alpine`'s `MODULE.star`; projects override via explicit
  `prefer_modules = {"pkg": "alpine.community"}`.
- R19a. The cutover MUST land `alpine_feed` declarations for **both `main` and
  `community`** in `module-alpine`'s `MODULE.star`, alongside a small
  hand-curated companion layer of `*-enable.star` units (and any explicit
  overrides) in `module-alpine/units/`. Today's hand-maintained layout ships
  ~3,672 main units + ~79 community units — the bulk auto-generated, with the
  community count artificially low because each package required a hand-edited
  `.star` file. After cutover the layout becomes:
  - `module-alpine/feeds/<section>/<arch>/APKINDEX` — checked-in upstream
    indices
  - `module-alpine/units/<service>-enable.star` — companion units, one per
    service the maintainer wants opt-in-able for projects
  - `module-alpine/units/<other>.star` — any hand-written override or custom
    addition that needs to shadow a synthetic feed entry
  - The 3,751-file auto-generated directory is deleted.

  Both full upstream APKINDEX catalogs (`main` ~3,800 packages, `community`
  ~5,000+ packages, per active arch) become available, with lazy synthesis (R20)
  ensuring no memory or hash-computation cost for unreferenced entries. This
  turns "I want to use Alpine community package X" from "hand-create an
  `alpine_pkg.star` and commit it to module-alpine" into "add the name to an
  image's `artifacts` and rebuild" — same UX as `apk add X` on a host system.
  Service auto-start remains opt-in via the companion enable units.

**Performance and scale**

This spec is the foundation for the Debian backend, whose `bookworm` catalog is
~60,000 binary packages per arch (vs. alpine's ~10,000). Eager materialization
of every entry as a `*Unit` would consume hundreds of MB of memory and add
multi-second project-load latency. The following requirements ensure the
mechanism scales to Debian-class catalogs while staying snappy at alpine scale.

- R20. **Lazy synthesis.** Synthetic units materialize only when the resolver
  references them by name. The synthetic-module registration captures the parsed
  index + provides table; per-entry `*Unit` construction defers until
  `module.Lookup(<name>)` is called. Materialized-unit count scales with the
  project's closure size, not the catalog size. A project that uses 300 debian
  packages out of bookworm's 60,000 materializes 300 units, not 60,000.
- R21. **On-disk parsed-index cache.** Parsed index data (entries, dep ASTs,
  provides table) serializes to a binary cache file next to each source index,
  keyed by the source file's content hash. Subsequent `yoe build` invocations
  load the cache (memory-map or fast deserialize) instead of re-parsing. Target:
  first build parses; subsequent builds load in &lt;300 ms even at Debian-class
  catalog size. Cache files are not committed (`.gitignore`d inside the module's
  feed directory).
- R22. **TUI shows project closure only; discovery via search.** Module-list
  and unit-list TUI surfaces show ONLY the project's closure (units actually
  referenced by the active machine's artifacts). There is no full-catalog
  pane mode. For "I want to find package X" workflows, the existing
  `tui-unit-query` search extends to query the synthetic-module catalog
  (read-only via `Names()`) without materializing units; results group by
  "in closure" vs "available in feed Y." For "I want to browse a section of
  the catalog" workflows, the idiom is a **virtual image** — a regular
  `image(name="catalog", artifacts=[...])` whose role is to pull a chosen
  subset into the closure. No toggle gestures, no mode multiplexing, no
  ~60k-entry pane renders.
- R23. **Format-agnostic infrastructure.** The resolver, the in-memory module
  and unit representations, and the TUI surfaces contain no format-specific
  (APKINDEX or deb822) logic. Adding `debian_feed` (Debian backend plan) or a
  future `ubuntu_feed` requires no changes to the shared resolver, module
  registration, lazy-synthesis materialization, or TUI machinery — only
  format-specific parsers (index parser, dep-syntax parser, provides-table
  builder) are new. The planning phase decides internal package layout.

**Performance budget (v1, against current alpine_pkg.star baseline):**

| Metric                                                     | Target                     |
| ---------------------------------------------------------- | -------------------------- |
| Project load time (cache-warm, alpine fixture)             | within 2× current baseline |
| Project load time (cache-warm, debian bookworm + 1 arch)   | &lt; 1 s                   |
| Working set (alpine fixture)                               | within 2× current baseline |
| Working set (debian bookworm + 2 arches, 300-unit closure) | &lt; 200 MB                |
| TUI initial render (project closure)                       | &lt; 200 ms                |

Budget enforcement: integration test in the planning phase runs against a real
bookworm `Packages` fixture and asserts these numbers. If v1 misses, fix before
ship.

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
  Additional canonical-identity cases: a module referenced once as
  `https://github.com/foo/bar` and once as `git@github.com:foo/bar.git` —
  resolved to the same commit SHA, deduped, no cycle. A module referenced by tag
  `v1.2.3` and by SHA `abc123` where the tag points to that SHA — deduped to one
  entry.

- AE4. **Covers R9.** Given an APKINDEX entry whose `D:` line includes
  `so:libcrypto.so.3=3.5.4-r0`, when the resolver walks the dep, it consults the
  provides table built from the same feed, finds the package whose `p:` line
  declares `so:libcrypto.so.3=<v>`, and adds that package as the runtime dep.

- AE5. **Covers R13, R14, R15.** Given an image with `init = "openrc"` lists
  `docker`, `docker-openrc`, and `docker-enable` in its artifacts, image
  assembly installs the docker binary (from `alpine.community`), the OpenRC
  init script (from `alpine.community`), and the tiny `docker-enable.apk`
  (built from `module-alpine/units/docker-enable.star`) whose only content is
  `/etc/runlevels/default/docker → /etc/init.d/docker`. The service starts at
  boot. A sibling image that omits `docker-enable` ships the binary and init
  script but does not auto-start docker.

- AE6. **Covers R11.** Given a maintainer runs `yoe update-feeds` inside
  `module-alpine`, yoe rewrites the in-tree `feeds/main/x86_64/APKINDEX` (and
  other declared arches) from upstream, leaves them staged, and exits. The
  maintainer then runs normal `git diff`, `git commit`, and `git push`.

---

## Example

The shape after migration, illustrated against a realistic edge-gateway
project consuming both `alpine.main` and `alpine.community`.

### `module-alpine/MODULE.star` — feed declarations

```python
module_info(
    name = "alpine",
    description = "Alpine Linux feed module: synthetic units from upstream APKINDEX plus companion enable units.",
)

alpine_feed(
    name = "main",
    url = "https://dl-cdn.alpinelinux.org/alpine",
    branch = "v3.21",
    section = "main",
    index = "feeds/main",
    keys = ["keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub"],
)

alpine_feed(
    name = "community",
    url = "https://dl-cdn.alpinelinux.org/alpine",
    branch = "v3.21",
    section = "community",
    index = "feeds/community",
    keys = ["keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub"],
)
```

These materialize as the synthetic modules `alpine.main` and
`alpine.community`. Hand-written companion units in `module-alpine/units/`
(such as `docker-enable.star`, `openssh-enable.star`) rank above the synthetic
feeds per R5.

### `module-alpine/units/docker-enable.star` — companion enable unit

```python
unit(
    name = "docker-enable",
    version = "1",
    runtime_deps = ["docker-openrc"],
    services = ["docker"],
)
```

No source, no build steps. The build pipeline produces a tiny apk whose only
content is the runlevel symlink `/etc/runlevels/default/docker →
/etc/init.d/docker`, materialized via the existing `services = [...]`
mechanism on `Unit{}`.

### `PROJECT.star` — consuming both feeds

```python
project_info(
    name = "edge-gateway",
    description = "Edge gateway device built on Alpine Linux.",
)

modules = [
    "local/module-core",
    module(url = "https://github.com/yoe-build/module-alpine", ref = "v3.21-1"),
    module(url = "https://github.com/yoe-build/module-rpi",    ref = "v0.5.0"),
]

# Pin a specific name to a feed when more than one module provides it.
prefer_modules = {
    # xz is source-built in module-core AND prebuilt in alpine.main.
    # Prefer the prebuilt (smaller, faster build).
    "xz": "alpine.main",
}

image(
    name = "edge-os",
    machine = "rpi5",
    distro = "alpine",
    init   = "openrc",
    artifacts = [
        # Core system — source-built from module-core
        "kernel-rpi5", "u-boot-rpi5", "busybox",

        # Prebuilt from alpine.main (resolves automatically by name)
        "openssh", "openssh-openrc",
        "openssh-enable",        # module-alpine companion: opts into sshd auto-start

        "chrony", "chrony-openrc",
        # No chrony-enable — chronyd ships but doesn't auto-start

        # Prebuilt from alpine.community (same resolver path)
        "docker", "docker-openrc",
        "docker-enable",         # opt into docker auto-start

        "python3", "python3-cryptography",

        # Project-built source unit
        "edge-agent",
    ],
)
```

### Resolution narrative

The image lists unit names directly. The resolver walks `module-core` first
(matches `kernel-rpi5`, `busybox`, `edge-agent`), then `module-alpine`'s
companion units (matches `openssh-enable`, `docker-enable`), then
`alpine.main` (matches `openssh`, `openssh-openrc`, `chrony`, `chrony-openrc`,
`python3`), then `alpine.community` (matches `docker`, `docker-openrc`,
`python3-cryptography`). The user never says "from alpine.community" — the
name resolves wherever it lives.

Auto-start is opt-in via the three-unit pattern (`docker`, `docker-openrc`,
`docker-enable`). To get docker available but not auto-starting, drop
`docker-enable`. To get only the docker CLI without dockerd, drop
`docker-openrc` too.

The same shape will carry to Debian when that lands: `debian_feed("bookworm",
"main")` and `debian_feed("bookworm-security", "main")` declared in
`module-debian/MODULE.star`, with companion `*-enable.star` units in
`module-debian/units/` for project-controlled service auto-start. Same
resolver, same project surface.

---

## Success Criteria

- A project that today consumes Alpine packages via the cached
  `module-alpine/units/main/` and `units/community/` directories builds an
  equivalent image after the change, with all of the cached `.star` files
  removed.
- Adding a new Alpine package (from **either main or community**) to a project
  requires zero file edits in `module-alpine` — listing the package name in an
  image's `artifacts` is enough. Today community packages outside the
  hand-curated ~79-package subset require a manual `alpine_pkg.star`
  contribution; after cutover, every community package is consumable
  immediately.
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
- `debian_feed` (and the Debian backend at large — dpkg parsing, deb extraction,
  apt-on-target, maintainer-script handling, signing) lives in a separate
  spec/plan (`docs/specs/2026-05-25-module-debian.md`,
  `docs/plans/2026-05-25-001-feat-module-debian-debian-backend-plan.md`). **This
  spec is the prerequisite foundation** — the Debian backend builds on the
  synthetic-module machinery, the resolver path, the on-disk cache, and the
  format-agnostic types defined here. Implementation order: feeds-as-modules
  ships first, Debian backend ships second.
- `ubuntu_feed` is mentioned for naming-convention purposes only — no
  implementation work in this spec or the Debian one. Follows when needed,
  reusing all the same machinery.

---

## Key Decisions

- **One mechanism forward.** After this spec ships, the auto-generated
  `alpine_pkg.star` directory is gone and `alpine_feed` is the only path for
  Alpine package consumption. `debian_feed` (Debian backend plan) uses the same
  machinery. `ubuntu_feed` and any future distro follow. Two parallel mechanisms
  (per-package `.star` files alongside synthetic-from-feed) is the trap to avoid
  — it doubles maintenance and confuses the resolver story. This decision drives
  the big-bang migration (R18) rather than long-lived coexistence.
- **Lazy synthesis turns the migration into a capability expansion.** Today's
  `module-alpine` ships a hand-curated ~79-package subset of Alpine community
  (out of ~5,000+ available) because every package costs a hand-edited `.star`
  file. After cutover (R19a), declaring one `alpine_feed("community", ...)`
  makes the full community catalog available; lazy synthesis (R20) keeps the
  memory and hash-computation cost proportional to _referenced_ units, not
  catalog size. This is a real product win, not just a code simplification — "I
  want X from community" becomes a one-line edit in an image's `artifacts`
  rather than a separate module-alpine commit.
- **Lazy synthesis as design discipline.** Synthetic units materialize on
  reference, not eagerly at feed parse time (R20). Reasoning: alpine at ~10k
  packages would tolerate eager materialization but Debian at 60k+ per arch
  would not. Building lazy from day one means alpine and debian use the same
  resolver path and neither pays a refactor cost later. Materialized memory
  scales with project closure, not catalog size.
- **On-disk parsed-index cache is v1, not deferred.** Parsing 50 MB of deb822
  Packages on every `yoe build` is unworkable; alpine's smaller APKINDEX doesn't
  strictly need caching but inherits it for consistency (R21). Cache files live
  next to source indices, keyed by content hash, gitignored.
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
  shape leaks. Internal code is still format-named and shared (R23).
- **Dot-style synthetic-module names.** `<distro>.<component>` for alpine (e.g.
  `alpine.main`); `<distro>.<suite>.<component>` for debian (e.g.
  `debian.bookworm.main`). The dot matches Starlark/Python attribute feel and
  reads cleanly inside `prefer_modules` dict literals. Resolves the Outstanding
  Question; same direction the Debian backend plan locks in.
- **APKINDEX checked into module git, not synthesized at build.** The module ref
  already pins the snapshot. No separate lockfile, no project-side bookkeeping.
- **Decompressed APKINDEX in tree.** Diff-readability on `yoe update-feeds` is
  the point. Size cost (~40MB for Alpine main+community across three arches) is
  one-time per module and git-deltas well.
- **Feed + companion module pattern.** A module that declares one or more
  `*_feed(...)` calls also ships hand-written companion units in `units/`.
  Companions serve three roles: service-enabler units (`*-enable.star`) that
  opt projects into auto-start, overrides that shadow synthetic feed entries
  to correct a field, and custom additions. Companions rank at the parent
  module's normal priority (above synthetics per R5) — so they shadow the
  feed's entries by name with no `prefer_modules` ceremony. This is the
  canonical shape for distro modules going forward (alpine today, debian and
  ubuntu when they land).
- **Service enablement stays unit-author-declared.** yoe's existing
  `services = [...]` field on `Unit{}` continues to bake the runlevel/target
  symlinks into the apk/deb at package time, exactly as it does today
  (`internal/artifact/apk.go:materializeServiceSymlinks`). Projects opt into
  service auto-start by listing a `*-enable` companion unit in their image's
  artifacts. The decoupled install-vs-enable model matches Alpine's own
  convention (where `docker` and `docker-openrc` are separately installable);
  yoe adds a third granularity (`docker-enable`) that is purely project-side
  intent. No rootfs scanning, no image-level `enable_services` field.

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
- Recursive module walking (R3) and module-sync recursion for transitively
  declared modules are new infrastructure. `module_info(deps=...)` currently
  parses into `ModuleInfo.Deps` but nothing reads the field; only the
  project-level `modules=[...]` is walked today (`loader.go:181`). Planning owns
  building the depth-first walk, module-sync recursion, and cycle detection.

---

## Outstanding Questions

### Resolved (committed in Key Decisions or requirements)

- Separator for synthetic module names → **dot** (`alpine.main`,
  `debian.bookworm.main`). See Key Decisions.
- Migration strategy → **big-bang** at cutover, per R18 and the one-mechanism
  commitment. See Key Decisions.
- Cache layer for parsed indices → **required, on-disk, content-hashed**. See
  R21.
- Init system detection → **explicit `init = "openrc" | "systemd"` field on
  image unit**. See R13. (Resolved during 2026-05-25 doc review.)
- Multi-version tiebreaker → **newest only**. See R7. (Resolved during
  2026-05-25 doc review.)
- Cycle detection canonical identity → **resolved commit SHA, URL/ref form
  agnostic**. See R4 + AE3. (Resolved during 2026-05-25 doc review.)
- `prefer_modules` mapping for `community` packages → **explicit feed name
  (`alpine.main` vs `alpine.community`); main wins by feed declaration order on
  collisions**. See R19. (Resolved during 2026-05-25 doc review.)
- Service auto-enablement design (M1 cluster, 7 personas) → **dropped
  rootfs-scan; kept unit-author-declared `services = [...]` baked into package
  at build time; companion `*-enable.star` units are the canonical opt-in
  mechanism**. See Key Decisions + R13/R14, AE5. The 2026-04-07-unit-services
  spec stays authoritative; no reversal. (Resolved during 2026-05-26 planning.)

### Deferred to Planning

- [Affects R10][Technical] APKINDEX in-tree storage arch scope:
  decompressed-all-arches (largest) vs decompressed-active-arches-only. (Key
  Decisions already commits to decompressed over compressed; this open question
  covers only the arch scope. The Debian backend plan commits to
  decompressed-active-arches-only for `Packages`; same default probably right
  here.)
- [Affects R12][Technical] Hash anchor implementation: use `C:` (Q1-SHA1)
  directly in `internal/resolve/hash.go`, or compute SHA256 at apk fetch and
  store both. Existing apks-from-tarballs path uses SHA256; consistency vs
  simplicity.
- [Affects R15][Technical] Service-template filter rules beyond `@` in the
  filename — e.g., are there OpenRC scripts that should be excluded by
  convention? Probably none, but verify with a real rootfs scan.
- [Affects R21][Technical] On-disk cache serialization format: protobuf, gob,
  msgpack, or hand-rolled binary. Constraints: fast load (mmap-friendly
  preferred), small, format-agnostic enough that both APKINDEX and `Packages`
  flavors share the format.

### From 2026-05-25 doc review (deferred for engagement)

These items surfaced in a multi-persona doc review and were deferred to the
planning phase rather than resolved inline. Each one carries the persona(s) that
flagged it and a one-line summary; the full review with evidence quotes lives in
the session transcript.

- [Affects R2][P1][Verification] **Priority direction conflicts with current
  resolver code** (design-lens, adversarial). `internal/starlark/builtins.go`
  (~L768) treats higher `ModuleIndex` as higher priority (later wins) while R2
  says "earlier wins." Verify direction, rewrite R2/R5/AE1/AE2 against confirmed
  direction.
- [Affects R18][P1][Process] **Big-bang migration needs rollback path and parity
  validation** (product-lens, adversarial, feasibility). Add a parity-validation
  step (resolve every name through both paths, assert identical
  `(version, runtime_deps, provides, replaces, apk_checksum)`) before the
  deletion commit. Use `scripts/gen-unit.py` diff to identify hand-edits before
  lifting. Document a tagged pre-cutover ref as the rollback path.
- [Affects R20][P1][Architecture] **Lazy synthesis conflicts with existing
  eager-walk code paths** (feasibility ×2, adversarial). Closure walk in
  `module-core/classes/image.star` and `BuildDAG` in `internal/resolve/dag.go`
  both iterate the full unit set today. Pick one path: (a) move
  `_resolve_runtime_deps` out of Starlark into Go where it can call
  `module.Lookup()` on demand, or (b) specify a lazy-proxy contract for
  `ctx.runtime_deps` / `ctx.provides`. Update R20's cost model to enumerate
  which data structures stay eager (provides table) vs lazy (Unit construction).
- [Affects R22][P1][UX] **TUI toggle mechanism for filter-first vs full-catalog
  browse is undefined** (design-lens). Specify the toggle gesture (keybinding,
  command, menu), the mode indicator in the pane header, and whether the mode
  persists per-session.
- [Affects R6][P1][Security] **Out-of-band key pinning to defend against
  module-repo compromise** (security-lens). The current trust model has both
  APKINDEX and signing keys in the same module git repo under the same access
  control. Define key-pinning at the consuming project level OR a documented
  key-review workflow where key changes require additional sign-off.
- [Affects R11][P1][Security] **`yoe update-feeds` must verify upstream APKINDEX
  signature before writing** (security-lens). Alpine signs `APKINDEX.tar.gz`;
  yoe currently has no requirement to verify the signature against the declared
  `keys` before rewriting the in-tree file. Without this, maintainer-diff review
  is the primary trust gate, not a secondary check.
- [Affects Performance budget][P2][Scope] **Budget table needs grounding or
  reframing** (feasibility, scope-guardian, adversarial, product-lens). Either
  move numeric targets to the implementation plan and replace with behavioral
  ceilings in the spec, OR keep targets but commit the alpine baseline (date-
  stamped, in-repo) before structural changes land plus a measurement protocol
  (fixture, harness, CI lane).
- [Affects R19a][P2][Risk] **Community-catalog expansion (~79 → ~5,000+) needs
  cutover safeguards** (product-lens, scope-guardian, adversarial). Soften the
  Success Criteria "one-line edit" claim to acknowledge the scriptlet
  limitation. Require a resolver smoke-run against the full community APKINDEX
  during cutover to surface name conflicts. Consider an opt-in `allow=[...]`
  field on `alpine_feed` for projects wanting curation.
- [Affects R17][P2][UX] **TUI module-list visual format** (design-lens). Specify
  minimum columns (name, type, source, unit-count), tree vs flat layout, and
  what drilling into a synthetic module shows.
- [Affects R12][P2][Security] **Verify full apk SHA256 at fetch time**
  (security-lens). `C:` covers the control segment only, not the data payload.
  CDN-or-mirror substitution of a malicious data segment passes `C:`
  verification today. Add SHA256-over-full-file verification at fetch time; `C:`
  retained for resolver identity.
- [Affects R21][P2][Reliability] **On-disk cache safety** (adversarial). Tighten
  R21: atomic write via tmp+rename to survive SIGINT; cache header with
  source-content hash AND yoe-internal format-version constant (mismatch →
  reparse); tolerance for missing/corrupt cache (treat as miss, not failure);
  explicit statement on multi-process concurrency safety.
