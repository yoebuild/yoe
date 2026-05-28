# Catalog and Materialization

This page describes yoe's in-memory **unit catalog** — the runtime data
structure that holds every unit a project knows about, how units enter
that structure (eagerly or lazily), and how the catalog is queried
during the build. It is the resolver's address book: the closure walker
and the build executor both ask the catalog the same question — *"give
me the Unit for this name in this context"* — and the catalog's design
governs how that answer is computed.

For the user-facing surface (what `unit()`, `image()`, `alpine_feed()`,
and `prefer_modules` look like in Starlark) see
[metadata-format.md](metadata-format.md) and
[naming-and-resolution.md](naming-and-resolution.md). This page is the
internals view: storage shapes, lifecycle, allocation cost, and the
invariants the resolver relies on.

## Three classes of units

Every Unit in the catalog falls into one of three categories. They
differ in *when* they enter the catalog and *what* the catalog actually
stores.

**Source-declared real units.** A `.star` file in a module's `units/`
directory calls `unit(...)`, `image(...)`, `machine(...)`,
`container(...)`. The builtin handler allocates a `*Unit`, fills its
fields from the call's kwargs, and registers it eagerly during
evaluation. Source-declared units are present in the catalog by the
end of the loader's evaluation phase, regardless of whether any image
references them.

