---
title: "feat: Source dev-mode toggle for units and modules"
type: feat
status: active
date: 2026-05-08
origin: docs/brainstorms/2026-05-08-source-dev-mode-requirements.md
---

# feat: Source dev-mode toggle for units and modules

## Overview

Add a TUI affordance to flip a unit's (or module's) local git checkout between
`pin` (yoe-managed shallow clone, no remote) and `dev` (real upstream remote
configured, full history, src dir off-limits to yoe). Surface the active state
in a new `SRC` column on the units and modules tabs and on the unit detail page.
Detect modifications live via fsnotify with a polling fallback. Allow promoting
a `dev-mod` checkout's HEAD back to the unit's `.star` pin in one keystroke.

The work splits cleanly into a state-detection foundation, the toggle and
promote actions on top, the TUI surfaces and live watcher, and a build-time
guard that keeps yoe from overwriting an active dev checkout.

---

## Problem Frame

Yoe units fetch source as shallow git clones at a pinned ref into
`build/<unit>/src/`, which is fine for builds but useless for development — no
remote, no history. The CLI (`internal/dev.go`) already understands the "local
commits beyond `upstream` tag" workflow, but using it requires hand-running git
commands inside the build dir. There's no visible state, no pin↔dev toggle, no
SSH/HTTPS choice on transition, and no live status update when files change. See
origin: `docs/brainstorms/2026-05-08-source-dev-mode-requirements.md`.

---

## Requirements Trace

- R1. One TUI keystroke flips a unit (or module) between `pin` and `dev`, with
  an HTTPS-vs-SSH popup on the way to dev (origin: Goals §1, §3).
- R2. The active source state is visible at a glance on the units tab, modules
  tab, and detail page, color-coded across four states
  (`pin`/`dev`/`dev-mod`/`dev-dirty`) plus `local` for overridden modules
  (origin: Goals §2, States section).
- R3. State display updates within seconds of an external edit — no manual
  refresh required (origin: Goals §4, Live status detection section).
- R4. Yoe never overwrites a dev unit's source dir on rebuild, even if the
  unit's `.star` source/tag changes; it warns instead (origin: Goals §5,
  Build-time warning section).
- R5. From `dev-mod`, a single keystroke promotes the checkout's HEAD back into
  the unit's `.star` pin, picking among tag/hash/branch in a popup; the local
  `upstream` tag advances to HEAD so the state transitions to plain `dev`
  (origin: Promote current dev state section).
- R6. State persists across TUI restarts in advisory caches (`build.json` for
  units, sibling file for modules); recoverable from the git checkout if the
  cache is lost (origin: Persistence section).

---

## Scope Boundaries

- No persistence to `local.star`. State is per-clone and ephemeral; users
  re-toggle after a `yoe init` or cache wipe.
- No multi-remote / fork management.
- No branch picker UI. Initial dev-mode checkout stays on the same commit as
  pin; switching branches happens in `$` shell.
- No `git fetch` to compare local against remote HEAD on view. State reflects
  local working tree only.
- The toggle never edits the unit's `source` URL in the `.star`. Only the
  promote-to-pin action (R5) writes to `.star`, and only the `tag`/`branch`
  field.

### Deferred to Follow-Up Work

- **fsnotify reliability detection on bind-mounts/network FS**: ship with
  watcher + poll fallback in parallel; deciding "watcher works here" is
  heuristic. If poll alone proves sufficient, the watcher can come out later.
- **Default branch detection** for upstream HEAD (`git remote show origin`
  parsing). Not needed in v1 because dev keeps the working commit unchanged.

---

## Context & Research

### Relevant Code and Patterns

- `internal/source/workspace.go` — `Prepare()` function already short-circuits
  when src has commits beyond `upstream` (`hasLocalCommits`). This is the
  natural gate to extend for "skip if any dev state."
- `internal/dev.go` — existing `DevDiff` / `DevExtract` / `DevStatus` shape:
  read git output, no side effects beyond `format-patch` to a patches dir. New
  functions follow the same shape.
- `internal/build/meta.go` — `BuildMeta` struct + `WriteMeta`/`ReadMeta`. Add
  `SourceState` string field, no other change to JSON shape.
- `internal/build/executor.go:543-552` — where `CreateAPK` and `repo.Publish`
  are called after task execution. The build-time warning for dev units fires
  earlier (in the source-prep phase).
- `internal/module/fetch.go` — `SyncIfNeeded` and `ResolveModulePaths` give the
  module name → cache path mapping needed for module-side state.
- `internal/tui/app.go` — TUI is a single bubble-tea model; existing patterns
  for column rendering (`renderUnitsBody`), confirm modals (`m.confirm`), and
  key handling (`updateUnits` / `updateDetail`). Module tab is `tabModules`
  (line 120). Detail page metadata block starts around the `viewDetail`
  rendering.
