# Source dev-mode toggle — requirements

**Date:** 2026-05-08 **Scope:** TUI affordance + persistent state for switching
a unit (or module) between its yoe-pinned source and an upstream-tracking dev
checkout, with visible state on the units tab, modules tab, and detail pages.

## Problem

Yoe units fetch source at a pinned ref into `build/<unit>/src/` as a tagged git
repo (`upstream` points at the pinned commit; patches are applied on top). The
clone is shallow — no history, no real remote — so the checkout is good for
producing a build but useless for development. The CLI already supports a "dev
workflow": if the source dir has commits beyond `upstream`, yoe leaves it alone,
and `yoe dev extract` turns those commits into `*.patch` files. Modules behave
similarly — they're git clones at a project-declared ref, but typically with
full history because they're navigated by humans.

What's missing:

1. **No TUI affordance for the pin↔dev toggle.** A developer who wants to hack
   on a unit has to manually `cd build/<unit>/src/`, unshallow the repo, set up
   the remote, etc.
2. **No visible state.** The TUI gives no indication whether a unit's source is
   fresh-from-pin, on a dev branch, has local commits, or has uncommitted edits
   — so it's easy to lose work to a `yoe build --clean` or to flip back without
   realising what's at stake.
3. **No SSH/HTTPS choice.** When switching to dev mode, the pinned URL is
   typically HTTPS (read-only). Devs who need to push want SSH. Today they
   `git remote set-url` by hand.
4. **No live status.** Once the TUI is running, edits made via the `$` shell
   shortcut (or any other tool) don't update the displayed state until the TUI
   is restarted.

This brainstorm captures requirements for adding all four to the TUI.

## What `dev` actually does

