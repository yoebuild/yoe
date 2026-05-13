---
title: "feat: Branch-aware dev mode and simplified pin-to-current"
type: feat
status: completed
date: 2026-05-13
origin: docs/brainstorms/2026-05-08-source-dev-mode-requirements.md
supersedes: docs/plans/2026-05-08-001-feat-source-dev-mode-toggle-plan.md
---

# feat: Branch-aware dev mode and simplified pin-to-current

## Summary

Migrate the dev-mode workflow from "pin↔dev keeps the same commit,
promote-to-pin via a tag/hash/branch picker" to "unit declares `branch` to opt
into auto-tracking `origin/<branch>` on toggle; the `P` keybinding stays but is
simplified to a single tag-only rewrite (writes HEAD's tag name or SHA, never
`branch`)." Persist the unit's source state into `build.json` on every build (so
`pin` is explicit in the cache) and render `pin` in the TUI's SRC column (dim
gray) so the four-state model is visible end-to-end. The bulk of dev-mode
machinery (state detection, watcher, build-time guard, TUI surfaces, fsnotify
wiring, `.star` field rewriter) already shipped under the predecessor plan; this
delta plan covers the design shift in the source-toggle semantics, the
simplified promote workflow, and the rendering fix that surfaces `pin`.

---

## Problem Frame

The original dev-mode design (predecessor plan
`docs/plans/2026-05-08-001-feat-source-dev-mode-toggle-plan.md`) preserved the
invariant that pin and dev built the same commit. Bumping a pin required the `P`
keystroke with a tag/hash/branch popup that regex-rewrote the unit's `.star`.
The updated brainstorm
(`docs/brainstorms/2026-05-08-source-dev-mode-requirements.md`) replaces that
with a declarative shape: `tag` is the pin, `branch` (if declared) is what dev
mode tracks. The `P` keybinding remains as a one-keystroke pin-to-current
convenience, simplified to write only the `tag` field — no picker, no
`branch`-rewriting, no kind parameter. See origin: §"What `dev` actually does"
and §"Pin to current".

---

## Requirements Trace

- R1. Toggling a unit from `pin` to `dev` checks out `origin/<branch>` HEAD when
  the unit declares a `branch`; otherwise the working tree stays at the pinned
  commit (origin: §"What `dev` actually does", Goals §1).
- R2. The local `upstream` git tag mirrors `origin/<branch>` for branch-declared
  units (so `git rev-list upstream..HEAD` counts commits past branch HEAD) and
  stays at the pinned commit otherwise. This is the anchor for `dev-mod`
  detection (origin: §"What `dev` actually does", States section).
- R3. The tag/hash/branch picker popup is removed. The `P` keybinding remains
  and calls a simplified `DevPromoteToPin` (or equivalently-renamed
  `DevPinToCurrent`) that always writes to the `tag` field — HEAD's tag name
  when HEAD has one, otherwise the 40-char SHA. The `branch` field is never
  written by `P`. The `.star` field rewriter (`internal/starlark/edit.go`) is
  kept and used for this simplified action. `P` is enabled in `dev` and
  `dev-mod`; disabled in `dev-dirty` and `pin` (origin: §"Pin to current — `P`
  keybinding").
- R4. The TUI detail page's SOURCE line surfaces when dev mode has advanced the
  working tree to `origin/<branch>` (commits past the pin tag) so the change
  isn't invisible (origin: §"Risks / open questions" — branch HEAD past pin).
- R5. `yoe-tool.md` and `CHANGELOG.md` reflect the simplified `P` action and the
  new `branch =` field semantics. Pre-1.0, no compatibility shim for the old
  picker workflow.

**Origin actors:** developer (toggles units), build pipeline (consumes
state-cache).

**Origin flows:** pin → dev toggle (branch-aware), dev → pin toggle (unchanged).

---

## Scope Boundaries

- **No re-implementation of dev-mode machinery already in main.** State
  detection, persistence, watcher, build-time guard, TUI SRC column, `u`
  keybinding, SSH/HTTPS popup, module toggle, and dev-state hash folding all
  shipped under the predecessor plan and stay as-is unless explicitly touched.
- **No new `branch` field plumbing.** Both `Tag` and `Branch` are already on
  `yoestar.Unit` and parsed by the Starlark binding
  (`internal/starlark/builtins.go:620-621`). The source layer already accepts a
  unit with both fields set.
- **No auto-discovery of the upstream's default branch.** Units without a
  `branch =` declaration keep today's behavior (working tree stays at pin
  commit). A future workstream may add `git remote show origin` parsing.
