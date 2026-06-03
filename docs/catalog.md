# Catalog and Materialization

This page describes yoe's in-memory **unit catalog** — the runtime data
structure that holds every unit a project knows about, how units enter that
structure (eagerly or lazily), and how the catalog is queried during the build.
It is the resolver's address book: the closure walker and the build executor
both ask the catalog the same question — _"give me the Unit for this name in
this context"_ — and the catalog's design governs how that answer is computed.

For the user-facing surface (what `unit()`, `image()`, `alpine_feed()`, and
`prefer_modules` look like in Starlark) see
[metadata-format.md](metadata-format.md) and
[naming-and-resolution.md](naming-and-resolution.md). This page is the internals
view: storage shapes, lifecycle, allocation cost, and the invariants the
resolver relies on.

## Terminology

A handful of terms recur throughout this page. They're defined here so the body
sections can use them without re-introducing each one.

**Unit.** The yoe representation of one buildable artifact — a `*Unit` struct
holding name, version, runtime/build deps, source URL, container choice, install
task, distro tag, and so on. Created either by a `.star` builtin (`unit(...)`,
`image(...)`, `container(...)`, …) or by a synthetic feed's materialization
callback.

**Closure.** The _transitive runtime-dependency set_ of an image — the set of
every unit reachable from the image's `artifacts = [...]` list by following
`RuntimeDeps` edges. If unit A runtime-depends on B and B on C, then C is in A's
closure. Term borrowed from graph theory (the transitive closure of a binary
relation) and from the Nix/Guix package-manager tradition that uses it for the
same concept.

**Synthetic module.** A registration in the catalog that names a feed
(`alpine.main`, `debian.main`, …) and provides a `Lookup(name) → *Unit`
callback. The callback runs only when something asks for a name the feed exposes
— registration is eager (one call per `alpine_feed()` / `debian_feed()` in
MODULE.star), but per-name `*Unit` allocation is lazy.

**Materialization.** The act of allocating a `*Unit` for a name a synthetic
module exposes. A synthetic's `Lookup` parses the upstream catalog entry (Alpine
APKINDEX or Debian `Packages`), builds dep tokens, constructs the `*Unit`, and
returns it. The walker then registers it into `UnitsByModule` and updates the
affected `DistroViews` entries.

**BFS (breadth-first search).** The traversal algorithm the closure walker uses:
maintain a queue, dequeue from the front, enqueue each visited unit's
`RuntimeDeps` at the back, mark visited names in a `seen` set so cycles don't
loop. Chosen over recursive depth-first because the queue keeps the working set
bounded by closure size, not dep-tree depth — a deeply nested transitive chain
can't blow the call stack.

**Provides resolution.** Replacing a _virtual_ name in a deps list (`linux`,
`toolchain`, `init`) with the concrete unit that satisfies it (`linux-rpi4`,
`toolchain-glibc`, `busybox-init`). Driven by the project's `Provides` map plus
per-distro filtering (a provides entry tagged for a different distro is
invisible to the lookup).

**Distro view.** A precomputed table `DistroViews[distro][name] → *Unit`
populated once at the end of the loader's evaluation phase. Maps every name
reachable in any image's closure to the winner of the per-distro resolution
(filter by distro tag → apply `prefer_modules` pin → apply module priority).
Consumers query through `LookupUnit` which reads this map in O(1).

## Three classes of units

Every Unit in the catalog falls into one of three categories. They differ in
_when_ they enter the catalog and _what_ the catalog actually stores.

**Source-declared real units.** A `.star` file in a module's `units/` directory
calls `unit(...)`, `image(...)`, `machine(...)`, `container(...)`. The builtin
handler allocates a `*Unit`, fills its fields from the call's kwargs, and
registers it eagerly during evaluation. Source-declared units are present in the
catalog by the end of the loader's evaluation phase, regardless of whether any
image references them.

