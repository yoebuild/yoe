# Caching

> **Status:** This document has two halves. The **survey** (how Nix, Bazel,
> Yocto, and others cache) is reference material. The **Yoe cache design** half
> is **planned** — it collects the direction we are converging on and the open
> questions, not shipped behavior. What yoe caches _today_ is in the
> [What Yoe caches today](#what-yoe-caches-today) section below (the
> content-addressed object store and S3 levels); implementation status is in the
> [content-addressed-cache plan](plans/content-addressed-cache.md).

Caching is the difference between an embedded build that takes minutes and one
that takes hours. It is also the concrete answer to the most common objection
yoe hears — _"why rewrite packages when nixpkgs already has tens of thousands?"_
A good cache means nobody rebuilds what someone else already built: the first
person (or CI, or a chip vendor) pays the build cost once, and everyone else
pulls the result. The goal for yoe is a cache that reaches **project → company →
global** scope, the way `cache.nixos.org` and PyPI wheels do, so that "build
from source" is the fallback, not the default.

This document surveys how the major build systems cache, extracts the shared
vocabulary, and then lays out the Yoe cache design.

## The shared vocabulary of build caches

Every build cache makes the same handful of decisions. Naming them makes the
systems comparable.

- **Granularity — the unit of caching.** What is the smallest thing that gets a
  cache entry? A whole package? One build step within a package? A single
  compiler invocation? Finer granularity means smaller rebuilds on a change but
  more entries to key, store, and look up.
- **Cache key — what the entry is addressed by.** Two families:
  - **Input-addressed** — the key is a hash of everything that _goes into_ a
    build (source, dependencies, flags, environment). If the inputs match, the
    output is assumed valid. Simple; this is what yoe does today.
  - **Content-addressed** — the key is a hash of what _came out_. Lets two
    different inputs that happen to produce identical output share one entry,
    and enables _early cutoff_ (below).
- **The store.** Where blobs live, and the fallback chain from local disk → LAN
  mirror → remote object store. The store is almost always immutable,
  write-once, keyed by hash — which is why S3-style object storage fits so well.
- **Source / fetch handling.** Downloads are usually cached separately from
  builds. Separating _fetch_ from _build_ (pin and download inputs up front,
  then build offline against the pinned set) is what makes a build reproducible
  and immune to "a tag moved underneath me" failures.
- **Early cutoff — stopping a rebuild cascade.** When an input changes but the
  _output_ of a build doesn't (a comment-only patch to gcc, a whitespace edit),
  a pure input-addressed cache rebuilds everything downstream anyway. Systems
  that content-address outputs can notice "the output is identical" and reuse
  all downstream cache entries. This is the single biggest differentiator
  between the simple and the sophisticated caches.
- **Trust & signing.** When you pull a prebuilt binary from a shared cache, you
  are trusting whoever built it. Signing (and verifying on pull) is what makes a
  shared cache safe — and is increasingly a compliance requirement (SBOM /
  provenance under the EU Cyber Resilience Act).
- **Garbage collection / eviction.** Immutable stores grow without bound; every
  system needs a policy (reference roots, last-access TTL, S3 lifecycle rules).

## Survey

### Nix

- **Granularity: per derivation (≈ per package).** A derivation is one atomic
  build action producing one or more output store paths. For a typical nixpkgs
  package that is the whole configure→compile→install run. Nix does **not**
  cache intermediate phases within a derivation; change anything that affects
  the derivation and the whole thing rebuilds.
- **Key: input-addressed by default.** The derivation hash is computed from all
  inputs (sources, dependency store paths, build script, flags). A matching
  output in the local store or a _substituter_ (binary cache) is reused.
- **Source/fetch: cleanly separated.** Fetches are **fixed-output derivations**
  keyed by the _content_ hash of what they download. Fetch is structurally its
  own derivation, so builds run offline against already-pinned sources. This is
  the property the Lobsters thread kept pointing at.
- **Store & remote:** `/nix/store` locally; `cache.nixos.org`, Cachix, or
  `nix-serve` remotely. Remote entries are **closures** packaged as NARs and can
  be large (1GB+), because the unit shipped is "this output plus everything it
  references."
