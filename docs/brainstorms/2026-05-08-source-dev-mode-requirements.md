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

Dev mode is **pinned source + connectivity**, with the working-tree commit
determined by the unit's source declaration. Two shapes:

- **Unit declares `tag` only** (e.g., `tag = "v1.36.1"`): pin and dev build the
  same commit. Switching pin → dev keeps the working tree at the pinned ref;
  only connectivity changes (remote becomes real, history gets unshallowed). The
  build is bit-identical between modes.
- **Unit declares both `tag` and `branch`** (e.g.,
  `tag = "v1.36.1", branch = "master"`): pin builds the tagged commit; switching
  pin → dev checks out `origin/<branch>` HEAD, which may be ahead of the tag.
  The build moves to branch HEAD; dev builds are inherently per-machine
  (dependent on fetch time).

The `tag` field accepts either a tag name or a 40-char SHA — the source layer
treats both as opaque refs. Use a SHA when there is no upstream tag to pin
against.

What changes on every pin → dev transition (regardless of which shape):

- **Remote URL becomes real.** Pinned clones are bare-bones (often shallow,
  often without an `origin` pointing anywhere usable). Dev mode rewrites
  `origin` to the upstream URL the user picks (HTTPS or SSH) so `git pull`,
  `git push`, `git log origin/main`, etc. all work.
- **History gets populated.** A `git fetch --unshallow` runs so the user can
  browse log, blame, and diff against earlier upstream commits.
- **The local `upstream` git tag always stays at the pinned commit.** That way
  `git rev-list upstream..HEAD` counts commits past the pin regardless of
  whether the unit declares a `branch`. The dev-mod signal then answers "would a
  build here produce different output than pin mode?" at a glance — a unit with
  `branch` declared that just toggled to dev sits on `origin/<branch>` HEAD,
  which is typically past the pin, so it goes straight to dev-mod.
- **The TUI stops rewriting the source on rebuild.** Once a unit is in dev mode,
  yoe will never `git clean -fdx` or re-clone the source dir, even if the unit's
  `source` URL or `tag` changes in the .star. Pin → dev is the user's commitment
  to manage the checkout themselves; rebuild just warns and proceeds with
  whatever's there.

The user moves between branches or off-branch commits inside dev mode by hand
(the detail page's `$` shortcut drops them into a shell). The toggle doesn't
prompt for a branch — the unit declaration is the policy.

## Goals

- One TUI keystroke flips a unit (or module) between **pin** and **dev** mode.
  Pin = fresh shallow clone at the declared ref, no remote. Dev = real remote
  (HTTPS or SSH), full history, working tree at the unit's declared `branch`
  HEAD if one is set, otherwise at the pinned commit.
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
- **Interactive branch picker.** Dev mode's branch is declared in the unit
  (`branch = "..."`), not chosen interactively at toggle time. Switching to a
  different branch inside dev mode = git checkout by hand from `$`.
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

`dev-mod` is the "build would differ from pin" state. `upstream` always anchors
at the pinned commit, so `dev-mod` means "commits past the pin" regardless of
whether the unit declares a `branch`. A branch-tracking unit that just toggled
to dev typically lands in `dev-mod` immediately because `origin/<branch>` HEAD
is past the pin tag — that's the visible signal that yoe is now building
something different from the pinned reference. `yoe dev extract` is still the
natural next action when the diff represents work to extract as patches.

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
  remote, fetch unshallow, persist `dev` state). When the unit declares a
  `branch`, the transition also checks out `origin/<branch>` HEAD; otherwise the
  working tree stays at the pinned commit.
- From `dev` / `dev-mod` / `dev-dirty`: prompts to confirm the dev → pin
  transition (which discards any commits beyond upstream and dirty edits).
  Single-keystroke confirm if the state is plain `dev` (nothing at risk).

### Modules tab — detail/expand

Same `u` binding from the module's expanded view. Same prompts. The "discard
local commits" warning matters more for modules since losing pushed-elsewhere
work is more common than for unit checkouts.

### Pin to current — `P` keybinding

A second keybinding on the detail page (`P` for "pin to current") captures the
working tree's current HEAD into the unit's `tag` field. The action is
deliberately simple — no popup, no tag/hash/branch picker:

- If HEAD has an annotated or lightweight tag pointing at it, the tag name is
  written: `tag = "v1.38.0"`.
- Otherwise the 40-char SHA is written: `tag = "abc123def..."`.
- The `branch` field is never written by `P`. Branch tracking is declared by the
  unit author; the pin command only updates the pin.

After the rewrite, yoe re-points the local `upstream` git tag to HEAD so
`git rev-list upstream..HEAD` returns zero and the unit transitions from
`dev-mod` (if it was there) back to plain `dev`.

`P` is available in `dev` and `dev-mod`. In `dev-dirty` it's disabled with a
hint (`P pin: commit or stash first`) so the captured state is reproducible. In
`pin` mode the unit's source dir isn't a working checkout, so the action isn't
offered.