**Synthetic units (feed-materialized).** A `MODULE.star` calls
`alpine_feed(...)` or `debian_feed(...)`. The builtin does NOT allocate one
`*Unit` per upstream package; it registers one **`SyntheticModule`** with a
`Lookup(name) → *Unit` callback. The 60k-entry upstream index sits
parsed-but-unmaterialized on disk and in a single `archCache` struct. A `*Unit`
for a specific name appears in the catalog only when something asks for it (see
[Lazy materialization](#lazy-materialization)).

**Virtual references.** A unit's `provides = ["toolchain"]` registers the name
`toolchain` as an alias for the providing unit's canonical name. Virtuals are
_not_ separate `*Unit` entries — they're entries in `Project.Provides` (a
`map[string]string`) that the resolver walks once at lookup time. A reference to
`"toolchain"` in a class's `container = "toolchain"` resolves to whichever unit
currently provides it (see
[virtual packages](naming-and-resolution.md#virtual-packages-ctxprovides)).

## Storage shape

The catalog has two layers: a primary store keyed by source module, and a set of
precomputed per-distro views that consumers actually query.

```go
type Engine struct {
    unitsByModule    map[string]map[string]*Unit  // [module][name]*Unit
    syntheticModules []*SyntheticModule
    project          *Project
    ...
}

type Project struct {
    UnitsByModule    map[string]map[string]*Unit  // primary storage; shared with Engine
    DistroViews      map[string]map[string]*Unit  // [distro][name]*Unit; precomputed
    Provides         map[string]string
    SyntheticModules []*SyntheticModule
    ...
}
```

**`UnitsByModule` is the primary store.** Every Unit registration — eager from
`.star` evaluation, lazy from a `SyntheticModule.Lookup` return — lands under
its source module. Cross-module same-name registrations coexist; `module-core`'s
`openssl` and `alpine.main`'s `openssl` live in separate buckets and never
overwrite each other.

**`DistroViews` is what consumers query.** Built once at the end of the loader's
evaluation phase via `buildDistroViews(proj)`. For each distinct unit name
across the project and for each distro the project sees, the loader runs a
three-step resolution:

1. **Filter** the candidate pool to units in `UnitsByModule[*][name]` whose
   `Distro` is `""` (visible to every distro) or matches the target distro.
   Cross-distro entries are eliminated structurally.
2. **Pin** — if `prefer_modules[name]` names a module still among survivors,
   that module's variant wins. Pins are hints, not guarantees: a pin that names
   a module the distro filter eliminated falls through to step 3 with a
   diagnostic.
3. **Priority** — pick the survivor from the highest-priority module. Project
   root outranks every external module; later-declared external modules outrank
   earlier-declared ones; every synthetic (feed-materialized) module ranks below
   every real module. The "synthetics rank below reals" rule means a from-source
   override in `module-core` automatically beats a same-named feed entry,
   without needing a `prefer_modules` pin to make it so.

The result is `DistroViews[distro][name] → *Unit`. Read-only after construction.
The closure walker, build executor, and any other consumer with distro context
call:

```go
func (p *Project) LookupUnit(distro, name string) *Unit {
    return p.DistroViews[distro][name]
}
```

— a single map access. Consumers without distro context (TUI list-all, source
workspace) iterate via `AllUnits()`, an `iter.Seq2[string, *Unit]` over
`UnitsByModule` that yields every registered unit. For same-name units across
modules, `AllUnits()` yields each separately; the caller decides what to do with
the collision.

**Distro is a visibility filter; module is the priority axis.** Two orthogonal
concerns. The catalog's job is to keep them separate: storage keys by module
(the source-of-truth label), the views slice by distro (the
runtime-compatibility class). This preserves the "module-core wins by default"
semantics — `module-core`'s source-built `openssl` wins for any image's
`openssl` lookup because `module-core` is the highest-priority module — while
letting `alpine.main`'s `libcap2` and `debian.main`'s `libcap2` coexist in
separate candidate pools, each visible only to closures of its own distro.

**Diagnostics-not-mutation.** Shadowing within a distro view is observable but
not visible in `DistroViews` itself: the loser is dropped from the view and the
collision is recorded in `Project.Diagnostics.Shadows` for the TUI to surface.
This is intentional — `DistroViews` is the _resolved_ state; the journey is in
Diagnostics. The same applies to a `prefer_modules` pin that fell through
because its module was distro-filtered out: the diagnostic records the
fallthrough so the user can see why their pin didn't take effect.

## Lazy materialization

### Where it starts

Materialization is triggered by image evaluation. The loader walks every
`images/*.star` file across the project root and every loaded module; each
`image(...)` call invokes `resolve_closure(artifacts, distro=...)` with its own
artifacts list and effective distro. So a project with five images defined
across its modules triggers five closure walks during load — not one per build,
but one per image found at evaluation time. A representative debian image:

```python
image(
    name = "debian-base-image",
    distro = "debian",
    artifacts = ["apt", "openssh-server", "linux-image-amd64"],
)
```

invokes
`resolve_closure(["apt", "openssh-server", "linux-image-amd64"], distro="debian")`.
The Go-side builtin takes the artifacts list as the **roots** of a breadth-first
walk through the runtime-dep graph; every name visited triggers a lookup, and
lookups that miss the catalog drive materialization.

Materializations don't repeat across closures. If three alpine images all
reference `openssh-server`, the synthetic `Lookup` fires once (during the first
image's walk); the other two walks read the cached `*Unit` from
`DistroViews["alpine"]["openssh-server"]`. The cost is union-of-closures, not
sum-of-closures.

What doesn't trigger materialization:

- Modules listed in PROJECT.star but containing no images.
- Names that exist in upstream catalogs but aren't reached from any image's
  closure (the 50,000-entry Debian `main` catalog parses to the `archCache`
  once, but only the few hundred names reached by some image's closure ever
  become `*Unit` objects).
- A feed type the project doesn't use — a project that declares `module-debian`
  but defines no debian image (and no alpine image pulls in a debian-tagged name
  through provides) parses the debian `Packages` file the first time some
  `Lookup` references it, which for a no-debian-image project never happens. The
  feed is registered eagerly; everything beyond registration is gated on actual
  use.

### The materialization cycle, step by step

For each name dequeued from the BFS frontier:

1. **Resolve provides.** If the name is a virtual like `toolchain`,
   `ResolveProvidesForDistro(distro, name)` substitutes the concrete providing
   unit's name (e.g. `toolchain-glibc` for distro=debian).
2. **Check the view.** `LookupUnit(distro, resolved)` reads
   `DistroViews[distro][resolved]`. Hit → return the existing `*Unit`, no
   allocation. (This is the common case after the first walk visits a name.)
3. **Walk synthetics on miss.** Visit each `SyntheticModule` in priority order.
   For each, call `sm.Lookup(resolved)`.
4. **First synthetic that has the name materializes it.** Inside `sm.Lookup`:
   - Pick the active arch (e.g. `x86_64` → debian arch `amd64`).
   - Load the `archCache` if it's not loaded yet — this is the one-time
     `~50–150ms` parse of `feeds/<section>/<arch>/APKINDEX` or
     `feeds/<section>/<arch>/Packages` from disk into a `[]Entry` plus a
     `byName` map.
   - Look up `resolved` in the `byName` map. Miss → return nil; the walker
     continues to the next synthetic. Hit → continue.
   - Call `MaterializeUnit(entry, providers, moduleName)`. This parses the
     upstream `Depends:` / `depends:` list, runs each token through the
     project-wide provides table (cross-feed via `multiFeedProviders` for
     Debian), and constructs one `*Unit` with its `RuntimeDeps`, `Provides`,
     `Replaces`, etc. filled in.
   - `populateBuildFields` adds the build-transport metadata: the upstream URL,
     the `PassthroughAPK` / `PassthroughDeb` filename, the toolchain container,
     the install task that extracts the archive into `$DESTDIR`, and the unit's
     `Distro` tag.
   - Return the `*Unit`.
5. **Register and update views.** The walker stores the returned `*Unit` under
   `UnitsByModule[<module>][resolved]` and updates the affected
   `DistroViews[*][resolved]` entries (only views for distros the unit is
   visible to — debian-tagged units land in `DistroViews["debian"]`, untagged
   units land in every view).
6. **Push runtime-deps onto the queue.** Each name in the new unit's
   `RuntimeDeps` goes onto the back of the BFS queue. Already-visited names (in
   a `seen` set) are skipped so cycles don't loop.

Repeat until the queue is empty. Then topologically sort the visited set so deps
come before dependents, and hand the result to the DAG builder.

### Why "lazy"

The synthetic feeds are registered eagerly during module evaluation — one
`alpine_feed()` or `debian_feed()` call per repo section. But each call only
registers the `SyntheticModule` (a name + a `Lookup` callback + the in-tree
`APKINDEX` / `Packages` path); no `*Unit` structures exist yet. The 50,000-entry
Debian catalog is on disk as text and in the `archCache` after first parse, but
the resolver allocates `*Unit` objects only for names the closure actually
references.

This dual structure — module-keyed storage plus precomputed per-distro views —
keeps the lookup-time path O(1) without sacrificing the laziness story.
Materialization happens on first reference; disambiguation (which feed's variant
wins for which distro) is resolved once during view construction, not on every
lookup.

### Scale

The materialization pass gives the resolver a O(closure size) working set
against an upstream index that can be much larger:

| Feed               | Upstream entries | Materialized (units, e2e) |
| ------------------ | ---------------: | ------------------------: |
| Alpine `main`      |          ~12,000 |                      ~150 |
| Alpine `community` |          ~24,000 |                      0–50 |
| Debian `main`      |          ~50,000 |                      ~200 |

The `archCache` (per-arch, per-feed) parses the on-disk `APKINDEX` / `Packages`
once on first `Lookup` call (~150ms for Debian's main; ~50ms for Alpine's),
holds the parsed entries in a `[]Entry` plus a name index (`map[string]*Entry`),
and serves every subsequent lookup from the cache. Per-process parse cost is
bounded by the number of active arches × feeds; allocation is paid only for
names actually consumed.

**Allocation cost per materialization** is the price of one `MaterializeUnit`
call: parse the upstream `Depends:` / `depends:` list into yoe's dep tokens,
look up each token through the provides table (which crosses sibling feeds for
Debian — `multiFeedProviders`), construct one `*Unit` and its `Tasks` / `Source`
fields, return. This is the cost the resolver pays at first reference of each
name; the returned pointer is then catalog-stable.

### The SyntheticModule contract

The `Lookup` callback the cycle calls is supplied by whichever feed builtin
registered the synthetic module (`alpine_feed` or `debian_feed`). The shape:

```go
type SyntheticModule struct {
    Name     string             // composed name, e.g. "alpine.main"
    Parent   string             // owning module, e.g. "module-alpine"
    Priority int                // negative; below every real module
    Lookup   func(name string) (*Unit, error)
    Names    func() []string    // enumeration for diagnostics/TUI
}
```

The walker doesn't know or care which format a synthetic wraps — APK indexes,
Debian `Packages` files, or anything else a future feed type adds. It just calls
`Lookup` and trusts the contract: return a fully-populated `*Unit` for known
names, return nil for misses, return an error only for genuine failures (corrupt
index, missing arch).

**One Lookup per name is load-bearing.** The materialization path allocates
fresh `*Unit` structures and triggers a Provides parse; calling `Lookup` for
_every_ synthetic on every name — to probe for a tagged variant before falling
back — pays that cost for names the walker would discard. Multiplied across a
closure walker's quadratic topological iteration, this allocates and throws away
`*Unit` structures unboundedly. The catalog's precomputed-views design exists
specifically to avoid this: disambiguation lives in the view construction at
load time, where each name is resolved exactly once; the walker never
re-resolves the same name twice.

## The closure walk as materialization driver

`resolve_closure(artifacts, distro=...)` is a Go-side Starlark builtin
(`fnResolveClosure` in `internal/starlark/closure.go`, registered as a builtin
in `internal/starlark/builtins.go`). Image classes call it from Starlark —
`modules/module-core/classes/image.star` invokes it with the image's effective
distro and its `artifacts = [...]` list. The Go implementation drives lazy
materialization. For each invocation, the closure walker:

1. **BFS** the runtime-dep graph rooted at the artifact names. For each name
   dequeued:
   - Resolve provides through `ResolveProvidesForDistro(distro, name)`.
   - `LookupUnit(distro, resolved)` returns the `*Unit` from
     `DistroViews[distro][resolved]`; on a miss, the synthetic walk fires and
     registers the result before returning.
   - Push the returned unit's `RuntimeDeps` onto the back of the queue.

2. **Topological sort** of the visited set. Emits units in dependency order so
   the DAG builder can validate edges and the build executor can schedule
   producers before consumers.

By the time the closure walker returns its ordered name list,
`DistroViews[distro]` contains every name the image actually needs for its
closure. The DAG builder (`internal/resolve/dag.go`) walks these names again via
`LookupUnit`, constructs edges from `Unit.Deps`, `Unit.Artifacts`, and container
references, and hands the executor the topologically-sorted plan.

The closure walker runs **once per image** during evaluation. Its output is
cached for the build executor; it does not run again between `yoe build` and
`yoe deploy`. Mutation of `UnitsByModule` is bounded by the number of
synthetic-unit names actually referenced across all images; mutation of
`DistroViews` is bounded by the same set times the number of distinct distros
the project targets.

## Hashing and the distro axis

Once units are in the catalog, each one's build output is content- addressed by
a hash over (a) the unit's metadata, (b) the hashes of its build-time
dependencies (recursively), (c) source inputs (commit sha / tarball digest), and
(d) the consuming image's **effective distro**. The hash is computed in
`internal/resolve/hash.go`'s `UnitHash`, called per-image with the image's
effective distro.

An image's effective distro is resolved through a three-level cascade: the
image's own `distro = "..."` field wins if set; otherwise the project's
`local.star` `default_distro_override` (per-developer, not committed) wins if
set; otherwise the project's `defaults.distro` in `PROJECT.star` is used; if
none of the three is set the image errors at evaluation. The cascade lets a
developer experiment with a different distro locally without editing committed
configuration.

Including effective distro in the hash gives every source-declared unit the
**build-twice-along-the-libc-axis** property: a single source unit
(`module-core/openssl`) builds once in `toolchain-musl` for an alpine image and
once in `toolchain-glibc` for a debian image, producing two distinct binary
outputs with two distinct cache entries. The catalog stores one `*Unit` (in
`UnitsByModule["module-core"]["openssl"]`); the build artifacts live under
per-distro paths on disk (`build/alpine/openssl.target/` vs
`build/debian/openssl.target/`); the hash key disambiguates the cache entries so
a debian build never reads back a musl-linked binary the alpine build produced.

For feed-materialized units, the `Distro` field on the materialized `*Unit` is
set by the feed (`"alpine"` for `alpine_feed`, `"debian"` for `debian_feed`);
the unit's hash naturally differs across distros because the unit itself
differs. For untagged source-built units, the consumer's effective distro is
what disambiguates — the unit definition is the same, but the cache key isn't.

## Working set in practice

A representative measurement from the e2e-project (alpine-only, qemu-x86_64,
base-image with the standard module set):

- Source-declared real units registered during load: ~80 across module-core
  (busybox, openssl, openssh, kernel, etc.), module-bsp, module-jetson, and the
  project root. These land in `UnitsByModule["module-core"]`,
  `UnitsByModule["module-bsp"]`, etc.
- Synthetic modules registered: ~5 (alpine.main, alpine.community, debian.main
  when listed). Each is one `*SyntheticModule` struct plus a per-arch
  `archCache` that's loaded lazily.
- Synthetic units materialized by the closure walk: ~150 from alpine.main, ~0
  from alpine.community (community is registered but rarely consumed in the e2e
  closure).
- Total `*Unit` entries across `UnitsByModule[*][*]` after evaluation: ~230.
- `DistroViews["alpine"]` size after view construction: ~230 (every visible unit
  is reachable in some closure).
- Total Go heap held by `Engine` post-evaluation: low tens of MB (units +
  provides + synthetic module struct + per-arch `archCache` for one arch + the
  precomputed views, which are pointer maps not unit copies).

The catalog is small enough that storage shape is not a memory or speed concern;
the structural property the design buys is correctness of cross-distro
disambiguation, not scale.

## Lifecycle and persistence

The catalog lives entirely in memory and is rebuilt from scratch on every yoe
invocation. Understanding what survives between processes and what doesn't tells
you when you can edit something and have it take effect, and when you have to
restart.

### Per-invocation: everything in memory is fresh

A yoe invocation — `yoe build`, `yoe deploy`, `yoe dry-run`, `yoe tui`,
`yoe update-feeds`, … — is a fresh process. The `Engine` struct, the
`archCache`, the materialized `*Unit` pointers, `UnitsByModule`, `DistroViews`,
the `Provides` map — all are constructed during loader evaluation and discarded
when the process exits.

So every invocation re-runs the loader in this order:

1. **Module discovery.** Parse `PROJECT.star`, resolve `modules = [...]` to
   clone paths, evaluate each `MODULE.star` in priority order.
2. **Eager registration.** Classes (`classes/*.star`), source-declared units
   (`units/*.star`), machines (`machines/*.star`), and synthetic modules (each
   `alpine_feed(...)` / `debian_feed(...)` call) all register during this phase.
   No `archCache` parse yet — the feed builtin only stores the on-disk index
   path and the callback.
3. **Image evaluation.** Every `image(...)` call in every module fires
   `resolve_closure(artifacts, distro=...)`, which drives BFS through the
   runtime-dep graph. The first time a feed's `Lookup` runs, its `archCache`
   parses the on-disk `APKINDEX` or `Packages` file (~50–150ms one-time).
   Subsequent lookups against the same feed are cheap map accesses.
4. **Distro views.** `buildDistroViews(proj)` runs once at the end of
   evaluation, filtering / pinning / priority-ranking per name per distro,
   producing the read-only `DistroViews` map.
5. **The actual command.** Build executor, deploy, TUI render — all read from
   the now-frozen catalog through `LookupUnit` and `AllUnits`.

### What survives between invocations

The in-memory catalog rebuilds every time, but several on-disk caches make that
rebuild cheap on the second-and-later invocations:

- **Build cache.** `build/<unit>.<scope>/destdir/` plus the content-addressed
  apk/deb output per unit. The build executor hashes each unit's inputs (source
  digest, dep hashes, effective distro) and re-uses any matching cache entry. A
  second `yoe build` with no source changes finishes in seconds because nothing
  actually compiles.
- **Source cache.** Downloaded tarballs and git clones under `cache/sources/`. A
  unit's source is fetched once per `(URL, ref)` tuple.
- **Module cache.** External modules cloned under `cache/modules/`. A
  `yoe build` does a `git fetch` and resets to the pinned ref but doesn't
  re-clone.
- **Feed indexes.** `APKINDEX` for Alpine, `Packages` for Debian — both live as
  plain text inside the module repos
  (`module-alpine/feeds/<section>/<arch>/APKINDEX`,
  `module-debian/feeds/<section>/<arch>/Packages`). These are refreshed only by
  `yoe update-feeds`; ordinary builds read what's checked in.

So every invocation re-parses these on-disk files into the in-memory
`archCache`, but the underlying data isn't regenerated unless you explicitly run
`update-feeds`.

### Reload semantics

There is no in-process reload API. The `archCache` and `DistroViews` are
computed once per process by design — making the in-memory state authoritative
for the lifetime of the process avoids race conditions between "what the
resolver thinks" and "what's on disk." If the resolver could re-read the catalog
mid-run, a build executor partway through a closure could find names resolving
differently than they did at the start of the build, a class of bug worth
avoiding structurally rather than handling explicitly.

For one-shot commands (`yoe build`, `yoe deploy`, `yoe dry-run`, etc.), each
invocation is a fresh process; any edit to any `.star` or feed index is picked
up on the next command automatically. For long-running surfaces (the TUI, or a
future daemon), a restart is required after any change that affects what the
catalog contains — see
[Restarting after edits](yoe-tool.md#restarting-after-edits) in the yoe-tool
guide for the change-vs-restart table.

## Invariants the resolver relies on

The catalog's contract to the rest of the system:

- **Pointer stability after registration.** Once a `*Unit` enters
  `UnitsByModule`, the pointer doesn't change. The DAG builder, the executor,
  the TUI, and the diagnostics layer all hold pointers into this map and assume
  they continue to point at the same Unit. `DistroViews` entries are aliases of
  the same pointers, so view reads are pointer-stable too.
- **One Lookup per name per project.** The lazy materialization path pays the
  per-call cost (provides parse, dep parse, struct alloc) exactly once per name
  across the entire project; subsequent references — from any closure walk, any
  image — hit the cached pointer via `DistroViews`.
- **Synthetic registration is module-priority-ordered.** The first
  `SyntheticModule` registered against an engine gets the highest synthetic
  priority (Priority = 0); each subsequent one is one step lower. All synthetics
  rank strictly below every real module.
- **View construction is single-pass.** `buildDistroViews` runs once at the end
  of the loader's evaluation phase, after every module has finished registering
  and `prefer_modules` has been validated. Mutation of `DistroViews` after that
  point is bounded by lazy materialization registering new entries; the
  resolution rules used for the initial pass are the same rules used for
  late-materialized entries, so the view stays consistent.
- **Empty `Unit.Distro` means "compatible with every distro."** An untagged unit
  appears in the candidate pool for every distro's view; tagged units appear
  only in their matching distro's pool. This is what lets `module-core` source
  builds serve both alpine and debian images from a single Unit definition while
  feed-materialized units stay scoped to their feed's distro.
- **Distro is a property of the Unit; module is a property of the
  registration.** Two orthogonal axes the catalog deliberately keeps separate.
  The Unit's Distro field decides which views it appears in; the registering
  module decides where it lives in `UnitsByModule` and its priority weight in
  view construction.

These invariants are what let the resolver run the closure walker with a single
map access per name, hold a small working set, and support cross-distro
coexistence without rebuilding storage on every lookup.