- **Early cutoff: opt-in via content-addressed derivations** (RFC 062 / CA
  derivations). With CA derivations, if a rebuild produces byte-identical
  output, dependents are _not_ rebuilt. Classic input-addressed Nix lacks this.
- **Trust:** binary caches are signed (ed25519); clients verify against
  configured public keys before substituting.

### Bazel (and remote execution)

- **Granularity: per action — the finest of any system here.** The cached unit
  is a single action (one command with declared inputs and outputs): compile one
  `.o`, link one binary, run one codegen. A single library is many actions.
- **Key: content-addressed action digest.** Each action's key is a hash of the
  command line, the _content_ hashes of all declared input files, the
  environment, and the execution platform. Outputs are content-addressed blobs.
- **Two cooperating caches:**
  - **Action Cache (AC)** — maps an action digest → an `ActionResult` (metadata
    pointing at output blobs).
  - **Content-Addressable Storage (CAS)** — stores the actual output blobs keyed
    by content hash.
- **Early cutoff: native.** Because outputs are content-addressed, a change that
  produces an identical output blob lets downstream actions hit the AC
  unchanged. Bazel gets cascade-stopping "for free" from content addressing.
- **Source/fetch:** external repositories (WORKSPACE / bzlmod) are fetched and
  stored in a **repository cache** keyed by declared content hash, separate from
  the action cache. In-tree sources are just content-hashed input files.
- **Store & remote:** the **Remote Execution API (REAPI)** is a standard
  protocol; `bazel-remote`, BuildBuddy, BuildBarn, and BuildGrid all implement
  AC+CAS, and the same protocol drives remote _execution_, not just caching.
- **Hermeticity is the precondition.** Caching is only sound because actions are
  sandboxed and must declare inputs; a non-hermetic action poisons the cache.

### Yocto / BitBake sstate

- **Granularity: per task.** A recipe is decomposed into tasks (`do_fetch`,
  `do_unpack`, `do_patch`, `do_configure`, `do_compile`, `do_install`,
  `do_populate_sysroot`, `do_package`, `do_package_write_{deb,rpm,ipk}`, …).
  **Shared state (sstate)** caches the outputs of the _setscene_-capable tasks —
  the ones that produce a stable, restorable artifact (sysroot population,
  packaged output, packagedata), not every task.
- **Key: task signature.** Each task's signature is a hash of every variable it
  references, file checksums of its inputs, and **the signatures of the tasks it
  depends on**. Change a variable gcc reads and gcc's signature changes, which
  propagates to everything downstream.
- **Early cutoff via the hash-equivalence server.** This is Yocto's clever
  answer to the cascade problem: a `hashequiv` service maps a task's _input_
  signature to an _output_ hash. If a change yields a different input signature
  but an _equivalent output_ (e.g. a docs-only patch to a library), downstream
  tasks can reuse existing sstate instead of rebuilding. It is the same idea as
  Nix CA-derivations and Bazel content addressing, bolted onto an
  input-addressed system after the fact.
- **Source/fetch:** downloads live in `DL_DIR`, keyed by filename with `.done`
  stamps, fed by `PREMIRRORS` / `MIRRORS`. sstate outputs live in `SSTATE_DIR`,
  shared via `SSTATE_MIRRORS` (file / HTTP / S3).
- **Cost:** the power is real but the setup is heavy — sstate mirrors, the
  hashequiv server, signature debugging (`bitbake-diffsigs`) when a cache miss
  is unexpected. This is exactly the friction the HN thread's "you haven't
  enabled sstate" exchange was about: sstate works well but is non-trivial to
  stand up and share, especially for a small team.

### Buildroot and ccache (the baseline and the compiler cache)

- **Buildroot** is the useful _contrast_: historically it has **no shared
  package/binary cache**. It caches downloads (`BR2_DL_DIR`) and supports
  ccache, but a configuration change generally means rebuilding from scratch.
  This is the "no early cutoff, no artifact reuse" baseline that makes the case
  for a cache in the first place.