The convenience over hand-editing: one keystroke replaces "find the right line
in the `.star`, get the tag name from git, paste, save, switch back to pin." The
picker variants from the original design (separate tag/hash/branch popup) are
intentionally collapsed — a single `tag =` rewrite covers the common workflows,
and the user can hand-edit when they want something exotic.

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
                       ┌──────────────────┐
                       │       pin        │
                       │ shallow, no rmt  │
                       └────────┬─▲───────┘
                                │ │
   press `u` → SSH/HTTPS prompt │ │ press `u` → confirm if dev-mod
   → fetch --unshallow          │ │ or dev-dirty (loses work)
   → checkout origin/<branch>   │ │ → re-clone shallow
     if `branch` declared       │ │
                                ▼ │
                       ┌──────────────────┐
                  ┌───►│       dev        │◄────────────────┐
                  │    └──┬───────────┬───┘                 │
                  │       │           │                     │
        git stash │       │ edit      │ git commit          │ press `P`
        / reset   │       │ files     │                     │ → rewrite
                  │       ▼           ▼                     │   tag = HEAD
                  │  ┌──────────┐ ┌──────────┐              │ → move upstream
                  └──┤dev-dirty │ │ dev-mod  ├──────────────┘   tag to HEAD
                     │uncommit'd│ │ N commits│
                     │  edits   │ │  ahead   │
                     └────┬─────┘ └─────┬────┘
                          │ git commit  │
                          └─────────────┘
                          (auto-detected)
```

Transitions:

- **pin → dev** (`u`): SSH/HTTPS prompt; rewrite remote,
  `git fetch --unshallow`. If the unit declares a `branch`, check out
  `origin/<branch>` HEAD; otherwise the working tree stays at the pinned commit.
  Persist `dev` state to build.json.
- **dev → pin** (`u`): if `dev-mod` or `dev-dirty`, prompt with warning about
  losing local work. On confirm: re-clone via the existing `source.Prepare`
  path.
- **dev / dev-mod → dev** (`P`): rewrite the unit's `tag` field to HEAD (tag
  name if HEAD has one, otherwise 40-char SHA); move the local `upstream` tag to
  HEAD so `dev-mod` collapses back to `dev`. Disabled in `dev-dirty` (commit or
  stash first) and in `pin`.
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
  `DevDetectState(unit)` / `DevPinToCurrent(unit)` and reuse the same git-cmd
  scaffolding.
- `DevToUpstream` reads the unit's declared `branch` field (if any) and checks
  out a local branch named `<branch>` at `origin/<branch>` HEAD after fetching.
  With no branch declared the working tree stays at the pinned commit. The local
  `upstream` git tag is always anchored at the pinned commit so dev-mod counts
  commits past the pin — a branch-tracked unit toggled into dev with branch HEAD
  ahead of pin lands in dev-mod immediately.
- `DevPinToCurrent` writes HEAD into the unit's `tag` field (tag name when HEAD
  has one, otherwise 40-char SHA) and moves the local `upstream` tag to HEAD.
  Only writes to `tag` — `branch` is never touched. A regex-based `.star` field
  rewriter (`internal/starlark/edit.go`) preserves surrounding comments and
  whitespace.
- The unit's source declaration allows `tag` and `branch` to coexist: `tag` is
  the pin (a tag name or a SHA), `branch` is the optional dev-mode tracking ref.
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
- **Default branch detection when the unit doesn't declare one.** A unit with no
  `branch =` field keeps the working commit at the pin on toggle. For units that
  should track upstream but don't yet declare a branch, the dev edits the .star
  to add `branch = "..."`. There is no auto-discovery of the upstream's default
  branch via `git remote show origin` in v1; a follow-on could add
  detection-on-toggle.
- **Branch HEAD is past the pin tag.** When a declared `branch` has moved ahead
  of the pinned `tag` between pin builds and the dev's first toggle to dev, the
  pin → dev transition silently advances the working tree to branch HEAD. The
  TUI should surface "moved N commits to origin/<branch>" in the SOURCE detail
  line so the change isn't invisible.

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
- A unit declaring `branch = "..."` is auto-checked-out to `origin/<branch>`
  HEAD on pin → dev toggle; a unit with no branch declaration keeps the working
  tree at the pinned commit. The `dev-mod` count reflects commits beyond the
  tracked anchor (branch HEAD when declared, the pin otherwise).
- The `P` keystroke on the detail page rewrites the unit's `tag` field to HEAD's
  tag (when one exists) or its 40-char SHA, advances the local `upstream` tag to
  HEAD, and leaves the unit in `dev` state. Disabled in `dev-dirty` and `pin`.
  Never writes the `branch` field.
- The displayed state updates within seconds of an external edit (shell via `$`,
  an editor in another window) — no TUI restart required.