**Synthetic units (feed-materialized).** A `MODULE.star` calls
`alpine_feed(...)` or `debian_feed(...)`. The builtin does NOT
allocate one `*Unit` per upstream package; it registers one
**`SyntheticModule`** with a `Lookup(name) → *Unit` callback. The
60k-entry upstream index sits parsed-but-unmaterialized on disk and
in a single `archCache` struct. A `*Unit` for a specific name appears
in the catalog only when something asks for it (see [Lazy
materialization](#lazy-materialization)).

**Virtual references.** A unit's `provides = ["toolchain"]` registers
the name `toolchain` as an alias for the providing unit's canonical
name. Virtuals are *not* separate `*Unit` entries — they're entries
in `Project.Provides` (a `map[string]string`) that the resolver walks
once at lookup time. A reference to `"toolchain"` in a class's
`container = "toolchain"` resolves to whichever unit currently
provides it (see [virtual packages](naming-and-resolution.md#virtual-packages-ctxprovides)).

## Storage shape

The catalog has two layers: a primary store keyed by source module, and
a set of precomputed per-distro views that consumers actually query.

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

**`UnitsByModule` is the primary store.** Every Unit registration —
eager from `.star` evaluation, lazy from a `SyntheticModule.Lookup`
return — lands under its source module. Cross-module same-name
registrations coexist; `module-core`'s `openssl` and `alpine.main`'s
`openssl` live in separate buckets and never overwrite each other.

**`DistroViews` is what consumers query.** Built once at the end of
the loader's evaluation phase via `buildDistroViews(proj)`. For each
distinct unit name across the project and for each distro the project
sees, the loader runs a three-step resolution:

1. **Filter** the candidate pool to units in `UnitsByModule[*][name]`
   whose `Distro` is `""` (visible to every distro) or matches the
   target distro. Cross-distro entries are eliminated structurally.
2. **Pin** — if `prefer_modules[name]` names a module still among
   survivors, that module's variant wins. Pins are hints, not
   guarantees: a pin that names a module the distro filter eliminated
   falls through to step 3 with a diagnostic.
3. **Priority** — pick the survivor from the highest-priority module.
   Project root outranks every external module; later-declared
   external modules outrank earlier-declared ones; every synthetic
   (feed-materialized) module ranks below every real module. The
   "synthetics rank below reals" rule means a from-source override
   in `module-core` automatically beats a same-named feed entry,
   without needing a `prefer_modules` pin to make it so.

The result is `DistroViews[distro][name] → *Unit`. Read-only after
construction. The closure walker, build executor, and any other
consumer with distro context call:

```go
func (p *Project) LookupUnit(distro, name string) *Unit {
    return p.DistroViews[distro][name]
}
```

— a single map access. Consumers without distro context (TUI list-all,
source workspace) iterate via `AllUnits()`, an `iter.Seq2[string,
*Unit]` over `UnitsByModule` that yields every registered unit. For
same-name units across modules, `AllUnits()` yields each separately;
the caller decides what to do with the collision.

**Distro is a visibility filter; module is the priority axis.** Two
orthogonal concerns. The catalog's job is to keep them separate:
storage keys by module (the source-of-truth label), the views slice by
distro (the runtime-compatibility class). This preserves the
"module-core wins by default" semantics — `module-core`'s source-built
`openssl` wins for any image's `openssl` lookup because `module-core`
is the highest-priority module — while letting `alpine.main`'s
`libcap2` and `debian.main`'s `libcap2` coexist in separate candidate
pools, each visible only to closures of its own distro.

**Diagnostics-not-mutation.** Shadowing within a distro view is
observable but not visible in `DistroViews` itself: the loser is
dropped from the view and the collision is recorded in
`Project.Diagnostics.Shadows` for the TUI to surface. This is
intentional — `DistroViews` is the *resolved* state; the journey is
in Diagnostics. The same applies to a `prefer_modules` pin that fell
through because its module was distro-filtered out: the diagnostic
records the fallthrough so the user can see why their pin didn't take
effect.

## Lazy materialization

A `SyntheticModule` is the lazy half of the catalog. Its contract:

```go
type SyntheticModule struct {
    Name     string             // composed name, e.g. "alpine.main"
    Parent   string             // owning module, e.g. "module-alpine"
    Priority int                // negative; below every real module
    Lookup   func(name string) (*Unit, error)
    Names    func() []string    // enumeration for diagnostics/TUI
}
```

`LookupUnit(distro, name)` checks `DistroViews[distro][name]` first
— a hit returns immediately. On a miss, the loader visits each
registered `SyntheticModule` in priority order; the first non-nil
return is registered into `UnitsByModule[<module>][name]` and the
affected `DistroViews[*][name]` entries are computed before returning.
Subsequent lookups for the same name hit the view fast path.

This dual structure — module-keyed storage plus precomputed per-distro
views — keeps the lookup-time path O(1) without sacrificing the
laziness story. Materialization happens on first reference;
disambiguation (which feed's variant wins for which distro) is
resolved once during view construction, not on every lookup.

The materialization pass gives the resolver a O(closure size) working
set against an upstream index that can be much larger:

| Feed                          | Upstream entries | Typical closure | Materialized |
| ----------------------------- | ---------------: | --------------: | -----------: |
| Alpine `main` (v3.21, x86_64) |          ~12,000 |     150 (e2e)   |         ~150 |
| Alpine `community`            |          ~24,000 |     0–50        |        0–50  |
| Debian `main` (bookworm)      |          ~50,000 |     ~200 (e2e)  |        ~200  |

The `archCache` (per-arch, per-feed) parses the on-disk `APKINDEX` /
`Packages` once on first `Lookup` call (~150ms for Debian's main; ~50ms
for Alpine's), holds the parsed entries in a `[]Entry` plus a name
index (`map[string]*Entry`), and serves every subsequent lookup from
the cache. Per-process parse cost is bounded by the number of active
arches × feeds; allocation is paid only for names actually consumed.

**Allocation cost per materialization** is the price of one
`MaterializeUnit` call: parse the upstream `Depends:` / `depends:` list
into yoe's dep tokens, look up each token through the provides table
(which crosses sibling feeds for Debian — `multiFeedProviders`),
construct one `*Unit` and its `Tasks` / `Source` fields, return. This
is the cost the resolver pays at first reference of each name; the
returned pointer is then catalog-stable.

**One Lookup per name is load-bearing.** The materialization path
allocates fresh `*Unit` structures and triggers a Provides parse;
calling `Lookup` for *every* synthetic on every name — to probe for a
tagged variant before falling back — pays that cost for names the
walker would discard. Multiplied across a closure walker's quadratic
topological iteration, this allocates and throws away `*Unit`
structures unboundedly. The catalog's precomputed-views design exists
specifically to avoid this: disambiguation lives in the view
construction at load time, where each name is resolved exactly once;
the walker never re-resolves the same name twice.

## The closure walk as materialization driver

`resolve_closure(artifacts, distro=...)` is a Go-side Starlark builtin
(`fnResolveClosure` in `internal/starlark/closure.go`, registered as a
builtin in `internal/starlark/builtins.go`). Image classes call it from
Starlark — `modules/module-core/classes/image.star` invokes it with the
image's effective distro and its `artifacts = [...]` list. The Go
implementation drives lazy materialization. For each invocation, the
closure walker:

1. **Breadth-first walk** of the runtime-dep graph rooted at the
   artifact names. Each name is visited once (a `seen` set prevents
   revisits across cycles), and for each visit:
   - Resolve provides through `ResolveProvidesForDistro(distro, name)`.
   - `LookupUnit(distro, resolved)` returns the resolved `*Unit` from
     `DistroViews[distro][resolved]`; on a miss, the synthetic walk
     fires and registers the result before returning.
   - Push the returned unit's `RuntimeDeps` onto the back of the
     queue. Breadth-first (queue, not recursion) keeps the working
     set bounded by closure size regardless of dep-tree depth — a
     deeply-nested transitive chain won't blow the call stack.

2. **Topological sort** of the visited set. Emits units in dependency
   order so the DAG builder can validate edges and the build executor
   can schedule producers before consumers.

By the time the closure walker returns its ordered name list,
`DistroViews[distro]` contains every name the image actually needs
for its closure. The DAG builder (`internal/resolve/dag.go`) walks
these names again via `LookupUnit`, constructs edges from `Unit.Deps`,
`Unit.Artifacts`, and container references, and hands the executor
the topologically-sorted plan.

The closure walker runs **once per image** during evaluation. Its
output is cached for the build executor; it does not run again
between `yoe build` and `yoe deploy`. Mutation of `UnitsByModule` is
bounded by the number of synthetic-unit names actually referenced
across all images; mutation of `DistroViews` is bounded by the same
set times the number of distinct distros the project targets.

## Hashing and the distro axis

Once units are in the catalog, each one's build output is content-
addressed by a hash over (a) the unit's metadata, (b) the hashes of
its build-time dependencies (recursively), (c) source inputs (commit
sha / tarball digest), and (d) the consuming image's **effective
distro**. The hash is computed in `internal/resolve/hash.go`'s
`UnitHash`, called per-image with the image's effective distro.

An image's effective distro is resolved through a three-level cascade:
the image's own `distro = "..."` field wins if set; otherwise the
project's `local.star` `default_distro_override` (per-developer, not
committed) wins if set; otherwise the project's `default_distro` in
`PROJECT.star` is used; if none of the three is set the image errors at
evaluation. The cascade lets a developer experiment with a different
distro locally without editing committed configuration.

Including effective distro in the hash gives every source-declared unit
the **build-twice-along-the-libc-axis** property: a single source unit
(`module-core/openssl`) builds once in `toolchain-musl` for an alpine
image and once in `toolchain-glibc` for a debian image, producing two
distinct binary outputs with two distinct cache entries. The catalog
stores one `*Unit` (in `UnitsByModule["module-core"]["openssl"]`); the
build artifacts live under per-distro paths on disk
(`build/alpine/openssl.target/` vs `build/debian/openssl.target/`); the
hash key disambiguates the cache entries so a debian build never reads
back a musl-linked binary the alpine build produced.

For feed-materialized units, the `Distro` field on the materialized
`*Unit` is set by the feed (`"alpine"` for `alpine_feed`,
`"debian"` for `debian_feed`); the unit's hash naturally differs
across distros because the unit itself differs. For untagged
source-built units, the consumer's effective distro is what
disambiguates — the unit definition is the same, but the cache key
isn't.

## Working set in practice

A representative measurement from the e2e-project (alpine-only,
qemu-x86_64, base-image with the standard module set):

- Source-declared real units registered during load: ~80 across
  module-core (busybox, openssl, openssh, kernel, etc.),
  module-bsp, module-jetson, and the project root. These land in
  `UnitsByModule["module-core"]`, `UnitsByModule["module-bsp"]`,
  etc.
- Synthetic modules registered: ~5 (alpine.main, alpine.community,
  debian.main when listed). Each is one `*SyntheticModule` struct
  plus a per-arch `archCache` that's loaded lazily.
- Synthetic units materialized by the closure walk: ~150 from
  alpine.main, ~0 from alpine.community (community is registered
  but rarely consumed in the e2e closure).
- Total `*Unit` entries across `UnitsByModule[*][*]` after
  evaluation: ~230.
- `DistroViews["alpine"]` size after view construction: ~230 (every
  visible unit is reachable in some closure).
- Total Go heap held by `Engine` post-evaluation: low tens of MB
  (units + provides + synthetic module struct + per-arch
  `archCache` for one arch + the precomputed views, which are
  pointer maps not unit copies).

The catalog is small enough that storage shape is not a memory or
speed concern; the structural property the design buys is correctness
of cross-distro disambiguation, not scale.

## Invariants the resolver relies on

The catalog's contract to the rest of the system:

- **Pointer stability after registration.** Once a `*Unit` enters
  `UnitsByModule`, the pointer doesn't change. The DAG builder, the
  executor, the TUI, and the diagnostics layer all hold pointers
  into this map and assume they continue to point at the same Unit.
  `DistroViews` entries are aliases of the same pointers, so view
  reads are pointer-stable too.
- **One Lookup per name per project.** The lazy materialization path
  pays the per-call cost (provides parse, dep parse, struct alloc)
  exactly once per name across the entire project; subsequent
  references — from any closure walk, any image — hit the cached
  pointer via `DistroViews`.
- **Synthetic registration is module-priority-ordered.** The first
  `SyntheticModule` registered against an engine gets the highest
  synthetic priority (Priority = 0); each subsequent one is one
  step lower. All synthetics rank strictly below every real module.
- **View construction is single-pass.** `buildDistroViews` runs
  once at the end of the loader's evaluation phase, after every
  module has finished registering and `prefer_modules` has been
  validated. Mutation of `DistroViews` after that point is bounded
  by lazy materialization registering new entries; the resolution
  rules used for the initial pass are the same rules used for
  late-materialized entries, so the view stays consistent.
- **Empty `Unit.Distro` means "compatible with every distro."** An
  untagged unit appears in the candidate pool for every distro's
  view; tagged units appear only in their matching distro's pool.
  This is what lets `module-core` source builds serve both alpine
  and debian images from a single Unit definition while
  feed-materialized units stay scoped to their feed's distro.
- **Distro is a property of the Unit; module is a property of the
  registration.** Two orthogonal axes the catalog deliberately
  keeps separate. The Unit's Distro field decides which views it
  appears in; the registering module decides where it lives in
  `UnitsByModule` and its priority weight in view construction.

These invariants are what let the resolver run the closure walker
with a single map access per name, hold a small working set, and
support cross-distro coexistence without rebuilding storage on
every lookup.