- **ccache** is orthogonal and composable: it caches _compiler_ output, keyed by
  a hash of the preprocessed source plus compiler flags. It sits one level below
  any of the above (per compiler invocation) and several systems layer it under
  their own caching. Worth knowing as a building block, not a competitor.

### Comparison

| System           | Granularity        | Key                         | Source/fetch               | Early cutoff                 | Remote backend              | Trust            |
| ---------------- | ------------------ | --------------------------- | -------------------------- | ---------------------------- | --------------------------- | ---------------- |
| **Nix**          | Per derivation     | Input-addressed (CA opt-in) | Fixed-output drv (content) | Only with CA derivations     | cache.nixos.org, Cachix, S3 | ed25519 signed   |
| **Bazel**        | Per action         | Content-addressed digest    | Repository cache (content) | Native (content-addressed)   | REAPI (bazel-remote, BB, …) | per-deployment   |
| **Yocto sstate** | Per task           | Task signature (inputs)     | DL_DIR (by filename)       | hashequiv server             | SSTATE_MIRRORS (HTTP/S3)    | optional GPG     |
| **Buildroot**    | Whole build        | n/a (download cache only)   | BR2_DL_DIR (by filename)   | None                         | None (downloads only)       | none             |
| **ccache**       | Per compile        | Preprocessed source + flags | n/a                        | n/a                          | Local / shared dir          | none             |
| **Yoe (today)**  | Per unit (package) | Input-addressed (SHA256)    | Object store (content)     | **None yet** (input cascade) | Any S3-compatible           | optional ed25519 |

The reading: yoe today sits closest to **Nix's classic (input-addressed)
model**, at **package granularity** like Nix, with **Bazel-style simple S3
object storage** and **a cleaner setup story than Yocto sstate**. The one
capability the sophisticated systems have that yoe does not yet is **early
cutoff** — and all three solve it the same way, by addressing _outputs_ by
content (Nix CA, Bazel CAS, Yocto hashequiv).

## What Yoe caches today

`[yoe]` uses a unified, content-addressed object store for both source archives
and built packages. The design is inspired by Nix's `/nix/store` and Git's
object database: immutable blobs keyed by cryptographic hashes, with a
multi-level fallback chain for local and remote storage. Implementation status
is in the [content-addressed-cache plan](plans/content-addressed-cache.md)
(local store working; full object store and remote pending).

### Object store layout

All cached artifacts live under `$YOE_CACHE` (default: `cache/`):

```
$YOE_CACHE/
├── objects/
│   ├── sources/
│   │   ├── ab/cd1234...5678.tar.gz     # tarball, keyed by content SHA256
│   │   ├── ef/01abcd...9012.tar.xz     # another tarball
│   │   └── 34/567890...abcd.git/       # bare git repo, keyed by url#ref hash
│   └── packages/
│       ├── x86_64/
│       │   ├── a1/b2c3d4...e5f6.apk    # built package, keyed by unit input hash
│       │   └── 78/90abcd...1234.deb
│       └── aarch64/
│           └── ...
├── index/
│   ├── sources.json                     # URL → content hash mapping
│   └── packages.json                    # unit name+version → input hash mapping
└── tmp/                                 # atomic writes land here first
```

**Key design points:**

- **Two-character prefix directories** (like Git) prevent any single directory
  from accumulating millions of entries.
- **Sources are keyed by content hash** — the SHA256 of the actual file, which
  units already declare in their `sha256` field. Two different URLs serving
  identical tarballs share one cache entry.
- **Git sources are keyed by `sha256(url + "#" + ref)`** — since a git repo is a
  directory (not a single file), content-addressing isn't practical. The URL+ref
  key ensures different tags/branches get separate clones.
- **Packages are keyed by unit input hash** — the same hash computed by
  `internal/resolve/hash.go` from unit fields, source hash, dependency hashes,
  and architecture. This is the Nix-like property: if the inputs haven't
  changed, the cached output is valid.
- **Index files** provide human-readable reverse lookups (hash → name) for
  debugging and `yoe cache list`. They are not authoritative — the object store
  is the source of truth.

### Build flow with cache

