# module-ubuntu — wrapping prebuilt Ubuntu packages

`module-ubuntu` wraps prebuilt Ubuntu `.deb` files as yoe units, mirroring the
role `module-debian` plays for Debian and `module-alpine` plays for Alpine. A
unit fetches a binary `.deb` from a pinned Ubuntu release, verifies its SHA256
against the upstream-signed `Packages` catalog, and republishes it through yoe's
project repo. The unit's "build" is just extracting the deb's `data.tar` into
`$DESTDIR`.

The module lives at <https://github.com/yoebuild/module-ubuntu>. Open it to
browse the bootstrap keyring, the in-tree `Packages` snapshots, or to send a PR
adding a new feed/component.

Ubuntu shares Debian's apt/dpkg/glibc machinery, so this module is thin: it
declares feeds with the same `apt_feed()` builtin `module-debian` uses, ships an
Ubuntu/glibc build toolchain, and rides the shared `mmdebstrap` rootfs assembly,
`.deb` packaging, and on-device apt that yoe applies to the whole apt family.
Read [module-debian](module-debian.md) for the apt-family mechanics that aren't
repeated here; this page covers what is Ubuntu-specific.

## When to reach for it

The same "yoe builds the small stuff, the distro module ships the
hard-to-build complexity" rubric from [module-debian](module-debian.md) applies.
The only axis that picks Ubuntu over Debian is the target: choose `module-ubuntu`
when an image must match Ubuntu's userland, ABI, or vendor BSP expectations.
Source units in `module-core` compile against either glibc toolchain through the
`container = "toolchain"` virtual reference, so most of a closure is shared
regardless of which apt distro the image targets.

## Ubuntu is its own distro

`apt_feed(distro = "ubuntu", ...)` tags every materialized unit with
`Distro = "ubuntu"`, and the images set `distro = "ubuntu"`. That makes Ubuntu a
first-class distro in yoe's resolver — an Ubuntu image's closure sees only
Ubuntu-tagged units, so a single project can declare both `module-debian` and
`module-ubuntu` and the two never collide. Under the hood Ubuntu rides the shared
apt/dpkg/glibc backend; only the feed identity, suite, and mirror differ.

## Ubuntu release coupling

The suite pinned in `MODULE.star` (`_UBUNTU_SUITE`, tracking **Resolute Raccoon /
26.04 LTS** at the time of writing) **must** match the `FROM ubuntu:<release>`
line in `containers/toolchain-glibc/Dockerfile`. The same three couplings Debian
documents apply: the glibc ABI between toolchain headers and target runtime libs,
the per-release archive signing key in `keys/ubuntu-archive-keyring.gpg`, and the
full cache invalidation that a suite bump rolls through every source-unit hash.
Plan a suite bump for a rebuild cycle.

`module-debian` also ships a `toolchain-glibc` under the same unit name. A
project listing both modules gets deterministic last-module-wins shadowing on
that name; since both are interchangeable glibc/dpkg toolchains, either can
assemble either rootfs.

## Split mirrors (amd64 + arm64)

Ubuntu serves its architectures from two hosts: `amd64`/`i386` live on
`http://archive.ubuntu.com/ubuntu`, while `arm64` and the other ports arches live
on `http://ports.ubuntu.com/ubuntu-ports`. A single `apt_feed` spans both via the
optional `arch_urls` map, which overrides the base `url` per architecture — used
both when `yoe update-feeds` fetches each arch's `Packages` and when the build
downloads each `.deb`. The InRelease is fetched once from `url` for signature
verification; both mirrors ship an InRelease signed by the same Ubuntu archive
key. Debian's single mirror serves every arch and needs no such override.

## Networking

Ubuntu images use **NetworkManager** as the connection manager, the same choice
the Debian images make. It self-enables via its postinst, integrates with the
`systemd-resolved` already in the image for DNS, and is the foundation for adding
wifi and cellular to a device image later.

Ubuntu needs one extra piece Debian does not. Ubuntu's `network-manager` package
ships `/usr/lib/NetworkManager/conf.d/10-globally-managed-devices.conf`, which
restricts NetworkManager to wifi and cellular and delegates wired ethernet to
netplan. yoe images carry no netplan configuration, so out of the box the wired
NIC is brought up by nothing — NetworkManager sees the device and then ignores
it (`nmcli device status` shows it `unmanaged`). To restore the zero-config
wired-DHCP behavior Debian has by default, the Ubuntu images include the
`nm-manage-ethernet` unit. It lays down a higher-priority drop-in at
`/etc/NetworkManager/conf.d/15-yoe-manage-ethernet.conf` that re-includes
ethernet in NetworkManager's managed set, so the wired NIC auto-DHCPs with no
connection profile.

If a custom Ubuntu image lists `network-manager` but has no connectivity, confirm
`nm-manage-ethernet` is also in its closure. On the device console,
`nmcli device status` should show the ethernet device `connected` (not
`unmanaged`), and `ip addr` should show a DHCP lease.

## Verifying an Ubuntu image

End-to-end verification mirrors the Debian flow in
[module-debian](module-debian.md) — build the `ubuntu-base-image` (or
`ubuntu-dev-image`) fixture under `testdata/e2e-project/`, boot it under QEMU, and
confirm SSH lands over the forwarded port. The image is reachable on first boot
with no connection profile because `nm-manage-ethernet` brings the wired NIC up
automatically.

## Layout

```
MODULE.star                # apt_feed(distro="ubuntu", ...) declaration
feeds/main/<arch>/Packages # checked-in catalog snapshots (archive + ports)
keys/                      # bootstrap keyring + fingerprint allow-list
containers/toolchain-glibc # Ubuntu/glibc build toolchain (provides "toolchain")
classes/kernel.star        # ubuntu_kernel() -> linux-image-generic
images/                    # base-image, ssh-image, dev-image
```