Dev mode is **the same build as pinned**, plus connectivity. Concretely, when
you switch from `pin` to `dev`, the working tree stays at the exact same commit
(branch / tag / sha — whatever the unit's source declared); the build output is
bit-identical. What changes:

- **Remote URL becomes real.** Pinned clones are bare-bones (often shallow,
  often without an `origin` pointing anywhere usable). Dev mode rewrites
  `origin` to the upstream URL the user picks (HTTPS or SSH) so `git pull`,
  `git push`, `git log origin/main`, etc. all work.
- **History gets populated.** A `git fetch --unshallow` runs so the user can
  browse log, blame, and diff against earlier upstream commits.
- **The TUI stops rewriting the source on rebuild.** Once a unit is in dev mode,
  yoe will never `git clean -fdx` or re-clone the source dir, even if the unit's
  `source` URL or `tag` changes in the .star. Pin → dev is the user's commitment
  to manage the checkout themselves; rebuild just warns and proceeds with
  whatever's there.

The user moves between branches, commits, or remote tags inside dev mode by hand
(the detail page's `$` shortcut drops them into a shell). The toggle doesn't
pick branches; it sets up the remote and history once.

## Goals

- One TUI keystroke flips a unit (or module) between **pin** and **dev** mode.
  Pin = fresh shallow clone at the declared ref, no remote. Dev = same commit
  checked out, real remote (HTTPS or SSH), full history.
- The active source state is visible at a glance on the units tab, the modules
  tab, and each detail page — including whether the work tree has uncommitted
  changes or commits beyond upstream.
- Switching to dev mode prompts for HTTPS vs SSH so users don't have to remember
  the `git remote set-url` incantation.
- The TUI keeps the displayed state fresh while running: edits made through the
  `$` shell shortcut (or any external tool) update within seconds, with no
  manual refresh.
- Dev units are sacred: yoe never silently overwrites the source dir of a unit
  in dev mode. Pin units rebuild their source freely; dev units only warn.

## Non-goals

- **Persisting dev-mode preferences across `yoe init`/cache wipes.**
  `local.star` is the natural home for that; deferring until we see whether
  ephemeral state is a real pain point.
- **Multi-remote / fork management.** No "set my fork as origin, upstream as
  upstream" workflow; that's a fancier dev story for later.
- **Branch picker.** Initial dev-mode checkout stays on the same commit pinned
  mode produced. Switching branches = git checkout by hand from `$`.
- **Auto-fetch on view.** State display reflects the local working tree only; it
  does not run `git fetch` to compare against remote HEAD.
- **Touching the unit's `source` URL in the .star.** The toggle only rewrites
  the local checkout's git remote, never the project source.

## States

Four states per unit/module, each a distinct color in the TUI. Detection is
local — no network — and runs from `git status` plus
`git rev-list upstream..HEAD`.

| State       | Meaning                                                    | Color |
| ----------- | ---------------------------------------------------------- | ----- |
| `pin`       | Pinned shallow clone, no upstream remote configured        | gray  |
| `dev`       | Real remote configured, work tree clean, no commits beyond | cyan  |
| `dev-mod`   | Dev mode + has commits beyond upstream                     | green |
| `dev-dirty` | Dev mode + uncommitted edits in the work tree              | red   |

There is intentionally no `pin-dirty`. The discipline is: **if you want to edit
a unit's source, switch it to dev first.** Pin is for the build pipeline to
manage; dev is for humans. A pin-mode src dir with edits is a misuse of pin
mode, not a state worth modelling — yoe is allowed to overwrite it.

`dev-mod` is the "I have work to extract" state. `yoe dev extract` is the
intended next action; the detail page should hint at it.

`dev-dirty` is the "I have unsaved edits" state — most warnings fire here.

### Modules

Modules use the same four states with one nuance: modules are typically already
cloned with a real remote (you `git clone`'d them) and may have full history.
The pin → dev transition for a module is therefore lighter:

- If the remote is already an HTTPS URL and the user wants SSH, rewrite it.
- If the clone is shallow, unshallow it.
- Otherwise no work — just flag the state as `dev` so the user sees the module
  is consciously under their control.

A locally-overridden module (`module(local = "../path")`) is shown as `local`
instead of any of the four; the toggle is disabled for it. Whatever the user
does in their local checkout is theirs to manage.

## Live status detection

The displayed state has to keep up with edits made outside the TUI (the `$`
shell shortcut, an editor in another window, an external git command). Two
mechanisms, in priority order:

1. **fsnotify watcher** on each `src/` directory and module clone currently in
   any `dev*` state. Recursive watch fires on file create/modify/delete or any
   change under `.git/` (which catches commits, branch switches, fetches). The
   TUI re-derives state for that unit/module within ~100ms.
2. **Periodic poll fallback** for filesystems where fsnotify can't watch
   (network mounts, some FUSE filesystems): every 2-3 seconds,
   `git status --porcelain` + `git rev-list --count upstream..HEAD` per dev
   unit. Skip pin units — they don't change without yoe's involvement, and yoe
   already triggers a state recompute when it rebuilds them.

Watcher scope is **every unit and module in dev state**, not just the visible
ones. The TUI's view shifts as the user scrolls or filters; the underlying state
shouldn't. Dev units are usually a small handful in practice (the ones a
developer is actively hacking on), so the kernel-watch budget is fine. Tying
watcher lifecycle to "in dev state" instead of "on screen" also keeps the
implementation simple — toggle a unit to dev, the watcher arms; toggle back to
pin, the watcher disarms. No bookkeeping against scroll position, query filter,
or detail-page-open state.

State changes the user cares about — `dev` → `dev-dirty`, `dev-dirty` →
`dev-mod` — surface in the TUI as the SRC column repaints. No popup; the
color/token swap is its own notice.

## TUI surfaces

### Units tab — list view

Add a column **SRC** between MODULE and SIZE, **9 characters wide** to fit the
longest token (`dev-dirty`), showing the state token in its color. Empty for
image and container units (which have no source dir).

### Modules tab — list view

Same `SRC` column in the same position alongside the existing module git-status
column. The column reads `pin` / `dev` / `dev-mod` / `dev-dirty` / `local` for
the module's own clone.

### Unit detail page

A new "Source" line near the top of the metadata block:

```
  SOURCE   dev-mod  (https://git.busybox.net/busybox)
           3 commits ahead of upstream
```

A new keybinding `u` ("upstream toggle") on the detail page. Pressing it:

- From `pin`: prompts SSH vs HTTPS, then runs the pin → dev transition (rewrite
  remote, fetch unshallow, persist `dev` state).
- From `dev` / `dev-mod` / `dev-dirty`: prompts to confirm the dev → pin
  transition (which discards any commits beyond upstream and dirty edits).
  Single-keystroke confirm if the state is plain `dev` (nothing at risk).

### Modules tab — detail/expand

Same `u` binding from the module's expanded view. Same prompts. The "discard
local commits" warning matters more for modules since losing pushed-elsewhere
work is more common than for unit checkouts.

### Promote current dev state to the .star pin

A second keybinding on the detail page (suggested: `P` for "pin to current"),
available when the unit is in `dev` or `dev-mod` state. It captures the
checkout's current git state back into the unit's `.star` definition, so the
next pinned-mode build picks up whatever the user has settled on (typically:
bumped to a newer upstream tag, or stabilised on a specific commit they
cherry-picked from upstream).

**Disabled when the src tree is dirty.** A `dev-dirty` unit must commit or stash
first; pinning a tree with uncommitted edits would either lose those edits on
the next pin-mode rebuild or freeze a non-canonical state into the project. The
detail page surfaces a hint instead of the action:
`P pin: commit or stash first`.

When invoked, yoe inspects the current HEAD and offers a popup asking which form
of pin to write:

- **Tag** — only offered if HEAD has an annotated or lightweight tag pointing at
  it. Writes `tag = "<tagname>"` in the unit's .star. Most common for "I bumped
  busybox to v1.38.0".
- **Hash** — always offered. Writes `tag = "<full-40-char-sha>"` (yoe's source
  layer accepts a sha in the `tag` field). Most reproducible; best when HEAD is
  on a commit with no tag.
- **Branch** — offered with a warning that branches are mutable and break build
  reproducibility. Writes `branch = "<branchname>"` and removes any existing
  `tag` field. Use sparingly.

Whichever form the user picks, the .star edit is a single-field rewrite
(regex-based to preserve comments and surrounding whitespace). The existing
`tag` / `branch` fields are updated in place; if the unit had a `branch` and the
user picks tag/hash, the `branch` line is removed.

After the pin lands, yoe also re-points the local `upstream` tag at the current
HEAD (`git tag -f upstream HEAD`). With the tag moved,
`git rev-list upstream..HEAD` returns zero commits and the unit transitions from
`dev-mod` back to plain `dev` — the visible acknowledgement that "what I'm
building is now what's pinned."

The underlying checkout doesn't move: the user's branch and working tree stay
where they were, just the upstream marker catches up. A later toggle back to pin
re-clones shallow at the new ref; the work tree stays consistent.

### Build-time warning, not overwrite

When `yoe build` is invoked on a unit/module in any `dev*` state, **the source
dir is never touched**. yoe prints a warning if the unit's declared `source` /
`tag` / `patches` would otherwise trigger a re-fetch or a re-apply:

```
warning: busybox is in dev mode; source URL changed in the .star but
yoe will not overwrite build/busybox.x86_64/src/ — switch back to pin
mode (`u` in the TUI) to pick up the new source.
```

The build proceeds against whatever the user's checkout currently has. Pin units
behave as before: source is freely re-fetched/re-cloned each rebuild.

## State machine

```
                 [pin: shallow clone, no remote]
                          │
                          │  press `u` → SSH/HTTPS prompt
                          │  → fetch --unshallow, set origin
                          ▼
              ┌──────── [dev] ────────┐
              │            │           │
   git commit │   edit     │  git      │
              │   files    │  reset    │
              ▼            ▼           │
         [dev-mod]    [dev-dirty]      │
              │            │           │
              │  press `u` → confirm   │
              │  → re-clone shallow    │
              ▼            ▼           ▼
                 [pin: shallow clone, no remote]
```

Transitions:

- **pin → dev** (`u`): SSH/HTTPS prompt; rewrite remote,
  `git fetch --unshallow`, persist `dev` state to build.json. Working tree
  commit unchanged.
- **dev → pin** (`u`): if `dev-mod` or `dev-dirty`, prompt with warning about
  losing local work. On confirm: re-clone via the existing `source.Prepare`
  path.
- **dev-mod → dev** (`P`): promote current HEAD to the unit's pinned ref —
  rewrite the .star's `tag`/`branch` field and move the local `upstream` tag to
  HEAD. Disabled when state is `dev-dirty` (commit or stash first).
- **dev ↔ dev-mod ↔ dev-dirty**: auto-detected from `git status` +
  `git rev-list upstream..HEAD`. No user action required.

## Persistence

- Per-unit: extend `build/<unit>.<scope>/build.json`'s `BuildMeta` struct with a
  `source_state` string field — the cached last-known state token.
- Per-module: new file `cache/modules/<module>/.yoe-state.json` (the cache dir
  is what yoe writes into when syncing modules; build/ is per-unit).
- Both files are advisory. If absent or stale, yoe re-derives state from the
  checkout. Lost on a `yoe repo clean` of build/, recovered silently on next
  refresh.
- Not synced to local.star. Defer that until cross-session persistence is a felt
  need.

## Implementation notes (for planning)

These are pointers, not the design — planning doc owns specifics.

- `internal/dev.go` already has `DevDiff` / `DevExtract` / `DevStatus` — extend
  with `DevToPin(unit)` / `DevToUpstream(unit, ssh bool)` /
  `DevDetectState(unit)` / `DevPromoteToPin(unit, kind)` (where `kind` is one of
  `tag`/`hash`/`branch`) and reuse the same git-cmd scaffolding.
- The .star rewriter for the promote action is regex-based — find the unit's
  `tag = "..."` / `branch = "..."` / `sha256 = "..."` line and splice.
  Round-tripping through a Starlark printer would lose comments.
- `BuildMeta.SourceState` string field; ReadMeta/WriteMeta unchanged.
- `internal/source/Prepare` already short-circuits when the src dir has local
  commits — extend the gate to "any unit in dev state" once the state file is
  the source of truth.
- TUI: new helpItem on the unit detail page, prompt rendering reuses the
  existing confirm-modal pattern (`m.confirm`).
- fsnotify watcher armed when a unit/module enters any `dev*` state and torn
  down when it returns to `pin`. Lifecycle is tied to the state, not to TUI
  rendering — typically just a handful of dev units, so the kernel-watch budget
  stays small without per-render bookkeeping.
- Module clones live under `cache/modules/<module>/` — `internal/module/` is the
  natural home for `ModuleDevToggle`.

## Risks / open questions

- **fsnotify on bind-mounts and network filesystems.** The watcher won't fire
  reliably on every filesystem; the polling fallback is the safety net.
  Detection of "watcher works here" is heuristic; planning to decide. Polling
  alone (no watcher) at 2-3s would also be acceptable for v1.
- **Modules in `local = "../path"` overrides.** Show `local` token, no toggle.
  The user's local checkout is theirs to manage.
- **`git fetch --unshallow` on huge upstream histories.** Linux kernel, llvm,
  etc. take meaningful time and disk to unshallow. Show the command running with
  a progress indicator; let the user cancel.
- **Default branch detection.** When the user wants to check out upstream's
  default branch (a follow-on we're not solving in v1), `git remote show origin`
  is one network round trip. v1 doesn't need this because dev keeps the working
  commit unchanged.

## Success criteria

- One keystroke on a unit's detail page flips its source between pin and dev,
  with HTTPS/SSH prompted on the way to dev.
- Units tab and modules tab both show the source state column. Color coding
  visible at a glance distinguishes the four states (plus `local` for overridden
  modules).
- A `yoe build` against a `dev*` unit warns and proceeds without overwriting the
  work tree.
- A `dev-mod` unit being switched back to pin shows the user the commit list and
  asks for confirmation before discarding.
- A `dev-mod` unit can be promoted to pin (`P`) by picking tag/hash/branch in a
  popup; the .star rewrites in place, the local `upstream` tag advances to HEAD,
  and the unit transitions to plain `dev`. The action is disabled (with a hint)
  for `dev-dirty`.
- The displayed state updates within seconds of an external edit (shell via `$`,
  an editor in another window) — no TUI restart required.