```
yoe build openssh
  │
  ├─ 1. Resolve DAG, compute input hashes for all units
  │     (internal/resolve/hash.go — already implemented)
  │
  ├─ 2. For each unit in topological order:
  │     │
  │     ├─ Check local object store: objects/packages/<arch>/<hash>.{apk,deb}
  │     │   Hit → publish to build/repo/, skip to next unit
  │     │
  │     ├─ Check remote cache: GET s3://bucket/packages/<arch>/<hash>.{apk,deb}
  │     │   Hit → download to local object store, publish to repo, skip
  │     │
  │     ├─ Cache miss → need to build:
  │     │   │
  │     │   ├─ Check source cache: objects/sources/<hash>.<ext>
  │     │   │   Hit → extract to build/<unit>/src/
  │     │   │   Miss → download, verify SHA256, store in object store
  │     │   │
  │     │   ├─ Build unit (sandbox or direct)
  │     │   │
  │     │   ├─ Package output (.apk for Alpine, .deb for Debian/Ubuntu)
  │     │   │
  │     │   ├─ Store package in local object store under input hash
  │     │   │
  │     │   ├─ Push to remote cache (if configured): PUT s3://bucket/...
  │     │   │
  │     │   └─ Publish package to build/repo/ for image assembly
  │     │
  │     └─ Next unit
  │
  └─ 3. Assemble image (if target is an image unit)
```

The critical property: **a cache hit on a package skips the entire build,
including source download.** This is why CI builds are fast — most packages come
from the remote cache, and only the changed unit (plus anything that
transitively depends on it) actually builds.

### Cache key computation

The cache key for a unit is computed by `internal/resolve/hash.go`. It is a
SHA256 hash of:

- Unit identity: name, version, class
- Architecture
- Source: URL, SHA256, tag, branch, patches
- Build configuration: build steps, configure args, Go package
- **Dependency hashes (transitive)**: the input hash of every dependency

The transitive dependency hashes are the key property. If `glibc` is rebuilt
(new version, new patch, new build flags), its hash changes. That propagates to
every package that depends on `glibc`, which all get new hashes, which all
become cache misses. This is automatic — there are no stale entries, only unused
ones.

For image units, the hash also includes the package list, hostname, timezone,
locale, and service list.

New hash-participating fields must be gated on a non-empty/non-zero check so
units that don't set them stay cache-neutral (see the CLAUDE.md
"Content-addressed caching" rule); an unconditional write invalidates every
unit's hash the moment the line lands.

### Cache levels

```
┌──────────────────────────────────────────────────┐
│  Level 1: Local Object Store                     │
│  $YOE_CACHE/objects/                             │
│  Fastest — no network. Populated by local builds │
├──────────────────────────────────────────────────┤
│  Level 2: LAN / Self-Hosted Cache (optional)     │
│  MinIO or S3-compatible on local network         │
│  ~1ms latency. Shared across team workstations   │
├──────────────────────────────────────────────────┤
│  Level 3: Remote Cache (optional)                │
│  AWS S3, GCS, R2, Backblaze B2, etc.            │
│  Shared across CI runners and distributed teams  │
└──────────────────────────────────────────────────┘
```

All levels use the same key scheme — the object path is the same locally and
remotely. Pushing a local object to S3 is a direct upload of the file under the
same key. Pulling is a direct download. No translation or repackaging needed.

### Why S3-compatible storage

Content-addressed packages are **immutable, write-once blobs** keyed by their
input hash. This maps directly to S3's key-value object model:

- **No coordination** — multiple CI runners push/pull concurrently without
  locking. Two builders producing the same hash write the same content; last
  writer wins harmlessly.
- **Widely available** — AWS S3, MinIO (self-hosted), GCS, Cloudflare R2, and
  Backblaze B2 all speak the same API. No vendor lock-in.
- **Built-in lifecycle management** — S3 lifecycle policies handle cache
  eviction (e.g., delete objects not accessed in 90 days). No custom garbage
  collection needed.
- **Right granularity** — S3 GET latency (~50-100ms) is negligible at
  package-level granularity. A cache hit that avoids a 5-minute GCC build is
  worth 100ms of network overhead.

