# On-Device Upstream Feeds (experimentation)

A booted yoe device installs from its **project repo** — the signed feed yoe
publishes (see [on-device-apk.md](on-device-apk.md)). During development you
often want a package the project never built — `htop`, `strace`, a stray library
— without adding a unit and rebuilding. `dev-image` ships a dormant enabler that
lets you pull such a package straight from the **upstream distro mirror**
(`dl-cdn.alpinelinux.org` for Alpine, `deb.debian.org` / Ubuntu for the apt
distros).

This is an **experimentation / one-off mechanism, not a production update
path**. Production devices keep updating from the signed project repo and
full-image A/B. Mixing upstream prebuilt packages into yoe's source-built
userland has sharp edges (see [Limitations](#limitations)); the feature is
scoped accordingly.

## How it ships

`dev-image` includes a small `upstream-feeds` package that lands two things on
the device, both **dormant**:

- `/usr/sbin/yoe-enable-upstream-feeds` — the enabler script.
- `/usr/share/yoe/upstream-keys/` — the upstream distro's signing key(s), in a
  **non-trust** location. Nothing trusts them until you opt in.

Until you run the script, the device has no upstream source configured and no
active upstream trust anchor — the package just sits there. An image that omits
`upstream-feeds` (any base or production image) carries neither the script nor
the keys.

## Enabling

Run the enabler, then install by explicit reference:

```
# Alpine
$ yoe-enable-upstream-feeds
$ apk update && apk add htop@upstream

# Debian / Ubuntu
$ yoe-enable-upstream-feeds
$ apt update && apt install -t <suite> htop
```

`yoe-enable-upstream-feeds --disable` reverses it: the upstream source and pin
are removed, and the keys return to untrusted.

## Held back by default

The enabler adds the upstream feed **held back**, so it can never replace a
package in yoe's source-built base. The base is what makes a yoe image
reproducible; a blanket upgrade pulling newer upstream builds over it would
defeat that. Each backend enforces this natively:

- **Alpine** — the upstream repo is added with an apk **tag** (`@upstream`). A
  plain `apk add`/`apk upgrade` ignores tagged repos entirely; only an explicit
  `apk add <pkg>@upstream` reaches upstream.
- **Debian / Ubuntu** — the source is written to
  `/etc/apt/sources.list.d/yoe-upstream.list` (key scoped to that one source via
  `signed-by`, so no global trust anchor) and pinned to `Pin-Priority: 100` in
  `/etc/apt/preferences.d/`. Priority below the default 500 means it is
  installable when named (`apt install -t <suite> <pkg>`) but never
  auto-selected and never used by `apt upgrade`. The project repo always wins.

So `apk add <pkg>@upstream` / `apt install -t <suite> <pkg>` pulls exactly the
named package; nothing else on the device changes.

## Branch matches the build

The enabler points at the **same release the image was built against** — Alpine
`v3.21`, Debian `trixie`, Ubuntu `resolute` — never "latest". A device pointed
at a newer release than its musl/glibc was built for could pull a package
needing libc symbols the running system lacks. The values are baked into the
script to match the module's feed, so there is no release knob to get wrong.

## What `apk update` / `apt update` do once enabled

The held-back tag/pin governs _which package is selected_, not _which indexes
are fetched_. Once the upstream feed is enabled, an `apk update` / `apt update`
refreshes **every** configured source — project repo and upstream both:

- The upstream index is large and lives on the network, so the refresh now needs
  upstream connectivity and pulls more data. On apt an unreachable upstream
  prints `Failed to fetch … Some index files failed to download` and exits
  non-zero while still updating the sources it could reach; apk treats a failed
  repo as a warning.
- A device that never runs the enabler has only the project repo, so its routine
  update stays small and offline-friendly. This is why the feed is opt-in rather
  than always present.
- To refresh only the project repo when both are configured, target a single
  source list — e.g. `apt update -o Dir::Etc::SourceParts=/dev/null`.

Signature verification succeeds because the enabler activated the upstream key
(apt via `signed-by`, apk by copying it into `/etc/apk/keys/`).

The **apt** feeds (Debian/Ubuntu) are fetched over plain **http**: apt derives
trust from the GPG-signed `InRelease`, not from the transport, so http is the
standard way to consume a signed repo — it gives up only confidentiality (an
observer can see which package names you fetch) and avoids depending on the
device having a working TLS CA store for apt's HTTPS method. This matches
Ubuntu's own default of `http://archive.ubuntu.com`. The **apk** (Alpine) feed
uses `https` — apk's TLS works on the Alpine image, and the signed `APKINDEX` is
the trust anchor regardless.

## Limitations

- **File-path conflicts with the source-built base.** yoe builds some packages
  monolithically where the distro splits them — for example yoe's `util-linux`
  ships `libmount.so.1`/`libblkid.so.1` directly, while Alpine splits them into
  separate packages. If an upstream package transitively needs the split form,
  the install fails on a file-path collision. That is an expected
  "pick-a-different-package" outcome for experimentation; it is the main reason
  this path is not for production. See [apk-passthrough.md](apk-passthrough.md)
  for the worked example.
- **No `-dev` packages.** yoe doesn't split headers out of its source-built
  libraries, so on-device building against upstream `-dev` packages hits the
  same wall.
- **Networked verification.** A live upstream install needs connectivity to the
  mirror; there is no offline path for packages the project didn't build.

## The device carries no build-machine feed

Independently of this feature, a yoe device's package manager is configured with
only feeds that are valid on the device. The image is assembled from a local
build-time mirror that does not exist on the booted device, so that mirror is
**not** baked into the device's source list — `/etc/apk/repositories` is a
commented template, and `/etc/apt/sources.list` is likewise an empty commented
template. The project feed is added on-device with `yoe device repo add`, and
the upstream feed with `yoe-enable-upstream-feeds`.
