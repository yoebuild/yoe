# Debian/Ubuntu-based images (planned)

> **Status:** Not implemented. Yoe today builds only the musl/OpenRC/Alpine
> configuration described in
> [libc, init, and the Rootfs Base](libc-and-init.md#what-yoe-ships-today).
> There is no `deb_pkg` class, no Debian/Ubuntu base, and no apt-feed conversion
> code. This document records the options for getting there and assesses their
> practicality so the path is decided before code is written. No Starlark
> builtin, project field, or class described here exists in the current
> implementation.

## Scope

[libc, init, and the Rootfs Base](libc-and-init.md) makes the strategic case for
why yoe should serve glibc/systemd products (edge AI on Jetson, SoC-vendor
blobs, commercial runtimes) and recommends the rootfs-base abstraction
(Strategic Option C). This document goes one level down: **how, concretely, do
we produce a Debian/Ubuntu-based image**, and **how practical is reusing
upstream `.deb` binaries by converting them to signed apks** rather than
rebuilding from source.

The driving profile for this base is the proprietary-binary case: NVIDIA L4T,
Ubuntu-validated vendor SDKs, commercial industrial runtimes. For that profile
**image size is explicitly not a primary concern** — these products already
carry hundreds of MB of CUDA/TensorRT payload, and the host hardware has the
storage for it. That single relaxation removes most of the pressure that shapes
the Alpine path and makes the simplest option also the recommended one.

## The on-target invariant (unchanged)

Whatever option we pick, the invariant from
[libc-and-init.md](libc-and-init.md#package-format-stays-apk-regardless-of-base-planned)
holds: **the on-target package manager is apk, always.** dpkg and apt never run
on the device. The target trusts exactly one signing key — the project key. The
dev loop, content-addressed cache, signed feed, image assembly, and OTA are
identical to the Alpine path. Only `(libc, init, filesystem conventions)` differ
underneath, and those are properties of the chosen base, not of the package
format.

So "Debian-based image" never means "ship dpkg/apt." It means: a rootfs whose
libc is glibc, whose init is systemd, whose lib paths are Debian multiarch
(`/usr/lib/x86_64-linux-gnu`), assembled and updated through yoe's apk pipeline.

That said — is "apk on the target" actually load-bearing, or incidental? The
next section takes the question seriously rather than assuming the answer.

## Alternative: keep deb/dpkg on the target (no conversion)

The question worth asking before building any conversion machinery: in the
Debian/glibc/systemd space, would it be **easier to just keep `.deb` and dpkg on
the target** and skip deb→apk conversion entirely?

Be honest about what conversion actually buys, and separate it from what yoe
genuinely differentiates on:

| Concern                                          | Lives at which layer | Needs apk-on-target?             |
| ------------------------------------------------ | -------------------- | -------------------------------- |
| Build DAG, content-addressed cache, dev loop     | **Build time**       | No — independent of on-target PM |
| Image assembly (partitions, bootloader, signing) | **Image time**       | No                               |
| Atomic image update + rollback (A/B)             | **Image time**       | No                               |
| Unified _incremental on-device_ package updates  | **Runtime**          | Yes                              |
| One signed feed, one trust root on device        | **Runtime**          | Yes                              |

yoe's strongest, most differentiated guarantees — the build/DAG/cache, image
assembly, and **atomic image update with rollback** — all live _above_ the
on-target package manager. They are preserved whether the image internally
contains apks or debs. What conversion actually preserves that native dpkg would
break is the **runtime row**: a single incremental on-device update story and a
single trust root, identical across Alpine and Debian bases.

So the decision reduces to one axis — **does this product profile need
incremental on-device package management, or is whole-image atomic update
enough?**

- **Atomic-image-only profile** (typical Jetson edge-AI appliance: a large,
  effectively immutable image, updated as an A/B whole-image swap with
  rollback). Here the on-target package manager is almost irrelevant — it is
  just what is frozen into the image. Keeping `.deb`/dpkg is the **lower-effort
  and better-supported** path: zero conversion code, maintainer scripts run
  natively, `update-alternatives`/`debconf`/triggers/divert all Just Work, and
  the vendor-documented `apt install` flow is exactly what NVIDIA/Ubuntu
  support. yoe still owns build, assembly, and atomic OTA + rollback at the
  image layer. Footprint — normally the argument against carrying dpkg+apt — is
  off the table for this base by explicit decision. In this profile the answer
  to "would it be easier to switch to deb packages?" is **yes, materially**.
- **Incremental-on-device-update profile** (the product relies on shipping
  individual package deltas to running devices, not whole-image swaps). Here
  conversion earns its keep: you want one feed format, one OTA mechanism, and
  one trust root across all bases, and `apt upgrade`'s in-place mutable-rootfs
  model is exactly what yoe positions against
  ([Comparisons → vs. Debian](comparisons.md)). Re-introducing native apt here
  would fork yoe's deployment model in two.

There is also a **hybrid** worth naming: keep `.deb`/dpkg _inside_ the image for
build/assembly-time package resolution and full vendor compatibility, but make
the **only** sanctioned update path the atomic whole-image swap — dpkg is
present for inspection and occasional manual post-deploy installs, never the OTA
mechanism. This keeps yoe's load-bearing rollback guarantee while paying none of
the conversion or dpkg-userland-residue cost. For the Jetson appliance profile
this is likely the sweet spot.

Implication for the rest of this document: the deb→apk conversion design below
is the right answer **only for the incremental-on-device-update profile**. For
the atomic-image-only profile, the recommended path is "don't convert" — see
[Recommended path](#recommended-path), which now branches on this axis.

## Two ways to obtain the base rootfs

### Option B — vendor/distro tarball wholesale (recommended default)

The project's `base()` declaration points at a vendor-supplied rootfs tarball:

- NVIDIA's official L4T sample rootfs for Jetson,
- `ubuntu-base-<version>-base-<arch>.tar.gz` for generic Ubuntu,
- a Debian `debootstrap`-produced tarball for generic Debian.

Yoe extracts the tarball as the starting rootfs, then overlays yoe-installed
apks on top with `apk add --root` (apk-tools owns its own DB and ignores files
it did not place, except on genuine content collisions).

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

### Option A — from-converted essentials (purist)

Every package, including the essentials (`libc6`, `libstdc++6`, `bash`,
`systemd`, `dbus`), comes from a yoe-converted apk in the project's feed; the
starting rootfs is empty and yoe owns the entire chain. Total reproducibility,
substantially more setup, and — with size off the table — its main advantage
over Option B (a smaller, audited base) mostly evaporates.

Reserve Option A for the customer who must audit every byte, where no vendor
tarball exists, or who wants one provenance story across all bases. It is not
the default for the Debian/Ubuntu base.

### Consequence: `deb_pkg` scope shrinks

If the base is a vendor tarball (Option B), `deb_pkg` conversion is **not**
needed for the base system at all. It is needed only for:

- the **value-add packages** yoe layers on top (CUDA, cuDNN, TensorRT,
  DeepStream, Argus from NVIDIA's apt feed; vendor SDK `.deb`s), and
- anything installed **post-deploy** via on-device `apk add`.

That is a far smaller, far safer surface than "convert all of Debian." The hard
parts of deb conversion (base-system maintainer scripts, alternatives churn,
debconf) largely live in the base packages we are now choosing _not_ to convert.

## Reusing upstream binary packages

The point of this base is to **not rebuild** the proprietary stack. The
practicality question is therefore: can we take an upstream `.deb` verbatim,
re-wrap it as a yoe-signed apk, and have it run?

**The binary payload: yes, cleanly.** Once libc + init + filesystem conventions
match what the package was built for — which is exactly what choosing the
matching base guarantees — the ELF binaries, shared libraries, and data files
inside a `.deb` run unchanged. There is no recompilation and no patching of the
binaries themselves. A glibc binary on a glibc base with Debian multiarch paths
is in its native habitat.

**The metadata: mostly mechanical.** A `.deb` is
`ar(debian-binary, control.tar.*, data.tar.*)`. Conversion:

1. `ar x` the `.deb`.
2. Extract `data.tar.{gz,xz,zst}` — this is the file tree, used verbatim.
3. Read `control` for metadata. Translate the dependency fields onto apk's
   model: `Depends:` → `D:`, `Provides:` → `p:`, `Replaces:` → `r:`,
   `Conflicts:`/`Breaks:` → conflict entries. Version constraints (`>=`, epochs,
   `~` pre-release ordering) need a correct comparator.
4. Repack the file tree + translated `.PKGINFO` as an apk.
5. **Re-sign with the project key.**

Several mature Go libraries parse `ar`, Debian control files, and apt repository
indices (`Release`/`Packages.gz`, signed with the upstream key), so reading
NVIDIA's or Ubuntu's apt feed is not novel work. The conversion is
content-addressed like any other unit input — the input `.deb`'s hash drives the
output apk's cache key.

**Where it is genuinely not trivial:** maintainer scripts that call
dpkg-specific userland (`update-alternatives`, `dpkg-divert`, `debconf`,
`dpkg-trigger`) which do not exist on a yoe target. The full enumeration and
per-tool mitigation lives in
[libc-and-init.md → Residual dpkg-userland concerns](libc-and-init.md#residual-dpkg-userland-concerns)
and is not duplicated here. The practical stance for _this_ base, given Option B
and the value-add-only `deb_pkg` scope:

- The base tarball already ran its own maintainer scripts when the vendor built
  it; we inherit a configured rootfs and do not re-run them.
- For the value-add packages, `update-alternatives` is resolved **statically at
  conversion time** (pick the priority winner, embed a real symlink). Most
  CUDA-class `.deb`s are non-interactive and do little in postinst beyond
  `ldconfig` and file placement.
- `ldconfig` is run once at end-of-rootfs-assembly instead of via Debian's file
  trigger; `mandb`/desktop-database/icon-cache are skipped for embedded images.
- If a package probes `/var/lib/dpkg/status`, the Option-B tarball already
  carries a real dpkg database, so probes succeed without a stub. (Under Option
  A, ship a minimal stub `status` marking everything installed.)

## Re-signing

Upstream keys (NVIDIA's apt key, Ubuntu's keyring, Debian's archive keys) are
used **only at fetch/verify time, inside the conversion**, to authenticate the
`.deb` we are consuming. They never reach the target. The converted apk is
signed with the **project key**, the single key the rootfs trusts (dropped into
`/etc/apk/keys/` at assembly). This is identical to how `alpine_pkg` re-signs
Alpine's apks today, and is the reason a target carries one installer and one
trust root regardless of how many upstream feeds fed into it.

**License note:** CUDA/cuDNN/TensorRT/DeepStream EULAs typically permit
inclusion in a shipped product image but **not** public mirroring. Converted
apks are fine in a customer's private product feed; they must not be hosted on a
public mirror. This is a feed-distribution policy concern, not a conversion
mechanic, but it belongs in any `deb_pkg` unit's documentation.

## systemd integration (the under-scoped piece)

Conversion and glibc are well understood; **systemd-as-PID1 is the larger
unknown** and is currently only sketched in libc-and-init.md. With Option B the
base tarball already ships a working systemd, so the spike does not have to
_build_ systemd — but yoe's image assembly still has to integrate with it where
it currently assumes OpenRC:

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

First, branch on the deciding axis from
[Alternative: keep deb/dpkg on the target](#alternative-keep-debdpkg-on-the-target-no-conversion):
**does the product need incremental on-device package updates, or is whole-image
atomic update with rollback enough?**

Common to both branches:

1. **Default to Option B** for the Debian/Ubuntu base — vendor/distro tarball
   wholesale, no trimming. Size is not the constraint here; vendor support and
   binary compatibility are.
2. **Treat systemd image-assembly integration as a first-class phase**, not a
   side effect of the Jetson prototype.
3. **Prove it with one throwaway Jetson spike** (single Orin Nano SKU, L4T
   sample rootfs as-is) before generalizing the rootfs-base abstraction.
   Discover what breaks; do not aim to ship.

**If atomic-image-only (likely default for Jetson edge-AI appliances):**

4. **Do not build `deb_pkg`.** Keep `.deb`/dpkg inside the image; let the vendor
   `apt` flow resolve packages at assembly time. The only sanctioned update path
   is the atomic whole-image A/B swap with rollback — dpkg is present for
   inspection and occasional manual installs, never the OTA mechanism (the
   hybrid in the alternative section). Lowest effort, fully vendor-supported,
   zero dpkg-userland-residue work.

**If incremental-on-device-update is required:**

4. **Scope `deb_pkg` to value-add and post-deploy packages only.** The base
   system comes from the tarball, already configured.
5. **Convert + re-sign value-add `.deb`s** through a `deb_pkg` class symmetric
   to `alpine_pkg`: fetch from the upstream apt feed (verify with upstream key),
   `ar`-extract, translate metadata, resolve `update-alternatives` statically,
   sign with the project key.

## Open questions

- Apt repo format edge cases: epochs, `~` pre-release ordering, `Multi-Arch:`
  semantics — which Go library handles all of these correctly, and what is the
  conformance test set?
- Post-deploy `apk add` of a converted package whose postinst genuinely needs
  `update-alternatives`: is the static-resolution-at-conversion strategy ever
  insufficient, and does that justify the small shell stub?
- Provenance/SBOM story for Option B: how do we record what the black-box
  tarball contained for customers who need a bill of materials even if they
  accept the vendor base?
- Kernel modules (NVIDIA out-of-tree drivers against L4T's kernel ABI) are
  orthogonal to packaging but block a bootable Jetson image — tracked
  separately, noted here so the spike does not assume it is free.

## See also

- [libc, init, and the Rootfs Base](libc-and-init.md) — the strategic case and
  the rootfs-base abstraction this fits into.
- [module-alpine](module-alpine.md) and
  [Alpine apk Passthrough](apk-passthrough.md) — the existing `alpine_pkg`
  precedent that `deb_pkg` mirrors.
- [Comparisons → vs. Debian](comparisons.md) — why yoe leaves dpkg/apt behind
  while still being able to consume their binaries.
