<!--
Spec: Closure-pinned rootfs assembly
Date: 2026-06-02
-->

# Closure-pinned rootfs assembly: make the installer obey yoe's resolution

## Summary

yoe resolves a deterministic, content-addressed dependency closure for every
image: a specific set of units, each at a specific version, chosen by the
closure walker and the project's `prefer_modules` pins. But the final assembly
step does not hand that closure to the installer verbatim — it hands over a list
of **package names** and lets the installer pick versions from the project repo.
On the Debian path, `mmdebstrap` runs `apt`, and apt selects the **highest**
version it finds in the `[trusted=yes]` pool. When the pool holds more than one
version of a name — which the shared, append-only pool readily accumulates — apt
can install a version yoe never chose, silently overriding the resolution.

This spec proposes making the installer install **exactly** the closure yoe
resolved: pass version-qualified specs to the assembler (`util-linux=2.38.1-5+deb12u3`,
not `util-linux`) so apt installs the resolved version or fails loudly when it is
absent. The principle is that **yoe is the resolver; the installer is a dumb
applicator** — apt must not make an independent version decision the rest of the
build doesn't know about.

## Problem Frame

### Two resolvers disagree

yoe already owns dependency resolution. The closure walker
(`internal/resolve`, `internal/starlark/closure.go`) decides which unit satisfies
each name, honoring per-distro `prefer_modules` pins and the distro identity of
the consuming image. The output is a concrete list of `(name, version)` pairs.

The assembly step then discards the version half. `_install_packages_debian` in
`modules/module-core/classes/image.star` builds an `mmdebstrap --include="<names>"`
from bare names and points apt at the project pool
(`deb [trusted=yes] copy:$REPO bookworm main`). apt is a full dependency solver;
given only names, it re-resolves the install set against the pool index and, per
its default policy, prefers the highest available version of each name. That is a
**second, independent resolution** that yoe neither drives nor observes.

In the steady state where the pool holds exactly one version of each name, the
two resolutions agree and nothing looks wrong. The divergence only surfaces when
the pool holds two versions of a name.

### The pool readily holds multiple versions

The project repo pool (`repo/<project>/debian/pool/`) is **shared and
append-only**:

- It is shared across every image and every distro view in the project.
- `PublishDeb` (`internal/build/executor.go`) copies each built `.deb` into the
  pool and regenerates the index; it never prunes.
- The build-twice model and `prefer_modules` pins mean one logical name can
  legitimately have produced different versions over a project's history — e.g. a
  unit that was once consumed from `module-core` and is now pinned to a distro
  feed leaves its old `.deb` behind.

So a stale or superseded `.deb` lingers in the pool indefinitely, and apt sees it
in the index alongside the version yoe actually resolved.

### Worked incident: getopt-less util-linux

The concrete failure that motivated this spec:

- The project pins `util-linux → debian.main` for Debian images. The pin works;
  the closure resolves `util-linux` to Debian's `2.38.1-5+deb12u3`, whose package
  ships `/usr/bin/getopt`.
- A pre-pin build had published `module-core`'s source-built `util-linux 2.41.3`
  into the same pool. That build uses `--disable-all-programs` with a curated
  enable list and does **not** build `getopt`.
- Both versions appeared in the generated `Packages` index. mmdebstrap passed
  apt the bare name `util-linux`; apt picked the higher `2.41.3`.
- The rootfs ended up without `getopt`. Boot-time `initramfs-tools` postinst ran
  `update-initramfs`, which calls `getopt`, failed with `getopt: not found`, and
  aborted the whole assembly with a `dpkg` postinst error.

The closure was correct. The pin was correct. The rootfs was still wrong, because
apt was allowed to choose a version yoe had not. The incident was resolved by
manually deleting the stale `.deb`, but nothing prevents the next stale artifact
from doing the same thing — and the failure mode is a silent version swap that
only manifests as a confusing downstream symptom (a missing binary in a
postinst), far from its cause.

### Why this is the dangerous kind of bug

- **Silent.** No error at resolution time; the wrong version installs cleanly.
  The symptom appears later, in an unrelated package's maintainer script, as a
  missing file or behavioral difference.
- **Non-local.** The cause (a stale `.deb` from an old build) and the symptom
  (an initramfs tool not found) are separated by the entire pool and the whole
  install transaction.
- **Latent and general.** It is not specific to util-linux or to getopt. Any name
  that ever had two versions in the pool is a landmine, and the same shape exists
  on the Alpine path (`apk add` of bare names also prefers the highest repo
  version).

## Proposed Approach

Make the installer install the resolved closure exactly, by version.

### Version-qualified install set

The assembly step receives the closure as `(name, version)` pairs, not bare
names, and emits version-qualified specs to the installer:

- Debian: `mmdebstrap --include="util-linux=2.38.1-5+deb12u3,libc6=2.36-9+deb12u14,…"`.
  apt installs precisely those versions, or fails if the exact version is not in
  the pool.
- The version string is the package's full Debian version **including epoch**
  (e.g. `2:1.02.185-2`), taken from the same metadata yoe used to resolve the
  unit — not a reconstructed or upstream-only version.

When apt cannot satisfy a pinned version, that is a **loud, early, correct**
failure: it means the pool does not contain what yoe resolved, which is a real
problem worth surfacing rather than papering over with a higher version.

