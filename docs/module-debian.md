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
> (`debian_feed`, the InRelease verify path, `dpkg --configure -a` under
> binfmt, the project repo emitter) live in
> [`docs/specs/2026-05-25-module-debian.md`](specs/2026-05-25-module-debian.md)
> and the matching plan under `docs/plans/`. This doc is the "when to reach
> for it" rubric.

## When to reach for it

The same policy yoe follows for Alpine applies to Debian. The choice between
the two is whether the image targets glibc (Debian) or musl (Alpine); the rest
of the rubric — yoe builds the small stuff, the distro module ships the
hard-to-build complexity — is identical.

1. **Yoe builds the easy stuff in `module-core`** regardless of distro target.
   The same `zlib`, `xz`, `expat`, ... source units compile against either
   toolchain via the `container = "toolchain"` virtual reference (U10).
2. **`module-debian` ships Debian-native and hard-to-build packages.**
   Debian-native means `dpkg`, `apt`, `debianutils`, `base-files`,
   `libc6`/`libc-bin`. Hard-to-build means packages where Debian's expertise
   earns its keep: `openssl`, `openssh-server`, `curl`, `python3`, `clang`,
   and the entire `linux-image-*` lineage when running stock kernels makes
   sense.
3. **Keep building from source anything where the build defines the product.**
   Toolchain, kernel (when custom), bootloader, init scripts, board-specific
   firmware — these are not packages, they are the distribution.

## Debian release coupling

The Debian suite pinned in `MODULE.star` (`_DEBIAN_SUITE = "bookworm"` at the
time of writing) **must** match the `FROM debian:<release>` line in
`@module-debian//containers/toolchain-glibc/Dockerfile`. Both currently point
at `bookworm`.

The coupling matters for three reasons:

- **glibc ABI.** Source units that link against headers/libs from the
  toolchain container produce binaries that need a matching glibc on the
  target rootfs. Mixing `bookworm-slim` headers with `trixie` runtime libs
  is a silent ABI mismatch.
- **Signing keys.** Each Debian release has its own archive signing key, and
  the in-tree `keys/debian-archive-keyring.gpg` is what `yoe update-feeds`
  verifies against. Bumping the suite without rotating the bootstrap keyring
  produces an `untrusted key` error at first `update-feeds` after the bump.
- **Cache invalidation.** Source units cache by hash; switching the toolchain
  container's `FROM` tag rolls every hash through it. Plan the bump for a
  full rebuild cycle.

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
re-signed each emit. `Valid-Until` defaults to 30 days, configurable
per-project — short enough to bound rollback windows, long enough for
disconnected development.

## Maintainer playbook

The flow mirrors `module-alpine`'s. Inside a checked-out `module-debian`:

1. **Refresh in-tree `Packages` snapshots.** Run `yoe update-feeds` inside the
   module directory. The command peeks `MODULE.star` for every
   `debian_feed(...)` call, fetches each declared suite's `InRelease` from the
   pinned mirror, verifies it against `keys/debian-archive-keyring.gpg` with
   Valid-Until enforcement, fetches per-arch `Packages.gz`, decompresses, and
   atomically writes the result into `feeds/<component>/<arch>/Packages`.
   Writes only — review with `git diff feeds/` and commit when ready.
2. **Push upstream.** yoe's external-module workflow (CLAUDE.md) fetches a
   pinned ref on every build, so the new `Packages` snapshot needs to land on
   the canonical remote before the next consumer's `yoe build` will see it.
3. **Key rotation.** When Debian rotates a release signing key — typically
   when a new stable ships — `yoe update-feeds` will refuse the new key
   until its fingerprint is in `keys/allowed-fingerprints`. Verify the
   fingerprint against <https://ftp-master.debian.org/keys.html>, then either
   edit `allowed-fingerprints` directly or use
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
thousand entries on the way to its closure — not the full 60k-entry catalog.

Multiple feeds compose: declaring `bookworm.main` plus
`bookworm-security.main` plus `bookworm-updates.main` gives apt-equivalent
priority resolution on the project side. The closure walker consults each in
declaration order; first match wins.