- `internal/tui/app.go:1192` — `updateSearch` shows the existing pattern for a
  focused-modal sub-update (the search bar takes over). Reusable shape for
  SSH/HTTPS popup and tag/hash/branch popup.
- `internal/tui/app.go:3633` — `refreshUnitSize` shows the per-unit
  refresh-on-build-completion pattern; new state refresh follows the same shape
  but is fired from the watcher rather than the build-completion channel.

### Institutional Learnings

- `docs/solutions/` was checked; nothing directly applicable.
- The recent passthrough/noarch work (commits `9809c8b` and `df3fcaf`)
  reinforces a yoe convention: state caches in `build.json` are advisory, always
  re-derivable. The dev-mode state cache follows that pattern —
  `cacheValid`-style fall-throughs to a fresh git probe.

### External References

- `fsnotify/fsnotify` (Go) — pattern: recursive watch via per-dir add, since
  fsnotify doesn't watch subtrees natively. yoe doesn't currently use fsnotify;
  this plan introduces the dependency.
- Bubble Tea — already in use; tea.Cmd-based event loop accepts external channel
  events (the watcher publishes via a tea.Msg).

---

## Key Technical Decisions

- **State is derived; cache is advisory.** Every detection path can fall back to
  running git. The `BuildMeta.SourceState` field exists only to avoid re-running
  git on every TUI render.
- **Toggle never edits `.star`.** The pin↔dev transition rewrites the local
  checkout's git remote, never the project source. Only the promote-to-pin
  action (`P`) edits `.star`, and only the `tag`/`branch` field, via regex.
- **Watcher armed per-dev-unit, not per-render.** Lifecycle is tied to state
  membership: enters `dev*` → arm watcher; returns to `pin` → tear down.
  Visibility/scroll position doesn't affect watcher set.
- **fsnotify + poll fallback both run.** Watcher is best-effort; the poll is the
  safety net for filesystems where fsnotify doesn't fire. Cheap to run both
  since dev units are typically a small handful.
- **Modules use the same four states.** Symmetric mental model is worth more
  than per-domain simplification. The pin→dev transition for modules is lighter
  (often just SSH rewrite + unshallow), but the state vocabulary is identical.
- **`local =` modules show `local` token, no toggle.** A user-overridden module
  is whatever the user has; yoe can't reason about its pin.
- **Unit hash inputs vary by source state.** Pin units hash from the .star
  fields exactly as today (URL, tag, branch, patches). Dev units fold in the
  actual src tree state — `git rev-parse HEAD` plus a content hash of any dirty
  diff — so an in-place edit invalidates the cache. Without this, a `yoe build`
  against a freshly-edited dev unit would hit the cache and silently skip the
  rebuild. The cost is one rebuild per pin → dev toggle (the hash inputs
  change), which is acceptable.

---

## Open Questions

### Resolved During Planning

- **Where does the module state file live?**
  `cache/modules/<module>/.yoe-state.json`, alongside the module's clone
  (planning confirmed: `cache/modules/<module>/` is what
  `internal/module/fetch.go:131-170` writes into).
- **Which `.star` field stores a sha-pin?** Yoe's `unit()` accepts a sha in the
  `tag` field (verified in `internal/source/`); no separate `sha` field is
  needed for the promote-to-pin rewrite.

### Deferred to Implementation

- **`git fetch --unshallow` progress UX.** The exact rendering — spinner, byte
  count, time elapsed — is best decided when the implementer can run it
  interactively against a large kernel checkout.
- **Watcher debounce window.** ~100ms is a reasonable default but the exact
  debounce should be tuned against an editor's save burst (vim, neovim, helix
  all have different write patterns).
- **Empty-checkout edge case.** If a user manually `rm -rf`s the src dir, state
  detection has to handle "src dir doesn't exist" gracefully — the implementer
  will see this when wiring `DetectState` into the watcher's fire path.

---

## High-Level Technical Design

> _This illustrates the intended approach and is directional guidance for
> review, not implementation specification. The implementing agent should treat
> it as context, not code to reproduce._

```
┌─────────────────────────────────────────────────────────────────┐
│  internal/source/state.go                                        │
│  DetectState(srcDir) -> State                                    │
│  - has .git? no  → return ""  (not yet built)                    │
│  - origin URL == ""? → pin                                       │
│  - origin URL set:                                               │
│      - rev-list upstream..HEAD count > 0 → dev-mod-or-dirty      │
│      - status --porcelain non-empty       → dev-dirty            │
│      - else                                → dev                 │
└──────┬──────────────────────────────────────────────────────────┘
       │ used by
       ├─► internal/dev.go  (DevToUpstream / DevToPin / DevPromoteToPin)
       ├─► internal/module/dev.go  (ModuleToUpstream / ModuleToPin)
       ├─► internal/source/workspace.go  (Prepare gate: skip if dev*)
       ├─► internal/tui/sourcewatch.go   (fsnotify + poll dispatcher)
       └─► internal/tui/app.go  (column render, detail page, key handlers)

         pin ◄──── u ────► dev ◄──── P ──── dev-mod
                            ▲                  │
                            │       commit     │
                            │       /reset     ▼
                            └──────────── dev-dirty
                                  (auto-detected)
```