- **No multi-remote / fork management.** Same scope boundary as the predecessor
  plan.
- **No keep-`upstream`-tag-fresh on external fetches.** When a user does
  `git fetch` from `$` in dev mode, the local `upstream` tag updates only on the
  next yoe-driven action (toggle, watcher fire, build). A real-time mirror would
  need an extra `.git/refs/remotes/origin/<branch>` watcher and isn't worth the
  complexity in v1.

### Deferred to Follow-Up Work

- **Default-branch detection** for units that should track upstream but don't
  declare a `branch`. The detection-on-toggle path adds a network round-trip;
  defer until the manual-add-branch ergonomics prove painful.
- **External-fetch detection.** A `.git/refs/remotes/origin/<branch>` watcher
  could re-point `upstream` automatically; today's behavior leans on the
  poll/watcher firing on HEAD changes, which is good enough.

---

## Context & Research

### Relevant Code and Patterns

- `internal/dev.go:226` — `DevToUpstream(projectDir, scopeDir, unit, opts)` is
  the surface that needs branch-aware behavior added. Currently it rewrites
  origin, fetches unshallow, and persists `dev` state without changing the
  working-tree commit.
- `internal/dev.go:277` — `devFetchOrigin` runs `git fetch --unshallow` and is
  the natural seam to add the branch checkout + `upstream` tag re-point after.
- `internal/dev.go:310` — `devPinnedRef(unit)` already returns the pin (tag or
  branch — historically these were mutually exclusive in the unit DSL). New
  semantics treat them as orthogonal: `Tag` is the pin, `Branch` is the dev
  tracking ref.
- `internal/dev.go:441` — `DevPromoteToPin(projectDir, scopeDir, unit, kind)` is
  the function to simplify. Drop the `kind PinKind` parameter and always write
  to `tag`. Internally: check `git tag --points-at HEAD`; if non-empty, write
  the tag name; otherwise `git rev-parse HEAD` and write the SHA. Never touch
  the `branch` field. Callers: TUI `P` handler.
- `internal/dev.go:517` — `findUnitStarFile` stays — still needed by the
  simplified pin command.
- `internal/starlark/edit.go` + `edit_test.go` — `RewriteUnitField` stays.
  `RemoveUnitField` may become unused (the picker variant that swapped tag for
  branch is gone); audit and delete only if no callers remain.
- `internal/tui/app.go:2002` — `case "P":` keybinding handler stays, but routes
  to the simplified pin command rather than opening the picker.
- `internal/tui/app.go:2453-2454` — help-bar entry stays. Update the label from
  `promote` to `pin` (more accurate for the simplified action).
- `internal/tui/sourceprompt.go:130` — the `promptPinKind` enum value and its
  rendering/handler are removed. The `P` keybinding skips the prompt entirely
  and runs the pin command directly (with a brief inline confirmation toast
  showing what was written, e.g., `pinned to v1.38.0`).
- `internal/source/state.go:68` — `DetectState(srcDir)` reads
  `git remote get-url origin`, `git status --porcelain`, and
  `git rev-list --count upstream..HEAD`. Already commit-anchor-agnostic because
  it uses the local `upstream` tag — moving that tag changes detection without
  changing this code.
- `internal/starlark/builtins.go:620-621` — `Tag` and `Branch` are both kwargs
  on `unit()`. No schema change needed.

### Institutional Learnings

- The predecessor plan landed almost entirely in main but documented its
  promote-to-pin design before the brainstorm pivoted. Treat the predecessor
  plan as "what was implemented"; treat this plan as "what changes after the
  pivot."
