<!--
Spec: Refresh branch-pinned modules in the build-time sync (fix stale warm-cache module checkouts)
Date: 2026-06-04
-->

# Refresh branch-pinned modules in the build-time sync

## Summary

`yoe build` syncs external modules through `module.SyncIfNeeded`, which clones a
module only when it is **missing** and never updates one that already exists.
For a module pinned to an immutable ref (a tag, e.g. `module-alpine @ 3.21`)
that is correct and fast. For a module pinned to a **branch** (a mutable ref,
e.g. `module-debian @ trixie`) it is a silent footgun: once the module is in the
cache, the checkout is frozen at whatever commit it was first cloned at, and no
later build ever advances it — even though the branch has moved upstream.

The cached copy then drifts from the upstream branch indefinitely. The failure
only surfaces later, indirectly, when the stale module code happens to be
incompatible with the current `yoe` binary — with no message pointing at the
real cause. That is exactly what wedged the E2E CI matrix: the persistent GitHub
Actions cache held `module-debian` at an old `trixie` commit that still called
the renamed `debian_feed` builtin, the build's `SyncIfNeeded` skipped it, and
every job failed with `undefined: debian_feed` while a fresh clone built fine.

The fix: in the build-time sync, distinguish mutable (branch) pins from
immutable (tag) pins using a local, offline check, and fast-forward only the
branch-pinned modules to their ref's upstream HEAD. Tag pins keep the current
fast no-network skip.

This is a reliability/correctness fix, not a feature. The `git fetch` &&
`git checkout FETCH_HEAD` mechanic already exists in `module.Sync` (the explicit
`yoe module sync` path); this brings the same freshness guarantee to branch pins
on the implicit build path, where users actually hit the staleness.

## The bug, precisely

`internal/module/fetch.go` has two sync entry points:

```go
// Sync (used by `yoe module sync`, cmd/yoe/main.go:291) — UPDATES existing modules:
if _, err := os.Stat(filepath.Join(moduleDir, ".git")); os.IsNotExist(err) {
    // git clone --depth 1 --branch <ref> ...
} else {
    // git fetch origin <ref>  &&  git checkout FETCH_HEAD   ← advances to ref HEAD
}

// SyncIfNeeded (wired into the build via WithModuleSync, cmd/yoe/main.go:663) — does NOT:
if _, err := os.Stat(filepath.Join(moduleDir, ".git")); err == nil {
    continue // already cloned — never fetched, never updated
}
// ... else git clone --depth 1 --branch <ref> ...
```

`SyncIfNeeded`'s doc comment states the intent explicitly: _"it does not
fetch/update modules that already exist — keeping it fast enough to call on
every build without adding latency."_ That tradeoff is right for tag pins (a tag
is effectively immutable, so re-fetching buys nothing) but wrong for branch pins
(a branch is a moving pointer; the whole point of pinning to `trixie` rather
than a tag is to track that branch).

`ModuleRef` (`internal/starlark/types.go:182`) carries only a free-form `Ref`
string — it does not record whether the ref is a branch or a tag — so the build
cannot tell the two apart from the project model alone.

### Why it bites beyond CI

This is not a CI-only artifact. Any developer with a warm `cache/modules/` who
pins a module to a branch gets the frozen checkout: they will keep building
against a stale module until they manually `yoe module sync`, blow away the
cache, or trip over a downstream incompatibility. The breakage is silent
(violates _"silent failures are bugs"_) and the staleness is implicit (violates
_"explicit over implicit"_). CI is just the place it became reproducible because
the build cache persists across runs by design.

### Evidence

CI run `26981736820`, all four matrix cells:

```
[yoe] cloning module module-ubuntu (ref: 26.04)...      ← cache-missing, cloned fresh
Error: evaluating cache/modules/module-debian/MODULE.star:27:1: undefined: debian_feed
```

`module-debian` is cache-present, so `SyncIfNeeded` prints nothing for it and
reuses the old `trixie` commit (`debian_feed`); the current `yoe` binary only
registers the renamed `apt_feed`, hence `undefined: debian_feed`. Upstream
`trixie` HEAD (`7caf6f1`) has used `apt_feed` since the rename — a fresh clone
gets it and builds. The only difference is the stale warm cache.

## Approach

### Distinguish mutable from immutable pins locally

The current clone uses `git clone --depth 1 --branch <ref>`, which accepts only
a branch or tag name (an arbitrary commit SHA is not supported), so every cached
module is at either a branch or a tag. Git records which locally:

- `--branch <branch>` creates a local branch ref → `refs/heads/<ref>` exists.
- `--branch <tag>` checks out the tag detached → only `refs/tags/<ref>` exists.

So the build can classify a cached module **offline**, no network round-trip:

```go
// refIsMutable reports whether ref names a branch (a moving pointer that must be
// refreshed) rather than a tag (immutable; safe to reuse the cached checkout).
// Checked against the cached repo's own refs — no network.
func refIsMutable(moduleDir, ref string) bool {
    cmd := exec.Command("git", "show-ref", "--verify", "--quiet",
        "refs/heads/"+ref)
    cmd.Dir = moduleDir
    return cmd.Run() == nil
}
```

### Refresh branch pins, keep the fast skip for tag pins

In `SyncIfNeeded`, when a module already exists, branch on its ref kind:

```go
if _, err := os.Stat(filepath.Join(moduleDir, ".git")); err == nil {
    if !refIsMutable(moduleDir, ref) {
        continue // tag pin — immutable, reuse cached checkout (fast, offline)
    }
    // branch pin — advance to upstream HEAD so a moving ref can't go stale
    if err := updateToRef(moduleDir, ref, w); err != nil {
        return fmt.Errorf("updating module %s: %w", name, err)
    }
    continue
}
```

`updateToRef` is the existing fetch+checkout pair lifted out of `Sync` so both
paths share one implementation (and `Sync`'s update branch calls it too):

```go
// updateToRef fast-forwards an already-cached module to the upstream tip of ref.
func updateToRef(moduleDir, ref string, w io.Writer) error {
    fmt.Fprintf(w, "[yoe] refreshing module %s (ref: %s)...\n",
        filepath.Base(moduleDir), ref)
    if err := runGit(moduleDir, "fetch", "origin", ref); err != nil {
        return err
    }
    return runGit(moduleDir, "checkout", "FETCH_HEAD")
}
```

Only branch-pinned modules pay a `git fetch` per build; tag-pinned modules (the
majority — `module-alpine @ 3.21`, `module-ubuntu @ 26.04`) stay on the
zero-network fast path. The added latency is proportional to the number of
branch pins, which is small and is precisely the set the user asked to track.

Unlike `Sync`'s current best-effort `cmd.Run() // best effort` on the checkout,
the build path should surface a failed refresh as an error: a branch pin that
cannot be advanced is a real problem the build should report, not swallow.

## Considered alternatives

- **Record branch-vs-tag in `ModuleRef` at parse time.** Would let the build
  classify without touching the cached repo, but the project model only has the
  ref string; determining its kind still requires asking git (locally or
  remotely). The local `show-ref` check needs no schema change and no network —
  preferred.

- **Always fetch every module on every build (drop the tag fast path).** Simple
  and always correct, but reintroduces a network round-trip per module on every
  build — exactly the latency `SyncIfNeeded` exists to avoid, and pure waste for
  immutable tags. Rejected.

- **Throttle branch refreshes (e.g. fetch at most once per N minutes via a
  timestamp marker).** Bounds the per-build cost of many branch pins, but adds
  state and a staleness window for marginal benefit at today's branch-pin count.
  Out of scope; revisit only if branch pins become common and fetch latency is
  measured to matter.

- **Leave the build path alone; rely on the CI `yoe module sync` step.** The
  workflow already runs an explicit sync before the build (option A, landed
  2026-06-04), which fixes CI. But it does nothing for local warm caches, which
  hit the same staleness. This spec fixes the underlying behavior so the CI step
  becomes a redundant safety net rather than the only thing standing between a
  branch pin and a stale build.

## Scope

Touches only `internal/module/fetch.go`:

- Add `refIsMutable(moduleDir, ref)` (local `git show-ref` check) and
  `updateToRef(moduleDir, ref, w)` (extracted fetch+checkout helper, with a
  small `runGit` wrapper for the repeated `exec.Command{Dir,Stderr}` pattern).
- `SyncIfNeeded`: when the module exists, refresh it via `updateToRef` if the
  pin is mutable, else keep the current `continue`.
- `Sync`: replace its inline fetch+checkout with `updateToRef` so both paths
  share one implementation (and stop swallowing the checkout error).

Add `internal/module/fetch_test.go` coverage using local file:// upstreams (the
pattern already used in `internal/dev_test.go`):

- Branch pin: clone, advance the upstream branch by one commit, run
  `SyncIfNeeded`, assert the cached checkout moved to the new commit.
- Tag pin: clone at a tag, advance the upstream branch, run `SyncIfNeeded`,
  assert the cached checkout did **not** move (and no fetch was performed).
- `refIsMutable`: true for a branch-cloned repo, false for a tag-cloned repo.

## Non-goals / limitations

- Commit-SHA pins remain unsupported (the `--branch` clone never accepted them);
  no change here.
- No change to dev-mode module handling (`internal/module/dev.go`) — that path
  is user-managed and deliberately not normalized.
- The CI `Sync modules` step added in option A stays as a belt-and-suspenders
  guarantee for CI determinism; it is harmless once this lands.

## Validation

- `go build ./...` and `go test ./internal/module/...`.
- Manual: with a warm `cache/modules/`, advance a branch-pinned module upstream,
  run `yoe build`, and confirm the build picks up the new commit (a tag-pinned
  module in the same project shows no fetch and no change).
