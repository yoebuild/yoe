# Machine/distro compatibility — Implementation Plan

> **Status:** Not started. Blocks the first build for any single-distro machine,
> including `arduino-uno-q` from
> [uno-q-flashing](2026-07-27-001-uno-q-flashing-plan.md).

## Problem

Selecting a machine whose kernel exists for only one distro breaks project
evaluation — before any build starts, and regardless of which image is being
built.

`image()` in `modules/module-core/classes/image.star` resolves the selected
machine's kernel for every image in every loaded module, at image-definition
time. When the machine's `kernel.distro_unit` has no entry for that image's
effective distro it calls `fail()`; when the machine uses the flat
`kernel(unit = …)` form, the unresolvable name surfaces from the closure walker
instead (`internal/starlark/closure.go`, `unresolved name %q`). Either way the
whole project fails to load:

```
$ yoe desc base-image        # machine = arduino-uno-q
Error: evaluating cache/modules/module-alpine/images/bun-image.star:
image bun-image: machine kernel has no entry for distro "alpine"
```

`bun-image` is an Alpine image that nobody asked to build for a Qualcomm board.
It fails because every image is evaluated against the one selected machine.

Both obvious workarounds are dead ends, verified:

- **Drop `@module-alpine`** — module-core units reach into the Alpine feed, so
  the project stops evaluating for a different reason:
  `unit "python-hello" depends on "py3-pip", which does not exist`.
- **Use a separate project for the board** — `CLAUDE.md` requires test builds to
  live in `testdata/e2e-project` and forbids spinning up a parallel project.

So this is the gate for a first UNO Q build, and it will gate every future
single-distro machine identically.

## Why not defer resolution to build time

The tempting fix — stop resolving closures during evaluation, do it when a build
runs — is the wrong trade. An image's resolved artifact list is not just a build
input:

- It feeds the **input hash**: `internal/resolve/hash.go` writes a
  `packages:<sorted artifacts>` line in the image-specific block.
- It becomes **DAG build edges**: `internal/resolve/dag.go` appends
  `unit.Artifacts` to an image's deps.
- It is **displayed**: `yoe desc` prints it, and the TUI's tree reads
  `Artifacts` / `ArtifactsExplicit`.

So an image's hash cannot be computed without first resolving it, and deferral
means moving all three of those consequences.

It is worth being precise about what deferral does _not_ break, because the
stronger version of this argument is wrong. The layer beneath Starlark is
already request-scoped:
`ComputeAllHashes(dag, arch, machine, srcInputs, effectiveDistro)` takes a
single context for an entire walk, and each of its three callers supplies one —
the executor from `opts`, the TUI from `Defaults.Machine`, `describe.go` with
empties. Resolving one image on demand and hashing it is therefore structurally
fine. What deferral would cost is hashing _every_ image without being asked,
which is a weaker property than it sounds and one a cheap-list/expensive-detail
split could preserve.

Deferral is rejected here on **size**, not on impossibility. It moves the
resolution boundary through the engine, the resolver, the image class, and the
display paths, and it puts a request-order dependency in the middle of feed
materialization (see the risks below). This plan is a marker field and one
branch. Doing the larger change first, to avoid the smaller one, would be
backwards — but it remains the better end state, and nothing here should make it
harder to reach.

## Design

**A machine's `kernel.distro_unit` keys are its declaration of which distros it
supports.** `image()` consults them before doing any work:

| Machine kernel form          | Image's effective distro | Behavior                                                        |
| ---------------------------- | ------------------------ | --------------------------------------------------------------- |
| `distro_unit = {debian: …}`  | `debian`                 | Resolve as today.                                               |
| `distro_unit = {debian: …}`  | `alpine`                 | Not buildable on this machine — skip, register, do not `fail()` |
| `unit = "linux-rpi5"` (flat) | any                      | Unchanged: the machine claims distro-neutrality.                |

For a non-matching image, `image()` skips the kernel resolution, the machine
package merge, and the closure walk, and registers the image marked **not
buildable on this machine**.

Three things make this the right shape:

**No new field, and no second source of truth.** A `distros = [...]` list on
`machine()` was considered and rejected: `distro_unit` already encodes exactly
this, and two declarations of the same fact can disagree. The keys _are_ the
declaration.

**It is not error-swallowing.** `CLAUDE.md` is explicit that silent failures are
bugs. This is a declarative pre-check, not a caught exception — the resolution
is never attempted, so there is no error to swallow. The flat-`unit` case keeps
failing loudly, which is correct: a machine using the flat form is asserting its
kernel works on every distro, and if that is false the assertion should break.