- The TUI uses fsnotify + poll; either mechanism will trigger a state recompute
  after `upstream` tag re-pointing, so no special signaling needed when yoe
  internally moves the tag.

### External References

- None new; the existing fsnotify, bubble-tea, and git invocations carry over.

---

## Key Technical Decisions

- **`Tag` and `Branch` are orthogonal on a unit.** `Tag` is what `pin` builds.
  `Branch`, when set, is the ref dev mode tracks. Both can coexist; the source
  layer already supports this.
- **Branch checkout uses detached HEAD at `origin/<branch>`.** Avoids creating a
  local branch that drifts independently. If the user wants a local branch to
  push from, they create one from `$`.
- **`upstream` tag is yoe's anchor for `dev-mod` counting.** When `Branch` is
  declared, `upstream` re-points to `origin/<branch>` on toggle and on any
  yoe-driven fetch. When `Branch` is empty, `upstream` stays at the pinned
  commit (today's behavior). Users running `git fetch` from `$` get
  slightly-stale `upstream` until the next yoe action; acceptable trade-off
  documented in scope boundaries.
- **Promote workflow simplifies, doesn't disappear.** The picker (tag/hash/
  branch popup with conditional availability) is removed; the `P` keystroke
  stays as a one-shot pin-to-current that always rewrites `tag` (tag name or
  SHA, picked automatically based on whether HEAD carries a tag). Covers the
  common case ("bumped busybox to v1.38.0") with zero ceremony.
- **`P` never writes the `branch` field.** Branch tracking is declared by the
  unit author; the pin command only updates the pin. A user wanting to change
  branch tracking edits the `.star` directly.

---

## Open Questions

### Resolved During Planning

- **Does the unit DSL already support both `tag` and `branch`?** Yes — verified
  in `internal/starlark/builtins.go:620-621`. No DSL change needed.
- **Does `DetectState` need code changes for branch-tracking?** No — it uses the
  local `upstream` tag as the anchor, so re-pointing the tag during
  `DevToUpstream` is sufficient.
