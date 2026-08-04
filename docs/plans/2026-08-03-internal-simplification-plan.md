<!--
Plan: Internal simplification — findings and execution plan
Date: 2026-08-03
-->

# Internal simplification: findings and execution plan

## Summary

A full-tree review of `internal/` and `cmd/` (~29k non-test Go lines) looking
for ways to simplify the data model, execution engine, parsing, and object
lifecycle. The review found:

- **9 confirmed defects** hiding inside duplicated or drifted code — a false
  cache marker written for builds that never ran, an unlocked APKINDEX
  regeneration racing across parallel workers, a UX-only kwarg leaking into the
  content-addressed hash, and six more.
- **One dominant structural theme**: the unit catalog exists in four
  representations (`Engine.units`, `Engine.unitsByModule`,
  `Project.UnitsByModule`, `Project.DistroViews`), and most of the recent distro
  bug class traces to the seams between them. The existing spec
  [distro-as-unit-identity](../specs/2026-05-29-distro-as-unit-identity.md)
  already frames the fix; this plan folds new supporting findings into it and
  schedules it.
- **~2,000–3,000 lines** of realistic net deletion across dead packages,
  copy-paste forks (`feeds/alpine` vs `feeds/apt` is a literal fork; gzip stream
  framing exists five times), and repeated per-command boilerplate.
- **A do-not-unify list**: several same-named file pairs (`apkindex/deps.go` vs
  `dpkg/deps.go`, the two version comparators, the two signature verifiers) look
  duplicated but differ by specification. Forcing them together would be worse
  than the duplication.

Findings are grouped by theme, then sequenced into seven phases that can land
independently. Phase 0 is defect fixes and should land regardless of appetite
for the rest.

## Method

Five parallel deep-read passes covered: `internal/starlark`;
`internal/{build,resolve,artifact,bootstrap}`;
`internal/{apkindex,dpkg,feeds,repo,deb,artifact}` (cross-side duplication);
`internal/tui` + `internal/tui/query`; and `cmd/yoe` plus the remaining support
packages (`module`, `source`, `device`, `feed`, root `internal` files). Every
non-test file was read; tests were skimmed for pinned invariants. The headline
claims below were then independently re-verified against the source (one claim
from the review was corrected during verification — see F-C1).

## Invariants that bound all of this work

These came out of the review as load-bearing and must survive every phase:

1. **Content-addressed hash inputs.** `resolve/hash.go` field writes must stay
   textually in `hash.go` — `TestUnitHash_CoversAllFields` greps the source for
   `unit.<Field>` literals. New lines gated on non-empty values. Phases marked
   _cache-neutral_ below must produce identical input hashes on the e2e project
   before/after.
2. **Lazy feed materialization.** `SyntheticModule.Names` never allocates a
   `*Unit`; the closure walk only materializes what it reaches. Anything that
   enumerates a feed to build an index is a regression at Debian scale (50k+
   entries). Guarded by `closure_test.go`.
3. **Deterministic resolution.** The provides table is built over sorted unit
   names specifically to keep the build cache stable; module-priority ordering
   (project root highest, synthetics negative) is assumed by every resolver.
   Kahn-style rewrites must keep a total order.
4. **Dev-mode source trees are never forcibly normalized.** The existing guards
   (plain `checkout` when a local branch exists, `-B` only when missing; no
   `clean -fdx`; no `RemoveAll`+reclone when reset-in-place works) were audited
   and are sound. Consolidating git _plumbing_ must not merge the module-vs-unit
   _policy_ functions that carry these guards.
5. **Scheduler ordering.** `executor_test.go` pins that no dependent reaches
   `[building]` before all deps are `[done]`, witnessed through writer line
   ordering — scheduler changes must preserve output ordering, not just the
   happens-before edge.
6. **TUI scope.** Project-closure-only view, search-based discovery. No
   full-catalog browse mode gets introduced by any TUI refactor.
7. **Hardware-bootable images, no cross-compilation, units as single source of
   truth** — unchanged framing for everything here; nothing in this plan touches
   image assembly semantics or toolchain selection.

## Confirmed defects (Phase 0 candidates)

Each of these was found during the review and re-verified in source. They are
worth fixing even if no other phase proceeds.

- **F-D1 — "Build already in progress" skip writes a false cache marker.**
  `internal/build/executor.go:519-523` returns `nil` when another process's PID
  lock exists; the worker (`executor.go:389`) treats `nil` as success and writes
  the cache marker, marks the unit rebuilt, and notifies `done` — with no
  destdir, no staged sysroot, no published artifact. Dependents then assemble
  from an empty stage dir (`sandbox.go:256-259` silently skips a missing stage
  dir) and fail two units later with a missing-header error. Reachable via a TUI
  build concurrent with a CLI build, or two TUI targets sharing a dep. **Fix
  now:** return a sentinel error (or dedicated result) so the skip path never
  writes the marker. The lock mechanism's future is decided in Phase 3 (F-E3).
- **F-D2 — APKINDEX publish is unlocked and O(N²).** `repo.Publish`
  (`internal/repo/local.go:57-108`) is called from parallel worker goroutines
  (`executor.go:971`) and regenerates the full index per package with no mutex —
  two workers interleave and the last writer's index can omit the other's
  package (silent, intermittent `apk add` failures). The deb side already fixed
  both problems (`debPublishMu`, `deb_emitter.go:21-27`; end-of-build regen,
  `executor.go:449-467`). **Fix:** mirror the deb pattern — serialize `Publish`,
  drop per-publish `GenerateIndex`, regenerate once per touched arch dir at end
  of build, plus a pre-image-assembly refresh mirroring the deb one at
  `executor.go:795-808`. Cache-neutral.
