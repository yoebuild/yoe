# Debian/Ubuntu-based images (planned)

> **Status:** Not implemented. Yoe today builds only the musl/OpenRC/Alpine
> configuration described in
> [libc, init, and the Rootfs Base](libc-and-init.md#what-yoe-ships-today).
> There is no Debian/Ubuntu base, no glibc toolchain container, no `.deb`
> package writer, and no signed apt-feed generation. This document records the
> options and their trade-offs — converting upstream `.deb`s to apk vs. native
> deb end to end — so the decision is informed when it is made. No option is
> mandated here; the format choice stays open and pragmatic. No Starlark
> builtin, project field, or class described here exists in the current
> implementation.

## Scope

[libc, init, and the Rootfs Base](libc-and-init.md) makes the strategic case for
why yoe should serve glibc/systemd products (edge AI on Jetson, SoC-vendor
blobs, commercial runtimes) and recommends the rootfs-base abstraction
(Strategic Option C). This document goes one level down: **how, concretely, do
we produce a Debian/Ubuntu-based image**, and **how to reuse upstream `.deb`
binaries**. It works through both options — converting upstream `.deb`s to apk,
and serving native deb end to end — with their trade-offs, so the choice is
informed. apk-everywhere is treated as a default, not a hard requirement; deb is
used where it makes sense. The choice can ultimately be per-project.

The driving profile for this base is the proprietary-binary case: NVIDIA L4T,
Ubuntu-validated vendor SDKs, commercial industrial runtimes. For that profile
**image size is explicitly not a primary concern** — these products already
carry hundreds of MB of CUDA/TensorRT payload, and the host hardware has the
storage for it. That single relaxation removes most of the pressure that shapes
the Alpine path and widens the set of reasonable options here.

## The inherited default, and why this base relaxes it

[libc-and-init.md](libc-and-init.md#package-format-follows-the-base-planned)
historically asserted a strong invariant: **the on-target package manager is
apk, always — regardless of base.** The reasoning was that a single format gives
one dev loop, one feed, one trust root, one OTA story across every base.

This document argues that apk-everywhere should be a **default, not a hard
requirement**, and that the Debian/glibc base is where relaxing it is most
defensible. The reason is libc:

> A project picks exactly one base. The Alpine base is a musl world (apk +
> apk-db); the Debian base is a glibc world (deb + dpkg-db). **They are never
> mixed in one image**, and the musl/glibc ABI split means effectively no
> package crosses between them. The two worlds are libc-firewalled.

Given that firewall, the headline benefit of apk-everywhere — one on-device
tooling story shared across bases — has little to act on: no image is ever both
musl and glibc, and no package meaningfully migrates between them. So forcing
glibc debs through a conversion layer is real work whose main payoff the
firewall already neutralizes. That makes native deb a legitimate, and on this
base probably preferable, option — but it is a project-level choice, not a
mandate in either direction. Both paths are documented below with their
trade-offs:

- **Convert upstream `.deb` → apk** (and emit yoe's units as apk): keeps the
  Alpine-identical on-device tooling and reuses the `alpine_pkg`-shaped
  machinery, at the cost of a conversion pipeline and the dpkg-userland concerns
  enumerated below.
- **Native deb end to end**: yoe builds its units as `.deb` and serves a signed
  apt feed; upstream `.deb`s mirror in verbatim and run on native dpkg. No
  conversion, no dpkg-userland emulation, at the cost of a second feed/signing
  shape and dpkg/apt on the target.

> **Reconcile upstream:** the absolute "apk regardless of base" wording in
> [libc-and-init.md](libc-and-init.md#package-format-follows-the-base-planned)
> has been softened to "format follows the base; apk-everywhere is a default,
> not a requirement," consistent with this document. Neither doc mandates a
> single answer.

## Option 1: convert upstream `.deb` → apk

Re-wrap each upstream `.deb` as a project-signed apk and emit yoe's own units as
apk too, so the target runs apk-tools with one package database — the
Alpine-identical model. Mechanically straightforward: `ar x` the `.deb`, take
`data.tar.{gz,xz,zst}` verbatim, translate `control` metadata (`Depends:` →
`D:`, `Provides:` → `p:`, `Replaces:` → `r:`, version constraints), repack, sign
with the project key.

Its appeal is real where a project genuinely wants identical on-device tooling
and a single installer across an Alpine fleet and a Debian fleet, or wants to
reuse the existing apk feed/signing path unchanged. The firewall argument above
says that appeal is thinner than it first looks, but "thinner" is not "never" —
keep it on the table.

The cost is concrete, and it is mostly the **residual dpkg-userland concerns**:
Debian packages ship maintainer scripts that call dpkg-specific userland tools
which do not exist on an apk target. These are the documented risks of the
conversion path; each has a bounded mitigation:

1. **`update-alternatives`.** Many Ubuntu packages register `/usr/bin/python` →
   `python3.10`, `/usr/bin/editor` → `vim.basic`, etc. Three strategies, in
   order of preference:
   - **Bake at conversion time.** Resolve alternatives statically during deb→apk
     repackaging — pick the priority-winning symlink, embed it as a real symlink
     in the apk's data tree. Stateless, deterministic, works for the common case
     where embedded products don't switch alternatives at runtime.
   - **Ship a tiny `update-alternatives` stub.** A few hundred lines of shell
     mimicking the file format and CLI surface. Required if any package is
     installed/upgraded post-deploy via `apk add` and its postinst calls
     `update-alternatives`.
   - **Translate calls during script conversion.** Rewrite postinst
     `update-alternatives --install ...` to direct `ln -sf` during conversion.
2. **`dpkg-divert`.** Relocates a file shipped by package A so package B can
   place its own version. Rare in practice; effectively absent from the L4T set.
   Defer until a package actually needs it.
3. **Triggers.** Debian's file-trigger mechanism (`/etc/ld.so.conf.d/` triggers
   `ldconfig`, `/usr/share/man/` triggers `mandb`, etc.). apk has no equivalent.
   Run `ldconfig` once at end-of-rootfs-assembly; skip
   mandb/desktop-database/icon-cache for embedded images, or run them as a
   post-image step. None affect runtime behaviour.
4. **`debconf` interactive prompts.** Conversion must pre-answer them. NVIDIA's
   debs are mostly non-interactive; the few that aren't get a per-package
   preseed declared in the unit.
5. **`/var/lib/dpkg/` probes.** Some scripts test for the dpkg database. If it
   matters for a specific package, ship a stub dpkg database (an empty tree with
   a `status` file marking everything "installed"). Tiny, one-time work in the
   rootfs base.
6. **License redistribution.** CUDA/cuDNN/TensorRT/DeepStream EULAs allow
   inclusion in shipped product images but generally not public mirroring.
   Converted apks are fine in a customer's private product feed; they must not
   be hosted on a public mirror. `alpine_pkg` has this concern in principle, but
   Alpine is FOSS-dominant; NVIDIA's stack is where it actually bites.
7. **APT mirror semantics.** Apt's repo format (signed `Release`, `Packages.gz`,
   version constraints with epochs and tildes) is more complex than Alpine's
   flat `APKINDEX`. The conversion must read it correctly; several mature Go
   libraries handle this — not novel work.

The kernel-module problem (NVIDIA out-of-tree drivers built against L4T's kernel
ABI) is orthogonal to package format — a Jetson-target problem, tracked
separately.

## Option 2: native deb feeds end-to-end

yoe's from-source units are built glibc and packaged as `.deb`, the project feed
is a signed apt repository, image assembly and any on-device installs use
`apt`/dpkg. Upstream proprietary `.deb`s (CUDA, TensorRT, vendor SDKs) are
consumed verbatim — no conversion, no re-wrap, no re-sign of the payload; they
are mirrored into the project's apt feed and the device trusts the project's apt
signing key. The dpkg-userland concerns above **do not arise**, because
maintainer scripts run on the native dpkg the base already ships — this is the
main argument in this option's favor.

Rough effort to build this:

| Component                          | Change                                                                                           | Effort                |
| ---------------------------------- | ------------------------------------------------------------------------------------------------ | --------------------- |
| Package writer                     | yoe build emits `.deb` (ar + control.tar + data.tar); unit metadata → `Depends/Provides/…`       | M                     |
| Feed/repo generation               | apt repo (`pool/`, `dists/<suite>/Release` + `Packages`, clearsigned `InRelease`); gpg signing   | **L** (the real cost) |
| Content-addressed cache            | Hash model is format-agnostic; feed/index layer needs a parallel deb path                        | M                     |
| Image assembly                     | `apt`/`dpkg --root` instead of `apk add --root` — conceptually simpler, no conversion            | S–M                   |
| On-device installer                | dpkg/apt natively; apt is never the in-place OTA mechanism (atomic image + rollback stays)       | S                     |
| Dev loop / deploy / signing / docs | Currently apk-shaped; the Debian base needs a parallel deb-shaped path (per-base, not per-image) | M                     |

The single large item is **signed apt-repo generation** — `dists/<suite>/`
indices plus a clearsigned `InRelease`/`Release.gpg`, heavier than apk's flat
`APKINDEX`. Everything else is bounded: the deb container format is simple, the
content-addressed cache is format-agnostic at the hash layer, and image assembly
is conceptually _simpler_ than the conversion path because there is no
repackaging in either direction. If a project goes this way, the image is
deb-only — no mixed apk/dpkg databases — the same way an Alpine-base image is
apk-only.

On balance, native deb is the **likely better fit for this base** (no
conversion, no dpkg-userland concerns, full vendor compatibility), and Option 1
is the alternative when Alpine-identical on-device tooling or feed-path reuse
specifically matters. The decision is left open and can be made per project; the
sections below apply to whichever option is chosen.

## Two ways to obtain the base rootfs

### Option B — vendor/distro tarball wholesale (recommended default)

The project's `base()` declaration points at a vendor-supplied rootfs tarball:

- NVIDIA's official L4T sample rootfs for Jetson,
- `ubuntu-base-<version>-base-<arch>.tar.gz` for generic Ubuntu,
- a Debian `debootstrap`-produced tarball for generic Debian.

Yoe extracts the tarball as the starting rootfs, then overlays yoe-built `.deb`s
on top with `apt`/`dpkg --root` against the project's apt feed. dpkg's database
is the rootfs's one and only package database.

Because **size is not a concern for this base**, there is no reason to trim the
vendor userland, no minbase-style stripping, and no fight with the
glibc/dpkg/perl footprint floor. Take the base the vendor tests and supports,
intact. This also sidesteps the glibc-version-floor problem entirely: the base's
glibc is whatever the vendor validated their proprietary binaries against, so
the binaries run unchanged.

**Trade-off:** the tarball is a black box — less reproducible, provenance is
"trust the vendor." For Jetson and Ubuntu-validated vendor stacks that is
exactly the support contract the customer wants, so it is the right default
here.

### Option A — from-built essentials (purist)

Every package, including the essentials (`libc6`, `libstdc++6`, `bash`,
`systemd`, `dbus`), is built from source by yoe and packaged as `.deb` in the
project's apt feed; the starting rootfs is empty and yoe owns the entire chain.
Total reproducibility, substantially more setup, and — with size off the table —
its main advantage over Option B (a smaller, audited base) mostly evaporates.

Reserve Option A for the customer who must audit every byte or where no vendor
tarball exists. It is not the default for the Debian/Ubuntu base.

### Format consequence per option

Obtaining the rootfs is independent of the format choice, but the two options
diverge on what happens to upstream packages:

- **Option 1 (convert).** Upstream `.deb`s are repackaged as project-signed
  apks; yoe's own units are apk too; the target runs apk-tools. Carries the
  [residual dpkg-userland concerns](#option-1-convert-upstream-deb--apk).
- **Option 2 (native deb).** No conversion anywhere. yoe's units are `.deb`;
  upstream proprietary packages (CUDA, cuDNN, TensorRT, DeepStream, Argus,
  vendor SDKs) are **mirrored verbatim** into the project's apt feed — payload,
  metadata, and maintainer scripts intact, run by native dpkg at install time.

## Reusing upstream binary packages

The point of this base is to **not rebuild** the proprietary stack. How clean
reuse is depends on the option:

**The binary payload (both options).** Once libc + init + filesystem conventions
match what the package was built for — exactly what choosing the matching base
guarantees — the ELF binaries, shared libraries, and data inside a `.deb` run
unchanged. A glibc binary on a glibc base with Debian multiarch paths is in its
native habitat.

**The maintainer scripts.** Under **Option 2**, preinst/postinst/prerm/postrm,
`update-alternatives`, `debconf`, triggers, and `dpkg-divert` all run on the
native dpkg/apt the base already ships — nothing to emulate; this is the single
biggest simplification of going native deb. Under **Option 1**, these are the
[residual dpkg-userland concerns](#option-1-convert-upstream-deb--apk) and each
needs its mitigation; that list is the cost ledger for choosing conversion.

**Integration into the project feed.** Either way, upstream `.deb`s are pulled
into the project's feed so the device trusts one feed and one key (see
[Feed and signing](#feed-and-signing)). Reading the upstream apt repo
(`Release`/`Packages.gz`, upstream-signed) and re-publishing selected packages
is well-trodden ground; several mature Go libraries parse apt repository
metadata. The mirror is content-addressed like any other unit input — the
upstream `.deb`'s hash drives the cache key.

## Feed and signing

Under **Option 1** the project feed stays the existing apk feed with apk's
per-package signing — unchanged from the Alpine path.

Under **Option 2** the project serves a **signed apt repository**: yoe's
from-source `.deb`s and the mirrored upstream `.deb`s side by side, the
`Release` file clearsigned (`InRelease`) / detached-signed (`Release.gpg`) with
the **project key** (the one key the rootfs trusts, installed into the apt
keyring at assembly). Upstream signing keys (NVIDIA's apt key, Ubuntu's keyring)
are used only at mirror time to verify what yoe pulls in; they never reach the
target.

Note the trust-model difference Option 2 introduces: apk signs **each package**;
apt signs **the repository index**, and per-package integrity follows
transitively from the signed `Packages` hashes. Equivalent end assurance,
different mechanism — the implementation must get the apt model right rather
than reuse apk's per-artifact signing.

**License note:** CUDA/cuDNN/TensorRT/DeepStream EULAs typically permit
inclusion in a shipped product image but **not** public mirroring. The mirrored
packages in the project feed are fine in a customer's private product feed; they
must not be hosted on a public mirror.

## systemd integration (the under-scoped piece)

glibc and the package-format question are well understood; **systemd-as-PID1 is
the larger unknown** and is currently only sketched in libc-and-init.md (applies
regardless of which format option is chosen). With the tarball base the base
tarball already ships a working systemd, so the spike does not have to _build_
systemd — but yoe's image assembly still has to integrate with it where it
currently assumes OpenRC:

- The CLAUDE.md rule "installed packages run their services" maps to systemd's
  `[Install] WantedBy=` / preset / `systemctl enable` semantics instead of
  OpenRC runlevel symlinks. The rootfs-scan that wires services at assembly time
  needs a systemd code path (create `.wants` symlinks per `[Install]`, honor
  presets, support an explicit per-image mask as the opt-out).
- `network-config` and similar yoe-defining units need a systemd-flavored
  variant (handled cleanly by the existing override/`provides` model).
- Merged-`/usr` and cgroup v2 are assumed by modern systemd; the chosen base
  tarball satisfies both, so this is a "verify, don't build" item for the spike.

This deserves its own roadmap phase rather than being folded into "first Jetson
prototype."

## Recommended path

Format choice is left open and can be made per project. Native deb (Option 2) is
the **likely better fit** for this base — no conversion, no dpkg-userland
concerns, full vendor compatibility — and Option 1 is the alternative when
Alpine-identical on-device tooling or apk feed-path reuse specifically matters.
The steps below are written for the likely path; the format-independent steps
(1, 4, 5) apply either way.

1. **Default to the vendor/distro tarball** (Option B above) — wholesale, no
   trimming. Size is not the constraint here; vendor support and binary
   compatibility are. Format-independent.
2. **If native deb:** yoe builds its from-source units as `.deb` and publishes
   them, alongside mirrored upstream `.deb`s, in a single project-signed apt
   repository. **If convert:** reuse the existing apk feed and add the deb→apk
   conversion with its
   [dpkg-userland mitigations](#option-1-convert-upstream-deb--apk).
3. **On-device:** native dpkg/apt (Option 2) or apk-tools (Option 1). Either
   way, atomic whole-image A/B update with rollback remains the OTA model; the
   package manager is the assembly-time and occasional manual-install resolver,
   never the in-place OTA mechanism.
4. **Treat systemd image-assembly integration as a first-class phase**, not a
   side effect of the Jetson prototype — it is the real unknown, and it is
   format-independent.
5. **Prove it with one throwaway Jetson spike** (single Orin Nano SKU, L4T
   sample rootfs as-is) before generalizing the rootfs-base abstraction.
   Discover what breaks; do not aim to ship.

If native deb is chosen, the one substantial build item is signed apt-repo
generation (step 2); the rest is bounded plumbing. If convert is chosen, the
substantial items are the conversion pipeline and the dpkg-userland mitigations.

## Open questions

- **Which option per project, and is one a sane global default?** This document
  leans Option 2 for this base but does not mandate it. What concretely tips a
  given project to Option 1 (existing Alpine fleet sharing tooling? feed-server
  reuse pressure?), and is the decision better made once or left per-project?
- _(Option 2)_ Signed apt-repo generation: which Go library produces a correct
  `dists/<suite>/` tree (`Release`/`Packages[.gz,.xz]`, clearsigned `InRelease`
  - detached `Release.gpg`), and what is the conformance test set against real
    `apt-get update`?
- _(Option 2)_ Apt metadata edge cases the yoe `.deb` writer must emit
  correctly: epochs, `~` pre-release ordering, `Multi-Arch:`,
  `Provides`/`Replaces`/`Breaks`.
- _(Option 1)_ Which of the three `update-alternatives` strategies is the
  default, and where is the line for shipping the shell stub?
- Provenance/SBOM story for the tarball base: how do we record what the
  black-box tarball contained for customers who need a bill of materials even if
  they accept the vendor base? Format-independent.
- Per-base feed shape: Option 2 introduces an apt feed alongside the Alpine apk
  feed. The feed server, `yoe deploy`, and signing docs would need a per-base
  path. How much of the existing apk feed code generalizes vs. needs a parallel
  deb implementation?
- Kernel modules (NVIDIA out-of-tree drivers against L4T's kernel ABI) are
  orthogonal to packaging but block a bootable Jetson image — tracked
  separately, noted here so the spike does not assume it is free.

## See also

- [libc, init, and the Rootfs Base](libc-and-init.md) — the strategic case and
  the rootfs-base abstraction this fits into. Its package-format wording has
  been softened to match this document: format follows the base, apk-everywhere
  is a default rather than a hard requirement.
- [module-alpine](module-alpine.md) and
  [Alpine apk Passthrough](apk-passthrough.md) — the `alpine_pkg` precedent;
  Option 1 here is its deb-side counterpart.
- [Comparisons → vs. Debian](comparisons.md) — yoe's relationship to dpkg/apt
  and Debian-derived bases.