- **What happens if a unit has `branch =` but no `tag =`?** The brainstorm's
  table requires both shapes to have a `tag` (it's the pin). Validate at
  `DevToUpstream` entry: a unit with `Branch != ""` and `Tag == ""` is an
  invalid state — error informatively.

### Deferred to Implementation

- **Detached-HEAD vs. local-branch checkout.** The plan says detached HEAD; the
  implementer should verify this works smoothly with the existing watcher
  (HEAD-file changes are detected) and with `git rev-list upstream..HEAD` (yes,
  by definition).
- **`upstream` tag re-pointing inside `DevToUpstream` vs. a separate helper.**
  Folding the re-point into `devFetchOrigin` keeps fetch+re-point atomic.
  Implementer's call.
- **Exact wording for the SOURCE line "moved N commits to origin/<branch>"
  hint.** Tune for clarity once the layout is in front of you.

---

## Implementation Units

### U1. Branch-aware `DevToUpstream` with `upstream` tag tracking

**Goal:** When the unit's source declaration includes `Branch != ""`, the pin →
dev transition fetches the remote, re-points the local `upstream` tag to
`origin/<branch>`, and checks out `origin/<branch>` as detached HEAD. When
`Branch == ""`, behavior is unchanged.

**Requirements:** R1, R2.

**Dependencies:** None (extends an existing function).

**Files:**

- Modify: `internal/dev.go`
- Modify: `internal/dev_test.go`

**Approach:**

- In `DevToUpstream` (`internal/dev.go:226`), after the existing
  `devFetchOrigin` call succeeds:
  - If `unit.Branch != ""`, run `git checkout --detach origin/<branch>` followed
    by `git tag -f upstream origin/<branch>`. Order matters: re-point `upstream`
    only after the working tree moves successfully, so a failed checkout doesn't
    leave a misleading anchor.
  - If `unit.Branch == ""`, no extra git work — the existing behavior (working
    tree stays at the pinned commit) is preserved.
- Validate up front: if `unit.Branch != ""` and `unit.Tag == ""`, return an
  informative error before touching git. The brainstorm requires `tag` to be set
  (it's the pin); a branch-only unit is malformed.
- Update the `devPinnedRef(unit)` helper (or its callers) if it currently treats
  `Branch` and `Tag` as mutually exclusive. New semantics: `Tag` is
  authoritative for the pin; `Branch` is the dev tracking ref only.

**Patterns to follow:**

- Existing `gitCmd(dir, args...)` helper in `internal/dev.go:180`.
- Existing error-wrapping convention (`fmt.Errorf("git checkout: %w", err)`).

**Test scenarios:**

- Happy path: unit with `Tag = "v1.36.1"`, no `Branch` → pin → dev → working
  tree HEAD == pinned commit; `upstream` tag points at pinned commit; state
  becomes `dev`.
- Happy path: unit with `Tag = "v1.36.1"`, `Branch = "master"` → pin → dev →
  working tree HEAD == `origin/master`; `upstream` tag points at
  `origin/master`; state becomes `dev` (and `dev-mod` if origin/master has
  commits past v1.36.1 — verify count).
- Happy path: unit with branch already at the pin commit → pin → dev → no
  visible commit advance; `git rev-list upstream..HEAD` returns 0.
- Edge case: unit with `Branch = "master"`, `Tag = ""` → returns informative
  error, no git changes made.
- Edge case: `origin/<branch>` doesn't exist after fetch → returns error
  pointing at the missing ref, state unchanged.
- Edge case: detached-HEAD checkout fails (working tree somehow dirty in a pin
  clone) → error returns; `upstream` tag is NOT re-pointed.
- Error path: `git fetch` fails (network down) → no checkout attempted,
  `upstream` tag unchanged.
- Integration: after a branch-declared pin → dev, `DetectState` returns `dev`
  (or `dev-mod` if branch is past pin), and the unit's hash (via
  `internal/source/hash.go`'s dev-state folding) picks up the new HEAD sha
  rather than the pin commit.

**Verification:**

- All existing `internal/dev_test.go` tests for `DevToUpstream` continue to pass
  — the no-branch path is unchanged.
- A new test fixture with `Branch = "master"` exercises the full
  checkout+re-point path against a temp git repo.

---

### U2. Simplify the promote workflow — tag-only pin-to-current

**Goal:** Collapse the tag/hash/branch picker into a single pin-to-current
action that always writes the unit's `tag` field. HEAD's tag name when HEAD has
one (`git tag --points-at HEAD`); otherwise the 40-char SHA. Never write
`branch`. The `P` keybinding stays; the picker UI and its modal state come out.

**Requirements:** R3.

**Dependencies:** None (touches files independent of U1).

**Files:**

- Modify: `internal/dev.go` — simplify `DevPromoteToPin` (line 441): drop the
  `kind PinKind` parameter, inline the tag-vs-SHA choice. Optionally rename to
  `DevPinToCurrent` for clarity. Keep `findUnitStarFile` (line 517) — still a
  caller.
- Modify: `internal/dev_test.go` — replace the three picker-variant tests (`tag`
  / `hash` / `branch`) with two scenarios (HEAD has tag → tag name written; HEAD
  has no tag → SHA written).
- Modify: `internal/tui/app.go` — change the `case "P":` handler (line 2002) to
  invoke the simplified action directly instead of opening the picker. Update
  the help-bar label from `{"P", "promote"}` to `{"P", "pin"}` (line 2453-2454).
- Modify: `internal/tui/sourceprompt.go` — remove the `promptPinKind` enum value
  (line 130) and its rendering branch / confirmation handler. Leave
  `promptSSHHTTPS` and `promptDiscardDev` untouched.
- Modify: `internal/tui/app_test.go` — replace picker-flow tests with
  direct-action tests.
- Modify: `internal/starlark/edit.go` — `RewriteUnitField` stays. Audit
  `RemoveUnitField`: it was used by the picker's "switch to branch, drop tag"
  variant; after simplification it may be unused. Delete only if grep confirms
  zero callers.

**Approach:**

- The simplified `DevPromoteToPin(projectDir, scopeDir, unit)`:
  1. Pre-flight: refuse in `dev-dirty` (return informative error). Allowed in
     `dev` and `dev-mod`. Refuse in `pin` or empty state.
  2. `git tag --points-at HEAD` → if non-empty, take the first / most-specific
     tag name; otherwise fall through.
  3. If no tag, `git rev-parse HEAD` → 40-char SHA.
  4. Call `RewriteUnitField(starPath, unitName, "tag", value)`.
  5. `git tag -f upstream HEAD` so `git rev-list upstream..HEAD` returns 0.
  6. Persist `dev` state via the existing state cache helper.
- The TUI handler shows a brief inline confirmation toast on success:
  `pinned to v1.38.0` (tag) or `pinned to abc1234 (no tag)` (SHA truncated for
  display). On `dev-dirty`, the toast says
  `commit or stash first to pin current state`.

**Patterns to follow:**

- Existing `gitCmd` shape in `internal/dev.go`.
- Existing toast / message pattern in `internal/tui/app.go` (`m.message = ...`).

**Test scenarios:**

- Happy path: `dev-mod`, HEAD on tag `v1.38.0` → `P` → `.star` rewritten with
  `tag = "v1.38.0"`, `upstream` tag at HEAD, state becomes `dev`.
- Happy path: `dev-mod`, HEAD on untagged commit → `P` → `.star` rewritten with
  `tag = "<40-char-sha>"`, `upstream` at HEAD, state `dev`.
- Happy path: `dev` (branch declared, HEAD at branch HEAD past pinned tag) → `P`
  → tag bumps to branch HEAD's tag/sha; state stays `dev`.
- Happy path: a unit with both `tag` and `branch` set → `P` rewrites only `tag`;
  the `branch` line is untouched in the `.star`.
- Edge case: HEAD has multiple tags (annotated and lightweight) → write per a
  deterministic rule (e.g., first from `git tag --points-at HEAD` sorted
  output); document the choice in the test.
- Error path: `dev-dirty` → returns error, no file change, no git tag move, TUI
  shows "commit or stash first" toast.
- Error path: called on `pin` or empty state → returns error, no-op.
- Integration: after `P`, `DetectState` returns `dev` (not `dev-mod`); the next
  pin → dev → pin cycle re-clones at the new tag.

**Verification:**

- The `.star` file parses cleanly after every pin action (re-parse via
  `internal/starlark.LoadProject`).
- `git tag --points-at upstream` matches HEAD after every successful pin.
- `RewriteUnitField` and `internal/starlark/edit.go` remain in the tree and pass
  their existing tests.
- `grep -r promptPinKind internal/` returns no hits.
- Help bar at the bottom of the detail view shows `P pin`, not `P promote`.

---

### U3. TUI SOURCE line — surface branch tracking and commit advancement

**Goal:** When a unit is in `dev` mode with a declared `Branch`, the detail
page's SOURCE line shows the tracked branch and how many commits the working
tree advanced past the pin tag (so the auto-checkout isn't silent). When no
branch is declared, the line is unchanged from today.

**Requirements:** R4.

**Dependencies:** U1 (branch-aware checkout must land first so the SOURCE line
has meaningful data to display).

**Files:**

- Modify: `internal/tui/app.go` — extend the SOURCE rendering in `viewDetail`.
- Modify: `internal/tui/app_test.go` — add rendering test cases.

**Approach:**

- Read `unit.Branch` at render time. If set, the SOURCE line shows:
  `SOURCE   dev   (https://...)`
  `         tracking origin/<branch> (3 commits past v1.36.1)` — the
  parenthetical "N commits past <pin>" is omitted when N is 0.
- If `unit.Branch == ""`, render as today (state token + URL + cached
  `git describe` string).
- The commit count comes from a small helper:
  `git rev-list --count <pin-tag>..HEAD` against the unit's src dir. Cache the
  result in the existing source-state refresh path so it doesn't re-run on every
  paint.

**Patterns to follow:**

- Existing SOURCE line rendering in `viewDetail`.
- `BuildMeta.SourceDescribe` caching pattern (already established by the
  predecessor plan's U12).

**Test scenarios:**

- Happy path: branch-declared unit, working tree at `origin/master` with 3
  commits past `v1.36.1` → SOURCE line shows "tracking origin/master (3 commits
  past v1.36.1)".
- Happy path: branch-declared unit at the pin commit (branch hasn't advanced) →
  SOURCE line shows "tracking origin/master" (no parenthetical count).
- Happy path: no-branch unit in dev mode → SOURCE line renders as today.
- Edge case: branch declared but src dir not yet built → SOURCE line shows
  "tracking origin/<branch> (not built)".
- Edge case: pin tag is a 40-char SHA → display the first 7 chars in the "N
  commits past <pin>" hint to keep the line readable.

**Verification:**

- Visual: switch a unit with `branch = "master"` to dev → SOURCE line
  immediately shows the advance count if any.
- A unit toggled while branch HEAD is at the pin shows the bare "tracking
  origin/<branch>" form.

---

### U4. Docs + CHANGELOG

**Goal:** Update user-facing documentation to reflect the new `branch =` field
semantics, the simplified `P` keybinding behavior, and the now-visible `pin`
state in the SRC column.

**Requirements:** R5.

**Dependencies:** U1, U2, U3, U5, U6.

**Files:**

- Modify: `docs/yoe-tool.md` — rename the `P promote` row in the detail-page
  keybinding table to `P pin`; add a "Tracking an upstream branch in dev mode"
  subsection describing the `branch =` field and the auto-checkout behavior;
  briefly describe what `P pin` writes (tag name or SHA).
- Modify: `CHANGELOG.md` — one entry covering both shifts (declarative branch
  tracking; `P` simplified to tag-only).

**Approach:**

- The CHANGELOG entry leads with the user benefit per project convention
  (`CLAUDE.md` Changelog entries section): something like "Dev mode now tracks
  an upstream branch automatically when the unit declares one, and `P` pins the
  current HEAD with no picker."
- The `docs/yoe-tool.md` keybinding table updates one row (`P promote` →
  `P pin`) with a new one-line description.
- The new doc subsection in `yoe-tool.md` shows a small `.star` snippet with
  `tag = "v1.36.1", branch = "master"` and explains what happens on toggle and
  on `P`.

**Patterns to follow:**

- CHANGELOG format established by recent entries (`CHANGELOG.md`).
- `docs/yoe-tool.md` keybinding tables and prose tone.

**Test scenarios:**

- Test expectation: none — pure documentation update. `yoe_format_check` must
  still pass.

**Verification:**

- `yoe_format_check` is clean.
- The keybinding table shows `P pin` (not `P promote`).
- A reader scanning the new dev-mode subsection can identify the `branch =`
  field and predict what toggle does, and knows that `P` writes only `tag`.

---

### U5. Persist `pin` SourceState when a build dir is initialized

**Goal:** After `source.Prepare()` succeeds in pin mode, the unit's `build.json`
carries `source_state: "pin"` explicitly. The cached state stays authoritative
so the TUI can render `pin` without re-running git on every paint.

**Requirements:** R6 (carried from origin: §Persistence — cached state is
advisory but populated on every build).

**Dependencies:** None.

**Files:**

- Modify: `internal/build/executor.go` — after a successful unit build (or after
  the source-prep phase completes for any unit), call
  `source.DetectState(srcDir)` and persist the result into the unit's
  `BuildMeta.SourceState`.
- Modify: `internal/build/executor_test.go` — add a test asserting `build.json`
  carries `source_state: "pin"` after a fresh pin build.
- Modify (if needed): `internal/source/workspace.go` — no schema change
  expected; the existing Prepare() path doesn't write BuildMeta directly. If the
  executor's BuildMeta write happens before Prepare's effect is visible to
  DetectState, restructure so detection runs after Prepare returns.

**Approach:**

- The executor already writes `BuildMeta` after a successful build. Extend that
  write to always include `SourceState`, derived from `DetectState(srcDir)`
  against the unit's prepared source directory.
- For a pin-mode build, the post-Prepare checkout has no `origin` remote and has
  the `upstream` tag at the pinned commit — `DetectState` returns `pin`.
- For a build against a dev unit (where Prepare short-circuited per the existing
  build-time guard), `DetectState` returns the live dev state and that's what
  lands in the cache.
- Idempotent — re-running detect + write on every build keeps the cache fresh.
- The `omitempty` JSON tag on `SourceState` stays; pre-existing `build.json`
  files without the field continue to round-trip until the unit is next built.

**Patterns to follow:**

- Existing `BuildMeta.WriteMeta` pattern in `internal/build/meta.go`.
- The "advisory cache, re-derive on miss" convention from `cacheValid` and the
  predecessor plan's state-detection work.

**Test scenarios:**

- Happy path: fresh pin-mode build of a unit → `build/<unit>.<scope>/build.json`
  contains `"source_state": "pin"`.
- Happy path: build against a unit already in `dev` state →
  `source_state: "dev"` lands.
- Happy path: re-build a pin unit twice → `source_state` stays `"pin"` on both
  passes (no spurious churn).
- Edge case: build fails before source prep finishes → `BuildMeta` write is
  skipped (existing error handling).
- Edge case: existing `build.json` without `source_state` field, then build →
  field is populated on the next write.
- Integration: TUI reads `build.json` and shows `pin` for the unit (this proves
  U6's renderer has data to display).

**Verification:**

- `jq .source_state build/<unit>.<scope>/build.json` returns `"pin"` after a
  fresh pin build.
- Pre-existing meta files still parse cleanly; only newly-written meta files
  carry the explicit `pin` value.

---

### U6. Render `pin` in the SRC column and detail-page SOURCE line

**Goal:** The TUI's SRC column displays `pin` in dim gray for units cached in
the pin state, instead of the current blank cell. Blank remains only for
image/container units (no source dir) and units with empty/unknown state. The
detail page's SOURCE line also distinguishes "pinned at v1.36.1" from "(not
built)".

**Requirements:** R6 (renders the cached state token per the brainstorm's States
section and Units tab description).

**Dependencies:** U5 (cache contains `pin` for built units; without it, the
renderer falls back to live `DetectState`, which works but is slower).

**Files:**

- Modify: `internal/tui/sourceprompt.go` — split the
  `case source.StatePin, source.StateEmpty:` collapse (line ~32) so the renderer
  handles each separately.
- Modify: `internal/tui/app.go` — extend the SRC column renderer and the
  detail-page SOURCE line to distinguish pin from empty.
- Modify: `internal/tui/app_test.go` — add rendering cases.

**Approach:**

- `StatePin` → render `pin` with a dim-gray style (use the existing `dimStyle`
  or equivalent lipgloss style). Three characters, fits comfortably in the
  9-char column.
- `StateEmpty` → render blank (today's behavior preserved).
- Image and container units → blank (these have no source dir; the existing
  unit-class guard in the renderer keeps them empty).
- Detail page SOURCE line:
  - `pin` state: `SOURCE   pin   (pinned at v1.36.1)` — show the pin ref from
    the unit's `Tag` field.
  - `empty` state: `SOURCE   (not built)`.
- The `dimStyle` choice keeps `pin` visually quieter than the dev states
  (cyan/green/red), so dev units still pop in a scan of the column.

**Patterns to follow:**

- Existing color/style helpers in `internal/tui/app.go` (`dimStyle`,
  `helpKeyStyle`, etc.).
- The SRC column rendering pattern established by the predecessor plan's U7.

**Test scenarios:**

- Happy path: unit cached in `pin` state → SRC column renders `pin` in dim gray.
- Happy path: unit cached in `dev` state → SRC column renders `dev` in cyan
  (unchanged from today).
- Happy path: image unit → SRC column is blank.
- Happy path: container unit → SRC column is blank.
- Happy path: unit with no cached state (never built) → SRC column is blank.
- Happy path: detail page for a `pin` unit shows `SOURCE pin (pinned at <tag>)`.
- Happy path: detail page for an unbuilt unit shows `SOURCE (not built)`.
- Edge case: narrow terminal (≤ 40 cols) → `pin` token still fits and doesn't
  truncate the MODULE column.

**Verification:**

- Visual: launch TUI on a project with mixed pin/dev units → pin units show
  `pin` in dim gray, dev units retain their distinctive colors, image/container
  units stay blank.
- An unbuilt unit still renders blank — distinguishable from `pin`.

---

## System-Wide Impact

- **Interaction graph:** `DevToUpstream` is the single point of behavior change.
  Callers (TUI `u` handler, any CLI invocation in the future) get the new
  behavior automatically.
- **Error propagation:** A failed branch checkout must not leave the `upstream`
  tag pointing at a stale `origin/<branch>` — re-point only after checkout
  succeeds.
- **State lifecycle risks:** `upstream` tag drift if the user runs `git fetch`
  from `$` and never triggers a yoe action. Documented as a deferred follow-up;
  the count shown is "commits since yoe last anchored," not "commits since
  upstream's current head." Acceptable for v1.
- **API surface parity:** No public CLI changes. The `P` keybinding stays; only
  its behavior shifts (one-shot tag rewrite instead of a picker). CHANGELOG
  describes the simplification.
- **Integration coverage:** U1's tests need to exercise the watcher + poll
  pickup of the moved `upstream` tag and HEAD — the existing watcher tests cover
  HEAD changes; verify they pass without modification after U1 lands.
- **Unchanged invariants:** All non-promote, non-branch dev-mode behavior is
  preserved exactly. State detection, persistence, the build-time guard
  generalizing to any `dev*` state, fsnotify + poll, dev-tree hash folding,
  module toggle, SSH/HTTPS prompt, and the SRC column all continue to work as
  shipped under the predecessor plan.

---

## Risks & Dependencies

| Risk                                                                                                                  | Mitigation                                                                                                                                                                                                                 |
| --------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The `upstream` tag re-point on toggle subtly affects the `dev` → `pin` transition (which discards work by re-cloning) | `DevToPin` re-clones from scratch, ignoring the `upstream` tag entirely. No risk — the re-clone walks the unit's `Source`+`Tag` fields, not local refs.                                                                    |
| Users relying on the `P` picker's hash or branch variants find the simplified action does only tag                    | Hash variant is preserved automatically (no-tag HEAD writes the SHA). Branch variant is intentionally dropped — that workflow now goes through the unit author editing `branch = "..."` directly. Documented in CHANGELOG. |
| `git checkout --detach origin/<branch>` produces noisier `git status` than the user expects                           | Document detached-HEAD as the default in `yoe-tool.md`; users wanting a local branch create one from `$`.                                                                                                                  |
| `RemoveUnitField` audit misses a stale caller and is removed prematurely (or kept when unused)                        | `go build ./...` and `go vet ./...` fail on unresolved symbols. Grep before delete; leave in place if any caller remains.                                                                                                  |
| `upstream` tag goes stale when user `git fetch`es manually                                                            | Documented in scope boundaries. The next yoe action (toggle, build, watcher tick) re-syncs. Real-time sync is a deferred follow-up.                                                                                        |

---

## Documentation / Operational Notes

- The `branch =` field semantics need explicit documentation; today the field
  exists but is undocumented as a dev-mode toggle. The new subsection in
  `docs/yoe-tool.md` fills this gap.
- No migration: existing pinned units (those with only `tag =`) keep their
  behavior unchanged. Units that opt into branch tracking add `branch = "..."`
  to their `.star` declaration.
- The `internal/starlark/edit.go` deletion is internal-only; no consumer-facing
  contract.

---

## Sources & References

- **Origin document:**
  [docs/brainstorms/2026-05-08-source-dev-mode-requirements.md](../brainstorms/2026-05-08-source-dev-mode-requirements.md)
- **Predecessor plan (superseded):**
  [docs/plans/2026-05-08-001-feat-source-dev-mode-toggle-plan.md](2026-05-08-001-feat-source-dev-mode-toggle-plan.md)
- Related code: `internal/dev.go`, `internal/source/state.go`,
  `internal/source/workspace.go`, `internal/tui/app.go`,
  `internal/tui/sourceprompt.go`, `internal/starlark/edit.go` (to delete),
  `internal/starlark/builtins.go:620-621`.