**Unbuildable stays loud at the point of use.** Skipping quietly during
evaluation is only acceptable if asking for the thing gives a real answer:

```
$ yoe build bun-image --machine arduino-uno-q
Error: image "bun-image" (distro "alpine") is not buildable on machine
"arduino-uno-q", whose kernel supports: debian
```

## Phase Overview

- **Phase 1** — Target-state reference doc.
- **Phase 2** — The compatibility check and the not-buildable marker.
- **Phase 3** — Loud errors and honest display at the point of use.
- **Phase 4** — Tests.
- **Phase 5** — Verify; changelog.

## Phase 1: Target-state docs

### Task 1.1: `docs/distro.md`

The per-distro kernel section already says the `"linux"` artifact resolves to
`distro_unit[effective_distro]` and that "the resolution happens when the image
is evaluated, the only point at which the effective distro is known." Extend it
with the consequence that sentence implies but does not state: a machine that
declares `distro_unit` supports exactly those distros, images targeting any
other distro are not buildable on it, and selecting such a machine no longer
breaks evaluation for unrelated images. Include the error text a user sees when
they ask for one.

Write it in final voice — no `(planned)` flag, no Status blockquote — per the
final-form-during-plan rule. Keep plan vocabulary out.

### Task 1.2: Index

Append a row to `docs/SPEC_PLAN_INDEX.md` in the same commit.

## Phase 2: The compatibility check

### Task 2.1: A not-buildable marker on `Unit`

Add a field recording that an image was registered without resolution because
the selected machine cannot boot its distro — carrying the machine name and the
supported distro set, so the error in Phase 3 can be specific rather than
generic.

**Cache neutrality.** Do not write the marker into the input hash at all: a
marked image's hash is never used to key a build (Task 3.2 concedes exactly
this), so hashing the field buys nothing and neutrality holds trivially by
omission. If a reason to hash it emerges during implementation, the
`fmt.Fprintf` in `internal/resolve/hash.go` must be gated on non-empty per the
content-addressed-caching rule — an unconditional write invalidates every unit's
hash the moment it lands and forces a full rebuild. Either way, the Phase 4
"existing hashes unchanged" test is the backstop.

### Task 2.2: The branch in `image()`

In `modules/module-core/classes/image.star`, replace the `fail()` on a missing
`distro_unit` entry with the skip path: no kernel resolution, no
`ctx.machine_config.packages` / `distro_packages` merge, no `resolve_closure`,
and `unit(...)` registered with the marker, an empty artifact list, **empty
`deps`, and no tasks**. The deps/tasks part matters: today `image()` appends
`container` to deps and builds `rootfs`/`disk` task closures over `resolved`.
Keeping the container dep would give `dag.go` a build edge from an image that
can never build, and the task closures cannot be constructed anyway because
`resolved` never exists on the skip path. The registered unit must be genuinely
inert.

**The check moves up.** The machine-package merge currently runs _before_ the
kernel check (`image.star` merges at ~125–131, checks at ~139–150), so the
compatibility check must be hoisted to just after `effective_distro` is settled
(~102–108) — otherwise the merge still runs on the skip path.

Keep the existing `fail()` for the genuinely contradictory case — a kernel
setting both `unit` and `distro_unit` — which `fnMachine` already rejects.

## Phase 3: Loud at the point of use

### Task 3.1: `yoe build`

Refuse a marked image with the machine, the image's distro, and the supported
set named. This is the error that replaces the evaluation-time crash, so it
carries the whole diagnostic burden — an unhelpful message here would just
relocate the confusion.

**Two entry paths, two behaviors.** `BuildUnits` only filters to requested
targets when names are given (`internal/build/executor.go`); with no names it
builds the entire per-distro DAG, and a marked image sits in its own distro's
view — so `yoe build --distro alpine` with no target on a debian-only machine
sweeps the marked image into the build order.

- **Named target** → hard error, as above. The user asked for this image; the
  answer is "not on this machine," loudly.
- **Swept into an unnamed full build** → skip it and print a notice naming the
  image, the machine, and the supported set. A hard error here would make full
  builds impossible in any mixed-distro project — recreating the original
  problem one level up — while a _silent_ skip violates the silent-failure rule.
  The notice is the middle path.

### Task 3.2: `yoe desc`

A marked image's artifact list is empty and its input hash is therefore computed
over nothing. Say so rather than printing an empty `Artifacts:` line and a hash
that looks meaningful. The hash is never used to key a build for this machine,
but displaying it unqualified invites someone to compare it against a real one.

### Task 3.3: TUI

