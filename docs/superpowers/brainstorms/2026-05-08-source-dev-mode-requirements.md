# Source dev-mode toggle — requirements

**Date:** 2026-05-08 **Scope:** TUI affordance + persistent state for switching
a unit (or module) between its yoe-pinned source and an upstream-tracking dev
checkout, with visible state on the units tab, modules tab, and detail pages.

## Problem

Yoe units fetch source at a pinned ref into `build/<unit>/src/` as a tagged git
repo (`upstream` points at the pinned commit; patches are applied on top). The
CLI already supports a "dev workflow": if the source dir has commits beyond
`upstream`, yoe leaves it alone, and `yoe dev extract` turns those commits into
`*.patch` files. Modules behave similarly — they're git clones at a
project-declared ref, and a developer might want to navigate them on a branch
tracking upstream.

What's missing:

1. **No TUI affordance for the pin↔dev toggle.** A developer who wants to hack
   on a unit has to manually `cd build/<unit>/src/`, set up the remote, switch
   branches, etc.
2. **No visible state.** The TUI gives no indication whether a unit's source is
   fresh-from-pin, on a dev branch, has local commits, or has uncommitted edits
   — so it's easy to lose work to a `yoe build --clean` or to flip back without
   realising what's at stake.
3. **No SSH/HTTPS choice.** When switching to dev mode, the pinned URL is
   typically HTTPS (read-only). Devs who need to push want SSH. Today they
   `git remote set-url` by hand.

This brainstorm captures requirements for adding all three to the TUI.

## Goals

- One TUI keystroke flips a unit (or module) between **pinned** and **dev**
  mode. Pinned = fresh clone at the declared ref. Dev = remote rewritten to
  point at upstream's main branch, ready to track and pull.
- The active source state is visible at a glance on the units tab, the modules
  tab, and each detail page — including whether the work tree has uncommitted
  changes or commits beyond upstream.
- Switching to dev mode prompts the user for HTTPS vs SSH so they don't have to
  remember the `git remote set-url` incantation.
- Any operation that would discard dirty or modified state (rebuild from pin,
  force-clean, switch back) prompts before doing so.
- State is per-unit/per-module, lives in the unit's build state file, and is
  recoverable from the git state of the checkout itself — losing the state file
  just means yoe re-detects on next view.

## Non-goals

- **Persisting dev-mode preferences across `yoe init`/cache wipes.**
  `local.star` is the natural home for that; deferring until we see whether
  ephemeral state is a real pain point.
- **Multi-remote / fork management.** No "set my fork as origin, upstream as
  upstream" workflow; that's a fancier dev story for later.
- **Branch picker.** Initial dev-mode checkout is whatever upstream HEAD's
  default branch is. Picking a different branch = git checkout by hand (the unit
  detail page already has a `$` shell shortcut).
- **Auto-fetch on view.** State display reflects the local working tree only; it
  does not run `git fetch` to compare against remote HEAD.
- **Touching the unit's `source` URL in the .star.** The toggle only rewrites
  the local checkout's git remote, never the project source.

## States

Five states per unit/module, with a distinct color each. The state is derived
from the local git checkout, not stored — but a one-line cache in the unit's
`build.json` (and a sibling file for modules) avoids re-running git on every TUI
render.

| State       | Meaning                                                    | Color  |
| ----------- | ---------------------------------------------------------- | ------ |
| `pin`       | Fresh clone, HEAD == `upstream` tag, work tree clean       | gray   |
| `pin-dirty` | At pinned ref but work tree has uncommitted edits          | yellow |
| `dev`       | Remote rewritten to upstream, on upstream branch HEAD      | cyan   |
| `dev-mod`   | Dev mode + has commits beyond upstream                     | green  |
| `dev-dirty` | Dev mode + uncommitted edits (regardless of commits ahead) | red    |

`pin-dirty` is the silent-data-loss state: yoe rebuilds will overwrite those
edits. Calling it out with a warning color earns its keep.

`dev-mod` is the "I have work to extract" state. `yoe dev extract` is the
intended next action; the TUI should hint at it.

`dev-dirty` is the "I have unsaved work even by git's standards" state. Most
warnings should fire here.

Modules use the same five states with the same semantics: `pin` = at the
declared ref, `dev` = remote rewritten to upstream and tracking its default
branch, `dirty`/`mod` track work tree and commits-ahead the same way. Symmetric
mental model is worth more than per-domain simplification.

## TUI surfaces

### Units tab — list view

Add a column **SRC** between MODULE and SIZE, four characters wide, showing the
state token (`pin`, `dev`, `dev-mod`, etc.) in its color. Empty for image and
container units (which have no source dir).

### Modules tab — list view

Same `SRC` column, in the same position alongside the existing module git-status
column. The column reads `pin` / `dev` / etc. for the module's own clone.

