# yoe and Nix

> **Status:** This page is a forward-looking design exploration. None of the
> mechanisms it describes (`module-nixpkgs`, a `nix_feed`, a Nix build backend,
> Nix-driven image assembly) exist in the code today, and the project has not
> committed to building them. It exists to map the design space honestly so the
> trade-offs are clear before any of it is attempted. For the shipped
> head-to-head comparison of the two systems, see the
> [NixOS / Nix section of Comparisons](comparisons.md#vs-nixos--nix).

[Nix](https://nixos.org/) and yoe answer the same question — _how do you build a
reproducible system from a declarative description and cache the results so you
never rebuild what hasn't changed?_ — and they answer it with the same core
idea: **input-addressed, hermetic builds backed by a binary cache.** That shared
foundation is why "yoe vs. Nix" is the obvious framing, and it's covered in
[Comparisons](comparisons.md#vs-nixos--nix).

This page asks the less obvious question: rather than compete with Nix, could
yoe **build _with_ Nix** — letting Nix realize the package graph while yoe
supplies the orchestration, custom units, image generation, and board support
that Nix does least well? The short answer is that one integration shape is
genuinely attractive, but it asks yoe to give up things it currently considers
core, and it's worth being precise about which.

## The same niche

Both systems already implement the part of each other that matters most:

| Concern         | Nix                                          | yoe                                                         |
| --------------- | -------------------------------------------- | ----------------------------------------------------------- |
| Cache key       | hash of a derivation's inputs                | `UnitHash` — unit definition + transitive dependency hashes |
| Build isolation | the derivation sandbox                       | container worker + read-only buildroot + per-unit sysroot   |
| Binary cache    | `cache.nixos.org` / Cachix (caches closures) | S3-compatible object store (caches `.apk` / `.deb`)         |
| Output unit     | a `/nix/store/<hash>-name` path              | a `.apk` / `.deb` installed into a standard FHS root        |

The honest consequence: yoe **already adopted Nix's best idea** —
content-addressed caching of hermetic builds. So for the package layer,
"integrate with Nix" is not additive; it largely means _choosing one
content-addressed engine instead of running two_. The interesting question is
whether Nix's package breadth and binary cache are worth running yoe's
orchestration on top of, given what that costs.

## The load-bearing mismatch: `/nix/store` vs. FHS

Everything downstream hinges on one fact: **a Nix-built binary is not
relocatable into a normal filesystem.** Its ELF interpreter points at
`/nix/store/…-glibc/lib/ld-linux.so`, and its `RPATH` entries point back into
`/nix/store/…`. That is not an accident to be patched away — it is _how_ Nix
achieves hermeticity and lets multiple versions of a library coexist.

yoe's runtime thesis is the opposite: install into a shared FHS root, keep the
base in the single-digit-MB class, resolve variation at runtime rather than by
versioned paths. The two models cannot share at the binary level. Anything that
consumes Nix outputs therefore has to either:

- **ship the whole `/nix/store` closure** into the image — at which point you've
  imported NixOS's runtime model wholesale, including its closure sizes, or
- **`patchelf` every binary** back onto the FHS interpreter and library paths —
  fragile, and it throws away the hermeticity that was the reason to use Nix in
  the first place.

This single fact is what makes the three plausible integration shapes play out
so differently.

## Three ways to "build with Nix"

### Nix as a package feed

The natural instinct: yoe already consumes upstream distro binaries through
feeds (`alpine_feed(...)` for apks, `apt_feed(...)` for `.deb`s — see
[module-alpine](module-alpine.md) and [module-debian](module-debian.md)), so add
a `nix_feed` that pulls prebuilt artifacts from a Nix binary cache.

This is where the store-path mismatch bites hardest. The existing feeds work
_because_ Alpine and Debian packages install into FHS — fetch the artifact,
re-sign, extract into the destination root. Nix closures are
`/nix/store`-anchored and do not. A `nix_feed` could not be the drop-in the
other feeds are; it would import Nix's runtime model, not just its artifacts.
**Lowest payoff, highest friction** — this is the shape to avoid.

### Nix as a per-unit build backend

A unit whose build step is `nix build .#foo`, with the result extracted into the
unit's staging directory. This fits yoe's task model fine — it's just commands
in a container worker. But it runs straight into the `patchelf` problem above,
_and_ it stacks two content-addressed caches that know nothing about each other:
Nix hashes the derivation's inputs, while yoe's `UnitHash` sees only an opaque
`nix build` task and caches on the unit definition. It works; it buys little.

### yoe orchestrating Nix to produce an image

This is the shape worth taking seriously, and the one this page is really about.
Here the roles invert: **Nix realizes the package graph and provides the binary
cache; yoe owns the layer Nix does poorly** — the build DAG above the packages,
custom units, machine/BSP definitions, and disk-image assembly.

Taken to its conclusion, this means yoe stops being a distro below the image
line and becomes a **NixOS image and BSP builder** with a friendlier front-end.
That is not a criticism — it's the precise shape, and it's an appealing product:
NixOS's runtime guarantees plus yoe's embedded ergonomics.

## The orchestration model in depth

### The technical heart: classes are already nixpkgs builders

What makes this shape cheap rather than a rewrite is that yoe's class system is
nearly isomorphic to nixpkgs's builder functions. A yoe class
([build-languages](build-languages.md)) is a Starlark function that turns a
unit's declarative fields into build phases; a nixpkgs builder does the same in
the Nix language:

| yoe class   | nixpkgs builder                        |
| ----------- | -------------------------------------- |
| `autotools` | `stdenv.mkDerivation` (default phases) |
| `cmake`     | `mkDerivation` + `cmakeFlags`          |
| `python`    | `buildPythonPackage`                   |
| `nodejs`    | `buildNpmPackage`                      |
| `binary`    | `runCommand` / file-copy derivation    |

A unit's fields map almost one-to-one onto a derivation's: `source` + `tag` →
`src` (`fetchgit`), `patches` → `patches`, `configure_args` → `configureFlags`,
`deps` → `buildInputs`. So a custom unit would not need a hand-written
derivation — **the class _is_ the translator.** `autotools(name = "myapp", …)`
emits `stdenv.mkDerivation { pname = "myapp"; … }`. That delivers the entire
"custom units" value proposition for nearly free, and it is meaningfully better
ergonomics than asking an embedded engineer to learn the Nix language and
overlays.

The "feed" concept collapses to almost nothing in this world: referencing a
package by name resolves to `pkgs.<name>` against a flake-pinned nixpkgs
revision. No mirroring, no re-signing — the upstream binary cache already serves
it. The nixpkgs revision pin plays the role that a feed's release pin plays
today.

### The boundary

```
PROJECT.star            → flake inputs (the pinned nixpkgs revision = the "feed" pin)
units/*.star (custom)   → stdenv.mkDerivation, via the class layer
units (upstream ref)    → pkgs.<name>
machines/<m>.star       → kernel package + defconfig + device tree + bootloader target
images/<i>.star         → the system closure to realize + partition / fs / boot layout

  ── Nix owns ───────────────────┼── yoe owns ──────────────────────────────────
  the derivation graph + build    │  the DAG above Nix (custom + upstream, one view)
  the closure (nix path-info -r)  │  reading the closure, laying it into a rootfs
  the binary cache                │  partitioning, mkfs, bootloader install, .img
  the kernel/bootloader build     │  kernel/bootloader config + selection (machine.star)
```

The closure walk that today resolves an image's runtime dependencies
(`resolve_closure` in the image class, see [architecture](architecture.md))
would become `nix path-info -r` over the system derivation — Nix computes the
closure, yoe consumes it for assembly. yoe's image-assembly path (partition
layout, filesystem creation, bootloader install, disk image) is exactly the part
nixpkgs' own image tooling handles least gracefully for embedded targets, and it
is where yoe's differentiation would live. **Board support is the strongest
pillar of the whole idea** — cross-and-embedded support is a long-standing rough
edge in the Nix ecosystem, and yoe's per-machine model (native builds under
emulation, a clean `machine.star` config surface) is a real improvement. In this
shape the kernel and bootloader still _build_ via Nix; yoe contributes their
_configuration and selection_, which is the right division of labor.

## Consequences to weigh

This shape is attractive, but it asks for three concessions that are easy to
underprice.

### The device runs Nix — you cede the on-device runtime model

Because Nix outputs are `/nix/store`-anchored at the ELF level, building with
Nix means the device carries `/nix/store` and Nix-style activation. In practice
the running system has NixOS semantics: generations, atomic rollback,
declarative activation, systemd. That is a genuine _win_ on-device — it's what
embedded fleets want. But it means yoe's current runtime identity dissolves
above first boot: the musl small base, apk on the target
([on-device-apk](on-device-apk.md)), OpenRC services, the convention that
services follow their packages ([architecture](architecture.md)). yoe would keep
the build, image, and BSP layers and inherit NixOS for everything past boot.
Many of yoe's existing design decisions — the ones about apk, service ownership,
and resolving runtime variation — simply become moot in this mode. That's a
coherent trade, but it should be made with eyes open.

#### The size of `/nix/store` on the device

The most tangible cost of carrying `/nix/store` is footprint. A minimal NixOS
system closure lands around **1–1.5 GB uncompressed** — versus yoe's
single-digit-MB base, two to three orders of magnitude more. For context against
the other systems in [Comparisons](comparisons.md):

| Target                                   | On-device floor (no app payload)   |
| ---------------------------------------- | ---------------------------------- |
| yoe / Alpine (musl + busybox, FHS)       | ~5 MB                              |
| Debian `minbase` (glibc, no systemd)     | ~150 MB                           |
| NixOS minimal closure (glibc + systemd)  | ~1,500 MB (~400–600 MB compressed) |
| Ubuntu Core (snaps, 4× retention)        | ~2,500 MB                         |

The useful question is _where_ that comes from, because most of it is **not**
Nix-specific:

- **The dominant cost is the userland choice, not the store model.** glibc + full
  GNU coreutils/util-linux + bash + perl + **systemd** is roughly the same floor
  Debian and Avocado pay; systemd's closure alone (dbus, kmod, util-linux, pam,
  lvm2, …) is ~100–200 MB. Swap that against yoe's musl + busybox (one
  multiplexed binary, single-digit MB) and you have already explained most of the
  gap — and it is the _same_ gap [Comparisons](comparisons.md#vs-debian) draws
  against Debian, not something unique to Nix.
- **The genuinely Nix-specific surcharge is modest — tens of percent on top.**
  Three store-model properties add weight beyond the userland choice: store paths
  are atomic, so you cannot file-slice them the way Canonical's Chisel carves a
  `.deb` or Alpine splits `-doc`/`-dev` (multi-output derivations recover much of
  this, not all); multiple library versions coexist whenever the dependency graph
  is not perfectly unified; and cross-package sharing happens only through
  exact-file hardlink dedup (`nix-store --optimise`), never the natural FHS
  sharing of a single `/usr/lib/libfoo.so`.
- **Compression and trimming soften it but cannot reach Alpine territory.** A
  read-only squashfs/erofs root cuts the closure ~2–3×, and aggressive embedded
  trimming (`environment.noXlibs`, dropping perl from activation, trimming
  locales, a minimal systemd) can reach ~200–400 MB — but that is real work that
  fights the ecosystem, and glibc + systemd are structural, not tunable away.
- **One place the Nix model genuinely wins: rollback history is nearly free.**
  Each retained generation is mostly shared through hardlink dedup, so keeping N
  rollback points costs about one closure plus deltas — far cheaper than Ubuntu
  Core's 4× full-squashfs retention or a naive A/B scheme's 2× full-image copies.
  Once you have accepted the ~1 GB floor, keeping history is cheap.

The bottom line: adopting Nix on the device means accepting roughly a
Debian-with-systemd floor _plus_ a store-model surcharge, and trading away yoe's
single-digit-MB thesis entirely below the image line. For a board with tens of
GB of storage this is a non-issue; for a cost-sensitive product with 128–512 MB
of flash it is disqualifying before any application code is added — the same line
the [Ubuntu Core](comparisons.md#vs-ubuntu-core) comparison draws.

### yoe's content-addressed cache becomes vestigial for the package layer

The `UnitHash` engine and the S3 object store
([build-dependencies-and-caching](build-dependencies-and-caching.md)) are core
pieces of yoe today. In this shape, Nix's store _is_ the cache for everything
Nix builds; yoe's own cache would cover only final image artifacts, and the
binary cache story would defer to `cache.nixos.org` plus a project-local cache
for custom packages. That is a real subtraction — not an addition. Nix's cache
is excellent, so it's a reasonable thing to defer to, but it means retiring most
of yoe's caching layer rather than extending it.

### The front-end collides with "no intermediate code generation"

The natural implementation is for yoe to generate a flake or NixOS module and
shell out to `nix build`. That is precisely the pattern this project's design
principles push back on: when it breaks, the user ends up debugging
machine-generated Nix instead of the Starlark they wrote. There are two honest
ways through, and choosing between them is the decision that determines whether
this is a few months of work or a research project:

- **Accept the generation** and treat the emitted derivation as an _interface
  boundary_ (like a compiler's intermediate representation) rather than a
  user-facing artifact — but then invest in first-class "show me the generated
  derivation" tooling so the debugging story doesn't regress.
- **Link Nix's evaluation/store API directly** from Go and instantiate and
  realize derivations programmatically. This honors the principle, but Nix's
  evaluator is not a clean library and the C API is young, so realistically this
  is the much heavier path.

## What's worth borrowing regardless

Even if yoe never builds with Nix, two Nix ideas are worth taking on their own
terms — implemented natively, not via `/nix/store`:

- **Generations and atomic rollback.** Nix's most compelling property for
  embedded is atomic system generations with rollback. yoe lists atomic image
  updates with rollback as a goal but has not committed to a mechanism
  ([roadmap](roadmap.md)). The _concept_ is worth adopting; the implementation
  would be yoe-native (A/B slots, or an apk-based scheme), not the Nix store.
- **Closure as a first-class output.** Nix records a build's runtime closure
  explicitly; yoe resolves the closure at assembly time from declared runtime
  dependencies. Nix's model catches under-declared dependencies that yoe's can
  miss. A verification pass that pins down the realized closure is worth a look
  independent of any Nix integration.

## Where this could go

The most coherent version of "yoe builds with Nix" is a clear and appealing
product: **NixOS's runtime guarantees, yoe's embedded BSP and image ergonomics,
and a Starlark front-end that's friendlier than the Nix language.** The
class-to-builder isomorphism makes the build side surprisingly cheap. The price
is that yoe gives up its own distro identity below the image line and most of
its caching layer, and the front-end's implementation runs into the project's
code-generation principle.

That trade may well be worth making for a Nix-flavored target someday — and
nothing about it forecloses yoe's apk-based path, which can stand alongside it
the same way the Alpine and Debian targets stand alongside each other
([distro](distro.md)). For now this page is a map of the terrain, not a route
chosen across it. If and when the idea is taken up, the first questions to
settle are the front-end implementation strategy above and a concrete test of
the class-to-builder isomorphism against a real unit.
