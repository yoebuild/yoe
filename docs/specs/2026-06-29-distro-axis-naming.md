<!--
Spec: Naming the build-variation axis (distro -> environment)
Date: 2026-06-29
-->

# Naming the build-variation axis: is "distro" the right word

## Summary

yoe keys every built artifact on `(distro, arch, scope)`. The `distro` axis
selects the libc/ABI, toolchain, and packaging world a unit builds against and
within — today that means Alpine (musl/apk) or Debian/Ubuntu (glibc/dpkg). The
word "distro" is accurate for those three, but yoe's stated direction
(heterogeneous build) reaches past Linux distributions: an RTOS such as Zephyr,
DSP firmware, and bare-metal microcontroller targets all need the same axis, and
none of them is a distribution. Calling the axis "distro" narrows a general
concept to one of its species.

This spec does not commit to a rename. It frames what the axis actually is,
surveys the cost of touching it, lays out the naming options with their
trade-offs, and recommends a direction and a sequencing relative to the
in-flight [distro as unit identity](2026-05-29-distro-as-unit-identity.md) work.
The decision to rename (and to which word) is left open for review.

## Problem frame

### The axis is broader than its current name

"Distro" answers "which Linux distribution's userspace am I building." The axis
it occupies answers a larger question: **for what build world is this artifact
produced** — which libc (or none), which toolchain, which packaging convention
(or none), running on which OS (or no OS at all). Linux distributions are the
members of that set yoe builds today; they are not the whole set the design
anticipates.

The companion blog post (`docs` is not its home; it lives under
`documentation/blog/yoe-build/`) opens on exactly this breadth: a modern system
carries a main CPU, GPU, DSP, and a scattering of embedded controllers, and
"pretty soon about everything will have an embedded controller that needs
software built, versioned, deployed, and managed." Each of those processing
units maps to a build world:

| Processing unit           | Build world (the axis value)        | Is it a "distro" |
| ------------------------- | ----------------------------------- | ---------------- |
| Application CPU (Linux)   | Alpine/musl, Debian/glibc           | yes              |
| MCU / embedded controller | Zephyr, bare-metal (`*-none-eabi`)  | no               |
| DSP                       | vendor DSP firmware toolchain       | no               |
| GPU (compute)             | (Mojo/MLIR territory; out of scope) | no               |

A name that already excludes three of four rows is the wrong name for the slot.

### Genus and species: the key distinction

The cleanest way to see the problem is that **"distro" is a species and the axis
is the genus.** Alpine is both a distro and a build environment. Zephyr is a
build environment but not a distro. Renaming the _axis_ does not claim Alpine
"isn't a distro" — it is one, and prose, module names (`module-alpine`), and the
`alpine_feed`/`apt_feed` builtins can keep saying "distro" where the noun
genuinely means a Linux distribution. What changes is the name of the _generic
slot_: the typed value, the field, the cache-key component, the directory level.

This distinction also bounds the rename. Only the genus-level uses move; the
species-level uses (a real Debian, a real Alpine feed) stay.

### What the axis carries — and why it is not just libc

It is tempting to call the axis "libc" (musl vs glibc). That under-describes it.
The axis bundles:

- **libc / ABI** — musl vs glibc; for an MCU, picolibc/newlib or none.
- **Toolchain** — the compiler and sysroot the build runs against.
- **Packaging convention** — apk vs dpkg; for Zephyr/bare-metal, _no_ package
  manager at all (a firmware image, not a package set).
- **OS target** — Linux, an RTOS, or no OS.

The packaging dimension is what makes "environment" an imperfect-but-best fit
(see Options): the GNU/LLVM triple's `environment` field captures the libc/ABI
part precisely, but yoe's axis also carries the packaging ecosystem, which the
triple does not model. Zephyr and bare metal have a toolchain and ABI but no
packaging world, which is itself a signal that the axis is "build world," not
"distribution."

### Cost of the name being load-bearing

"Distro" is not a label on a few structs; it is threaded through the system as a
first-class axis. Surveying the tree:

- **Go:** ~1226 references across 56 non-test files. The typed surface includes
  `Distro` (122), `EffectiveDistro` (48), `DefaultDistro` (41),
  `DefaultDistroOverride` (40), `EffectiveDistroForImage` (21), `DistroViews`
  (18), `DistroUnit` (17), `DistroPackages` (13), `DistroRuntimeDeps` (6),
  `DistroDeps` (5).
