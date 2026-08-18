<!--
Plan: Distro as unit identity — implementation
Date: 2026-08-04
Spec: docs/specs/2026-05-29-distro-as-unit-identity.md
-->

# Distro as unit identity: implementation plan

Implements
[distro-as-unit-identity](https://github.com/yoebuild/yoe/blob/main/docs/specs/2026-05-29-distro-as-unit-identity.md),
folding in the supporting findings from
[the internal simplification review](https://github.com/yoebuild/yoe/blob/main/docs/plans/2026-08-03-internal-simplification-plan.md).

## What this plan assumes is already done

The simplification review's Phases 0–3 landed first, and three of its catalog
findings landed with them. Those are not repeated here:

- Cross-distro same-name units no longer report as shadowing, and the
  lower-priority one is no longer dropped from the per-module catalog (the
  review's F-S5).
- The kwarg-to-field mapping is one table that also derives the reserved-kwarg
  set, so a field cannot be added without being excluded from the hashed `Extra`
  map (F-S6).
- The closure walk resolves each dependency once, during the walk, and sorts
  topologically with Kahn's algorithm over a sorted ready queue (F-S2). The
  order is now reproducible rather than dependent on Go's map iteration.
- `ResolveProvidesForDistro`'s per-distro tier reads a precomputed index instead
  of scanning every unit in every module (F-S1, first half).

## The measurement that shapes this plan

Before assuming any of this is a speedup, it was measured. Loading the e2e
project (710 units in the Ubuntu view, an apt feed with a 50k-entry index) takes
~1.9s, and neither the closure rewrite nor the provides index moved it. The cost
is in feed index parsing, not in the resolution walks.

Two consequences:

1. **Do not sequence this work as a performance project.** The reason to
   collapse the catalog is that the seam between two models is where the distro
   bug class lives. Perf is not the payoff and should not be used to justify a
   risky step.
2. **The one measured hot spot is feed index parsing.** If perf becomes the
   goal, that is where to look, and it is out of scope here.

## The trap this plan exists to avoid

An attempt to replace the build-dependency fixpoint (`loader.go`, the
`for { added := 0; for d := range distroSet { for name := range eng.Units() ... } } }`
loop) with a per-distro worklist was made and reverted. It dropped 411 of 699
units from the Debian view and 342 of 710 from Ubuntu. Two things were wrong,
and both are easy to repeat:

- **Materialization can register a unit under a name other than the one looked
  up.** Queueing the name that was looked up leaves the unit that actually
  appeared with its own edges never walked, so the whole tail of a feed's
  closure goes missing.
- **The fixpoint's convergence is global across distros, not per distro.** The
  distro loop sits _inside_ the round loop, so a unit materialized while
  processing Debian is visible when the next round processes Ubuntu, and a unit
  that was not resolvable in round N can become resolvable in round N+1. A
  worklist that finishes one distro before starting the next does not reproduce
  that.

So: **do not rewrite the fixpoint as a standalone optimization.** It is listed
as a step below, but only after the catalog is keyed — at which point
"materialize `(distro, name)` on miss" makes the fixpoint's job small enough
that its shape stops mattering.

Whatever replaces it must be verified the way the failure was found:
`yoe build --distro <d> --dry-run` on `testdata/e2e-project` for alpine, debian,
and ubuntu, diffed against the same command before the change. Unit _count_ is
the signal that caught this; hash equality alone would not have, because the
missing units were simply absent rather than different.

## Decisions the spec left open

**Distro representation** — `type Distro string`. The migration is then
compiler-driven and mechanical, which is what makes step 1 safe to land on its
own. Libc family stays fused into distro, as today: nothing in the tree wants
alpine-glibc, and splitting the axis is a separate change with its own reason to
exist. Record the seam in `docs/distro.md` rather than building for it.

**Catalog owner** — the `Engine`. It is what materializes, and materialization
is the only thing that mutates the catalog. `Project` keeps a read handle
populated at the end of loading, which is what every consumer outside the loader
already uses. Making `Project` the owner would mean the engine mutating another
object's state throughout evaluation, which is the arrangement being removed.

**Bootstrap peek** — `Project.PeekImageDistro(name string) (Distro, error)`.
Deliberately narrower than a general `AnyUnit`: it answers one question, for
image units only, and returns an error rather than a unit, so it cannot become
the escape hatch `AnyUnit` became. Everything else that reaches for `AnyUnit`
today either has a distro available or is resolving a container reference, which
`resolve.ResolveContainer` already handles.

## Steps

Each step builds, vets, and passes the full suite on its own, and each is
verified against the e2e project as described under Verification.

### Step 1 — Target-state docs

Rewrite `docs/distro.md` and the catalog section of
`docs/naming-and-resolution.md` to describe the single keyed catalog, the typed
`Distro`, and the decide-once rule — in final form, with no "planned" markers,
per the project's convention once a plan commits. Add the governance rule from
the spec's R8 to `CLAUDE.md`: a new touchpoint may consume a carried `Distro`
freely and may decide one only at the enumerated boundaries.

This lands first so the target is reviewable before any code moves.

### Step 2 — Typed `Distro`, mechanical

Introduce `type Distro string`, `AnyDistro`, and an invalid zero. Convert fields
and parameters, letting the compiler find the call sites. No behavior change:
this step is complete when the tree builds and every test passes with no logic
edited.

Two places need judgment rather than a mechanical swap:

- `Unit.Distro`, where `""` legitimately means "every distro" today. That value
  becomes `AnyDistro`, and the Starlark parse layer maps an absent `distro =`
  kwarg to it. Hash input must not change — see Verification.
- Function parameters where `""` currently means "not decided". Those become the
  invalid zero, and every one of them is a site that must either receive a
  decided distro or fail loudly.

### Step 3 — Collapse the resolvers

Reduce `EffectiveDistro`, `EffectiveDistroForImage`, and the inline
`DefaultDistroOverride → DefaultDistro` chain to one owner of precedence, with
the image-scoped form delegating to it. Add `PeekImageDistro`. No leaf may call
a resolver after this step; a leaf that needs a distro receives one.

### Step 4 — One keyed catalog

Collapse `Engine.units`, `Engine.unitsByModule`, `Project.UnitsByModule` and
`Project.DistroViews` into one `(Distro, name) → *Unit` structure owned by the
engine, populated lazily on miss by materializing that specific key. Delete
`lookupOrMaterialize`'s priority walk, pin probe and visibility reconciliation;
delete `visibleToDistro` and `AnyUnit`'s shadow fallback.

The lazy-on-miss property is not optional: a Debian feed's index has 50k+
entries and eager materialization is what the current design was built to avoid.
`SyntheticModule.Names` must still not allocate a `*Unit`, and `closure_test.go`
guards that.

### Step 5 — Simplify the build-dependency fixpoint

Only after step 4. With `(distro, name)` as the key, the fixpoint no longer has
to reconcile which variant a name means for a distro, which is what made its
shape load-bearing. Re-derive it from what it now needs to do rather than
porting the existing loop.

Read the trap section above before starting. Verify by unit count per distro,
not by hashes.

### Step 6 — Tests and fixtures

Migrate tests off `SetFlatUnits` to keyed construction. Add regression tests for
the scenarios the spec lists: per-image distro selection, cross-distro variant
selection, and the empty-distro shadowing case (which should by then be
unrepresentable — assert that the type makes it so, rather than testing the old
behavior).

### Step 7 — Close the gap between docs and code

Re-read what step 1 wrote against what shipped, and fix whichever is wrong.
Record the measured e2e numbers.

## Verification

Run for every step, not only at the end.

**Hash and membership neutrality.** For each of alpine, debian, ubuntu:

```sh
cd testdata/e2e-project
YOE_PROJECT=. yoe build --distro <d> --dry-run | sed 's/ \[cached, skip\]//' | sort
```

Diff against the same output from the previous commit's binary. Both the hashes
and the number of lines must match. The line count is what catches a unit
silently dropping out of the catalog, which is the failure mode this area
actually has.

Expected counts as of this plan: alpine 396, debian 699, ubuntu 710.

**Determinism.** Run each of the above five times and confirm identical output.
Resolution feeds an image's artifact list; an order or membership that varies
between runs is a difference with no cause behind it.

**Boot.** At the end of step 4 and again at step 7, build and boot one image per
distro in QEMU and confirm SSH. Neither the dry-run diff nor the test suite
covers rootfs assembly.

## What this plan does not do

- It does not add a third distro, change packaging format, toolchain selection,
  or repo layout.
- It does not touch split-package runtime-closure propagation.
- It does not chase load time. The one measured hot spot is feed index parsing,
  which is untouched here and would be its own effort.