### The installer stops being a resolver

With every top-level package version-pinned, apt's role collapses to: fetch these
exact `.deb`s and their hard dependencies and configure them. It no longer makes
version choices yoe didn't make. Transitive hard-dependency resolution still runs
(apt pulls a pinned package's `Depends`), but those dependencies are themselves
closure members yoe resolved, so they too should be pinned — i.e. the install set
is the **full** closure, version-pinned, leaving apt no degrees of freedom over
which version of anything lands.

## Design Considerations

- **Where versions come from.** The closure already carries each unit's version
  (`unit.Version`). For passthrough/feed units the version must be the exact
  `Version:` field from the feed/pool index (epoch included), so it matches what
  apt sees in the index. This must be verified end-to-end: a mismatch (e.g.
  dropping an epoch) would make every install fail. Source-built units use their
  unit version, which is what `packageDeb` already writes into the `.deb`.

- **Epoch and version fidelity.** Debian versions can carry epochs (`N:`) and
  complex revisions. The spec requires byte-exact agreement between the version
  yoe pins on the command line and the version in the generated `Packages` index.
  The safest source of truth is the index/`.deb` control itself, read back at
  assembly time, rather than any upstream or unit-declared string that might have
  been normalized.

- **Virtual packages and `Provides`.** A version-qualified spec must name a real
  package, not a virtual one. yoe closure members are concrete units with
  concrete versions, so this should hold by construction; the design must confirm
  no closure entry is a bare virtual name (these would need to resolve to their
  concrete provider before pinning).

- **Interaction with the versioned-provides work.** Source-built units now emit
  versioned self-provides (`Provides: libssl3 (= 3.4.1)`) so apt accepts them for
  versioned dependencies. Pinning the install set by version is complementary:
  provides-versioning lets the resolved openssl satisfy others' `libssl3 (>= …)`
  deps; install-pinning ensures the resolved openssl itself is the one that
  lands. Both push in the same direction — apt obeys yoe.

- **Pool hygiene is related but separate.** Version-pinning fixes *correctness*
  (the right version installs) without requiring the pool to be clean. Stale
  `.deb`s still accumulate as disk cruft and still bloat the index. A pool-prune
  or single-version-per-view policy is a worthwhile follow-up but is **not**
  required for correctness once the install set is pinned, and should not be
  conflated with this change.

- **Alpine parity.** The same principle applies to `apk add` on the Alpine path,
  which also prefers the highest repo version of a bare name. The Alpine pool has
  not yet bitten (its pins currently yield single versions), but the latent gap
  is identical. The design should decide whether to pin the Alpine install set in
  the same change or land Debian first and follow with Alpine, keeping the
  "installer obeys the resolver" principle uniform across distros.

## Alternatives Considered

- **Prune the pool to the closure before assembly.** Build a per-image filtered
  repo view (symlink/copy only the resolved `.deb`s) and point mmdebstrap at it,
  so apt physically cannot see other versions. Robust and also defends against
  bare-virtual surprises, but adds per-image repo-staging machinery. A stronger
  long-term option; heavier than version-pinning. Could layer on top later.

- **apt pin-priority / preferences.** Generate an `apt_preferences` file pinning
  the feed or specific versions. Expressive but fragile and indirect; encodes the
  resolution in a second policy language apt interprets, which is exactly the
  coupling this spec wants to remove.

- **Single-version pool / prune on publish.** Keep only one version per name in
  the pool. Wrong for a pool shared across distros and images, where a name can
  legitimately hold different versions for different consumers.

- **Just keep deleting stale artifacts by hand.** What we did for the incident.
  Not a fix — it leaves the silent-divergence mechanism in place for the next
  stale `.deb`.

## Scope

In scope:

- Thread resolved versions into the Debian assembly step and emit
  version-qualified `--include` specs.
- Verify epoch-exact agreement between pinned versions and the generated index.
- Decide and document the Alpine parity path.

Out of scope (candidate follow-ups):

- Pool pruning / per-image filtered repo views.
- General pool-hygiene / garbage-collection of superseded `.deb`s.
- Any change to how the closure itself is resolved — this spec assumes the
  closure is correct and only ensures the installer honors it.

## Open Questions

- Is the exact, epoch-bearing version string reliably available at the assembly
  boundary for every closure member (feed passthrough and source-built alike), or
  does the assembly step need to read it back from the index/`.deb` control?
- Should the full transitive closure be pinned, or only the top-level `--include`
  set, relying on apt for hard deps? (Leaning: pin the full set so apt has no
  version freedom anywhere.)
- Land Alpine parity in the same change or immediately after?
- Does any current closure entry resolve to a bare virtual/`Provides` name that
  cannot be version-pinned directly?

## Acceptance Criteria

- A Debian image whose pool contains two versions of a name installs the version
  yoe resolved, not the highest in the pool.
- If the resolved version is absent from the pool, assembly fails loudly at
  install time with a clear message, rather than silently installing a different
  version.
- The util-linux/getopt incident cannot recur from a stale pool artifact: with
  the core `2.41.3` `.deb` present in the pool, a Debian image pinned to Debian's
  `2.38.1` still installs `2.38.1` and retains `getopt`.
- The principle holds uniformly: the installer never selects a package version
  the closure did not resolve (Alpine path included, per the parity decision).