Self-hosted MinIO is the recommended starting point for teams that want shared
caching without cloud dependency. It runs as a single binary, supports the full
S3 API, and works in air-gapped environments. The key simplification over Yocto
sstate: no hash equivalence server, no sstate mirror configuration, no signing
key infrastructure to get started — point `cache.url` at a bucket and it works
(see the survey comparison above for the full contrast).

### Language package manager caches

Language-native package managers (Go modules, Cargo crates, npm packages, pip
wheels) have their own download caches. `[yoe]` shares these across builds:

- **Go** — `GOMODCACHE` is set to a shared directory; the Go module proxy
  (`GOPROXY`) can point to a local Athens instance or the public
  `proxy.golang.org`.
- **Rust** — `CARGO_HOME` is shared; a local
  [Panamax](https://github.com/panamax-rs/panamax) mirror can serve as a
  registry cache.
- **Node.js** — `npm_config_cache` is shared; a local Verdaccio instance can
  proxy the npm registry.
- **Python** — `PIP_CACHE_DIR` is shared; a local devpi instance can proxy PyPI.

These caches are **not** content-addressed by `[yoe]` — they are managed by the
language toolchains themselves. `[yoe]` ensures the cache directories persist
across builds and are shared across units that use the same language. Folding
their provenance into yoe's own cache is an open question (see below).

### Cache signing and verification

Packages pushed to a remote cache are signed with a project-level key. When
pulling from a remote cache, `yoe` verifies the signature before using the
cached package. This prevents cache poisoning — a compromised cache server
cannot inject malicious packages.

The signing key is configured in `PROJECT.star` (`cache(signing=...)`). For CI,
the private key is provided via environment variable; workstations can use a
read-only public key for verification only.

### Current limitation

The limitation the survey isolates: keying is purely input-addressed, so any
input change cascades a rebuild through every dependent even when the rebuilt
output is identical. Closing that gap is the main open design decision below.

## Yoe cache design (planned)

This section is direction, not shipped behavior.

### Design goals

1. **Reuse beats rewriting.** The cache is the mechanism that makes yoe's small
   package catalog a non-issue: pull prebuilt artifacts instead of building. The
   target is a tiered cache — **project, company, then a public global cache** —
   modeled on `cache.nixos.org` and PyPI wheels.
2. **Low setup cost stays a feature.** "Point `cache.url` at a bucket and it
   works" is a deliberate advantage over sstate. Any new capability (early
   cutoff, signing) must not reintroduce hashequiv-level operational burden as a
   _requirement_; it can be an opt-in.
3. **Frictionless and auditable are different axes.** Approachability must not
   cost provenance. A yoe build should be reproducible and fully pinned _when
   you want it_, so the cache is a stronger CRA/SBOM story than either Yocto-LTS
   or a stock binary distro — answering the "friction = security" critique
   directly rather than trading one for the other.

### Granularity: stay per-unit

Per-unit (per-package) is the right default and we should keep it:

- It matches the artifact users actually consume (`.apk` / `.deb`), so a cache
  entry _is_ a shippable package — the same property that makes nixpkgs and
  wheels globally reusable.
- It keeps the store simple and the S3 mapping trivial (one immutable blob per
  key, no closure packing).
