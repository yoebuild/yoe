<!--
Spec: Distro as unit identity
Date: 2026-05-29
-->

# Distro as unit identity: collapse the dual unit model

## Summary

yoe now builds both Alpine (musl) and Debian (glibc) images, so "distro" is a
real configuration axis: one logical name (`python3`, `meson`, `dev-image`) must
resolve to a different built artifact depending on the distro consuming it. The
codebase grew that support incrementally and currently runs **two unit models at
once** — a legacy flat name-keyed catalog with distro applied as a post-hoc
filter, and a newer `(distro, name)`-keyed view. The reconciliation seam between
them (`lookupOrMaterialize`, `visibleToDistro`, `AnyUnit` shadow resolution) is
where the recent run of distro bug-fixes all landed. This spec proposes
finishing the migration to a single `(distro, name)`-keyed model, making distro a
typed value decided once at well-defined boundaries and flowed thereafter, and
deleting the flat model. The goal is a **net reduction** in distro-branching code
and an end to the per-call-site bug class — not a new feature.

## Problem Frame

### Distro is a configuration axis threaded by hand

`grep -ci distro` finds ~800 references across 37 non-test Go files. Most are
legitimate *consumers* — cache marker paths, sysroot assembly, source prep, repo
subtrees, build dirs — that correctly need to know the distro. Threading a value
to consumers is not the problem. The problem is that the value is stringly-typed,
overloaded, and re-decided at the leaves.

### Two coexisting representations

1. **Flat model (legacy).** The engine holds `units map[string]*Unit` keyed on
   bare name. Distro is applied *after* lookup as a visibility filter
   (`visibleToDistro`), backed by a ~60-line `lookupOrMaterialize` that walks
   synthetic feed modules in priority order, probes `prefer_modules` pins, and
   reconciles cross-distro name collisions. Its own comments record that the
   "probe every synthetic on every lookup" approach was tried and pulled because
   it became "a multi-GB hot loop." `Project.AnyUnit` mirrors this flat
   shadow-resolution (highest module priority wins).

2. **Keyed model (newer).** `Project.DistroViews map[string]map[string]*Unit` is
   a precomputed `(distro, name) → *Unit` map, read via `LookupUnit(distro,
   name)` — O(1), no priority rescan, no synthetic walk. This is the correct
   shape; it just isn't the source of truth yet.

`lookupOrMaterialize` exists only to bridge the two. Every recent distro fix is
friction at this seam:

| Commit | Symptom | Seam |
| --- | --- | --- |
| `ff50f6b` | virtual deps resolved via global `Provides` | distro applied after resolution, not in the key |
| `1d1a8a1` | image built in project default distro, not its own | flat model has no per-target distro concept |
| `3532ca8` | same-named images collide; module priority picks wrong variant | `AnyUnit` shadow resolution decides distro at a leaf |
| `39c122e` | `python3` materialized once with empty distro, shadowing the other feed | `""` distro flowing into `lookupOrMaterialize` |
| `147994d` | split-deb runtime closure missing from sysroot | *not* a distro-identity bug — provider/closure propagation (out of scope) |

### Two specific smells

- **`distro` is a bare `string` with an overloaded `""` sentinel.** Empty means
  three different things depending on context: on a non-image unit's `Distro`
  field it means "visible to every distro" (a legitimate value); as a function
  argument it means "default / not yet decided"; in resolution it triggers a
  fallthrough. A typed value with a loud zero would have made `39c122e`
  impossible at compile time.
- **Distro is decided at the leaves, not once at the top.** Three resolvers exist
  — `EffectiveDistro()`, `EffectiveDistroForImage(name)`, and the bare
  `DefaultDistroOverride → DefaultDistro` chain duplicated inline in several
  places. `3532ca8` was a leaf calling the wrong one. Every new leaf that
  re-derives distro is a fresh chance to derive it wrong.

### Why not just keep patching