### Unit detail page

A new "Source" line near the top of the metadata block:

```
  SOURCE   dev-mod  (https://git.busybox.net/busybox)
           3 commits ahead of upstream
```

Add a single keybinding (suggested: `u` for "upstream toggle") on the detail
page. Pressing it:

- From `pin*`: prompts SSH vs HTTPS, then runs the pin→dev transition (rewrite
  remote, fetch, checkout upstream branch). If the source dir is `pin-dirty`,
  prompts to confirm losing the dirty edits first.
- From `dev*`: prompts to confirm the dev→pin transition (which discards any
  commits beyond upstream and dirty edits). Single-keystroke confirm if the
  state is plain `dev` (nothing at risk).

### Modules tab — detail/expand

Same `u` binding from the module's expanded view. Same prompts. The "discard
local commits" warning may matter more for modules since losing pushed-elsewhere
work is more common than for unit checkouts.

### Status bar warnings

When `yoe build` is invoked on a unit/module in `pin-dirty` state, emit a stderr
warning before the unit is rebuilt:

```
warning: busybox has uncommitted changes in build/busybox.x86_64/src/
that will be overwritten. Switch to dev mode (`u`) or commit first.
```

Don't block the build — just warn.

## State machine

```
                    +---------+
                    |         | external file edits
        pin <------>+ pin-dirty
         ^   warn   |   ^
         |          |   | git commit (still on upstream tag)
         |          |   | / local edits
         |          v   v
        u           |   |
         |          |   |
         v          v   v
        dev <----> dev-dirty
         ^   ^
         |   | git commit
         v   |
        dev-mod  (commits beyond upstream)
```

Transitions:

- **pin → dev** (`u`): if pin-dirty, prompt; ask SSH/HTTPS; rewrite remote,
  `git fetch`, `git checkout <default-branch>`, set tracking. Persist `dev`
  state to build.json.
- **dev → pin** (`u`): if dev-mod or dev-dirty, prompt with warning about losing
  local work. On confirm: re-clone at pinned ref (re-runs the existing
  `source.Prepare` path).
- **Auto-detected**: pin↔pin-dirty and dev↔dev-mod↔dev-dirty are detected by yoe
  on TUI refresh from `git status` + `git rev-list upstream..HEAD` — no user
  action needed.

## Persistence

- Per-unit: extend `build/<unit>.<scope>/build.json`'s `BuildMeta` struct with a
  `source_state` string field.
- Per-module: new file `cache/modules/<module>/.yoe-state.json` (the cache dir
  is what yoe writes into when syncing modules; build/ is per-unit).
- Both files are advisory. If absent, yoe re-derives state from the checkout.
  Lost on a `yoe repo clean` of the build/, recovered silently on next refresh.
- Not synced to local.star. Defer that until cross-session persistence is a felt
  need.

## Implementation notes (for planning)

These are pointers, not the design — planning doc owns specifics.

- `internal/dev.go` already has `DevDiff` / `DevExtract` / `DevStatus` — extend
  with `DevToPin(unit)` / `DevToUpstream(unit, ssh bool)` /
  `DevDetectState(unit)` and reuse the same git-cmd scaffolding.
- `BuildMeta.SourceState` string field; ReadMeta/WriteMeta unchanged.
- TUI: new helpItem on the unit detail page, prompt rendering reuses the
  existing confirm-modal pattern (`m.confirm`).
- Module clones live under `cache/modules/<module>/` — `internal/module/` is the
  natural home for `ModuleDevToggle`.

## Risks / open questions

- **Pin-dirty detection cost.** `git status` on every unit's src/ at TUI startup
  may be noticeable on a project with 100+ units. Cache in the build state file;
  only refresh on view.
- **Modules in `local = "../path"` overrides.** A locally-overridden module
  isn't really "pinned" — it's whatever the user has. Probably display `local`
  as a fixed state with no toggle; needs decision in planning.
- **Default branch detection.** `git remote show origin` to discover HEAD branch
  is one network round trip per transition; alternative is to assume `main` and
  fall back. Planning call.
- **What happens when upstream's default branch is renamed (`master` → `main`)
  after a switch to dev?** Probably fine — the user's local branch keeps
  tracking whatever was set; rebuild from pin recovers. Not solving in v1.

## Success criteria

- One keystroke on a unit's detail page flips its source between pin and dev,
  with HTTPS/SSH prompted on the way to dev.
- Units tab and modules tab both show the source state column. Color coding
  visible at a glance distinguishes the five states.
- A `pin-dirty` unit being rebuilt prints a warning before yoe overwrites the
  work tree.
- A `dev-mod` unit being switched back to pin shows the user the commit list and
  asks for confirmation before discarding.
- State display recovers correctly after the user manually edits the checkout
  from a shell (`$` shortcut), with no stale TUI state.