- Finer granularity (Bazel's per-action) buys faster local incremental rebuilds
  but explodes entry count and only pays off with a hermetic action graph yoe
  doesn't have. The roadmap's "record completed tasks in `build.json` and resume
  at the first incomplete task" item is the _local incremental_ answer to that
  need and does not require action-level remote caching.

Open sub-question: **multi-output units.** As units grow to emit `-dev` / `-doc`
/ `-libs` subpackages (roadmap: "Units output multiple packages"), the cache
must key and store each output artifact, while the _build_ still keys on the
unit input hash. This is the Nix multiple-outputs shape and should be designed
in, not bolted on.

### Early cutoff: the main open design decision

This is the one real capability gap. Three options, in increasing cost:

1. **Do nothing (status quo).** Accept rebuild cascades on any input change.
   Simplest; wastes work on no-op changes (the docs-only-patch case).
2. **Output content-addressing with an equivalence map** — yoe's analog to
   Yocto's hashequiv / Nix CA. After building a unit, hash its _output_
   artifact; record `input_hash → output_hash`. When a dependency's input hash
   changes but its recorded output hash does not, reuse the dependent's existing
   cache entry instead of rebuilding. This is the highest-leverage improvement
   and worth a spec on its own.
3. **Full content-addressed derivations** (Nix RFC 062 model). Maximally
   principled, maximally complex; almost certainly more than yoe needs.

Recommendation to explore first: **option 2**, scoped as an optional layer over
the existing input-hash store so the simple path still "just works."

### Fetch / build split and determinism

yoe already separates source fetching (content-addressed `sources/`) from
building, which is most of the win. To close the determinism gap the survey and
the Lobsters thread raised:

- **Pin mutable refs.** The roadmap already flags "warn if units specify Git
  branches." Go further: support fetch-time resolution of a branch to an
  immutable commit + content hash, so a moved/recreated tag (senekor's war
  story) is structurally impossible to silently consume.
- **Offline build mode.** A flag that forbids network during the build phase,
  building only against the pinned/fetched set — making "deterministic when you
  want it" a demonstrable claim, not an assertion.

### Trust, signing, and provenance

- **Sign on push, verify on pull** (ed25519), already staged in the
  content-addressed-cache plan. This is the precondition for a _shared_ cache to
  be safe, and the foundation of the CRA/SBOM story.
- **Record provenance per cache entry** — what inputs produced it, who/what
  built it, and (with option 2) the realized output closure. The roadmap's
  ["closure as a first-class output"](roadmap.md) item feeds directly into this:
  a verified, pinned closure per artifact is both an early-cutoff input and an
  SBOM source.
- **Policy hooks for safety-critical builds** — the ability to _require_
  building from source (no binary substitution) for designated units, mirroring
  the caution already noted in
  [build-dependencies-and-caching.md](build-dependencies-and-caching.md) about
  pulling binaries from global ecosystems.

### The global cache vision

The endgame is a **public yoe binary cache** the way `cache.nixos.org` serves
Nix: a chip vendor or the yoe project builds the common BSP + base packages
once, signs them, and every downstream small team pulls them. Tiering:

- **Project cache** — per-repo, the default local + S3 object store today.
- **Company cache** — a shared MinIO/S3 bucket across a team's CI and
  workstations (Level 2/3 today).
- **Global cache** — a public, signed, read-mostly cache of common artifacts.
  This is the structural answer to the Nix crowd's "reuse beats rewriting":
  yoe's catalog can be small as long as the _cache_ is large and shared.

### Open questions

- Does early-cutoff (option 2) carry its weight before there is a large shared
  cache, or only after? (Cascade waste hurts most when rebuilds are common, i.e.
  before the global cache exists.)
- How are language-ecosystem caches (Go modules, Cargo, pip wheels) accounted
  for in provenance? They are reused today
  ([build-dependencies-and-caching.md](build-dependencies-and-caching.md)) but
  live outside the yoe object store.
- Garbage collection: lean on S3 lifecycle rules (current plan) indefinitely, or
  add reference-root-aware GC for the local store?
- Cache key stability across yoe versions — when `internal/resolve/hash.go`
  changes shape, every key moves. Do we version the key scheme to avoid mass
  invalidation on a yoe upgrade?

## References

- [build-environment.md](build-environment.md) — build containers, sandboxing,
  and multi-target builds (the caching architecture now lives here).
- [build-dependencies-and-caching.md](build-dependencies-and-caching.md) — the
  three kinds of build dependency and why caching is symmetric at the unit
  level.
- [content-addressed-cache plan](plans/content-addressed-cache.md) — the
  implementation plan (Partial: local store working, full object store and
  remote pending).
- [nix.md](nix.md) — deeper Nix comparison, including where yoe's cache would
  become vestigial under a Nix-backed package layer.
- [comparisons.md](comparisons.md) — broader tool-by-tool comparison.