- **F-D3 — `artifacts_explicit` leaks into the cache key.**
  `internal/starlark/builtins.go:744` reads the kwarg into
  `Unit.ArtifactsExplicit` (declared UX-only in `hash_test.go`'s skip list), but
  the kwarg is missing from `reservedUnitKwargs` (`builtins.go:262-280`), so it
  is also captured into `Unit.Extra` — which _is_ hashed (`hash.go:153`). Every
  image sets it via `module-core/classes/image.star`. **Fix:** add it to
  `reservedUnitKwargs`. One-time image hash invalidation — acceptable pre-1.0;
  land with a changelog note. The underlying hazard (hand-kept parallel kwarg
  list vs struct fields) is addressed in Phase 4 (F-S6).
- **F-D4 — TUI `status:` queries go stale.** `m.visible` is only recomputed in
  `applyQuery`, which runs on query edits; the status mutations in
  `buildEventMsg`/`buildDoneMsg`/source handlers (`internal/tui/app.go:822-873`,
  `783-819`) never re-run it, so a `status:building` or `status:failed` filter
  does not track a live build. **Fix:** route all status writes through one
  `setStatus` that refreshes the filter (or compute `visible` at render time).
- **F-D5 — `yoe desc` prints hashes that can never match a build.**
  `internal/resolve/describe.go:21-29` computes hashes over a distro-less DAG
  with empty machine and nil source inputs; each of those flips gated hash lines
  relative to the real build. The TUI had this exact bug and was fixed
  (`app.go:610-615` documents it); `describe.go` was not. **Fix:** thread
  effective distro, machine, and `build.SrcInputsFn` into `Describe`.
- **F-D6 — TUI and executor both create `executor.log`.** `tui/app.go:4953-4959`
  creates the file and passes the handle to `BuildUnits`; `buildOne`
  (`executor.go:589-595`) creates the same path again and tees into it — two
  handles, independent offsets, interleaved output, pre-scan lines truncated.
  **Fix:** `buildOne` owns the file; the TUI passes a plain writer.
- **F-D7 — Six commands load the project without required loader options.**
  `cmd/yoe/main.go:667-679` (`projectLoadOpts`) registers the feed builtins and
  module-sync callback; `internal/configcmd.go:11`, `internal/layer.go:11`,
  `internal/source/commands.go:15,35,72`, and `internal/dev.go:24` load bare.
  `main.go:792-794` documents the resulting crash (`undefined: alpine_feed`) —
  fixed at one call site, never propagated. **Fix:** change these helpers to
  accept the already-loaded `*yoestar.Project`; `main.go` loads once. Deletes
  six load sites.
- **F-D8 — TUI and executor disagree on "cached".** The TUI uses `IsBuildCached`
  (marker only); the executor's `cacheValid` (`executor.go:1324`) additionally
  requires the artifact to exist. The TUI shows `cached` for units the executor
  rebuilds. **Fix:** export `build.CacheValid`, use it in both TUI sites.
- **F-D9 — Two `isGitURL` implementations disagree.** `source.isGitURL` treats
  bare `github.com/...` as git; `devIsGitURL` (`internal/dev.go:705-710`) does
  not — so a unit sourced that way fetches as git but `yoe dev` rejects it as
  "non-git source". **Fix:** export `source.IsGitURL`, delete the copy.

Also verified and worth noting: `yoe config show` resolves `local.star`
overrides against `"."` rather than the project root
(`internal/configcmd.go:21`), so it disagrees with `yoe build` when run from a
subdirectory — fixed as a side effect of F-C4 below.

**Corrected during verification (F-C1):** the review flagged the fallback distro
scan in `cmdBuild` (`cmd/yoe/main.go:391-413`) as having an unconditional
`break` that made it a coin flip over map order. Re-reading the source shows the
`break` is inside the match branch — the code is correct, just an O(catalog)
scan per named unit that collapses to `proj.AnyUnit(n)`. Downgraded from defect
to cleanup (Phase 2).

## Findings by theme

### Theme 1 — One unit catalog (the structural core)

The engine and project hold four representations of "the units": `Engine.units`
(self-described "legacy flat catalog", `engine.go:19`), `Engine.unitsByModule`
("primary per-module storage"), `Project.UnitsByModule` (assigned from the
engine map — twice, and the second assignment is a no-op reassignment of the
same live map), and `Project.DistroViews` (a precomputed memo that goes stale by
design and is papered over by a three-tier fallback in `LookupUnit`,
`types.go:352-366`). The
[distro-as-unit-identity spec](../specs/2026-05-29-distro-as-unit-identity.md)
already proposes the target state: one `(distro, name)`-keyed catalog, typed
`Distro` with a loud zero, distro decided once at enumerated boundaries.

New findings from this review that the spec's implementation plan should absorb:

- **F-S1 — Provides resolution is O(catalog) per lookup, and its distro-aware
  tier is inert during the closure walk.** `Project.ResolveProvidesForDistro`
  (`types.go:429-462`) full-scans every unit in every module on essentially
  every lookup, and `Engine.lookupOrMaterialize` calls it first for every name
  (`closure.go:198`). Worse, `Project.UnitsByModule` is first assigned _after_
  the image phase runs (`loader.go:545`), so during `resolve_closure` the
  function degenerates to a flat `p.Provides` read — the distro-aware dispatch
  it exists for never operates there. Fix: wire `proj.UnitsByModule` once right
  after `project()` evaluation; build a per-distro provides index
  (`map[distro]map[virtual]string`) so lookup is O(1).
- **F-S2 — The closure topological sort is O(V²) with redundant re-resolution.**
  Pass 1 of `Engine.closure` (`closure.go:107-148`) materializes every reachable
  unit then discards the pointers; pass 2 re-resolves each name per round, and
  mixes a distro-blind resolver where pass 1 used the distro-aware one. Fix:
  keep `map[string]*Unit` from pass 1 and run Kahn's algorithm; one resolver
  throughout; sorted ready-queue for determinism.
- **F-S3 — The build-dep fixpoint rescans the whole catalog every round**
  (`loader.go:642-734`). Replace with a worklist seeded from current units,
  pushing only newly materialized ones. Preserves the "every image-targeted
  distro gets its feed-only build deps" invariant documented at
  `loader.go:667-686`.
- **F-S4 — Dead branch in `lookupOrMaterialize`'s synthetic path**
  (`closure.go:255-275`): tracing both entry paths shows the function always
  returns `u`; the `existing` re-read and conditional are dead. Trivial
  deletion, do it early.
- **F-S5 — Shadow detection is distro-blind** (`builtins.go:821-861`): two
  modules shipping the same-named unit for different distros (the exact
  coexistence `unitsByModule` was built for, e.g. `dev-image` in module-debian
  and module-ubuntu) are reported as shadowing in Diagnostics. Apply the same
  different-distro exemption the provides-collision path already has
  (`loader.go:482-484`).
- **F-S6 — `reservedUnitKwargs` is a hand-maintained shadow of the field list**
  in `registerUnit`, and it has already drifted once (F-D3). Replace the
  parallel map + giant struct literal with one `{kwarg, setter}` table so "read
  into a typed field" and "excluded from Extra" cannot diverge.
- **F-S7 — Five dependency-closure walkers exist across packages**:
  `Engine.closure`, `resolve.BuildDAG`+`appendRuntimeClosureOfDeps`,
  `resolve.RuntimeClosure`, `tui/query.BuildInClosure` — each re-derives
  provides routing, distro filtering, and image-artifact promotion with subtly
  different missing-name policies (`RuntimeClosure` panics on empty distro;
  `BuildDAG` accepts it; `BuildInClosure` silently degrades to a build-only
  closure). Consolidate on one `resolve.Walk(proj, roots, opts)` with explicit
  options; the starlark walker keeps materialization side effects and stays
  separate — which is itself an argument for splitting "resolve" from
  "materialize" in the catalog work.

### Theme 2 — Build engine state and lifecycle

- **F-E1 — Three on-disk records of one fact.** `.yoe-hash` marker, `build.json`
  (`BuildMeta`), and the PID lock all describe "what happened to this unit";
  `.yoe-hash` is exactly `meta.Hash` when `meta.Status == "complete"`. Meanwhile
  `internal/dev.go:530-580` re-declares the `BuildMeta` shape twice (already
  drifted: `omitempty` everywhere, `any` timestamps) and hand-joins build paths,
  because an import cycle — `internal/build` imports root `internal` for
  container helpers — prevents `dev.go` from importing `build`. Fix: move
  `internal/container.go` to a new leaf package `internal/container` (consumers:
  `build`, `device`, `clean.go`, `cmd`), then delete the shadow structs and
  `.yoe-hash` (`IsBuildCached` becomes a `ReadMeta` check). Pre-1.0: stale
  `.yoe-hash` files are simply ignored.
- **F-E2 — Cache checks and force logic run 2–3× per unit.** Pre-scan, worker,
  and dry-run each recompute `cacheValid` (a 3-level pool glob per check on the
  apt path) and `forceThis`. Fix: compute a `[]unitPlan` (name, hash, scope,
  cached, force) once after hashing; workers and dry-run read it; dry-run
  becomes a formatter.
- **F-E3 — The cross-process PID lock is unsound.** Beyond F-D1, it is
  Linux-only (`/proc/<pid>`), racy under PID reuse, and its only cross-process
  effect is the skip. Decide: delete it (TUI `statusBuilding` branch adjusts),
  or replace with a blocking `flock(2)` on the build dir. Deletion is the
  simpler, honest option; in-process scheduling already serializes within a run.
- **F-E4 — Container reference resolution exists twice**: `resolve/dag.go`'s
  `appendContainerDeps` decides what gets _scheduled_; `executor.go:1203-1252`'s
  `resolveContainerImage` decides what docker _runs_. Disagreement means
  building container X and running container Y. One `resolve.ContainerRef(...)`
  used by both; the per-task override stops cloning the whole `Unit` to change
  one string (`executor.go:832-837`).
- **F-E5 — apk/deb packaging branches are scattered.** `packageAPK` /
  `packageDeb` share their shape, and `IsAptFamily` branches appear at six
  executor sites, including a thrice-hardcoded
  `Arches: []string{"amd64","arm64"}` list. A small `packager` interface
  (alpine/apt), selected once, collapses the branches; the arch list moves to
  `internal/arch` (F-X2). Do this after F-D2/F-E2 (same lines).
- **F-E6 — Mutation-after-construction.** `BuildUnits` back-fills `opts` and
  then reads `effectiveDistro` and `opts.EffectiveDistro` interchangeably;
  `RealExecer.Run` mutates the shared `SandboxConfig` and restores `NoUser` to
  literal `false` rather than the saved value. Fix: a `buildCtx` built once and
  passed down (also removes the 8-argument signatures); per-call shallow copy of
  `SandboxConfig`.
- **F-E7 — Two root-owned-dir removers.** `executor.go`'s
  `removeDirRobust`+`chownDirToHost` vs `internal/clean.go`'s
  `RemoveDirAnyOwner`. Keep one, in `internal/container` after the F-E1 move:
  chown-then-retry strategy (never runs `rm -rf` as uid 0 against a concatenated
  path), with `LocalToolchainImage` fallback so unit-less callers work.
- **F-E8 — Small scheduler/DAG cleanups.** `TopologicalSort` computes in-degrees
  twice, discarding the first (`dag.go:268-277`); `BuildUnits`,
  `ComputeAllHashes`, and the TUI each re-sort the same DAG (memoize
  `DAG.Order()`); `launchReady` rescans all units per completion (optional
  incremental decrement — only if already in this code, ordering test pins
  behavior).
- **F-E9 — `internal/bootstrap` (309 lines) cannot run.** Its unit list names
  units that exist in no module; past that check it builds a `SandboxConfig`
  with no container (immediate error); `Stage1` pins a stale toolchain tag (a
  bug class already fixed elsewhere via `LocalToolchainImage`); its command
  extractor drops `Fn`/`Install` steps. Delete the package, the `bootstrap`
  subcommand, and `SandboxConfig.BuildRoot` (exists only for `Stage1`). **Needs
  a product call** — self-hosting can be re-derived from the current unit model
  when actually wanted; check the roadmap before deleting.

### Theme 3 — Copy-paste forks and repeated helpers (cross-cutting)

- **F-X1 — gzip stream framing exists five times.** Byte-identical
  `gzipStreamBoundaries` in `apkindex/verify.go:224`,
  `feeds/alpine/tarstream.go:51`, `source/fetch.go:80` (two of which carry
  comments saying "promote to a shared package when a third caller appears" — it
  did), plus two independent solutions in `repo/index.go:251-295` and
  `artifact/apk.go:199-221`. Create `internal/gzipframe`; migrate the three
  verbatim copies immediately, the two stragglers as a follow-up (different I/O
  shapes). Fold in `decodeChecksum`, duplicated between `apkindex/parse.go` and
  `source/fetch.go` with a comment admitting the two must stay byte-identical
  for cache-key purposes.
- **F-X2 — Four arch-mapping tables, two incomplete.** yoe→apk in
  `feeds/alpine/builtin.go` and `executor.go` (`ApkArch`); yoe→deb in
  `feeds/apt/builtin.go` (missing riscv64) and `executor.go` (`debArchForYoe`,
  silent passthrough default that contradicts the map's hard error). Plus
  `multiarchTuple`, `binfmtArchName`, and duplicate host-arch probes
  (`build.Arch` vs `yoe.HostArch`, byte-identical, both shelling out to `uname`
  uncached). One `internal/arch` package: per-format tables, `Supported()`,
  memoized `Host()`. Values must not change (they feed repo paths and PKGINFO).
- **F-X3 — `feeds/alpine` vs `feeds/apt` is a literal fork.** 257 identical
  lines in `builtin.go` alone; `atomicWrite` has already drifted (alpine wraps
  the rename error, apt does not). Two-stage fix: (a) non-generic `feedcore`
  package for the verbatim half — `atomicWrite`, `humanBytes`, `relTo`,
  `pickArches`, `countStanzas`, the `PeekFeedDecls` harness, the engine-feeds
  registry (~180 lines, low risk); (b) the generic half (`ArchState[E,T]` + a
  `Backend` interface over the two index types) only behind a benchmark gate —
  the Debian index is 50k+ entries and `apt/builtin.go` documents parse cost as
  a known concern; an interface-dispatched accessor in the hot `Names()` path
  may not pay for ~150 lines. Kwarg parsing and `populateBuildFields` stay
  per-backend (genuinely different). Prerequisites: normalize where `Distro` is
  stamped (dpkg materializer takes a param; alpine feed hardcodes it) and decide
  the unresolved-dep policy — apk materialization hard-errors, dpkg silently
  skips; pick one deliberately.
- **F-X4 — Git plumbing four ways.** `gitCmd` (`dev.go`) and `gitOut`
  (`module/dev.go`) are byte-identical; `stateGit` and inline
  `exec.Command("git", ...)` sites in `source/workspace.go` vary; `httpsToSSH`
  is copied verbatim between `dev.go` and `module/dev.go` (the comment blaming a
  circular import is outdated — `internal/module` does not import root
  `internal`). One `internal/gitutil` leaf package (`Run`, `HTTPSToSSH`,
  `FetchOrigin` with an explicit skip-when-full option). **Policy functions stay
  split** per invariant 4 — only plumbing merges.
- **F-X5 — Atomic file writes.** `deb_emitter.go` copies atomically (hardened
  after a documented race); `executor.go`'s `copyDebFile` and the apk `Publish`
  copy do not — the apk path writes into a tree parallel builds scan.
  `internal/fsutil`: `CopyFileAtomic`, `WriteFileAtomic` (absorbs the two feed
  `atomicWrite`s).
- **F-X6 — Smaller repeats.** The apk repositories marker block emitted by both
  `device/deploy.go` and `device/repo.go` (drift breaks `device repo remove`) →
  one `apkRepoBlock` helper. Two `-rN` version splitters inside `apkindex`
  (`splitRelease` vs `splitPkgver`) that agree today with nothing keeping them
  agreeing → keep `splitPkgver` (external contract with `alpine_pkg.star`).
  Duplicated partition parsing in `builtins.go` (22 identical lines twice) →
  `parsePartitions`. The four-phase eval loop copy-pasted four times in
  `loader.go` → `evalPhase`. `InstallStepValue`/`InstallStep` same-struct
  duplication → one struct implementing `starlark.Value` (hash reads unchanged
  fields).

### Theme 4 — Command layer and support packages

- **F-C1 — `cmdBuild` fallback distro scan** (corrected, see above): collapse to
  `proj.AnyUnit(n)`.
- **F-C2 — Feed bring-up written three times, mDNS discovery four times**
  (`serve.go`, `deploy.go`, `tui/feed.go`, `device.go`, `tui/deploy.go`) with
  drifted error handling. Two helpers in `internal/feed`: `ConfigForProject`,
  `DiscoverForProject`. The "multi-distro serve" TODO currently appears in all
  three copies — it would land once.
- **F-C3 — Deploy pipeline duplicated CLI vs TUI** with drift (CLI validates
  image-class and can start an ephemeral feed; TUI does neither). One
  `device.BuildAndDeploy`-style entry; callers keep only flag parsing / TUI
  plumbing.
- **F-C4 — Project-root and overrides handling.** Three copies of the
  PROJECT.star walk-up (one dead); `LoadProject` computes the root and discards
  it → add `Project.Root`. The `local.star` load-modify-write cascade is spelled
  out four times in `cmd/yoe` and **eight** times in the TUI (two of which
  swallow the write error) → one helper each side (`withLocalOverrides` /
  `m.mutateOverrides`). Fixes the `config show` root inconsistency.
- **F-C5 — CLI boilerplate.** 91 `os.Exit(1)` sites, 49 identical print-and-exit
  blocks (→ `fail()` helper), 9 inline copies of the `YOE_PROJECT`-or-`.` idiom
  that disagree with `projectDir()` on absolutization, and 8 hand-rolled nested
  string-switch dispatchers with drifting usage text (→ small `subcmd` table; no
  CLI framework — the hand-rolled global-flag pre-scan is deliberate).
- **F-C6 — Dead surface.** Entire packages `internal/config` (`FindProjectRoot`,
  zero callers) and `internal/check_debug` (scratch `main`). Dead
  flags/commands: `--all` (parsed, discarded), `--allow-duplicate-provides` (can
  only set its default), `cache` in usage with no dispatch arm, `bootstrap`
  dispatchable but undocumented, `module info`/`check-updates` stubs,
  `container build`/`status` stubs. Dead params (`arch` in dev/clean entry
  points), dead helpers (`unitSrcDir`, `ResolveModulePaths`), misnamed
  `internal/layer.go` (contains `ListModules`).

### Theme 5 — TUI

Largest package (7.5k lines), one 95-field model struct, 26 distinct UI states.
Beyond F-D4/F-D6/F-D8 and the overrides helper (F-C4):

- **F-T1 — Five hand-rolled scroll windows** (~30 lines each) plus six
  `xViewportHeight()` functions that hand-count chrome lines nothing keeps in
  sync with the views. One `pane` component where height derives from a chrome
  declaration. Existing layout tests pin the output.
- **F-T2 — Cursor indexes the unfiltered slice**, so seven separate linear scans
  invert the cursor↔visible mapping per keypress. Store the cursor as a row in
  `m.visible`; the scans become arithmetic. Write a
  cursor-preservation-during-filter test first.
- **F-T3 — Repeated key handling.** Five identical quit-with-confirm arms; six
  from-scratch clamped-list navigators; three Setup pickers that are the same
  function with different fields (views equally copy-pasted). Helpers:
  `handleListNav`, `quitOrConfirm`, a `picker` component.
- **F-T4 — Two help systems that drift.** The `?` overlay's switch already
  misses `viewSourceProgress` (shows Units-tab keys during a git fetch). One
  keymap table renders both the bottom bar and the overlay.
- **F-T5 — Status derivation duplicated** between `Run` and `recomputeStatuses`
  (identical four-branch cascade); `m.dag`/`m.hashes` exist almost solely to
  feed it; a comment references a `newModel` constructor that does not exist
  while 37 test sites hand-build `model{}` literals. Extract `scanStatuses` + a
  real constructor.
- **F-T6 — Four hand-rolled text inputs** with three different printable-char
  predicates and two caret styles; `bubbles` is already a dependency — adopt
  `textinput.Model` or one shared 40-line field.
- **F-T7 — Message-type overlap.** Build status exists as string → enum → string
  across the pipeline; `buildDoneMsg` re-sets statuses that `buildEventMsg`
  already set; `sourceOpDoneMsg` vs `sourceStateChangedMsg` carry the same data
  and invalidate the same caches; `sourcePrompt.target` is a string where its
  sibling uses the enum. Consolidate types; pick one status representation
  end-to-end.
- **F-T8 — `query` package is right-sized** (418 lines; deliberate,
  plan-backed). Three trims: drop the speculative `cursor` parameter from
  `Complete` (single caller, append-only input); replace the
  `InRoot`/`inSet`/`Matches` split with `query.Compile` returning a matcher
  (removes a model field and an ordering hazard); consolidate `BuildInClosure`
  with the `resolve` walker as part of F-S7.
- **F-T9 — God-struct split last.** Field-usage census shows
  flash/deploy/setup/detail clusters are fully self-contained (41 fields, ~1,900
  lines) — extract sub-models _after_ the shared machinery above exists, or each
  sub-model re-implements it. Every `model{}` test literal changes; do it only
  once Tier-1 items have landed.

Rendering correctness notes found along the way (fix opportunistically):
`wrapLine` byte-slices styled output after `ansi.Truncate` (continuation lines
lose style, can split escape sequences); `clipFixed` can cut UTF-8 mid-rune
(unit names come from feeds); `renderStatus` returns a twelve-space literal
silently coupled to `len("▌building...")`.

### Theme 6 — Dead code and vestigial data model

Beyond the packages in F-C6 and F-E9:

- **Starlark data model, verified never-read:** `SourcesConfig` +
  `Project.Sources` (the `sources()` builtin's result is discarded),
  `CacheRemote` + `CacheConfig.{Remote,Retention,Signing}` (`s3_cache()` ditto),
  `BootloaderConfig` + `Machine.Bootloader` + `uboot()`,
  `KernelConfig.{Repo,Branch,Tag,DeviceTrees}`. `KernelConfig.Defconfig` is
  exposed to Starlark but no module reads it. `Unit.Conffiles` is parsed,
  **hashed**, and consumed by nothing — implement or delete; deletion is a
  cache-invalidation event, so bundle with F-D3's. Note:
  `testdata/valid-project` uses `sources()`; deleting the builtin makes that a
  hard resolver error — decide delete vs accepted-and-ignored per "explicit over
  implicit" (lean delete, fix the fixture).
- **Dead functions/fields:** `hasTask`, `HasBuildLog`, `APKSha1`,
  `LookupInSynthetics` (or make `closure.go` use it), `Engine.moduleInfo` deps
  parsing (lossy — drops `path=`/`local=`; a live trap if ever read), `pathDir`
  (hand-rolled, buggy `filepath.Dir`), `ModuleRef.peekName`, `moduleRecord.id`,
  TUI `flashImageSize`, `lineWriter.buf` (value receiver defeats it), 10
  `ok := u != nil` residues, a shadowed `dimStyle`, `var _ =` keep-alive hacks
  in `deb_emitter.go` and `feeds/alpine`.
- **Module-ref parsing exists four times** (`fnProject`, `fnModuleInfo` (lossy),
  the peek builtin with its own five-method value type) → one `parseModuleRef`;
  have the peek return the same struct type so `moduleRefValue` and
  `parsePeekDeps` disappear. Related: `peekModuleInfo` hardcodes today's
  feed-builtin names as no-op stubs; thread the caller-supplied builtin names
  instead so a third feed type cannot silently break module naming.
- **`Engine.mu` provides no guarantee** — hot-path reads are unlocked while
  writes lock; evaluation is single-threaded per engine. Delete the mutex and
  document "one Engine per load, not concurrency-safe".
- **`Unit.Scope` is unvalidated** where `Architecture` is — a typo silently gets
  arch scoping. One-line check in `registerUnit`.
- **gofmt drift:** `engine.go`, `install_step.go`, `synthetic_module.go` (+ four
  test files).

### Theme 7 — Duplication that must NOT be unified

Recorded so future passes do not "fix" these:

- **apk vs dpkg version comparison** — incompatible by specification (epochs,
  `~`, `-rN`, `_pre`/`_rc`); a unified comparator would need a format tag on
  every call. (Adjacent option, separate decision: replace the ~160-line
  hand-rolled apk comparator, self-described "good enough", with a spec-complete
  library — a correctness win, not a dedup.)
- **Dependency grammars** — apk's `so:`/`cmd:`/`pc:` virtuals vs deb's
  alternatives (`a | b`) and arch qualifiers; a common struct loses information
  on both sides. The shared contract is just `Resolve(token) (name, bool)` —
  already an interface.
- **Signature verification** — RSA-over-gzip-streams with filename-suffix trust
  vs OpenPGP clearsign with keyring trust plus `Valid-Until`.
- **Repo index emission bodies** — `.PKGINFO`-in-gzip vs deb822; control-stream
  SHA-1 vs whole-file SHA-256; fan-in vs fan-out noarch. At most a thin
  `Emitter` interface over the seam so the executor stops branching — worth
  doing only if/when a third distro family lands.
- **apk vs deb tar writers** — contradictory format requirements (apk needs PaX
  checksum records; dpkg rejects PaX).
- **Module vs unit dev-mode policy** (invariant 4) — plumbing merges, policy
  does not.
- **`errSentinel`** duplicated 3 lines each in two packages — an import edge is
  worse than six lines.

## Execution plan

Phases are independently landable; order within a phase is by value/effort. Each
step: build + `go vet` + full `go test ./...`; phase-specific verification
noted. Changelog entries only where behavior is user-visible; the matching
reference-doc update lands in the same commit per project rules.

### Phase 0 — Defect fixes (small, do first)

1. F-D1 skip-path marker (sentinel error only; lock decision deferred).
2. F-D2 APKINDEX publish: mutex + end-of-build/pre-image regen. Verify: parallel
   e2e alpine build (`-j`), then `apk add` every built package from the repo.
3. F-D4 TUI status-query staleness (`setStatus` funnel).
4. F-D5 `yoe desc` hash inputs. Verify: `yoe desc <unit>` hash equals the marker
   hash after `yoe build <unit>` in e2e.
5. F-D6 `executor.log` single owner.
6. F-D7 project loading unified (signatures take `*Project`).
7. F-D8 `build.CacheValid` exported, TUI uses it.
8. F-D9 `source.IsGitURL` exported, copy deleted.
9. F-D3 `artifacts_explicit` → `reservedUnitKwargs`, **bundled** with the
   `Conffiles` decision and Theme-6 hashed-field deletions into one
   cache-invalidation commit. Changelog note: one-time rebuild.

Cache-neutral except step 9 (deliberately bundled invalidation).

### Phase 1 — Deletions

Dead packages (`internal/config`, `internal/check_debug`), dead
functions/fields/flags/stubs from Theme 6 and F-C6, F-S4's dead branch,
`TopologicalSort`'s discarded first pass, gofmt pass. F-E9
(`internal/bootstrap`) rides here **after** a roadmap check confirms
self-hosting is not near-term. Changelog: removed `bootstrap` subcommand and
dead flags (with doc updates in the same commit). ~600+ lines removed, no
behavior change otherwise.

### Phase 2 — Shared leaf packages

1. `internal/gzipframe` (three verbatim copies + `decodeChecksum`); stragglers
   (`repo/index.go`, `artifact/apk.go`) as a follow-up commit. Byte-exact
   behavior pinned by existing apk-compat tests; migrate one caller per commit.
2. `internal/arch` (F-X2). Verify repo paths/PKGINFO unchanged on e2e.
3. `internal/fsutil` (F-X5) — includes the apk publish atomicity fix.
4. `internal/gitutil` (F-X4) — plumbing only; dev-mode tests must stay green
   untouched.
5. `internal/container` move (F-E1 first half): relocate `container.go`, fix
   imports, delete `dev.go` shadow structs/path joins, unify the root-owned-dir
   remover (F-E7).
6. Small helpers: `apkRepoBlock`, `parsePartitions`, `evalPhase`, `splitPkgver`
   consolidation, install-step struct merge, F-C1 cleanup.

All cache-neutral.

### Phase 3 — Build engine consolidation

1. F-E2 single `unitPlan` pass; dry-run over the plan.
2. F-E1 second half: delete `.yoe-hash`, `BuildMeta` as the single record.
3. F-E3 lock decision (recommend deletion; adjust TUI cascade).
4. F-E4 one container-reference resolver in `resolve`.
5. F-E6 `buildCtx`; `SandboxConfig` per-call copy.
6. F-E5 `packager` interface (after 1–2).
7. F-E8 DAG order memoization; optional incremental `launchReady`.
8. F-C2/F-C3 feed + deploy consolidation; F-C4 root/overrides helpers; F-C5
   `fail()`/`projectDir()`/dispatch table.

Verify: e2e alpine + debian builds byte-identical input hashes vs before
(cache-neutral claim); scheduler ordering test; QEMU boot for one image per
distro (publish-path changes).

### Phase 4 — One unit catalog (executes the 2026-05-29 spec)

Write the implementation plan for
[distro-as-unit-identity](../specs/2026-05-29-distro-as-unit-identity.md),
folding in F-S1 (provides index + `UnitsByModule` wiring), F-S2 (Kahn closure),
F-S3 (fixpoint worklist), F-S5 (distro-aware shadows), F-S6 (kwarg table), F-S7
(`resolve.Walk`), the `DistroViews` lazy-memo replacement, `Engine.mu` removal,
module-ref parser consolidation, and the typed-`Distro`/resolver-collapse
requirements already specced. Sequence inside that plan: perf-isolated wins
first (F-S1/F-S2/F-S3 are meaningful speedups on Debian-scale projects
independent of the catalog cutover), then delete `Engine.units`. Hash-neutrality
is the spec's R9 and gates every step.

### Phase 5 — Feed scaffolding dedup (F-X3)

1. Normalize `Distro` stamping and the unresolved-dep policy across the two
   backends (deliberate decision, then code).
2. Non-generic `feedcore` half (~180 lines, low risk).
3. Generic `ArchState`/`Backend` half **only if** a before/after benchmark on
   the Debian index (50k entries: parse, `Names()`, lookup) shows no regression;
   otherwise stop at 2 and record why.

#### Outcome (measured 2026-08-04)

**Step 2 landed, smaller than estimated. Step 3 rejected. Step 1 is still
open and is a behavior decision, not a refactor.**

The "literal fork, 257 identical lines in `builtin.go` alone" finding does not
survive a function-by-function comparison. Of the 406 lines in functions the two
backends share a name for, 49 are byte-identical, 10 more match after renaming
types, and **347 are genuinely different** — different index formats, dependency
grammars, transports, signature schemes, and kwargs. The files read as parallel
because they have the same shape, not because they hold the same code.

`internal/feeds/feedcore` therefore holds only `HumanBytes`, `RelTo`, and
`StringList` — the three helpers that are both identical and type-independent.
`feedStatesFor` and `registerFeedState` are byte-identical text but operate on
each package's own `archState`, so moving them requires the generic half.

The generic half is rejected, but not for the reason the plan anticipated.
Benchmarked against the real Ubuntu main index (7.5MB, ~50k stanzas) on a
Ryzen 9 3900X:

| Operation             | Time   | Allocations   |
| --------------------- | ------ | ------------- |
| `ParseIndexFile`      | 64 ms  | 402k          |
| `BuildProvidesTable`  | 4.0 ms | 105k          |
| `Names()` iteration   | 6.1 µs | 0             |

`Names()` is six microseconds. An interface call in front of it is free, so the
performance objection is answered. The reason to stop is different: an
`ArchState[E,T]` plus a `Backend` interface would exist to abstract over 347
lines that differ on purpose, to save at most 59. That trades readable
duplication for machinery that hides real differences — the same judgment
Theme 7 already applies to the version comparators and dependency grammars.

The benchmark is kept as `internal/feeds/apt/index_bench_test.go` so a future
proposal is argued against these numbers. It also records the one measured hot
spot in feed loading: parsing, at 64 ms per index, which nothing in this plan
touches.

Step 1's unresolved-dep policy is **left as an open decision**, deliberately.
`apkindex.MaterializeUnit` errors on a token no provider satisfies;
`dpkg.MaterializeUnit` skips it silently. Both are documented with a rationale,
and they are not obviously reconcilable: Debian's graph legitimately contains
tokens yoe cannot resolve (alternatives, packages outside the configured
sections), so erroring would make the apt feed unusable, while a silent skip is
the failure mode that produces an empty sysroot two units later. The likely
answer is "skip, but record what was skipped so Diagnostics can surface it" —
which is a behavior change with its own verification needs, not something to
fold into a dedup pass.

### Phase 6 — TUI

Order: F-T5 (`scanStatuses` + `newModel`) → F-T1 (`pane`) → column specs → F-T3
(nav/picker/quit helpers) → F-T2 (cursor as visible row; test first) → F-T4
(keymap table; fixes the missing help case) → F-T6 (`textinput`) → F-T7 (message
consolidation) → F-T8 (`query.Compile`, `Complete` signature) → F-T9 (god-struct
split, optional, last). Rendering fixes (`wrapLine`, `clipFixed`) opportunistic.
Verify with the existing layout tests plus manual TUI passes; no scope changes
(invariant 6).

#### Outcome (2026-08-04)

Landed: F-T5 (`scanStatuses` in Phase 0, `newModel` here), F-T4's actual
defect, F-T8's trims, one `paneHeight` helper, and the three rendering
correctness notes.

The help overlay's view switch is now exhaustive with no default branch, so a
new view without keys fails to compile rather than silently offering the Units
keys — which is what the source progress screen did, listing build, flash and
deploy shortcuts during a blocking git fetch when none of them do anything.

The rendering fixes were all real. `clipFixed` measured and cut by byte, so a
non-ASCII unit name, module path or version both mis-sized its column and could
be sliced mid-rune; it now measures display width, and pads back out when a
double-width character makes the cut land a column early. `wrapLine` sliced the
original string by the byte length of an `ansi.Truncate` result, which is only a
prefix of its input when the line has no escape sequences — so wrapped build-log
lines lost their color and a break could land inside an escape sequence. It uses
`ansi.Hardwrap` now. `renderStatus`'s blank is derived from the label it
alternates with instead of being a hand-counted run of twelve spaces.

Not done, and each still worth doing: F-T1's full `pane` component (only the
height arithmetic was shared; the chrome declarations stay per-view), F-T2
(cursor as a row in `m.visible`), F-T3 (nav/picker/quit helpers), F-T6
(`textinput`), F-T7 (message-type consolidation), and F-T9 (the god-struct
split, which the plan already scopes last and optional). F-T2 and F-T9 both
touch the 37 hand-built `model{}` test literals, which is the reason to do them
together and deliberately rather than opportunistically.

### Explicitly deferred / rejected

- Generic feed half without a benchmark gate (Phase 5.3's condition).
- `repo.Emitter` interface — revisit when a third distro family lands.
- apk version-comparator library replacement — separate decision, worth a short
  spec of its own (correctness vs new dependency).
- Everything in Theme 7's do-not-unify list.
- CLI framework adoption — rejected; the hand-rolled flag pre-scan is
  deliberate.

## Estimated impact

| Phase | Net lines                                   | Character                                             |
| ----- | ------------------------------------------- | ----------------------------------------------------- |
| 0     | ~ -100                                      | 9 defect fixes                                        |
| 1     | ~ -600                                      | pure deletion (incl. bootstrap, pending product call) |
| 2     | ~ -400                                      | shared leaf packages, 5 new small packages            |
| 3     | ~ -500                                      | engine state/lifecycle consolidation                  |
| 4     | net-negative (spec's own success criterion) | structural core                                       |
| 5     | ~ -200 to -350                              | feed fork dedup                                       |
| 6     | ~ -700                                      | TUI machinery + drift fixes                           |

Roughly 2,500–3,000 lines net removal if every phase lands, with the defect
fixes and the catalog unification carrying most of the durable value.