The number of distro touchpoints is not the disease and will not shrink — a
configuration axis legitimately reaches many consumers. The disease is (a) two
models with a hand-written reconciler between them, and (b) a value that is
decided in N places instead of one. Both are fixable structurally, and the fix
removes code rather than adding it.

## Goals

- One authoritative `(distro, name)`-keyed unit catalog; the flat model and its
  reconciliation seam deleted.
- Distro represented as a typed value whose zero state is invalid and fails
  loudly, with "visible to all distros" expressed as a distinct explicit value
  rather than `""`.
- Distro **decided once** per top-level target at enumerated boundaries, then
  flowed as a typed argument; leaf functions consume it and never re-derive it.
- Feed laziness preserved — working memory tracks closure size, not catalog size.
- A documented governance rule (consume vs decide) that prevents re-sprawl.
- Net-negative diff in distro-branching code.

## Non-Goals

- **No `select()` / configuration-transition DSL.** Importing Bazel/Buck2's
  configurable-attribute language would be the over-engineering this spec exists
  to avoid; it fights "explicit over implicit" and "one unit, one .apk; resolve
  variation at runtime."
- **No change to cache/hash semantics.** musl and glibc variants still build
  separately — that is correct, not a regression. `UnitHash` continues to key on
  the image's effective distro, so existing caches stay valid.
- **No new distros.** Scope is Alpine + Debian as they exist today; the design
  should not *preclude* a third, but adding one is out of scope.
- **Split-package runtime-closure propagation** (`147994d`) is a separate
  concern (provider/closure semantics) and is not addressed here.

## Requirements

**R1 — Typed `Distro` value.** Replace bare `string` distro parameters and
fields with a `Distro` type. The zero value is invalid: using it in a resolution
or path-building context must fail loudly (error or panic at the boundary), never
silently mean "default" or "all." The legitimate "this unit is compatible with
every distro" tag is a *distinct, explicit* value (e.g. `AnyDistro`), separated
from "no distro has been decided." This single change makes the `39c122e`
empty-string bug class unrepresentable.

**R2 — Single keyed catalog as source of truth.** Collapse the engine's flat
`units map[string]*Unit` and `Project.DistroViews` into one authoritative
`(distro, name) → *Unit` structure with a single owner. Lookup is a direct keyed
read. Delete `lookupOrMaterialize`'s priority walk / pin-probe / visibility
reconciliation, the `visibleToDistro` post-filter, and `AnyUnit`'s
shadow-resolution fallback (except the narrow bootstrap case in R4).

**R3 — Lazy population preserved.** The keyed catalog populates on miss by
materializing the synthetic feed's unit for that specific `(distro, name)`. No
eager full-feed materialization. This preserves the closure-size-not-catalog-size
property and resolves the multi-GB hot-loop concern that originally shaped
`lookupOrMaterialize` (the precomputed/lazy keyed read is the O(1) structure that
concern called for).

**R4 — Decide distro once, at enumerated boundaries.** The only sanctioned
places that *decide* a distro are: the project default/override, and an image's
own `Distro` field (per-target). Resolve the consuming distro once per top-level
target and pass it down as a typed value. There is a genuine chicken-and-egg:
reading an image's own `Distro` to choose its closure's distro requires a
distro-agnostic peek at one unit. Define a single narrow, explicitly named API
for that bootstrap peek (the only sanctioned descendant of `AnyUnit`); it is not
a general escape hatch.

**R5 — Collapse the three resolvers.** `EffectiveDistro`,
`EffectiveDistroForImage`, and the inline `DefaultDistroOverride → DefaultDistro`
chain reduce to one place that owns precedence, with image-effective delegating
to it. No other site re-implements the cascade.

**R6 — No compatibility shims (pre-1.0).** Delete the flat model outright. Migrate
tests off `SetFlatUnits` to keyed construction (provide a keyed test helper if
needed). No legacy conversion path.

