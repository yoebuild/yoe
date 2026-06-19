# Yoe and distributions

Every yoe image targets exactly one **distro** — alpine, debian, ubuntu, or (in
the future) something else. The choice determines the package format, the libc
family, the toolchain container, the on-target package manager, and which
prebuilt packages are reachable from the image's closure. This page is the
orientation guide: what "distro" means inside yoe, when to pick which one, and
how distros plug into the rest of the system. For per-distro detail, see
[module-alpine](module-alpine.md), [module-debian](module-debian.md), and
[module-ubuntu](module-ubuntu.md).

## What a distro means in yoe

A distro in yoe is a **runtime compatibility class**, not a brand preference.
Choosing `distro = "alpine"` on an image means:

- **Package format:** `.apk`. The image-time installer is `apk-tools`.
- **Libc family:** `musl`. The toolchain container is `toolchain-musl`; every
  binary in the image links against musl.
- **Userland conventions:** OpenRC for init, busybox utilities,
  alpine-baselayout for `/etc` structure, alpine signing keys for upstream
  packages.

Choosing `distro = "debian"` means the corresponding glibc / `.deb` /
systemd-or-sysvinit / dpkg-trust stack. The two are not mix-and-match within a
single image; a `.deb` won't install in an alpine rootfs and musl-linked
binaries don't run in a glibc rootfs.

### Setting the distro

Each `image(...)` declaration can carry an explicit `distro` field:

```python
image(
    name = "edge-image",
    distro = "alpine",
    artifacts = [...],
)
```

When unset, yoe resolves the effective distro through a three-level cascade:

1. **The image's own `distro` field** — highest priority.
2. **`local.star`'s `default_distro_override`** — a per-developer override (not
   committed) for trying a different distro locally without editing project
   config.
3. **`PROJECT.star`'s `defaults.distro`** — the project-wide fallback.

If none of the three is set, image evaluation errors immediately. The distro
choice is too consequential to pick silently.

`yoe build` also accepts a `--distro` flag that overrides the default for a
single invocation, sitting at the same level as `default_distro_override` (an
image's own explicit `distro` still wins). This is how you build a multi-distro
image — one definition that carries a `distro_artifacts` map (see below) — as a
specific distro without editing `local.star`:

```sh
yoe build --distro alpine base-image
yoe build --distro debian base-image
```

### Source-built units are typically distro-neutral, but can be tagged

A unit declared with `unit(...)` (in `module-core` or anywhere else) defaults to
distro-neutral: leave `distro` unset and the unit is visible to every consuming
image regardless of its distro. The same `openssl` or `zlib` source unit builds
against musl when consumed by an alpine image and against glibc when consumed by
a debian image, producing two distinct binaries cached under two distinct hash
keys. The unit's definition is the same; the build context (which toolchain,
which libc) is different.

This is what lets a project share most of its source-built userland across
distros while still producing libc-correct binaries. It's the common case for
`module-core`'s userland units.

But the `distro` field is available on every `unit(...)` declaration, including
source-built ones. Set it explicitly when the unit genuinely is distro-specific
— when the build assumes alpine's patches or musl's headers, when it ships
configuration that only makes sense on one libc family, when the upstream source
is hard- coded to one userland's conventions:

```python
# A unit whose configure flags assume musl's nsswitch shape;
# building it under glibc would produce a broken binary even if
# the toolchain were available.
unit(
    name    = "some-musl-only-thing",
    distro  = "alpine",
    source  = "https://...",
    tag     = "v1.2.3",
    ...
)
```

A tagged source unit becomes invisible to closures of other distros, exactly
like a feed-materialized one. The same closure walker filter applies regardless
of where the unit registered. The default is "no tag" because most source builds
work fine against both libc families; the tag is an opt-in for the cases where
they genuinely don't.

Feed-materialized units (from `alpine_feed` / `apt_feed`) always carry a hard
distro affinity automatically — an alpine `.apk` literally is not a debian
`.deb`, and the synthetic module that produces them sets `distro` on every
materialized `*Unit`. You don't write that tag; the feed builtin writes it for
you.

