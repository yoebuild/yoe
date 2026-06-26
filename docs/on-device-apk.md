# On-Device Package Management (apk / Alpine)

`apk-tools` ships in `dev-image` and any other image that includes it, so booted
yoe systems can install, upgrade, and inspect packages against the project's
signed repo using stock Alpine `apk` commands.

This page covers the **apk** path used by Alpine/musl images. yoe also produces
`.deb` packages for Debian/Ubuntu/glibc images, where the on-device flow is the
same shape (point at the signed project repo, refresh the index,
install/upgrade) but driven by `apt`/`dpkg` against an apt-format repository.
The concepts below — signed repo, index refresh, OTA rebuild-publish-upgrade,
non-atomic in-place upgrades — carry over; the commands and on-disk paths
differ.

## What's already on the device

After a successful `yoe build dev-image && yoe run dev-image`:

- `/sbin/apk` — the apk-tools binary.
- `/lib/apk/db/` — the installed-package database, populated at image assembly
  time via `apk add`.
- `/etc/apk/keys/<keyname>.rsa.pub` — the project's signing public key, shipped
  by `base-files`. apk uses it to verify signatures on every
  `add`/`upgrade`/`update` without any flag-passing on your part.
- `/etc/apk/repositories` — a commented-out template. **You override this** with
  your project's repo URL before doing anything live.

## Pointing at a repository

Edit `/etc/apk/repositories` and add a single line — one repo per line. A few
common shapes:

```
# Project repo served over HTTPS by an nginx behind your CA
https://repo.example.com/myproj

# Project repo served by a plain HTTP server on the LAN
http://10.0.0.1/repo/myproj

# Local filesystem path (e.g., bind-mounted USB stick or sshfs)
/var/cache/yoe/repo
```

Then update the index cache:

```
$ apk update
```

Yoe-built repos use Alpine's standard `<repo-root>/<arch>/APKINDEX.tar.gz`
layout, so `apk` picks the right arch automatically — point the repositories
file at the _root_, not at the per-arch subdirectory.

### Pointing at a yoe-served feed

For development, run `yoe serve` on your build host and configure the device
with `yoe device repo add <host>`. See [feed-server.md](feed-server.md) for the
full dev-loop walkthrough.

### Pulling a package from the upstream distro

To grab a package the project never built — straight from the upstream Alpine or
Debian/Ubuntu mirror, for experimentation on a dev image — see
[on-device-upstream-feeds.md](on-device-upstream-feeds.md).

## Installing and upgrading

Once a repository is wired up:

```
$ apk add htop          # install one package
$ apk add --update vim  # refresh index, then install
$ apk upgrade           # upgrade everything to the latest available
$ apk del strace        # remove a package
$ apk info -vv | head   # list installed packages
$ apk verify            # re-verify every installed package's hashes
```

All of these run with signature verification on. If apk reports "BAD signature"
or "untrusted", the public key under `/etc/apk/keys/` doesn't match the key the
repo's apks were signed with. See `docs/signing.md` for the key-rotation flow.

## OTA flow (rebuild → publish → upgrade)

The recommended OTA path for yoe-built devices:

1. **Bump versions.** Edit one or more units' `version =` (or `release =` if
   just rebuilding the same source) on your dev host.
2. **Build the new apks.** `yoe build <unit>` produces the new `.apk` files in
   `<projectDir>/repo/<project>/<arch>/` and refreshes `APKINDEX.tar.gz`. Both
   are signed with the project key.
3. **Sync to your hosting.** Copy the entire `<projectDir>/repo/<project>`
   subtree to wherever you serve it from — e.g., a static-site bucket, an nginx
   vhost, or a release server. The on-disk layout is already correct; no
   transformation needed.
4. **On-device upgrade.** `apk update && apk upgrade`.

## Hosting the repo over HTTP/HTTPS

Any static file server works. nginx example:

```nginx
server {
    listen 443 ssl;
    server_name repo.example.com;
    ssl_certificate     /etc/letsencrypt/live/repo.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/repo.example.com/privkey.pem;

    root /srv/yoe-repos;
    autoindex off;

    # Tighten cache headers — APKINDEX.tar.gz changes on every publish,
    # but individual .apk files are content-addressed by version+release
    # and never change once published.
    location ~ /APKINDEX\.tar\.gz$ {
        add_header Cache-Control "no-cache";
    }
    location ~ \.apk$ {
        add_header Cache-Control "public, max-age=31536000, immutable";
    }
}
```

Drop your project's repo subtree under `/srv/yoe-repos/myproj/` and point
`/etc/apk/repositories` at `https://repo.example.com/myproj`.

## Constraints worth knowing

- **Kernel upgrades need a reboot.** apk doesn't restart anything; a new
  `linux-*` apk replaces files in `/boot` and the running kernel keeps running
  until you reboot.
- **No automatic rollback.** If an upgrade leaves the system unbootable, there's
  no built-in A/B rollback in this layer. For atomic-rootfs workflows
  (RAUC-style A/B partitioning, or btrfs-snapshot rollback), layer them above
  the apk repo — apk handles the package contents, the rootfs strategy handles
  atomicity.
- **In-place upgrade is non-atomic.** apk extracts each package's files
  individually. A power loss during `apk upgrade` can leave the rootfs in a
  half-upgraded state. For deployments where that's not OK, ship upgrades as
  full image artifacts via flash/A-B and use the apk repo for development
  iteration only.
- **No remote network at install time during image build.** Image assembly runs
  `apk add --no-network` against the local repo. This is intentional: build
  artifacts must be reproducible from the project tree alone.
