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

Deferring resolution means an image's input hash cannot be computed until build
time, which breaks hash-up-front — the property that lets the TUI and `yoe desc`
show what would be built without building it. That is a much larger change than
the problem justifies, and it trades a correctness property for a convenience.

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

**Cache neutrality.** Gate the field's `fmt.Fprintf` in
`internal/resolve/hash.go` on non-empty, per the content-addressed-caching rule.
An unconditional write invalidates every unit's hash the moment it lands and
forces a full rebuild.

### Task 2.2: The branch in `image()`

In `modules/module-core/classes/image.star`, replace the `fail()` on a missing
`distro_unit` entry with the skip path: no kernel resolution, no
`ctx.machine_config.packages` / `distro_packages` merge, no `resolve_closure`,
and `unit(...)` registered with the marker and an empty artifact list.

Keep the existing `fail()` for the genuinely contradictory case — a kernel
setting both `unit` and `distro_unit` — which `fnMachine` already rejects.

## Phase 3: Loud at the point of use

### Task 3.1: `yoe build`

Refuse a marked image with the machine, the image's distro, and the supported
set named. This is the error that replaces the evaluation-time crash, so it
carries the whole diagnostic burden — an unhelpful message here would just
relocate the confusion.

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
- **Existing units' input hashes are unchanged** by the new field. Compare a
  known hash before and after; this is the test that catches an ungated
  `Fprintf`.

## Phase 5: Verify and close

- `testdata/e2e-project` selects `arduino-uno-q` and evaluates with
  `@module-alpine` still loaded — the condition that is impossible today.
- `yoe build base-image --machine arduino-uno-q --dry-run` resolves a closure
  containing the Arduino feed's kernel and board packages.
- Re-read `docs/distro.md` against shipped behavior; close any gap.
- Changelog entry, user-facing: selecting a single-distro machine no longer
  breaks projects that also contain images for other distros.
- Flip this plan's row in `docs/SPEC_PLAN_INDEX.md`.

## Risks and open questions

- **The underlying architecture is unchanged.** yoe still evaluates every image
  against one globally selected machine; this plan makes the mismatch a
  first-class outcome instead of a crash. The deeper fix is lazy evaluation per
  (image, machine) pair, which would also remove the hash-up-front tension in
  "Why not defer resolution to build time." Out of scope here, and worth its own
  design if machine-conditional images ever become common.
- **The flat-`unit` diagnostic stays poor.** A flat-`unit` machine whose kernel
  is really distro-specific still fails with an opaque `unresolved name`. The
  fix is the machine author switching to `distro_unit`, so the error should say
  that — cheap to add while in this code, and worth doing.
- **An empty artifact list is a valid image elsewhere.** A rootfs-only image
  with no packages is legal, so the marker must be what distinguishes "not
  buildable here" from "genuinely empty" — do not infer it from an empty list.
