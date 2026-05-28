# Yoe and distributions

Every yoe image targets exactly one **distro** — alpine, debian, or
(in the future) something else. The choice determines the package
format, the libc family, the toolchain container, the on-target
package manager, and which prebuilt packages are reachable from the
image's closure. This page is the orientation guide: what "distro"
means inside yoe, when to pick which one, and how distros plug into
the rest of the system. For per-distro detail, see
[module-alpine](module-alpine.md) and
[module-debian](module-debian.md).

## What a distro means in yoe

A distro in yoe is a **runtime compatibility class**, not a brand
preference. Choosing `distro = "alpine"` on an image means:

- **Package format:** `.apk`. The image-time installer is `apk-tools`.
- **Libc family:** `musl`. The toolchain container is `toolchain-musl`;
  every binary in the image links against musl.
- **Userland conventions:** OpenRC for init, busybox utilities,
  alpine-baselayout for `/etc` structure, alpine signing keys for
  upstream packages.

Choosing `distro = "debian"` means the corresponding glibc / `.deb` /
systemd-or-sysvinit / dpkg-trust stack. The two are not mix-and-match
within a single image; a `.deb` won't install in an alpine rootfs and
musl-linked binaries don't run in a glibc rootfs.

### Setting the distro

Each `image(...)` declaration can carry an explicit `distro` field:

```python
image(
    name = "edge-image",
    distro = "alpine",
    artifacts = [...],
)
```

When unset, yoe resolves the effective distro through a three-level
cascade:

1. **The image's own `distro` field** — highest priority.
2. **`local.star`'s `default_distro_override`** — a per-developer
   override (not committed) for trying a different distro locally
   without editing project config.
3. **`PROJECT.star`'s `default_distro`** — the project-wide fallback.

If none of the three is set, image evaluation errors immediately. The
distro choice is too consequential to pick silently.

### Source-built units are typically distro-neutral, but can be tagged

A unit declared with `unit(...)` (in `module-core` or anywhere else)
defaults to distro-neutral: leave `distro` unset and the unit is
visible to every consuming image regardless of its distro. The same
`openssl` or `zlib` source unit builds against musl when consumed by
an alpine image and against glibc when consumed by a debian image,
producing two distinct binaries cached under two distinct hash keys.
The unit's definition is the same; the build context (which
toolchain, which libc) is different.

This is what lets a project share most of its source-built userland
across distros while still producing libc-correct binaries. It's the
common case for `module-core`'s userland units.

But the `distro` field is available on every `unit(...)` declaration,
including source-built ones. Set it explicitly when the unit
genuinely is distro-specific — when the build assumes alpine's
patches or musl's headers, when it ships configuration that only
makes sense on one libc family, when the upstream source is hard-
coded to one userland's conventions:

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

A tagged source unit becomes invisible to closures of other distros,
exactly like a feed-materialized one. The same closure walker filter
applies regardless of where the unit registered. The default is "no
tag" because most source builds work fine against both libc families;
the tag is an opt-in for the cases where they genuinely don't.

Feed-materialized units (from `alpine_feed` / `debian_feed`) always
carry a hard distro affinity automatically — an alpine `.apk`
literally is not a debian `.deb`, and the synthetic module that
produces them sets `distro` on every materialized `*Unit`. You don't
write that tag; the feed builtin writes it for you.

## Choosing a distro

The picks are bounded today:

| Distro | Status              | When it's the right choice |
| ------ | ------------------- | -------------------------- |
| Alpine | Production          | Default for new projects. Small footprint, well-curated package set, all of yoe's tooling exercised against it. Picks up `module-alpine`'s ~12k main + community packages via passthrough; source-built userland from `module-core` links musl cleanly. |
| Debian | Experimental        | Reach for it when an image needs glibc (CUDA, vendor drivers, enterprise software that hasn't been musl-ported), the broad apt ecosystem (debian main is ~50k packages), or compatibility with existing debian-based fleet management. End-to-end boot + SSH is not yet verified — treat any production deployment as untested. See [module-debian.md](module-debian.md) for current limitations and workarounds. |

If you don't have a hard reason for debian — a vendor-supplied
binary, a glibc-only library, a fleet already running debian — start
with alpine. The defaults work, the cache hits land, and the
boot-and-SSH path has miles on it.

If you do have a hard reason, debian's plumbing is in place: feeds
resolve, packages mirror verbatim, the image assembler runs
`dpkg --configure -a` under a no-network sandbox, the project repo
emits a signed `InRelease`. What's still pending is end-to-end
verification that the assembled rootfs actually boots in QEMU and
accepts SSH; until that's done, expect to iterate.

## Mixing distros in one project

A project can define alpine images and debian images side-by-side.
Each image's effective distro is independent — yoe doesn't enforce
"one distro per project."

Cross-distro coexistence is handled in three parallel layers that all
keep distros separated:

- **On-disk repos** are per-distro. `repo/<project>/alpine/<arch>/`
  holds apks; `repo/<project>/debian/dists/<suite>/` holds debs.
  Each on-target package manager sees only its own subtree.
- **On-disk build directories** are per-distro:
  `build/<distro>/<unit>.<scope>/destdir/`. A source-built unit
  consumed by both an alpine and a debian image has two separate
  destdirs, each holding a libc-correct binary.
- **The in-memory catalog** stores every unit by `(module, name)`
  and exposes per-distro views: an alpine image queries
  `DistroViews["alpine"]` and gets alpine-tagged units (plus
  distro-neutral source units); a debian image queries
  `DistroViews["debian"]` and gets debian-tagged units (plus the
  same source units). Same-named entries from different distro feeds
  live in different `UnitsByModule` buckets and different
  `DistroViews` cells; they never clobber each other.

The one architectural cost mixing distros DOES pay:

- **Source-built units build per consuming distro.** A source-built
  `openssl` consumed by both an alpine and a debian image builds
  twice — once in each toolchain container — producing two binaries
  cached separately. This is the correctness mechanism, not a bug;
  the cost is one cache entry per (unit, distro) pair, and every
  subsequent build hits the cache.

### The primary multi-distro use case: alpine app containers on a debian host

The pattern that motivates mixing distros within a single project is
**building alpine-based application containers that get deployed
inside a debian host image.** The host image is debian for the
reasons that drive picking debian in the first place — glibc
compatibility for vendor drivers, broad apt ecosystem, an existing
fleet management story. The application containers are alpine for the
reasons that drive picking alpine: small footprint, fast startup,
minimal attack surface, comprehensive musl-clean package wrapping.

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

> **Status (planned):** the `container(...)` form shown above —
> producing a deployable application-container artifact from an
> `artifacts = [...]` list, embeddable in a host image's own artifacts
> — is not yet implemented. Today, `container(...)` exists only for
> declaring **build** containers (`toolchain-musl`, `toolchain-glibc`,
> …) from a `Dockerfile`. The planned extension repurposes the same
> builtin name for deployable application containers: when called
> with `artifacts = [...]`, the unit emits an OCI image rather than
> building a Dockerfile. See
> [Deployable Containers](specs/2026-05-25-deployable-containers.md)
> for the spec and the current implementation status. The
> architectural shape this section describes — distros as orthogonal
> axes, multi-distro projects, three-layer separation — is current
> behavior; only the deployable-container form of the `container(...)`
> builtin is future work.

Both build from the same `PROJECT.star`, share the same source-built
userland where applicable (a source unit consumed by both builds
twice — once musl-linked for the alpine container, once glibc-linked
for the debian host — under separate cache keys), and ship together
as part of the same project release.

Other multi-distro shapes exist (a product line with a small alpine
edge device and a larger debian gateway, both shipped from one repo)
but the alpine-app-in-debian-host pattern is the one yoe's distro
mixing was designed to make ergonomic. For the practical current-state
behavior of multi-distro projects on versions where catalog separation
is still landing, see
[module-debian.md known limitations](module-debian.md#known-limitations).

## How distros plug in (high-level)

Each distro is delivered as a module that the project pulls in:

- **`module-alpine`** registers `alpine.main` and `alpine.community`
  synthetic feeds, supplies the `toolchain-musl` container unit, and
  ships the upstream signing keys for verifying `APKINDEX`. Source:
  [module-alpine.md](module-alpine.md).
- **`module-debian`** registers `debian.main` synthetic feed,
  supplies the `toolchain-glibc` container unit, and ships the
  upstream signing keys for verifying `InRelease`. Source:
  [module-debian.md](module-debian.md).

Both modules use the same yoe primitives — `module_info()`,
`alpine_feed()` / `debian_feed()`, `container()`, and a small
`units/*-enable.star` companion layer for services the maintainer
wants exposed at boot. The internal Go support — `internal/apkindex`,
`internal/feeds/alpine`, `internal/dpkg`, `internal/feeds/debian` —
is parallel by design: each distro has its own format-named parser,
its own materializer, its own update-feeds driver. No special-case
branching in the resolver beyond the distro field on Unit and the
per-distro views in the catalog.

For the resolver-side mechanics — how synthetic modules materialize
lazily, how per-distro views resolve cross-distro collisions, how
effective distro flows into cache keys — see
[Catalog and Materialization](catalog.md). For the apk-specific
mirror-verbatim mechanism, see
[Alpine apk Passthrough](apk-passthrough.md). For the apk signing
trust chain, see [apk Signing](signing.md).

## Adding a new distro

The pattern is parallel across distros: a Go-side parser for the
upstream format, a feed builtin that registers a synthetic module
with a `Lookup` callback, a materializer that constructs `*Unit`
objects from upstream entries, a project repo emitter for republishing
verified-mirror packages, and an image assembler branch that knows how
to install packages of the format. The two existing distros are the
reference templates:

- Alpine: `internal/apkindex/`, `internal/feeds/alpine/`,
  `internal/artifact/apk.go`, `internal/repo/index.go`.
- Debian: `internal/dpkg/`, `internal/feeds/debian/`,
  `internal/deb/`, `internal/repo/deb_emitter.go`.

Ubuntu is the cheapest plausible next distro — it's `.deb`-format
with different upstream keys and URLs, so a future `module-ubuntu`
could mostly shim over `debian_feed()` with a different keyring +
suite. Fedora / RHEL would need a new format parser (`.rpm`,
`repodata`), a new materializer, and a new image-assembler branch
(`rpm -i` instead of `dpkg --configure -a`); the infrastructure is
already factored to make this additive rather than invasive.

There's no plan in flight for a third distro today. The split between
alpine and debian was driven by concrete user needs (musl footprint
vs glibc compatibility); adding a third should be driven the same way,
not by completeness.