Mark such images rather than hiding them. Hiding would make a project look like
it has fewer images than it does depending on which machine is selected, which
is the opposite of explicit.

## Phase 4: Tests

- A project with a `distro_unit`-only machine and images from another distro
  evaluates cleanly — the direct regression test for the reported failure.
- A matching image still resolves its kernel via `distro_unit` and gets the
  machine's `packages` / `distro_packages` merged.
- A flat-`unit` machine is unchanged, including that an unresolvable kernel name
  still errors.
- Building a marked image errors with machine, distro, and supported set present
  in the message.
- An unnamed full build whose per-distro DAG contains a marked image skips it
  with a notice and builds the rest — it does not error and does not skip
  silently.
- **Existing units' input hashes are unchanged** by the new field. Compare a
  known hash before and after; this is the test that catches an ungated
  `Fprintf`.
- **Skipping an image does not change the resolution of any image that is not
  skipped.** Resolve a Debian image in a project with Alpine images present,
  then again with them removed, and assert the artifact list and input hash are
  identical. This is the test for the shared-catalog risk below, and the one
  most likely to fail in a way nobody notices.

## Phase 5: Verify and close

- `testdata/e2e-project` selects `arduino-uno-q` and evaluates with
  `@module-alpine` still loaded — the condition that is impossible today.
- `yoe build base-image --machine arduino-uno-q --distro debian --dry-run`
  resolves a closure containing the Arduino feed's kernel and board packages.
  The `--distro debian` is load-bearing: e2e-project's `defaults.distro` is
  `alpine`, `--machine` does not influence the effective-distro cascade
  (`image.distro → override → defaults.distro`), and without the flag
  `base-image` evaluates as an alpine image on a debian-only machine — i.e. it
  gets marked and the dry run hits the Task 3.1 refusal instead of resolving.
- The same command _without_ `--distro debian` produces the Task 3.1 error — the
  footgun above doubles as a live check of the refusal path.
- Re-read `docs/distro.md` against shipped behavior; close any gap.
- Changelog entry, user-facing: selecting a single-distro machine no longer
  breaks projects that also contain images for other distros.
- Flip this plan's row in `docs/SPEC_PLAN_INDEX.md`.

## Risks and open questions

- **Skipping changes the shared catalog, and resolution order matters today.**
  This is the subtlest risk in the plan. `lookupOrMaterialize`
  (`internal/starlark/closure.go`) registers materialized feed units into the
  shared `e.units` map under their bare names, guarded by an explicit first-wins
  rule — the code comments it as "register under the bare name only if not
  already taken, so the first-evaluated image's resolution stays visible." Today
  the order is deterministic because it follows `.star` evaluation order. This
  plan changes the _set_ of images that resolve at all, so it changes which feed
  units land in the catalog and in what sequence. A bare-name lookup for an
  image that still resolves could therefore pick a different variant than
  before, which would move its hash. `visibleToDistro` and `findVisibleByName`
  should make this safe, but "should" is why Phase 4 asserts it rather than
  assuming it.
- **The underlying architecture is unchanged.** yoe still evaluates every image
  against one globally selected machine; this plan makes the mismatch a
  first-class outcome instead of a crash. The deeper fix is to make the
  resolution context — arch, machine, distro — an explicit value rather than
  engine state, and resolve per (image, context) on demand.

  Two things make that more tractable than it looks. The layer beneath Starlark
  is already request-scoped (see "Why not defer resolution to build time"), so
  the change is confined to the evaluation phase. And **host-scoped units need
  the same refactor**: a host-arch tool resolving against a target-arch feed in
  one pass is the identical "context as a value" problem, and it is on the
  critical path for flashing. Doing that refactor once, driven by the feature
  that needs it now, is preferable to each feature bending `Engine.activeArch`
  its own way.

  Related: yoe now has four hand-rolled per-distro selectors —
  `distro_artifacts`, `distro_packages`, `distro_unit`, `distro_deps` — each
  with its own resolution code, and this plan fixes the compatibility bug in
  only one of them. A single configuration-dependent-value mechanism resolved in
  one place would collapse all four. Nothing here should entrench
  `distro_unit`-specific logic that makes that consolidation harder.

- **The flat-`unit` diagnostic stays poor.** A flat-`unit` machine whose kernel
  is really distro-specific still fails with an opaque `unresolved name`. The
  fix is the machine author switching to `distro_unit`, so the error should say
  that — cheap to add while in this code, and worth doing.
- **An empty artifact list is a valid image elsewhere.** A rootfs-only image
  with no packages is legal, so the marker must be what distinguishes "not
  buildable here" from "genuinely empty" — do not infer it from an empty list.