- **Starlark (user-facing surface):** ~195 references. The kwarg family is
  `distro`, `distros`, `distro_deps`, `distro_runtime_deps`, `distro_artifacts`,
  `distro_packages`, `distro_unit`, `distro_override`, `distro_bootcmd`,
  `default_distro`, `default_distro_override`.
- **On-disk layout:** `build/<distro>/<name>.<scope>/` and
  `repo/<project>/<distro>/<arch>/`. Renaming the directory level is a
  cache-path change (every existing build tree relocates), distinct from
  renaming symbols.
- **Docs:** ~58 markdown files reference "distro."

This scale is the core tension: the name is wrong for the vision but deeply
correct-feeling for what ships today, and the change is broad. That argues for
deciding the _word_ now (cheap) and timing the _rename_ to ride an existing
mechanical migration rather than standing up its own (see Sequencing).

## Options for the name

Each option is judged on: does it cover the non-distro members (Zephyr, DSP,
bare metal); is it precise (does it name the real concept, not a vibe); does it
collide with existing yoe or industry vocabulary; and how does it read in the
kwarg surface (`*_deps`, `*_artifacts`).

### Option A — `environment`

The libc/ABI field of a GNU/LLVM target triple (`arch-vendor-os-environment`) is
_literally_ called the environment, and `gnu` vs `musl` is that field
(`x86_64-linux-gnu` vs `x86_64-alpine-linux-musl`). LLVM's `Triple` class names
it `EnvironmentType`. It extends cleanly to the targets we care about:
`arm-none-eabi` (bare metal), and the DSP/RTOS worlds slot in as further
environments. Because yoe is already riffing on Lattner's
heterogeneous-compilation framing, this term is _on-theme_, not a retreat to a
vaguer word.

- **Covers non-distros:** yes — this is its main advantage.
- **Precise:** yes for libc/ABI; partially for packaging (the triple's
  environment field does not model apk/dpkg, but nothing better does either).
- **Collisions:** "environment" can be misread as shell environment / env vars.
  In a build-system context this is usually clear, but it is the main cost.
- **Kwarg surface:** `env_deps`, `env_artifacts`, `env_packages`, `default_env`
  / `default_environment`. Reads cleanly; `env_` is a slightly unfortunate
  prefix because of `$ENV` associations, though `environment_` is available if
  verbosity is acceptable.

### Option B — `target`

The compiler-world word for "the thing you are building for." Thematically
closest to LLVM/Lattner, and unambiguous to a toolchain audience.

- **Covers non-distros:** yes.
- **Precise:** _too_ broad and overloaded. In yoe the key is already
  `(distro, arch, scope)` — `arch` and the machine are _also_ part of "the
  target." Naming one component "target" invites confusion with the whole target
  (arch + machine + environment). A full triple folds arch and environment
  together under "target"; yoe deliberately splits them, so reusing "target" for
  the split-out environment field fights the existing model.
- **Collisions:** high — "build target," "target machine," "target arch" all
  already mean things.
- **Kwarg surface:** `target_deps`, `target_artifacts` read fine but inherit the
  ambiguity.

### Option C — `platform`

Widely understood for "the thing you build for" (Bazel `platform`, Go
`GOOS/GOARCH`). It reads naturally in prose — "the Zephyr platform," "the Debian
platform" — and in the kwarg surface (`platform_deps`, `platform_artifacts`,
`default_platform`), with none of the shell-env misread that dogs `environment`.
On readability it is arguably the best of the options, and the genuine runner-up
to `environment`.

- **Covers non-distros:** yes.
- **Precise:** the real objection is _absorption_, not branding. "Platform"
  conventionally bundles arch (Bazel/Go platforms are os+cpu), and in embedded
  vernacular it often means the board/BSP. yoe splits _both_ of those out —
  `arch` and machine `scope` are their own key components — so "platform" names
  a bundle yoe has deliberately pulled arch and board out of. A reader carrying
  the usual meaning expects more in it than the axis holds.
- **Brand note:** the identity doc's "platform thinking" is a separate,
  higher-altitude concept; reusing "platform" for a build-model axis is not a
  real conflict and is **not** weighed against this option.
- **Kwarg surface:** `platform_deps`, `platform_artifacts`, `default_platform` —
  clean and intuitive.

### Option D — `world` / `buildworld`

Captures the "two-world problem" framing (musl world vs glibc world) directly,
and it is honest that the axis is a whole self-consistent universe of
libc+toolchain+packaging.