### Per-distro dep additions

A source unit often works fine in both backends but needs different package
_names_ for the same role. Alpine packages setuptools as `py3-setuptools`;
debian splits it across `python3-setuptools` and friends. Alpine bundles
headers + library in one apk (`zlib`); debian splits them (`zlib1g-dev` for
build, `zlib1g` for runtime). The unit's behavior is the same; the dep names
aren't.

`distro_deps` and `distro_runtime_deps` express that without resorting to two
tagged copies of the same unit or per-project conditionals that bake one
distro's names in at registration time and break the other distro's closure
walks:

```python
unit(
    name = "meson",
    ...
    deps = ["samurai", "toolchain"],
    distro_deps = {
        "alpine": ["python3", "py3-setuptools"],
        "debian": ["python3.11", "python3-setuptools"],
    },
)
```

Effective deps at any consuming closure = `deps + distro_deps[consumer_distro]`.
A unit with no `distro_deps` entry for the consumer's distro just gets plain
`deps` — no error, no fallback to some other distro's list. Same shape for
`runtime_deps` / `distro_runtime_deps`.

Reach for `distro_deps` when one source unit can satisfy both backends with
different dep names. Reach for the `distro` tag instead when the build itself
only makes sense for one libc family (musl-only configure flags, distro-
specific patches), or when the two backends warrant materially different build
recipes — then maintain two tagged units rather than one unit with branching
build steps.

### Per-distro image artifacts

An `image(...)` often ships the same role across distros whose package _names_
differ entirely: Alpine boots busybox + OpenRC + apk, the apt distros boot
systemd + glibc + dpkg, and even a shared role like SSH is `openssh` on Alpine
and `openssh-server` on Debian. A single flat `artifacts` list can't express
"this image, every distro," so an image splits per distro via `distro_artifacts`
— the image-level analog of `distro_deps`:

```python
image(
    name = "ssh-image",
    artifacts = ["linux", "bash"],          # distro-neutral entries
    distro_artifacts = {
        "alpine": ["busybox", "openrc", "apk-tools", "openssh", ...],
        "debian": ["systemd-sysv", "libc6", "dpkg", "openssh-server", ...],
        "ubuntu": ["systemd-sysv", "libc6", "dpkg", "openssh-server",
                   "nm-manage-ethernet", ...],
    },
)
```

Effective artifacts = `artifacts + distro_artifacts[effective_distro]`. Only the
branch matching the build's effective distro is consulted; the others are inert
lists — never resolved, never forcing their feed module to load — so a shared
image carrying a `debian` branch builds fine in an Alpine-only project that
never loads `module-debian`. There is no closed-distro key check: the distro set
is open, and a typo'd key for a distro you never build is simply never reached.

This is what lets `module-core` define `base-image`, `dev-image`, and
`ssh-image` once, each targeting Alpine, Debian, and Ubuntu, rather than
maintaining three near-parallel copies per distro module.

### Per-distro kernel selection

The kernel differs across distros — a from-source kernel on Alpine, the stock
feed meta-package (`linux-image-amd64` / `linux-image-arm64`) on the apt distros
— so a machine declares its kernel per distro and images simply reference the
virtual name `"linux"`:

```python
machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    kernel = kernel(
        distro_unit = {
            "alpine": "linux",
            "debian": "linux-image-amd64",
            "ubuntu": "linux-image-generic",
        },
        provides = "linux",
        cmdline = "console=ttyS0 root=/dev/vda1 rw",
    ),
)
```

An image's `"linux"` artifact resolves to `distro_unit[effective_distro]` — the
resolution happens when the image is evaluated, the only point at which the
effective distro is known. A machine whose kernel is the same across distros
keeps the flat single-`unit` form
(`kernel(unit = "linux-rpi5", provides = "linux", ...)`): a Raspberry Pi 5
image, for instance, carries the same custom `linux-rpi5` on Alpine and Debian
alike. Setting both `unit` and `distro_unit` on one kernel is an error.

