# module-debian — wrapping prebuilt Debian packages

`module-debian` is a yoe module that wraps prebuilt Debian `.deb` files as yoe
units, mirroring the role `module-alpine` plays for Alpine. Where `module-core`
builds packages from upstream source, units in this module fetch a binary `.deb`
from a pinned Debian release, verify its SHA256 against the upstream-signed
`Packages` catalog, and republish it through yoe's project repo. The unit's
"build" is just extracting the deb's `data.tar` into `$DESTDIR`.

The module lives at <https://github.com/yoebuild/module-debian>. Open it to
browse the bootstrap keyring, the in-tree `Packages` snapshots, or to send a PR
adding a new feed/component.

> **Implementation details:** how Debian debs pass through yoe's pipeline
> (`debian_feed`, the InRelease verify path, `dpkg --configure -a` under binfmt,
> the project repo emitter) live in
> [`docs/specs/2026-05-25-module-debian.md`](https://github.com/yoebuild/yoe/blob/main/docs/specs/2026-05-25-module-debian.md)
> and the matching plan under `docs/plans/`. This doc is the "when to reach for
> it" rubric.

> **Status: experimental.** The Debian backend's plumbing (debian_feed, .deb
> passthrough, InRelease verify, project repo emitter, `dpkg --configure -a`
> under binfmt) is implemented and exercised by the e2e fixture, but **the
> end-to-end boot-and-SSH path has not been verified yet**. A Debian image built
> by yoe today should produce a configured rootfs; whether it boots in QEMU and
> accepts an SSH session is currently an expectation, not a fact. See
> [Known limitations](#known-limitations) below before relying on this in
> production.

## When to reach for it

The same policy yoe follows for Alpine applies to Debian. The choice between the
two is whether the image targets glibc (Debian) or musl (Alpine); the rest of
the rubric — yoe builds the small stuff, the distro module ships the
hard-to-build complexity — is identical.

1. **Yoe builds the easy stuff in `module-core`** regardless of distro target.
   The same `zlib`, `xz`, `expat`, ... source units compile against either
   toolchain via the `container = "toolchain"` virtual reference.
2. **`module-debian` ships Debian-native and hard-to-build packages.**
   Debian-native means `dpkg`, `apt`, `debianutils`, `base-files`,
   `libc6`/`libc-bin`. Hard-to-build means packages where Debian's expertise
   earns its keep: `openssl`, `openssh-server`, `curl`, `python3`, `clang`, and
   the entire `linux-image-*` lineage when running stock kernels makes sense.
3. **Keep building from source anything where the build defines the product.**
   Toolchain, kernel (when custom), bootloader, init scripts, board-specific
   firmware — these are not packages, they are the distribution.

## Debian release coupling

The Debian suite pinned in `MODULE.star` (`_DEBIAN_SUITE = "bookworm"` at the
time of writing) **must** match the `FROM debian:<release>` line in
`@module-debian//containers/toolchain-glibc/Dockerfile`. Both currently point at
`bookworm`.

The coupling matters for three reasons:

- **glibc ABI.** Source units that link against headers/libs from the toolchain
  container produce binaries that need a matching glibc on the target rootfs.
  Mixing `bookworm-slim` headers with `trixie` runtime libs is a silent ABI
  mismatch.
- **Signing keys.** Each Debian release has its own archive signing key, and the
  in-tree `keys/debian-archive-keyring.gpg` is what `yoe update-feeds` verifies
  against. Bumping the suite without rotating the bootstrap keyring produces an
  `untrusted key` error at first `update-feeds` after the bump.
- **Cache invalidation.** Source units cache by hash; switching the toolchain
  container's `FROM` tag rolls every hash through it. Plan the bump for a full
  rebuild cycle.

## Trust chain

```
sources.list.d/<project>.sources
  ├── Signed-By: /etc/apt/keyrings/<project>.gpg
  └── URIs: https://<host>/<project>/debian

apt fetch InRelease
  → gpg verify against /etc/apt/keyrings/<project>.gpg
  → REJECT if Valid-Until expired
apt fetch Packages
  → SHA256 verified against InRelease
apt fetch <pkg>.deb
  → SHA256 verified against Packages
  → install + run maintainer scripts via dpkg
```

The project repo is regenerated every time a unit changes; the InRelease is
re-signed each emit. `Valid-Until` defaults to 30 days, configurable per-project
— short enough to bound rollback windows, long enough for disconnected
development. Embedded fleets with strict security cadence may want a shorter
window; offline-tolerant fleets may want longer. The trade-off is
fleet-specific; pick a value that matches your update cadence and ability to
push fresh InRelease files when needed.

Repository URLs must be HTTPS. yoe validates this at project evaluation time; an
`http://` URL in a `debian_feed(...)` call fails fast with a clear error.
Plaintext mirrors expose the trust chain to MITM injection — the bootstrap
keyring's job is to verify what the mirror says, but the mirror can't be trusted
to deliver bytes faithfully without TLS.

## Maintainer playbook

The flow mirrors `module-alpine`'s. Inside a checked-out `module-debian`:

1. **Refresh in-tree `Packages` snapshots.** Run `yoe update-feeds` inside the
   module directory. The command peeks `MODULE.star` for every
   `debian_feed(...)` call, fetches each declared suite's `InRelease` from the
   pinned mirror, verifies it against `keys/debian-archive-keyring.gpg` with
   Valid-Until enforcement, fetches per-arch `Packages.gz`, decompresses, and
   atomically writes the result into `feeds/<component>/<arch>/Packages`. Writes
   only — review with `git diff feeds/` and commit when ready.
2. **Push upstream.** yoe's external-module workflow (CLAUDE.md) fetches a
   pinned ref on every build, so the new `Packages` snapshot needs to land on
   the canonical remote before the next consumer's `yoe build` will see it.
3. **Key rotation.** When Debian rotates a release signing key — typically when
   a new stable ships — `yoe update-feeds` will refuse the new key until its
   fingerprint is in `keys/allowed-fingerprints`. Verify the fingerprint against
   <https://ftp-master.debian.org/keys.html>, then either edit
   `allowed-fingerprints` directly or use
   `yoe update-feeds --allow-key-update=<fpr>` to append it in one step.

## Declaring a feed

In `MODULE.star`:

```python
debian_feed(
    name = "main",
    url = "https://deb.debian.org/debian",
    suite = "bookworm",
    component = "main",
    arches = ["amd64", "arm64"],
    index = "feeds/main",
    keyring = "keys/debian-archive-keyring.gpg",
)
```

Each call registers a `SyntheticModule` named `<parent>.<suite>.<component>`
(e.g. `debian.bookworm.main`). Units materialize lazily as the runtime closure
references them, so a project pulling in `openssh-server` parses about a
thousand entries on the way to its closure — not the full 60k-entry catalog. See
[Catalog and Materialization](catalog.md) for the resolver-side mechanics (how
synthetic modules differ from real modules, lazy-Lookup contract, and the
working-set sizes the resolver operates at).

Multiple feeds compose: declaring `bookworm.main` plus `bookworm-security.main`
plus `bookworm-updates.main` gives apt-equivalent priority resolution on the
project side. The closure walker consults each in declaration order; first match
wins.

> **Naming convention change (planned):** the suite segment will be dropped from
> the module identity, making `debian_feed(name="main", suite="bookworm")`
> register as `debian.main` (matching alpine's `alpine.main`). Suite stays as a
> feed configuration parameter — it chooses which on-disk `Packages` file is
> parsed — but it won't appear in the module name. Today the synthetic module is
> still named `debian.bookworm.main`. Pins in `prefer_modules` written as
> `debian.main` will start working as soon as the rename lands; until then,
> write the suite-embedded form.

## Known limitations

These are user-visible behaviors today. Each one names the workaround that
bridges the gap until the underlying limitation is removed.

- **End-to-end boot is unverified.** The image assembler produces a configured
  rootfs and the project repo emitter ships a valid InRelease, but a
  `yoe run debian-base-image` → `ssh root@localhost` round trip hasn't been
  demonstrated yet on a clean repo. Treat any production deployment as untested
  until the round trip has been verified on the version of yoe you're running.
  The expectation is that it will work; the verification is pending.
- **Cross-distro multi-image projects collide on same-named units.** If your
  project defines both an alpine image and a debian image, and any unit name
  appears in both feeds (e.g. `libcap2` from `alpine.main` and from
  `debian.main`), the second-registered variant overwrites the first in the flat
  catalog. The closure of the losing image points at the wrong variant.
  Workarounds:
  - Pin the colliding name with `prefer_modules` to the module appropriate for
    the dominant image.
  - Build the alpine and debian images in separate `yoe build` invocations —
    each invocation resolves its own catalog without cross-contamination.
- **Source-built units shared between alpine and debian closures produce
  wrong-libc binaries.** `module-core`'s source-built `openssl` (and similar)
  currently caches under a single hash regardless of consuming distro. If you
  build the alpine image first, the cache holds a musl-linked binary; the
  subsequent debian build hits that cache and gets a binary that won't run in a
  glibc rootfs. Workarounds:
  - Build alpine and debian images separately, running `yoe clean <unit>`
    between invocations to evict the wrong-libc binary.
  - Pin the affected names with `prefer_modules` to the distro feed's prebuilt
    (e.g. `"openssl": "debian.bookworm.main"` for debian-side consumption), so
    the closure resolves to the feed's prebuilt rather than the source-built
    version.
- **Some upstream `.deb` postinsts assume network access.** yoe runs
  `dpkg --configure -a` under `--network=none` for hash stability and
  reproducibility, so postinsts that reach out to the network (`cloud-init`
  provisioning, telemetry agents, license-prompt downloaders) will fail loudly
  during image assembly. The narrow set of packages this affects isn't
  appropriate for embedded images; replace with a from-source `module-core` unit
  if equivalent functionality is needed.