- **Covers non-distros:** yes.
- **Precise:** evocative but informal; not an established term, so it carries no
  taxonomy weight and would need defining wherever it appears.
- **Collisions:** low (though BSD "buildworld" exists and means something
  different).
- **Kwarg surface:** `world_deps`, `world_artifacts` — reads oddly.

### Option E — `toolchain`

Names the compiler+libc+sysroot bundle the build runs against.

- **Covers non-distros:** yes (Zephyr SDK, DSP toolchain, bare-metal GCC are all
  toolchains).
- **Precise:** captures the build-side correctly but _undersells the runtime
  packaging ecosystem_. Alpine vs Debian differ in far more than toolchain — apk
  vs dpkg, openrc vs systemd, the whole package base. Calling that difference a
  "toolchain" hides the ecosystem dimension that is most of why the variants
  cannot share.
- **Collisions:** moderate — yoe has real toolchains as units; "toolchain" as
  both a unit and the axis would confuse.
- **Kwarg surface:** `toolchain_deps` — misleading for the same reason.

### Option F — `profile`

The Conan/Yocto-adjacent word for "a named bundle of build settings."

- **Covers non-distros:** yes, by being maximally generic.
- **Precise:** generic to the point of vague — a profile is "whatever settings,"
  which loses the specific meaning (a coherent libc+packaging world) the axis
  actually has.
- **Collisions:** low.
- **Kwarg surface:** `profile_deps`, `profile_artifacts` — readable but
  characterless.

### Option G — keep `distro`, add `environment` as a separate broader axis

Rather than rename, model two axes: `distro` stays for the Linux-distribution
packaging world, and a new `environment` (or `target`) axis covers the
toolchain-only worlds (Zephyr, DSP, bare metal). A Linux build sets both
(`environment=linux-glibc`, `distro=debian`); a Zephyr build sets only
`environment=zephyr` with no distro.

- **Covers non-distros:** yes.
- **Precise:** arguably the most honest model — it admits that a packaged distro
  and a pack-less firmware target are genuinely different kinds of thing.
- **Cost:** highest. It adds an axis to the cache key
  (`(environment, distro, arch, scope)`), multiplies the resolution surface, and
  contradicts the in-flight effort to _collapse_ distro handling into one typed
  value. It reintroduces exactly the multi-axis complexity that
  [distro as unit identity](2026-05-29-distro-as-unit-identity.md) is trying to
  reduce.
- **Verdict:** likely over-engineering for the present, but worth recording
  because the libc-vs-distro seam is real (the existing identity spec already
  flags "is libc a separate axis from distro" as an open question). If a future
  target needs both a distro _and_ an independent libc choice (alpine-glibc,
  say), this two-axis model is where that goes.

## Discussion

**The honest tension is genus vs species, not "wrong word."** "Distro" is the
correct _species_ name and will stay correct for Alpine/Debian/Ubuntu forever.
The problem is using a species name for the genus-level slot. Any rename should
move only the genus uses (the typed value, the cache-key field, the directory
level, the generic kwargs) and leave the species uses (module names, feed
builtins, prose about an actual distribution) alone. A rename that tries to
purge the word "distro" everywhere would be wrong; Alpine _is_ a distro.

**"Environment" is the strongest single-axis rename** because it is the one
option that is simultaneously (a) broad enough for Zephyr/DSP/bare-metal, (b) a
real term from the toolchain taxonomy rather than a coined one, and (c) on-theme
with the Lattner framing the work already invokes. Its one genuine weakness —
the shell-env misread — is mitigated by context and by preferring `environment_`
over `env_` in any ambiguous spot.

**"Platform" is the close second; "target" is a distant one.** Both read
naturally, but yoe deliberately splits `arch` (and machine `scope`) out of the
key, and both words conventionally _absorb_ arch — "platform" additionally
absorbs the board in embedded usage. So each names a bundle yoe has pulled arch
(and board) out of. Between them, "platform" reads far better than "target"
(which is further overloaded with "build target" and "target machine") and is
the genuine runner-up. The deciding edge for `environment` over `platform` is
narrow and structural: in the triple, `environment` sits _beside_ arch
(`arch-vendor-os-environment`), matching yoe's arch split, whereas "platform"
conventionally contains arch. The "platform thinking" brand overlap is **not** a
factor in this comparison.

