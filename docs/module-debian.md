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
> (`apt_feed`, the InRelease verify path, mmdebstrap-driven image assembly, the
> project repo emitter) live in
> [`docs/specs/2026-05-25-module-debian.md`](https://github.com/yoebuild/yoe/blob/main/docs/specs/2026-05-25-module-debian.md)
> and the matching plan under `docs/plans/`. This doc is the "when to reach for
> it" rubric.

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
`@module-debian//containers/toolchain-debian-13/Dockerfile`. Both currently
point at `bookworm`.

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
`http://` URL in a `apt_feed(...)` call fails fast with a clear error. Plaintext
mirrors expose the trust chain to MITM injection — the bootstrap keyring's job
is to verify what the mirror says, but the mirror can't be trusted to deliver
bytes faithfully without TLS.

## Maintainer playbook

The flow mirrors `module-alpine`'s. Inside a checked-out `module-debian`:

1. **Refresh in-tree `Packages` snapshots.** Run `yoe update-feeds` inside the
   module directory. The command peeks `MODULE.star` for every `apt_feed(...)`
   call, fetches each declared suite's `InRelease` from the pinned mirror,
   verifies it against `keys/debian-archive-keyring.gpg` with Valid-Until
   enforcement, fetches per-arch `Packages.gz`, decompresses, and atomically
   writes the result into `feeds/<component>/<arch>/Packages`. Writes only —
   review with `git diff feeds/` and commit when ready.
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
apt_feed(
    name = "main",
    url = "https://deb.debian.org/debian",
    suite = "bookworm",
    component = "main",
    arches = ["amd64", "arm64"],
    index = "feeds/main",
    keyring = "keys/debian-archive-keyring.gpg",
)
```

Each call registers a `SyntheticModule` named `<parent>.<component>` (e.g.
`debian.main`, `debian.contrib`, `debian.non-free`) — matching alpine's
`alpine.main` / `alpine.community` shape. The `suite` kwarg configures which
on-disk `Packages` file is parsed but does not appear in the module name; one
Debian suite per project, enforced at evaluation, so the suite has no
disambiguating role at the module level. Units materialize lazily as the runtime
closure references them, so a project pulling in `openssh-server` parses about a
thousand entries on the way to its closure — not the full 60k-entry catalog. See
[Catalog and Materialization](catalog.md) for the resolver-side mechanics (how
synthetic modules differ from real modules, lazy-Lookup contract, and the
working-set sizes the resolver operates at).

Multiple feeds compose: declaring `debian.main` plus security and updates
overlays (each with its own `apt_feed(...)` call, same suite, different
component or apt-overlay URL) gives apt-equivalent priority resolution on the
project side. The closure walker consults each in declaration order; first match
wins.

## Verifying a Debian image

End-to-end verification — does the image actually boot? — runs against the
`debian-base-image` fixture under `testdata/e2e-project/`. The fixture targets
the smallest closure that boots in QEMU and accepts SSH: kernel, init,
networking, openssh-server.

`prefer_modules` is scoped per consuming distro, so the alpine pins in the
e2e-project PROJECT.star (`xz`, `zstd`, `util-linux`, `curl` → `alpine.main`)
don't bleed into the debian closure: a debian image build resolves those names
through their distro-neutral module-core equivalents. The fixture builds out of
the box on a mixed-distro project.

```sh
cd testdata/e2e-project

# 1. Refresh the in-tree Packages files if upstream has moved since the
#    cached snapshot was taken. Safe to skip on a clean check-out.
yoe update-feeds

# 2. Build the image. This pulls every artifact's .deb from the cached
#    bookworm feed, builds toolchain-debian-13 on first run, installs the
#    closure into the rootfs with `mmdebstrap` (apt + dpkg in one pass,
#    running maintainer scripts), stages the project APT keyring + deb822
#    sources file, and writes a bootable disk image. Expect ~5–10 min
#    on first run (toolchain build); subsequent runs hit the cache.
yoe build debian-base-image

# 3. Boot under QEMU. The fixture's machine config forwards host port
#    2222 → guest port 22 so SSH lands without extra flags.
yoe run debian-base-image

# 4. From another terminal, connect to the running image.
ssh -p 2222 root@localhost
```

If `apt-get update` and `apt-get install` work from inside the booted image
against the project's own repo (`https://<feed-host>/<project>/debian`), the
verification has fully closed: project repo emission, on-device APT trust
staging, and the iterate–deploy–update loop all work end-to-end.

When the build fails or the image won't boot, the failure usually surfaces in
one of these places:

- `mmdebstrap` inside the toolchain container — postinst error in the configure
  log; check whether the package needs network access (see Known limitations).
- Bootloader install — `extlinux` / `syslinux-common` must be present in the
  toolchain-debian-13 Dockerfile so `_install_syslinux_debian` can find
  `/usr/lib/SYSLINUX/mbr.bin`.
- Init startup — kernel and systemd-sysv pull in /sbin/init transitively; if the
  kernel boots but init doesn't run, check that `init` (the symlink package) is
  in the closure.
- SSH not running on first boot — Debian's `openssh-server` package enables
  itself via systemd preset; verify with `systemctl status ssh` on the device
  console.

## Known limitations

Three structural properties of the debian backend that users will encounter
regardless of yoe version. None is a bug or a transitional gap — all are
deliberate trade-offs that the architecture chose, and changing any one is a
substantial follow-up rather than routine work.

- **`mmdebstrap --variant=custom` installs the resolved closure, not Debian's
  base system.** To keep images content-addressed and minimal, yoe tells
  `mmdebstrap` to install exactly the closure yoe resolved (`--variant=custom`)
  rather than the implicit Essential / Priority:required base that `debootstrap`
  and the stock mmdebstrap variants pull in. An image therefore contains only
  what its closure names plus apt's hard-dependency expansion — there is no
  rescue userland, no `Priority: standard` set, unless an image lists it. Three
  consequences follow. All are handled automatically during assembly, but they
  are visible in the log and shape what an image must declare:
  - **The Essential / required userland is seeded explicitly.** Debian
    maintainer scripts assume `sed`, `grep`, `awk`, `find`, `gzip`, `login`, and
    the rest of the Priority:required toolset are present — `libc6`'s own
    preinst calls `sed`. Custom variant pulls none of it implicitly, so the
    image class seeds a fixed Essential + required baseline into every Debian
    image's closure. A package whose maintainer script reaches for a tool
    outside that baseline must add the tool to the image. The baseline also
    includes `udev` and `kmod`, which a systemd system needs but custom variant
    drops: `systemd` only _Recommends_ `udev`, and assembly runs with Recommends
    disabled, so without seeding it the rootfs ships no `systemd-udevd`. That
    leaves every `.device` unit perpetually inactive, and a serial console hangs
    on `dev-<tty>.device` instead of reaching a login prompt; `kmod` supplies
    `/sbin/modprobe` for runtime module loading.
  - **usr-merge is established before extraction.** Custom variant skips the
    `/bin`→`/usr/bin` merge the normal variants set up. A setup-hook creates the
    merged-usr symlinks against the empty target before any package unpacks;
    without it the `usrmerge` package's post-hoc conversion fails inside the
    build chroot.
  - **Configuration is one unordered `dpkg --install --force-depends` pass.**
    The whole closure unpacks and then configures, instead of the staged
    configure-essentials-first bootstrap `debootstrap` performs. The assembly
    log shows benign `ignoring pre-dependency problem` warnings (e.g. systemd
    Pre-Depends on a `libc6` that is unpacked but not yet configured) — dpkg
    proceeds and the configure pass resolves them. A tool provided through
    `update-alternatives` (notably `awk` via `mawk`) can be needed before its
    provider's postinst registers the link, so the image class pre-stages those
    links. A final `dpkg-query` audit fails the build loudly if any package is
    left half-configured, so a genuinely broken closure never ships as a subtly
    incomplete image.

- **Some upstream `.deb` postinsts assume network access.** yoe runs
  `mmdebstrap` under `--network=none` for hash stability and reproducibility — a
  configure pass that reaches out to a DNS resolver, a metadata server, or a
  license-prompt download produces different output depending on what's
  reachable when, which would break the content-addressed cache. Packages whose
  postinsts do this (`cloud-init` provisioning, telemetry agents, license-prompt
  downloaders, a small set of enterprise-software installers) fail loudly during
  image assembly. The narrow set this affects isn't appropriate for embedded
  images anyway; replace with a from-source `module-core` unit if equivalent
  functionality is needed, or carry the package and provide the configuration it
  would have fetched via the project rootfs overlay.

- **One Debian suite per project, enforced at evaluation.** Every
  `apt_feed(...)` call in a project must agree on its `suite` kwarg; the
  resolver errors at load time if it sees `bookworm` and `trixie` declared in
  the same project. The constraint exists because the toolchain container
  (`@module-debian//containers/toolchain-debian-13`) pins one Debian release,
  and source units built against that toolchain's headers/libs can't safely mix
  with prebuilt packages from a different release's libc. Multi-suite support
  would require a suite axis in the toolchain cache key and parallel toolchain
  containers per suite — feasible but out of scope today. For most projects this
  is the correct constraint: a fleet runs one Debian release at a time.