## Choosing a distro

The picks are bounded today:

| Distro | Status       | Release cadence                                                                                                         | Image assembly¹                                                                                                                                            | When it's the right choice                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| ------ | ------------ | ----------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Alpine | Production   | New stable branch ~every 6 months; ~2-year security support per branch. `edge` rolls continuously.                      | **~10 s** — a single `apk` extract, near-deterministic run to run.                                                                                         | Default for new projects. Small footprint, well-curated package set, all of yoe's tooling exercised against it. Picks up `module-alpine`'s ~12k main + community packages via passthrough; source-built userland from `module-core` links musl cleanly.                                                                                                                                                                                                                                                                                                                      |
| Debian | Experimental | New stable ~every 2 years; ~5-year support including LTS. `testing` and `unstable`/`sid` roll between releases.         | **~100 s** — `mmdebstrap` plus per-package `dpkg` maintainer scripts (and QEMU for a foreign arch); roughly 10× alpine, and noisier run to run (90–120 s). | Reach for it when an image needs glibc (CUDA, vendor drivers, enterprise software that hasn't been musl-ported), the broad apt ecosystem (debian main is ~50k packages), or compatibility with existing debian-based fleet management. End-to-end boot + SSH is exercised nightly in CI (QEMU, both arches), but production hardening is still light — treat it as experimental, not unproven. See [module-debian.md](module-debian.md) for current limitations and workarounds.                                                                                             |
| Ubuntu | Experimental | LTS every 2 years (April of even years), interim releases every 6 months; 5-year LTS support, 10 with Ubuntu Pro / ESM. | **~100 s** — same `mmdebstrap` + `dpkg` path as Debian (Ubuntu rides the shared apt/dpkg backend).                                                         | Reach for it over Debian when you need Ubuntu's commercial hardware enablement (e.g. NVIDIA Jetson L4T is Ubuntu-based), certified-hardware driver stacks, or compatibility with an existing Ubuntu fleet. `module-ubuntu` wraps Ubuntu's archive via `apt_feed(distro = "ubuntu", ...)` and ships its own keyring + glibc toolchain. End-to-end boot + SSH is CI-verified alongside Debian, with the same experimental caveat. See [module-ubuntu.md](module-ubuntu.md) for Ubuntu specifics and [module-debian.md](module-debian.md) for the shared backend's limitations. |

¹ Wall-clock to reassemble a working dev image on a `qemu-x86_64` target with
the full dependency closure already built and cached, so the figure isolates the
image-assembly step — package install plus configure — rather than the source
builds behind it. Measured as a median over several runs; the gap reflects how
much each package format does at install time (apk extracts; dpkg also runs
maintainer scripts), so it widens with package count and on foreign-arch targets
where those scripts run under emulation.

**Footprint.** The two backends differ sharply in size. The minimal boot + SSH
image — the platform floor for a device you can log into, with no developer
tooling on either side (kernel, init, libc, shell, package manager, `sshd`,
DHCP, one login user) — is about **85 MB on Alpine and ~405 MB on Debian** on a
`qemu-x86_64` target (Debian trixie), roughly 4.8×. Most of the gap is the stock
`linux-image-amd64` kernel, which ships a driver for every machine it might ever
run on; Alpine here boots a kernel yoe built from source and tailored to the
target, so its module tree is a few megabytes rather than ~107 MB. A production
Debian image can swap in a tailored kernel too — out of the box, the distro
kernel brings everything. The remainder is the platform itself: glibc and its
multiarch libraries, systemd, the full apt/dpkg stack, complete coreutils
instead of busybox applets. Alpine's musl + busybox + OpenRC base is simply
lighter, and on a device that difference compounds.

If you don't have a hard reason for debian — a vendor-supplied binary, a
glibc-only library, a fleet already running debian — start with alpine. The
defaults work, the cache hits land, and the boot-and-SSH path has miles on it.

If you do have a hard reason, debian's plumbing is in place: feeds resolve,
packages mirror verbatim, the image assembler runs `mmdebstrap` against the
project's local repo to unpack and configure the rootfs in a single pass, and
the project repo emits a signed `InRelease`. The assembled rootfs boots in QEMU
and accepts SSH — exercised nightly in CI on both arches — so the path works end
to end; what keeps the experimental label is production hardening (tailored
kernels, security review, a settled OTA story), not basic bring-up.

## Mixing distros in one project

A project can define alpine images and debian images side-by-side. Each image's
effective distro is independent — yoe doesn't enforce "one distro per project."

Cross-distro coexistence is handled in three parallel layers that all keep
distros separated:

- **On-disk repos** are per-distro. `repo/<project>/alpine/<arch>/` holds apks;
  `repo/<project>/debian/dists/<suite>/` holds debs. Each on-target package
  manager sees only its own subtree.
- **On-disk build directories** are per-distro:
  `build/<distro>/<unit>.<scope>/destdir/`. A source-built unit consumed by both
  an alpine and a debian image has two separate destdirs, each holding a
  libc-correct binary.
- **The in-memory catalog** stores every unit by `(module, name)` and exposes
  per-distro views: an alpine image queries `DistroViews["alpine"]` and gets
  alpine-tagged units (plus distro-neutral source units); a debian image queries
  `DistroViews["debian"]` and gets debian-tagged units (plus the same source
  units). Same-named entries from different distro feeds live in different
  `UnitsByModule` buckets and different `DistroViews` cells; they never clobber
  each other.

What this implies for builds — expected behavior, not a special case:

- **Source-built units build once per consuming distro.** A source-built
  `openssl` consumed by both an alpine and a debian image builds twice — once in
  each toolchain container — producing two binaries cached separately. Building
  the same unit more than once along the distro axis is normal: musl-built and
  glibc-built binaries cannot share at the ABI level, so a libc-correct artifact
  per distro is the correctness mechanism, not a bug. The unit stays the single
  definition of how the package is built; only the build context (toolchain,
  libc) differs. The cost is one cache entry per (unit, distro) pair, and every
  subsequent build hits the cache.

### The primary multi-distro use case: alpine app containers on a debian host

The pattern that motivates mixing distros within a single project is **building
alpine-based application containers that get deployed inside a debian host
image.** The host image is debian for the reasons that drive picking debian in
the first place — glibc compatibility for vendor drivers, broad apt ecosystem,
an existing fleet management story. The application containers are alpine for
the reasons that drive picking alpine: small footprint, fast startup, minimal
attack surface, comprehensive musl-clean package wrapping.

A representative `PROJECT.star` shape:

```python
# Host image: debian. Boots the device, runs vendor agents,
# manages the container runtime, handles OTA. The app container
# is in artifacts, so the image build embeds it into the host's
# container store at image-assembly time.
image(
    name = "device-host",
    distro = "debian",
    artifacts = [
        "apt", "openssh-server", "linux-image-amd64",
        "containerd",
        "app",                # the alpine container below
    ],
)

# App container: alpine. Holds the actual product workload.
# Built as a deployable OCI artifact, not as a bootable image.
container(                                          # (planned)
    name = "app",
    distro = "alpine",
    artifacts = ["busybox", "my-app", "my-app-config"],
)
```

> **Status (planned):** the `container(...)` form shown above — producing a
> deployable application-container artifact from an `artifacts = [...]` list,
> embeddable in a host image's own artifacts — is not yet implemented. Today,
> `container(...)` exists only for declaring **build** containers
> (`toolchain-musl`, `toolchain-glibc`, …) from a `Dockerfile`. The planned
> extension repurposes the same builtin name for deployable application
> containers: when called with `artifacts = [...]`, the unit emits an OCI image
> rather than building a Dockerfile. See
> [Deployable Containers spec](https://github.com/yoebuild/yoe/blob/main/docs/specs/2026-05-25-deployable-containers.md)
> for the spec and the current implementation status. The architectural shape
> this section describes — distros as orthogonal axes, multi-distro projects,
> three-layer separation — is current behavior; only the deployable-container
> form of the `container(...)` builtin is future work.

Both build from the same `PROJECT.star`, share the same source-built userland
where applicable (a source unit consumed by both builds twice — once musl-linked
for the alpine container, once glibc-linked for the debian host — under separate
cache keys), and ship together as part of the same project release.

Other multi-distro shapes exist (a product line with a small alpine edge device
and a larger debian gateway, both shipped from one repo) but the
alpine-app-in-debian-host pattern is the one yoe's distro mixing was designed to
make ergonomic. For the practical current-state behavior of multi-distro
projects on versions where catalog separation is still landing, see
[module-debian.md known limitations](module-debian.md#known-limitations).

## How distros plug in (high-level)

Each distro is delivered as a module that the project pulls in:

- **`module-alpine`** registers `alpine.main` and `alpine.community` synthetic
  feeds, supplies the `toolchain-musl` container unit, and ships the upstream
  signing keys for verifying `APKINDEX`. Source:
  [module-alpine.md](module-alpine.md).
- **`module-debian`** registers `debian.main` synthetic feed, supplies the
  `toolchain-glibc` container unit, and ships the upstream signing keys for
  verifying `InRelease`. Source: [module-debian.md](module-debian.md).
- **`module-ubuntu`** registers `ubuntu.main` synthetic feed over Ubuntu's split
  archive/ports mirrors, supplies its own `toolchain-glibc` container unit, and
  ships the Ubuntu archive keyring. It rides Debian's shared apt/dpkg/glibc
  backend. Source: [module-ubuntu.md](module-ubuntu.md).

These modules use the same yoe primitives — `module_info()`, `alpine_feed()` /
`apt_feed()`, `container()`, and a small `units/*-enable.star` companion layer
for services the maintainer wants exposed at boot. The internal Go support —
`internal/apkindex`, `internal/feeds/alpine`, `internal/dpkg`,
`internal/feeds/debian` — is parallel by design: each distro has its own
format-named parser, its own materializer, its own update-feeds driver. No
special-case branching in the resolver beyond the distro field on Unit and the
per-distro views in the catalog.

For the resolver-side mechanics — how synthetic modules materialize lazily, how
per-distro views resolve cross-distro collisions, how effective distro flows
into cache keys — see [Catalog and Materialization](catalog.md). For the
apk-specific mirror-verbatim mechanism, see
[Alpine apk Passthrough](apk-passthrough.md). For the apk signing trust chain,
see [Package Signing](signing.md).

## Adding a new distro

The pattern is parallel across distros: a Go-side parser for the upstream
format, a feed builtin that registers a synthetic module with a `Lookup`
callback, a materializer that constructs `*Unit` objects from upstream entries,
a project repo emitter for republishing verified-mirror packages, and an image
assembler branch that knows how to install packages of the format. The two
existing distros are the reference templates:

- Alpine: `internal/apkindex/`, `internal/feeds/alpine/`,
  `internal/artifact/apk.go`, `internal/repo/index.go`.
- Debian: `internal/dpkg/`, `internal/feeds/debian/`, `internal/deb/`,
  `internal/repo/deb_emitter.go`.

Ubuntu was the cheapest next distro and is already shipped — it's `.deb`-format
with different upstream keys and URLs, so `module-ubuntu` mostly shims over
`apt_feed()` with a different keyring, suite, and split archive/ports mirrors
(see [module-ubuntu.md](module-ubuntu.md)). Fedora / RHEL would need a new
format parser (`.rpm`, `repodata`), a new materializer, and a new
image-assembler branch (`dnf --installroot` instead of `mmdebstrap`); the
infrastructure is already factored to make this additive rather than invasive.