**The two-axis option (G) is the right answer to a question we are not yet
asking.** It cleanly separates packaged-distro from packless-firmware, but it
adds axis complexity precisely when a separate effort is removing it. Keep it on
the shelf, tied to the existing "is libc a separate axis" open question; do not
build it preemptively.

## Sequencing — do this with, not before, the typed-`Distro` migration

The cost of a rename is dominated by the stringly-typed sprawl, not by the
concept. [distro as unit identity](2026-05-29-distro-as-unit-identity.md)
proposes replacing bare `string` distro values with a typed `Distro` and
collapsing the catalog. **Once the axis is a single named type, renaming it is a
compiler-driven mechanical change** (`Distro` -> `Environment`,
`EffectiveDistro` -> `EffectiveEnvironment`, etc.) with the compiler enforcing
completeness.

Therefore the recommended order is:

1. **Decide the word now** (this spec) — cheap, unblocks consistent naming in
   new code and docs.
2. **Land the typed-`Distro` collapse** as already specced — do _not_ expand its
   scope with a rename mid-flight.
3. **Rename as a dedicated mechanical pass** right after, when the surface is
   one type and a bounded kwarg set, not 1226 scattered strings. Or, if the word
   is decided before the typed migration starts, fold the rename into that
   migration's mechanical `string -> type` step so the tree is touched once.

Renaming _before_ the typed collapse would mean editing 1226 stringly-typed
sites by hand and then having the collapse churn them again — the worst order.

## Scope, if a rename proceeds

In scope (genus-level):

- The typed value and its derived symbols (`Distro`, `EffectiveDistro`,
  `DefaultDistro`, `DistroViews`, `EffectiveDistroForImage`, the `Distro*` field
  names).
- The generic Starlark kwargs (`distro`, `distro_deps`, `distro_runtime_deps`,
  `distro_artifacts`, `distro_packages`, `distro_unit`, `distro_override`,
  `distro_bootcmd`, `default_distro`, `default_distro_override`).
- The cache-key component and the `build/<distro>/…`,
  `repo/<project>/<distro>/…` directory level (a cache-path change — existing
  trees relocate; acceptable pre-1.0, but call it out so no one expects cache
  continuity).
- Docs that describe the axis generically.

Out of scope (species-level — stays "distro"):

- Module names (`module-alpine`, `module-debian`, `module-ubuntu`) and the
  `alpine_feed` / `apt_feed` builtins.
- Prose and comments where the word means an actual Linux distribution.
- The packaging-ecosystem facts (apk/dpkg) — those are distro properties, not
  axis properties.

## Recommendation

- **Lead: `environment`** as the genus-level name, keeping `distro` as the
  species name for actual Linux distributions. The edge over `platform` is
  structural, not branding: in the target triple `environment` sits beside arch,
  matching yoe having pulled `arch` into its own key component. Prefer
  `environment_` over `env_` in any spot where shell-env confusion is plausible.
- **Closest alternative: `platform`**, defensible if readability is weighted
  above taxonomic precision. It reads the most naturally of the options and has
  no shell-env baggage; the only real cost is that "platform" conventionally
  includes arch (and, in embedded, the board), both of which yoe keeps as
  separate axes. The "platform thinking" brand overlap is not a reason to avoid
  it.
- **Do not implement now.** Decide the word, keep building with it in new
  surfaces, and execute the rename as a mechanical pass coupled to the
  typed-`Distro` migration (Sequencing, step 3).
- **Record the two-axis model (Option G) as deferred**, linked to the existing
  "is libc a separate axis from distro" open question; revisit only if a target
  needs an independent libc-and-distro choice.

## Open questions

- **Word choice confirmation.** `environment` is the lead recommendation;
  `platform` is the closest single-axis alternative (better readability, weaker
  arch-precision), and the two-axis model is the structural alternative. This is
  the decision the spec exists to surface.
- **Kwarg prefix.** `env_` (short, slight `$ENV` collision) vs `environment_`
  (verbose, unambiguous). Affects the most visible user-facing surface.
- **Directory-level rename.** Worth the cache-path churn
  (`build/<environment>/`), or leave the on-disk layout as `<distro>` and rename
  only symbols and kwargs. Renaming symbols without the directory keeps cache
  trees stable but leaves a visible inconsistency on disk.
- **Relationship to libc-as-separate-axis.** If the typed-`Distro` migration
  decides libc is fused into the axis (its current recommendation), does
  "environment" still read correctly when a future alpine-glibc would need the
  two-axis split. The name should not foreclose that, which Option G preserves.