The state-detection module is a single file with one exported function and one
exported enum. Every other piece imports it. Side-effecting transitions (toggle,
promote) live in `internal/dev.go` (units) and a new `internal/module/dev.go`
(modules) — two separate package-level homes because units and modules currently
have separate management code, and the dev shape is a thin layer on top.

---

## Implementation Units

- [ ] U1. **Source state detection**

**Goal:** Pure-function package that classifies a git working tree as `pin` /
`dev` / `dev-mod` / `dev-dirty` / `local` / unbuilt, with no side effects.

**Requirements:** R2, R3, R6.

**Dependencies:** None.

**Files:**

- Create: `internal/source/state.go`
- Test: `internal/source/state_test.go`

**Approach:**

- Define `State` as a string typed alias with the five tokens.
- `DetectState(srcDir string) (State, error)` runs `git remote get-url origin`,
  `git status --porcelain`, and `git rev-list --count upstream..HEAD` in
  sequence. Empty / no-origin / no-`.git` produces `pin` (or empty when src dir
  doesn't exist).
- `IsDevState(s State) bool` helper for the watcher gate.

**Patterns to follow:**

- `internal/dev.go`'s `gitCmd(dir, args...)` helper — copy it (or factor up;
  small enough that copy is fine).
- Returning errors as the second value, even when the function rarely fails —
  TUI callers use `_ = err` and fall back to "unknown" display.

**Test scenarios:**

- Happy path: a fresh shallow clone (no remote, has `upstream` tag, clean work
  tree) → `pin`.
- Happy path: clone with `origin` set, on `upstream` commit, clean work tree →
  `dev`.
- Happy path: dev clone with one commit beyond upstream, clean work tree →
  `dev-mod`.
- Happy path: dev clone with edited tracked file → `dev-dirty`.
- Happy path: dev clone with untracked new file → `dev-dirty`.
- Edge case: src dir doesn't exist → empty state, no error.
- Edge case: src dir exists but `.git` missing (someone deleted it) → empty
  state, no error.
- Edge case: no `upstream` tag (corrupted state) → returns the closest
  reasonable state plus a warning error so the caller can log it.

**Verification:**

- All five state branches return their expected token across the test fixtures.
- No side effects on the test git repos (state is purely observational).

---

- [ ] U2. **Persist source state in BuildMeta + sibling module file**

**Goal:** Cache the last-known state token alongside the existing build metadata
so the TUI can render without re-running git on every paint.

**Requirements:** R6.

**Dependencies:** U1.

**Files:**

- Modify: `internal/build/meta.go`
- Create: `internal/module/state.go`
- Test: `internal/build/meta_test.go` (add cases),
  `internal/module/state_test.go`

**Approach:**

- Add
  `SourceState string \`json:"source_state,omitempty"\``to`BuildMeta`. `omitempty`
  keeps existing meta files round-trippable.
- Module side: a small read/write pair on a JSON file at
  `cache/modules/<module>/.yoe-state.json` carrying `{state: "dev"}`.
- Both helpers are advisory: a `Get` returns empty if the file is missing or
  unparseable; the caller falls through to `DetectState`.

**Patterns to follow:**

- `internal/build/meta.go`'s `ReadMeta`/`WriteMeta` shape — match it.
- `internal/repo/local.go`'s "advisory state" pattern: missing file is normal,
  not an error.

**Test scenarios:**

- Happy path: write+read round-trips a `dev` state.
- Happy path: existing `build.json` without the field reads back with empty
  `SourceState` and no error.
- Edge case: corrupted JSON → returns empty state, no panic.
- Edge case: module dir doesn't exist → empty state, no error.

**Verification:**

- Existing `BuildMeta` JSON files still parse cleanly.
- The module state file is a peer of the module clone, not inside `.git/`.

---

- [ ] U12. **Fold dev-state src tree into the unit hash**

**Goal:** When a unit is in any `dev*` state, the unit hash captures the actual
git state of `build/<unit>/src/` — HEAD sha plus a content hash of any dirty
diff — so cache validity matches what the user will actually build. For `pin`
units the hash is unchanged.

**Requirements:** R3, R4, R6.

**Dependencies:** U1 (state detection), U2 (cached state lookup so the hash
function knows whether to fold tree state in without a git call on every
render).

**Files:**

- Modify: `internal/resolve/hash.go`
- Modify: `internal/build/meta.go` (capture `git describe --dirty --always`
  alongside `SourceState`)
