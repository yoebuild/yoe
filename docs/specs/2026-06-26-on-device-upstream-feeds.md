# On-device upstream feeds (experimentation)

**Date:** 2026-06-26 **Status:** Implemented (build/boot verification pending) —
see [the plan](../plans/2026-06-26-001-on-device-upstream-feeds-plan.md)

## Problem

A booted yoe device can install and upgrade packages from its **project repo**
(the signed feed yoe publishes; see [on-device-apk.md](../on-device-apk.md) and
`yoe device repo add`). What it cannot do is reach straight to the **upstream
distro** mirror — `dl-cdn.alpinelinux.org/alpine/v3.21` for an Alpine image,
`deb.debian.org/debian bookworm` for a Debian one — to grab a package that the
project never built.

During development and one-off bring-up that gap is felt constantly: you want
`htop`, `strace`, `tcpdump`, or some library on the device _right now_ to debug
something, and the only path today is to add a unit, rebuild, republish, and
`apk upgrade`. The upstream package already exists, prebuilt, for the exact
branch/arch the image targets. yoe already knows that branch/arch/distro at
build time (it consumed the same feed on the build host via `alpine_feed` /
`apt_feed`). The developer experience we want is one command away: run the
shipped enable script, then `apk add htop@upstream`.

This is an **experimentation / one-off mechanism**, never the production update
path (that stays the signed project repo plus full-image A/B, the existing OTA
story in `on-device-apk.md`).