**R7 — Net-negative brittleness budget.** The change retires more distro-branching
code than it adds. Concretely, `lookupOrMaterialize`'s reconciliation,
`visibleToDistro`, and `AnyUnit`'s shadow fallback are gone, and the count of
distro *decision* sites drops from ~3-smeared-across-leaves to the boundaries in
R4.

**R8 — Governance rule documented.** Record the durable guard in the appropriate
project doc / CLAUDE.md: a new distro touchpoint may *consume* a carried `Distro`
freely; it may *decide* a distro only at the R4 boundaries. Count decision
points, not usage points.

**R9 — Hash and cache neutrality.** The cutover must not change unit input
hashes; existing caches stay valid. Verify `UnitHash` still keys on the image's
effective distro and not on the unit compatibility tag.

## Key Decisions

- **KD1 — Distro is modeled as identity, not a post-hoc filter.** This is the
  "configured target" idea (one node per `(label, config)`) borrowed from
  Bazel/Buck2 — but borrowed as a *data-model* decision only, without their
  configuration-language machinery.
- **KD2 — Reject `select()` / transitions DSL.** Explicitly out, as the
  complexity this effort is meant to remove.
- **KD3 — Single keyed catalog, lazily populated.** Simultaneously kills the
  collision/shadowing bug class and satisfies the performance constraint that
  produced `lookupOrMaterialize`.
- **KD4 — Typed `Distro`, loud zero, explicit `AnyDistro`.** The overloaded `""`
  is the root of the empty-distro bugs; splitting "any" from "undecided" removes
  it.
- **KD5 — Decide-once-then-flow, with a codified governance rule.** Caps decision
  points structurally and keeps them capped over time.

## Scope Boundaries

**In scope:** the engine/project unit catalog and lookup path
(`internal/starlark/{engine,closure,loader,types}.go`), the `Distro` type and its
threading through build/resolve/image/source/device call sites, the three
resolver functions, and the test fixtures that construct catalogs.

**Out of scope:** split-package runtime-closure propagation (`147994d`); adding a
third distro; any change to packaging format, toolchain selection, or repo layout
beyond what the type change mechanically touches; the cache hash algorithm.

## Success Criteria

- All five recent distro bug scenarios are either unrepresentable by construction
  (collisions, empty-distro shadowing) or covered by a regression test
  (per-image distro, variant selection).
- `lookupOrMaterialize`, `visibleToDistro`, and `AnyUnit` shadow fallback are
  deleted; lookup is a single keyed read with one lazy-materialize-on-miss path.
- Distro decision sites are limited to the R4 boundaries; no leaf calls an
  `EffectiveDistro*` resolver.
- `go build ./...` and the full test suite pass; the e2e project building **both**
  an Alpine image and a Debian image produces unchanged input hashes (no cache
  churn) and the Debian image still boots in QEMU and accepts SSH.
- The diff is net-negative in distro-branching lines.

## Outstanding Questions

### Resolve before planning

- **`Distro` representation.** `type Distro string` (cheap, mechanical migration)
  vs a small struct. Is libc family (musl/glibc) a *separate* axis from distro,
  or fused into it as today? Today distro implies libc; if we ever want
  alpine-glibc or a glibc-musl split independent of distro, fusing now is a
  future refactor. Recommend fusing for now (one axis) and noting the seam.
- **Catalog owner.** Does the single keyed catalog live on the `Engine` or the
  `Project`? Today it is split (engine materializes, project views). Pick one
  owner and one population path.
- **Bootstrap-peek API.** Exact shape of the sanctioned `AnyUnit` replacement for
  reading an image's `Distro` before the consuming distro is known.

### Deferred to planning

- Migration sequencing: mechanical `string → Distro` type change first
  (compiler-driven, zero behavior change), then collapse the catalog as a second
  step — vs a single cutover.
- `SetFlatUnits` test-helper replacement and the blast radius across existing
  starlark tests.