- Modify: `internal/build/executor.go` (write the describe string when
  populating `BuildMeta`)
- Test: `internal/resolve/hash_test.go`

**Approach:**

- In `UnitHash`, after the existing pin-mode inputs, check the unit's cached
  `SourceState`:
  - `pin` (or empty): no additional input. Existing behavior preserved so
    already-cached pin builds stay valid.
  - `dev` / `dev-mod`: append `git rev-parse HEAD` of the unit's src dir.
  - `dev-dirty`: append the HEAD sha **and** a sha256 of `git diff HEAD` output
    (so two different dirty states produce distinct hashes).
- Use `git stash create` as an alternative content sha when `git diff HEAD` is
  too noisy (binary files, mode changes); evaluate at impl time which is more
  robust.
- Add
  `SourceDescribe string \`json:"source_describe,omitempty"\``to`BuildMeta`. Populate it in the executor's `defer`-set finalize block with `git
  describe --dirty
  --always`against the src dir for dev units. Empty for pin units (no useful describe —`upstream`
  tag is the only ref).
- Document in the existing `Hash` field comment that the input set varies by
  source state.

**Patterns to follow:**

- Existing `fmt.Fprintf(h, "%s:%s\n", key, value)` accumulator pattern in
  `hash.go`.
- The "advisory cache, fall through to git on miss" pattern from `cacheValid`.

**Test scenarios:**

- Happy path: a pin unit's hash is identical before and after the change (no
  behavior change for non-dev workflows).
- Happy path: same dev unit hashed twice with no edits in between → same hash.
- Happy path: dev unit + edit a tracked file + rehash → different hash.
- Happy path: dev-dirty unit, edit one file then a different file (each followed
  by `git diff HEAD`) → two different hashes.
- Happy path: dev unit, commit the dirty change → state moves to `dev-mod`, hash
  reflects the new HEAD sha (different from when the edit was uncommitted).
- Edge case: dev unit with broken `.git` → falls back to "unknown" hash
  component (constant), unit still builds but always cache-misses until git
  state is reachable.
- Edge case: pin → dev transition on the same commit → hash changes (because dev
  now includes HEAD sha as input). One rebuild per toggle. Acceptable cost.
- Integration: `BuildMeta.SourceDescribe` round-trips through
  `WriteMeta`/`ReadMeta`.

**Verification:**

- `yoe build` against a dev unit, edit a file, rebuild → executor logs show
  `[building]` not `[cached]`.
- Pin units behave exactly as today: no extra git invocations during hashing,
  identical hashes vs. pre-change.
- The TUI detail page can read `BuildMeta.SourceDescribe` to render the unit's
  git description (e.g. `v3.4.1-3-gabc1234-dirty`) — that rendering belongs to
  U7's SOURCE line addition.

---

- [ ] U3. **Pin↔Dev toggle for units (no UI yet)**

**Goal:** The state-changing transitions for unit checkouts:
`DevToUpstream(unit, ssh bool) error` and `DevToPin(unit) error`. Library
functions; UI invokes them later.

**Requirements:** R1, R4.

**Dependencies:** U1, U2.

**Files:**

- Modify: `internal/dev.go`
- Test: `internal/dev_test.go`

**Approach:**

- `DevToUpstream(unit, ssh)`:
  1. Compute upstream URL from `unit.Source` (if HTTPS, optionally rewrite to
     SSH form: `https://github.com/x/y.git` → `git@github.com:x/y.git`; git host
     detection via URL parse).
  2. `git remote remove origin` (ignore missing), `git remote add origin <url>`.
  3. `git fetch --unshallow origin` (fall through gracefully if the repo was
     never shallow — `git fetch origin` instead).
  4. Persist `dev` state via U2's helper.
- `DevToPin(unit)`:
  1. Verify state is not `dev-dirty` and there are no commits beyond upstream
     (callers handle confirms; this is the no-confirm path). If callers want to
     discard, they can pass a `force bool` flag.
  2. Delete the src dir, persist empty state, let the next build re-run
     `source.Prepare`.
- The HTTPS→SSH URL transform is a small helper; only handles GitHub / GitLab /
  generic-port-22 SSH for now (others fall through with HTTPS).

**Patterns to follow:**

- `internal/dev.go`'s existing `gitCmd` helper.
- `internal/source/workspace.go`'s git invocation conventions.

**Test scenarios:**

- Happy path: pin-state checkout → `DevToUpstream(unit, false)` → state becomes
  `dev`, origin is the unit's `Source` URL (HTTPS).
- Happy path: pin-state checkout → `DevToUpstream(unit, true)` for a GitHub
  HTTPS URL → origin becomes SSH form.
- Happy path: dev-state checkout → `DevToPin(unit)` (clean) → src dir removed,
  state file cleared.
- Edge case: `DevToUpstream` on a non-git source (.tar.gz upstream) → returns
  informative error, doesn't touch the src dir.