The whole capability is **one package** — a `upstream-feeds` unit that carries
the enable script _and_ the upstream signing keys, and ships dormant. Nothing is
configured until the developer runs the script. This keeps the everyday device
quiet (an always-present upstream source would make every routine `apk update` /
`apt update` fetch the full upstream index over the network — see
[How update behaves](#how-apk-update--apt-update-behave)), and it makes the
production story trivial: **don't include the unit and the entire apparatus —
script and keys — is absent.** When the script is run, the feed is added _held
back_ (see [Held-back by default](#held-back-by-default)) so it never
participates in a normal install or upgrade.

## Why this is simple

Everything the device needs is already present or trivially shippable, and it
all fits inside one unit:

- `apk` / `apt` ship in `dev-image`.
- A unit ships files into the rootfs via its data tar — exactly how `base-files`
  already lands the _project_ signing key in `/etc/apk/keys/`. The
  `upstream-feeds` unit ships the upstream keys the same way.
- The mirror URL, branch/suite, sections, and keyring all already live in the
  distro module that built the image (`alpine_feed(...)` / `apt_feed(...)` plus
  `keys/`). A unit homed in that module reads them locally — no cross-module
  plumbing, no Go, no image-assembly hooks.

So there is no executor change and no image-assembly key staging: the unit is
self-contained. The only work outside the unit is an unrelated cleanup the
investigation surfaced (see
[Cleaning up build-machine-only feeds](#cleaning-up-build-machine-only-feeds)).

## Design

A **self-contained `upstream-feeds` companion unit in each distro module** —
`module-alpine`, `module-debian`, `module-ubuntu` — alongside the feed it
serves. This is the pattern the distro modules already use for their
`units/*-enable.star` service companions; no new class or abstraction. (A shared
`module-core` class was considered and rejected as premature: there are only two
distinct scripts, and yoe's install-step resolution forbids a module-core file
from shipping a distro module's keys anyway — so a class couldn't even own the
unit cleanly. If the apt script's duplication across the two apt modules ever
becomes a problem, a feed-emitted companion is the dedup path — see
[Open questions](#open-questions).)

Each unit ships two things into the rootfs, both **dormant**:

- the enable/disable script at `/usr/sbin/yoe-enable-upstream-feeds` — a plain
  committed shell script with the module's mirror/branch/suite values written in
  directly (kept in sync with the module's `*_feed(...)` call, same module, same
  change), and
- the upstream key(s) at a **non-trust** path (`/usr/share/yoe/upstream-keys/`),
  _not_ in `/etc/apk/keys/` or `/etc/apt/trusted.gpg.d/`. (A unit can only ship
  files that live in its own asset directory, so each module keeps a copy of its
  feed key beside the unit.)

All three units share the name `upstream-feeds`, so each **must carry a
`distro = "<distro>"` tag**. An untagged unit is distro-neutral (visible to
every distro), so three untagged same-named units would collide under module
priority — last-module-wins would resolve one variant (e.g. Ubuntu's
`dpkg`-based script) into _every_ image, including Alpine. The tag puts each in
its own distro bucket, exactly like the feed-materialized packages (`bash`
exists in both the alpine and debian feeds without colliding), so an image
resolves the variant matching its effective distro. `dev-image` can therefore
list `upstream-feeds` once in its distro-neutral `artifacts`.

Running the script **activates** the feed and its key together (held back); a
matching `--disable` removes both. Until it runs, there are zero active upstream
trust anchors and no upstream source — the unit is inert at rest. After enable,
the user pulls a package by name (`apk add <pkg>@upstream` /
`apt install -t <suite> <pkg>`); nothing else changes.

Production opt-out needs no toggle: an image that omits `upstream-feeds` from
its artifacts carries neither the script nor the keys.

### Held-back by default

This is the load-bearing decision. The device's installed-package database
describes **yoe units** — a source-built, often _monolithic_ userland — not the
distro's actual package set. Letting an upstream feed compete on equal footing
with that base would let `apk upgrade` / `apt upgrade` silently replace
yoe-built packages with prebuilts (upstream `openssl` is usually newer than the
pinned one), drifting the whole userland away from any buildable artifact. Both
backends have a native mechanism to prevent exactly that:

**apk — repository tags (pinning).** apk has no numeric priority; it has tags. A
tagged repo is drawn from _only_ when a package is requested as `pkg@tag` (or to
satisfy a dependency of such a package). A plain `apk add` / `apk upgrade`
ignores tagged repos entirely.

```
# /etc/apk/repositories
<feed>/<proj>/alpine                                    # project repo (default set)
@upstream https://dl-cdn.alpinelinux.org/alpine/v3.21/main
@upstream https://dl-cdn.alpinelinux.org/alpine/v3.21/community
```

The user runs `apk add htop@upstream`. The base set is structurally untouchable
by the upstream feed — no `--no-upgrade` discipline required, the tag enforces
it.

**apt — pin priority.** apt has numeric per-origin pinning in `preferences.d/`.
The enable script writes a `sources.list.d` entry whose key is scoped to that
one source via `signed-by` (pointing at the unit's shipped keyring — no global
trust anchor), and pins the origin below the default 500:

```
# /etc/apt/sources.list.d/yoe-upstream.list
deb [signed-by=/usr/share/yoe/upstream-keys/debian-archive-keyring.gpg] \
    http://deb.debian.org/debian trixie main

# /etc/apt/preferences.d/yoe-upstream.pref
Package: *
Pin: origin deb.debian.org
Pin-Priority: 100
```

`100` means available if explicitly named (`apt install -t trixie foo`) but
never used for `apt upgrade` and never preferred over the project repo. The
project repo stays at the default 500 and wins.

The apt source is plain **http**, not https: apt's trust comes from the
GPG-signed `InRelease` verified against the `signed-by` keyring, not from TLS,
so http is the standard transport for a signed repo and avoids depending on the
device's CA store for apt's HTTPS method (the same reason Ubuntu defaults to
`http://archive.ubuntu.com`). The apk side keeps `https` — apk's TLS works on
the Alpine image, and the signed `APKINDEX` is the trust anchor regardless.

### Branch must match the build

The feed the enable script writes **uses the branch/suite from the image's
build**, never "latest". module-alpine pins `branch = v3.21`; the on-device feed
must point at `v3.21`, not `edge`. A device pointed at a newer release than its
musl/glibc was built against can pull a package needing libc symbols the running
system lacks → runtime breakage. Because the value is the same Starlark constant
the `*_feed(...)` call uses (carried into the unit at build time), the script
cannot drift from what the image was built against; there is no user-facing
branch knob.

### Trust anchors

The unit ships the upstream keys to a **non-trust** path
(`/usr/share/yoe/upstream-keys/`), and the enable script activates them only
when run: apt references the key via `signed-by` (scoped to the one upstream
source, never a global anchor); apk, which has no per-repo scoping, copies the
`*.rsa.pub` into `/etc/apk/keys/` and `--disable` removes it. So there are two
levels of opt-out:

- **Exclude the unit** — no script, no keys, nothing. This is the production
  default, and needs no toggle: just don't list `upstream-feeds` in the image.
- **Include the unit, don't run the script** — keys sit dormant in `/usr/share`,
  untrusted; no upstream source exists. Zero active trust anchors at rest.

Only a deliberate `yoe-enable-upstream-feeds` makes the device trust and reach
upstream, and `--disable` fully reverses it. This is the same upstream-trust
trade-off discussed in
[mirror-alpine-keep-keys](2026-05-18-mirror-alpine-keep-keys.md), but contained:
the anchor exists only while a developer has explicitly opted in.

### What pinning does not fix

Tags/priority govern _selection_, not _conflict resolution_. The sharp edge
documented in [apk-passthrough.md](../apk-passthrough.md) (the docker-openrc /
util-linux worked example) still applies: yoe builds a monolithic `util-linux`
shipping `libmount.so.1`/`libblkid.so.1`, while Alpine splits those into
separate apks. If `foo@upstream` transitively needs Alpine's split `libmount`,
the install fails on a file-path collision with yoe's util-linux. `-dev`
packages have the same wall (no `-dev` split on the yoe side).

For experimentation this is an acceptable "it errored, pick a different package
or build a unit" outcome — and it is precisely _why_ the feature is scoped to
experimentation and not production. The docs surface this failure mode up front
so a file-conflict or missing-package error is self-diagnosing rather than
mysterious.

### One narrow thing it does _better_

Direct upstream `apk add` runs apk's real on-target trigger machinery. yoe's
image-assembly path currently skips install-time triggers (`apk-passthrough.md`,
"No triggers execution"), so for a package whose `.trigger` matters, an
on-device upstream install is actually more faithful than the build-host repack
path. Minor, but worth noting as a point in favor for the dev loop.

### How `apk update` / `apt update` behave

The held-back tag/priority governs **selection at install time**, not whether an
index is **fetched**. Once the enable script has added the upstream feed, an
`apk update` (or `apk add --update-cache`) / `apt update` refreshes _every_
configured source — project repo and upstream both. Consequences:

- The upstream index is large and lives on the network, so the refresh now needs
  upstream connectivity and pulls tens of MB. On apt an unreachable upstream
  prints `Failed to fetch … Some index files failed to download` and exits
  non-zero, while still updating the sources it could reach; apk is softer (a
  failed repo is a warning).
- This is exactly why the feed is opt-in via the script rather than always-on. A
  device that never runs the script has only the project repo configured, so its
  routine update stays small and offline-friendly.
- Escape hatch when both are configured but you only want the project repo: apt
  can target a single source list (`apt update -o Dir::Etc::SourceParts=...`);
  apk can be pointed at a single repository for a one-off `add`.

Signature verification works on the refreshed upstream index because the unit
shipped the key and the enable script activated it — the upstream HTTP source is
properly signed, unlike the local project mirror which is consumed with
`trusted=yes` (see
[Cleaning up build-machine-only feeds](#cleaning-up-build-machine-only-feeds)).

## Surface

- **Per-distro companion units** — `module-alpine/units/upstream-feeds.star`,
  `module-debian/units/upstream-feeds.star`,
  `module-ubuntu/units/upstream-feeds.star`. Each is a plain `unit()` whose
  build task `install_file`s the committed enable script to
  `/usr/sbin/yoe-enable-upstream-feeds` (mode 0755) and its key copy to
  `/usr/share/yoe/upstream-keys/`. Self-contained; no shared class. The apt
  script recurs in the two apt modules (the accepted cost of homing the unit
  with its keys — a feed-emitted variant could later dedupe it, see
  [Open questions](#open-questions)).
- **`dev-image` inclusion** — `upstream-feeds` goes in `dev-image`'s
  distro-neutral `artifacts` list (alongside `curl`, `htop`, …): each distro
  module ships its own `upstream-feeds`, so the resolver picks the right one per
  image. Base/ssh images omit it. Multi-distro projects (host image + deployable
  containers can differ; distro is per image-bearing artifact) get the right
  unit per image automatically.
- **Idempotence** — enable edits in place (writes each line/file once); disable
  removes the source, the pin, and the activated key. A `yoe device` verb may
  later wrap push+run as a convenience.

No `module-core` class, no executor change, no image-assembly key staging: the
units are self-contained in the distro modules.

## Cleaning up build-machine-only feeds

This is a **separate, pre-existing bug** the investigation surfaced —
independent of upstream feeds, and worth fixing on its own merits. It is the one
piece that must live in image assembly (`classes/image.star`) rather than the
unit, because the offending file is written by mmdebstrap, which assembly
drives.

The device should boot with **no feed that only made sense on the build
machine**. Today that invariant holds for apk but not apt.

- **apk is already clean.** Image assembly consumes the local repo with
  `apk add -X $REPO` — the repo is a command-line argument to the build step,
  not persisted. The booted device's `/etc/apk/repositories` is `base-files`'
  commented template, nothing more.
- **apt persists a build path.** mmdebstrap is handed
  `deb [trusted=yes] copy:$REPO $SUITE main`
  (`modules/module-core/classes/image.star`) and, by default, **writes that line
  into the target's `/etc/apt/sources.list`**. `$REPO` is a build-host path
  (`/project/repo/<proj>/debian`) that does not exist on the booted device, so
  the source is simply invalid — every `apt update` errors on it
  (`copy-stat (2: No such file or directory)`). It is a leftover build artifact,
  not a working device feed.

Bring apt to parity with apk: the local repo is an **input to assembly, not a
device feed**. After mmdebstrap, image assembly removes the build-path source
from the rootfs and replaces `/etc/apt/sources.list` with a commented template
mirroring `base-files`' apk `repositories` file (how to add the project feed,
where the layout lives). Mechanism: an mmdebstrap `--customize-hook` or a
post-run rewrite of `$DESTDIR/rootfs/etc/apt/sources.list`. The project feed
then reaches a device the same way Alpine's does — `yoe device repo add` writing
`sources.list.d/yoe-dev.list` — and the upstream feed via this spec's enable
script.

It also makes the upstream-feed story more legible: with the stray `copy:`
source gone, the only thing the enable script adds is the upstream feed, and the
only thing `apt update` talks to is feeds that are actually valid on the device.
But the fix stands alone — it should land whether or not upstream feeds ship,
and could be tracked as its own change.

## Scope

In scope: a `upstream_feeds` class in `module-core` and thin `upstream-feeds`
units in `module-alpine` / `module-debian` / `module-ubuntu` that ship the
held-back enable/disable script plus their upstream keys (dormant, activated on
enable); `dev-image` inclusion; feed values sourced from each module's existing
`*_feed(...)` constant; docs (a new `docs/on-device-upstream-feeds.md` and a
cross-link from `on-device-apk.md`). Separately: removal of the build-machine
`copy:` feed from the device rootfs (apt parity with apk).

Out of scope:

- Any production-update role. Production stays project repo + full-image A/B,
  and simply omits the `upstream-feeds` unit.
- Resolving subpackage-split / file-path conflicts or the missing-`-dev` wall —
  documented as a known limitation, not fixed here.
- A numeric-priority abstraction over apk (apk has none; tags are the model).
- On-device _building_ against upstream `-dev` packages.

## Open questions

- **Hand-written units vs. feed-emitted.** The apt enable instantiation recurs
  in `module-debian` and `module-ubuntu` (no shared parent). Acceptable as two
  thin calls to the shared class now; a later option is to have `apt_feed(...)`
  / `alpine_feed(...)` emit the companion unit (the feed already holds url +
  keys), deduping it at the cost of more Go. Defer unless the duplication
  becomes a problem.
- **Enable surface.** The on-image script is the offline-capable primitive. Is a
  `yoe device feed enable-upstream` verb (extending `yoe device repo …`,
  push+run over the deploy channel) worth adding on top, or is the on-image
  script enough for the experimentation scope?
- **What replaces the stripped apt `sources.list`** (cleanup). Empty file vs. a
  commented template mirroring `base-files`' apk `repositories`. The commented
  template is the apk-parity answer and self-documents how to add the project
  feed; confirm nothing in image assembly or first boot depends on a populated
  `/etc/apt/sources.list`.
- **Interaction with the repack/re-sign model.** The project repo re-signs
  upstream apks with the project key (`RepackAPK`); the upstream feed is
  verified against upstream keys. A package available from both is selectable
  two ways (`foo` from the project repo, `foo@upstream` from upstream). Confirm
  that is merely redundant, not confusing, on-device.
- **Per-distro held-back idiom parity.** apk tags and apt pin-priority both
  achieve "present but never auto-used," but they read differently
  (`pkg@upstream` vs. `apt install -t <suite> pkg`). Confirm the docs make the
  per-distro invocation obvious so a developer on either backend reaches for the
  right one without surprise.