- Edge case: `DevToUpstream` when origin is already set → idempotent; rewrites
  cleanly.
- Error path: `git fetch` fails (network down) → returns error, state unchanged.
- Integration: after `DevToUpstream`, `DetectState` returns `dev`. After
  `DevToPin`, `DetectState` returns empty (no src dir).

**Verification:**

- Both functions are pure library calls; no TUI code.
- Existing `DevDiff` / `DevExtract` / `DevStatus` continue to pass their
  existing tests.

---

- [ ] U4. **`.star` field rewriter**

**Goal:** Regex-based rewriter that updates a unit's `tag = "..."` /
`branch = "..."` field in place, preserving comments and surrounding formatting.
Used by U5.

**Requirements:** R5.

**Dependencies:** None (pure file IO + regex).

**Files:**

- Create: `internal/starlark/edit.go`
- Test: `internal/starlark/edit_test.go`

**Approach:**

- `RewriteUnitField(starPath, unitName, field, value string) error`:
  1. Read the file.
  2. Locate the matching `unit(` or class-call (`autotools(`, `cmake(`, etc.)
     whose `name = "<unitName>"` field is set.
  3. Find the targeted `<field> = "..."` line within that call.
  4. Regex-replace the value, leaving leading whitespace and trailing comments
     alone.
  5. If `field == "tag"` and the unit has a `branch = "..."`, remove that branch
     line. (Mutually exclusive in yoe's source layer.)
  6. Write atomically (write to tmp, rename).
- `RemoveUnitField(starPath, unitName, field string) error` — for the case where
  the user picks branch and we strip the existing tag.

**Patterns to follow:**

- The .star files in `modules/module-core/units/base/` are representative
  inputs; tests should cover the autotools and cmake forms in addition to bare
  `unit()`.

**Test scenarios:**

- Happy path: rewrite `tag = "v1.0"` → `tag = "v1.1"` on a bare `unit()` call.
- Happy path: rewrite `tag = ...` on an `autotools(...)` call.
- Happy path: switching branch → tag drops the `branch =` line and
  inserts/updates `tag =`.
- Edge case: the file has multiple `unit()` calls; only the matching one is
  rewritten.
- Edge case: comment on the same line as the field is preserved.
- Error path: unit name not found in file → returns error, file untouched.
- Error path: write failure (permission denied) → tmp file cleaned up.

**Verification:**

- Round-trip test: rewrite, parse the result with
  `internal/starlark.LoadProject`, verify the new field value is picked up.

---

- [ ] U5. **Promote-to-pin action**

**Goal:** `DevPromoteToPin(unit, kind PinKind) error` where `kind` is
`tag`/`hash`/`branch`. Updates the unit's `.star` and advances the local
`upstream` tag to HEAD so state transitions `dev-mod` → `dev`.

**Requirements:** R5.

**Dependencies:** U1 (detect state), U3 (DevTo\* infrastructure), U4 (rewriter).

**Files:**

- Modify: `internal/dev.go`
- Test: `internal/dev_test.go`

**Approach:**

- Pre-flight: assert state is `dev-mod` (returns error otherwise — UI is
  responsible for blocking the call from `dev-dirty` etc.).
- For `kind == tag`: read `git tag --points-at HEAD`, fail if empty. Call U4's
  `RewriteUnitField` with `field=tag`, value=tag name.
- For `kind == hash`: `git rev-parse HEAD` → 40-char sha. Rewrite `tag` field
  with sha (yoe accepts sha in `tag`).
- For `kind == branch`: `git rev-parse --abbrev-ref HEAD`, rewrite `branch`
  field.
- After the rewrite: `git tag -f upstream HEAD` so `git rev-list upstream..HEAD`
  returns 0.
- Persist `dev` state via U2.

**Patterns to follow:**

- Same `gitCmd` shape as elsewhere in `internal/dev.go`.

**Test scenarios:**

- Happy path: `dev-mod` with HEAD on a tag → promote to tag → `.star` updated,
  `upstream` tag at HEAD, state `dev`.
- Happy path: `dev-mod` with HEAD on an untagged commit → promote to hash →
  `tag = "<sha>"` lands.
- Happy path: promote to branch → `branch = "<name>"` lands, prior `tag = ` line
  removed.
- Error path: called on `dev-dirty` → returns error before any file change.
- Error path: called on `pin` or plain `dev` → returns error (no-op).
- Error path: kind=tag but HEAD has no tag → returns informative error.
- Integration: after promote, `DetectState` returns `dev` (not `dev-mod`).

**Verification:**

- The `.star` file parses cleanly after every promote variant.
- The `upstream` git tag points at HEAD after every successful promote.

---

- [ ] U6. **Module pin↔dev toggle**

**Goal:** Module-side equivalent of U3: `ModuleToUpstream(module, ssh)` /
`ModuleToPin(module)`.

**Requirements:** R1, R2.

**Dependencies:** U1 (state detection works on any git dir), U2 (module state
persistence).

**Files:**

- Create: `internal/module/dev.go`
- Test: `internal/module/dev_test.go`

**Approach:**

- Module clones already have a real remote (they were `git clone`d).
  `ModuleToUpstream` mostly does:
  1. If user wants SSH and current origin is HTTPS → rewrite.
  2. If clone is shallow (`git rev-parse --is-shallow-repository` → true) →
     `git fetch --unshallow`.
  3. Persist `dev` state.
- `ModuleToPin(module)`: re-run the project's module-sync logic to reset the
  clone to the declared ref. `internal/module/fetch.go`'s `SyncIfNeeded` is the
  natural reuse point.
- Locally-overridden modules (`module(local = "...")`) skip the toggle entirely
  and report `local` state — the caller checks `ResolvedModule.Local != ""`.

**Patterns to follow:**

- `internal/module/fetch.go` for clone path resolution.

**Test scenarios:**

- Happy path: module on HTTPS origin → `ModuleToUpstream(name, true)` → origin
  rewritten to SSH.
- Happy path: shallow module → `ModuleToUpstream(name, false)` → unshallowed.
- Happy path: full-history HTTPS module + user picks HTTPS → no-op, state set to
  `dev`.
- Happy path: dev-state module → `ModuleToPin(name)` → resets to declared ref.
- Edge case: `local` module → both functions return informative error, no side
  effects.

**Verification:**

- After `ModuleToUpstream`, `DetectState(moduleDir)` returns `dev`.
- After `ModuleToPin`, the module's HEAD matches the project's declared ref.

---

- [ ] U7. **TUI: SRC column on units and modules tabs + detail-page Source
      line**

**Goal:** The visible state surface — color-coded SRC column (9 chars wide) on
both list views, plus a SOURCE line near the top of the unit detail page.

**Requirements:** R2.

**Dependencies:** U1 (state detection), U2 (state cache lookup), U12
(`BuildMeta.SourceDescribe` for the detail-page git description).

**Files:**

- Modify: `internal/tui/app.go`
- Test: `internal/tui/app_test.go`

**Approach:**

- Add a `srcStateStyle(state)` helper returning a lipgloss style for each token
  (gray / cyan / green / red / dim for `local` / blank for unbuilt).
- Insert the SRC column between MODULE and SIZE in the units-tab table render.
  Skip image/container units (display blank).
- Same column on the modules-tab list.
- On the detail page, add a SOURCE line below the existing metadata block: state
  token + remote URL + the `git describe --dirty --always` string cached in
  `BuildMeta.SourceDescribe` (e.g. `v3.4.1-3-gabc1234-dirty`).
- State source-of-truth at render time: the cached `BuildMeta.SourceState`, with
  `DetectState` as the cold-start fallback the first time a unit is rendered
  after restart.

**Patterns to follow:**

- Existing column render in `renderUnitsBody` (the `clipFixed` helper).
- `helpKeyStyle` / `dimStyle` for color-coding.
- Detail metadata rendering — match the indentation of the existing rows.

**Test scenarios:**

- Happy path: a unit with cached `dev-mod` state renders with the green token in
  the SRC column.
- Happy path: an image unit renders empty SRC (no source dir).
- Happy path: detail page renders SOURCE line for a unit, with remote URL.
- Edge case: a unit with no cached state and no `.git` dir → empty SRC, detail
  page shows "(not built)".
- Edge case: column truncates correctly when the table is in a narrow terminal.

**Verification:**

- A `yoe build` followed by a `tui` invocation shows the right state for each
  unit.
- Toggling a unit's state via the CLI (call U3 directly) and re-rendering picks
  up the new state.

---

- [ ] U8. **TUI: `u` and `P` keybindings + popups**

**Goal:** Wire the toggle and promote actions to the detail page. SSH-vs-HTTPS
popup on pin → dev. Tag/hash/branch popup on `P`. Confirm-modal warnings before
any state-discarding transition.

**Requirements:** R1, R5.

**Dependencies:** U3 (unit toggle), U5 (promote), U6 (module toggle), U7
(rendering of state).

**Files:**

- Modify: `internal/tui/app.go`
- Test: `internal/tui/app_test.go`

**Approach:**

- Add `viewSourcePrompt` modal stage (similar to existing `viewConfirm` and
  `viewSetup`). Carries an enum of which prompt is showing: `promptSSHHTTPS`,
  `promptPinKind`, `promptDiscardDev`.
- Detail page: `u` key — fires SSH/HTTPS prompt if state is `pin`; fires
  discard-confirm if state is `dev-mod`/`dev-dirty`; goes straight through if
  state is `dev`.
- Detail page: `P` key — only available in `dev-mod`; fires tag/hash/branch
  prompt. The prompt shows which kinds are valid (greys out tag if HEAD has no
  tag).
- Modules-tab expanded view: same `u` binding.
- After action completes, refresh the state cache and re-render.

**Patterns to follow:**

- `m.confirm` modal pattern (existing).
- `updateSearch` for the focused-modal sub-update shape.
- The recently-added context-dependent help bar (`searchEditHelpItems` pattern).

**Test scenarios:**

- Happy path: `pin` state, press `u` → SSH/HTTPS prompt appears → pick HTTPS →
  `DevToUpstream` runs → state becomes `dev`.
- Happy path: `dev-mod`, press `P` → tag/hash/branch prompt → pick tag → `.star`
  rewritten, state becomes `dev`.
- Happy path: `dev-dirty`, press `u` → discard-confirm prompt with count of
  uncommitted files → user accepts → `DevToPin` runs.
- Edge case: `dev-mod`, press `P` → tag option disabled (HEAD has no tag) → user
  picks hash → succeeds.
- Edge case: `pin`, press `P` → no-op with hint message.
- Error path: SSH selected but URL doesn't translate (not a github/gitlab host)
  → error toast, state unchanged.
- Integration: after a successful pin → dev transition, the SRC column for that
  unit repaints to `dev` immediately.

**Verification:**

- All four states' actions produce the expected state transitions end-to-end
  through the TUI.
- No state leaks into other modal flows (confirm prompt, search bar, etc.).

---

- [ ] U9. **Live status detection: fsnotify + poll fallback**

**Goal:** Keep displayed state fresh while the TUI is running. Watcher set
armed/disarmed in lockstep with each unit/module's dev-state membership.

**Requirements:** R3.

**Dependencies:** U1 (state detection), U7 (state-driven rendering).

**Files:**

- Create: `internal/tui/sourcewatch.go`
- Test: `internal/tui/sourcewatch_test.go`
- Modify: `internal/tui/app.go` (wire watcher events into the model)
- Modify: `go.mod` / `go.sum` (add `github.com/fsnotify/fsnotify`)

**Approach:**

- A `SourceWatch` struct owns the fsnotify watcher, a poll ticker, and a
  `tea.Msg` channel. Bubble Tea consumes via a `tea.Cmd`.
- On entering `dev*` for a unit/module: recursively add directory watches for
  `src/` (and `.git/refs/`, `.git/HEAD`).
- On returning to `pin`: remove watches.
- Poll loop: every 2-3s, call `DetectState` on each known dev unit/module. If
  state changed, push a refresh message.
- Debounce fsnotify events: collapse bursts within ~100ms into a single
  re-derive.

**Patterns to follow:**

- Bubble Tea's command pattern for sending messages from a goroutine: a
  long-lived `tea.Cmd` that receives from a Go channel.

**Test scenarios:**

- Happy path: write a file in a watched src dir → watcher fires within debounce
  window → state recomputes → message arrives.
- Happy path: poll loop catches a state change on a filesystem where fsnotify is
  muted (simulated by skipping the watcher add).
- Edge case: src dir deleted out from under the watcher → watcher cleans up;
  state recomputes to empty.
- Edge case: rapid burst of edits (vim swap-file save) → single debounced
  re-derive.
- Integration: `DevToUpstream(unit)` causes the watcher to arm; the next file
  edit produces a state change in the TUI.

**Verification:**

- Stand-alone `SourceWatch` test passes against a temp git repo.
- Manually: `vim build/<unit>/src/foo.c`, save, the SRC column flips to
  `dev-dirty` within a couple seconds.

---

- [ ] U10. **Build-time guard for dev units**

**Goal:** When `yoe build` runs against a unit (or implicitly via an image)
whose state is `dev*`, skip the source-preparation step (fetch / extract /
patch-apply) and let the build proceed against the user's existing src dir
as-is. The unit still builds — the guard only protects the working tree from
yoe's writes.

This generalizes the existing `hasLocalCommits` short-circuit in
`internal/source/workspace.go:29`: today, `Prepare()` already returns the
existing src dir untouched when there are commits beyond `upstream`, and the
executor proceeds straight into the unit's build tasks. U10 widens the trigger
from "commits beyond upstream" to "any state in the `dev*` set." A warning is
logged so the user knows the .star changes (changed `source` URL, `tag`,
`patches`, …) won't be picked up until they switch back to pin.

**Requirements:** R4.

**Dependencies:** U1, U2, U12 (so cache validity tracks dev edits — without U12,
this guard would still let a stale apk be reused after a dev edit).

**Files:**

- Modify: `internal/source/workspace.go`
- Modify: `internal/build/executor.go` (just to thread a writer for the warning,
  if not already in scope)
- Test: `internal/source/workspace_test.go`

**Approach:**

- `Prepare()` already short-circuits on `hasLocalCommits(srcDir)` — generalize
  to "any state in `pin`'s skip set." Concretely: if cached or detected state is
  `dev` / `dev-mod` / `dev-dirty`, log a warning to the writer and return the
  existing src dir without touching it.
- The warning text mentions the .star fields that _would_ have been applied
  (changed `source` URL, `tag`, `patches`, etc.) so the user knows what they're
  missing.

**Patterns to follow:**

- Existing `hasLocalCommits` short-circuit at line 29.

**Test scenarios:**

- Happy path: `dev` unit, `Prepare()` called → no fetch, no clone, no patch
  apply, no error.
- Happy path: `dev` unit with the .star's `tag` changed since the toggle →
  warning printed, src dir untouched.
- Happy path: `pin` unit (no cached state, no .git) → Prepare proceeds normally.
- Edge case: cached state says `dev` but the src dir is gone → fall through to
  Prepare (state cache was stale).

**Verification:**

- A unit toggled to `dev` and then rebuilt does not have its src dir
  overwritten.
- The warning surfaces in the build log.

---

- [ ] U11. **Docs + CHANGELOG**

**Goal:** Document the new keybindings, states, and behavior.

**Requirements:** All.

**Dependencies:** U1-U10 functionally complete.

**Files:**

- Modify: `docs/yoe-tool.md` (TUI keybinding tables, SRC column description,
  state legend)
- Modify: `CHANGELOG.md`

**Approach:**

- Add a "Source state" subsection under the TUI documentation with the
  four-state table and color legend.
- Document `u` and `P` keybindings on the detail page.
- One CHANGELOG entry summarizing the feature.

**Test scenarios:** Test expectation: none — pure documentation update.

**Verification:**

- `yoe_format_check` passes.
- The state legend matches what the TUI actually renders.

---

## System-Wide Impact

- **Interaction graph:** `internal/source/Prepare` is the choke point for the
  build-time guard; image-class units that pull in dev units get the same
  protection without changes (Prepare is called per-unit upstream of image
  assembly).
- **Error propagation:** A failing `DevToUpstream` (network down) must not leave
  the checkout in a half-rewritten state — git operations are individually
  atomic, but the persisted state file must stay consistent with the actual git
  state.
- **State lifecycle risks:** The cached state file can outlive the src dir
  (manual `rm -rf`). All consumers tolerate this — re-derive on miss.
- **API surface parity:** No public CLI or API changes. The toggle is TUI-only
  in v1; `DevToUpstream` / `DevToPin` are internal-only.
- **Integration coverage:** U3 + U7 + U9 cover the round trip from a CLI-side
  toggle to a TUI repaint. U8 + U9 cover the round trip from a TUI keybinding
  through the watcher back to a repaint.
- **Unchanged invariants:** The existing `yoe dev extract` / `yoe dev diff` /
  `yoe dev status` CLI commands keep working unchanged. The `hasLocalCommits`
  short-circuit in Prepare remains for callers that don't have a state cache
  yet.

---

## Risks & Dependencies

| Risk                                                                                                    | Mitigation                                                                                                                                                              |
| ------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| fsnotify silently fails on bind-mounts / network FS                                                     | Poll fallback runs in parallel; the system stays correct at slightly higher latency.                                                                                    |
| `git fetch --unshallow` on huge histories (kernel, llvm) takes minutes                                  | Run inside a `tea.Cmd` so the TUI stays interactive; surface progress in the prompt area. Allow Esc to cancel.                                                          |
| Regex-based `.star` rewriter breaks on unusual formatting (multi-line tag values, string concatenation) | Tests cover the formats actually used in `module-core`; if a unit author uses something exotic, the rewriter errors out cleanly and the user falls back to manual edit. |
| State cache can drift from actual git state if a process external to yoe rewrites the .git dir          | All cache reads fall through to `DetectState` on cold start; the watcher catches `.git/` changes during runtime.                                                        |
| New fsnotify dependency widens the build surface                                                        | Already a common Go dep (used by k8s, docker, terraform); minimal supply-chain risk.                                                                                    |

---

## Documentation / Operational Notes

- New TUI keybindings (`u`, `P`) belong in the keybindings table at
  `docs/yoe-tool.md`'s detail-page section.
- The four-state legend with colors is worth a screenshot/ASCII example in the
  same doc.
- No migration: existing yoe projects keep working; the SRC column shows blank
  for any unit yoe hasn't observed yet.

---

## Sources & References

- **Origin document:**
  [docs/brainstorms/2026-05-08-source-dev-mode-requirements.md](../brainstorms/2026-05-08-source-dev-mode-requirements.md)
- Related code: `internal/source/workspace.go`, `internal/dev.go`,
  `internal/build/meta.go`, `internal/module/fetch.go`, `internal/tui/app.go`.
- External docs: `github.com/fsnotify/fsnotify` (recursive watch pattern).
