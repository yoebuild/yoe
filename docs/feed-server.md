# Feed server and `yoe deploy`

The dev loop for installing in-progress builds onto a running yoe device. Three
commands, layered:

- **`yoe serve`** — long-lived HTTP feed for the project's apk repo, advertised
  via mDNS so devices and `yoe deploy` find it without configuration.
- **`yoe device repo {add,remove,list}`** — configure `/etc/apk/repositories` on
  a target device so `apk add` from the device pulls from your dev feed.
- **`yoe deploy <unit> <host>`** — build, ship, and install a unit on a running
  device in one command. Pulls the unit and all its transitive deps via apk on
  the device side, so dependency resolution mirrors production OTA.

![Feed server topology](assets/feed-server-topology.png)

The model is **pull**, not push. Every install — image-time, on-device OTA, and
the dev loop — uses the same apk repo, the same `APKINDEX.tar.gz`, and the same
signing key. Adding a new runtime dep to a unit doesn't require updating deploy
machinery; apk on the device resolves it.

## Trust

apks and APKINDEX are signed by the project key (`docs/signing.md`). Every yoe
device has the matching public key in `/etc/apk/keys/` via `base-files`. apk
verifies signatures unconditionally, so the HTTP transport is plain — package
integrity is enforced at the package layer, not the network layer.

For production OTA, layer HTTPS via reverse proxy (`docs/on-device-apk.md`).

## Common workflows

### One-time setup on a fresh device

A device that was just flashed with an image built by your project needs nothing
— the public key is already in `/etc/apk/keys/`. Configure the repo:

```bash
# Dev host, in your project dir
yoe serve &

# In another terminal — autodiscovers the running serve via mDNS
yoe device repo add dev-pi.local
```

After this, on the device:

```bash
apk update
apk add htop strace gdb         # any unit your project builds is now installable
```

If the device was flashed from someone else's image (no project key), pass
`--push-key`:

```bash
yoe device repo add dev-pi.local --push-key
```

### Iterating on a single unit

```bash
yoe deploy myapp dev-pi.local
```

Builds `myapp`, starts an ephemeral feed (or reuses your running `yoe serve` if
it's advertising the same project), ssh's to the device, and runs
`apk add --upgrade myapp`. Transitive deps are resolved on the device.

A `# >>> yoe-dev` … `# <<< yoe-dev` block in `/etc/apk/repositories` on the
target is left in place after deploy — same block `yoe device repo add` would
have written. So the first deploy to a fresh device doubles as the persistent
feed config.

### Multiple devices on a LAN

Run `yoe serve` once on the dev host. Each device runs `yoe device repo add`
once. After that, `apk update && apk upgrade` on each device picks up new
builds.

### Tearing it down

```bash
yoe device repo remove dev-pi.local
```

Strips the `# >>> yoe-dev` block from `/etc/apk/repositories`. The device falls
back to whatever else is configured (typically nothing, in dev).

### Inspecting the device's repo config

```bash
yoe device repo list dev-pi.local
```

Cats `/etc/apk/repositories`, prefixed with the source filename. (Also reads
`/etc/apk/repositories.d/*.list` if present, though apk-tools 2.x does not read
those itself — they're informational only.)

## Command reference

### `yoe serve`

```
yoe serve [--port PORT] [--bind ADDR] [--no-mdns] [--service-name NAME]
```

- `--port` — TCP port. Default `8765`. Pinned (not random) so the URL written by
  `yoe device repo add` stays valid across `yoe serve` restarts.
- `--bind` — listen address. Default `0.0.0.0` (LAN-visible).
- `--no-mdns` — skip the mDNS advertisement (multicast-hostile networks).
- `--service-name` — mDNS instance name. Default `yoe-<project>`.

### `yoe device repo add`

```
yoe device repo add <[user@]host[:port]> [--feed URL] [--name NAME]
                                          [--push-key] [--user USER]
```

- `<[user@]host[:port]>` — ssh destination. Examples: `dev-pi.local`,
  `pi@dev-pi.local`, `localhost:2222` (QEMU), `pi@dev-pi.local:2200`.
- `--feed URL` — explicit URL. If omitted, browses mDNS for `_yoe-feed._tcp` on
  the LAN; errors clearly on 0 or >1 matches.
- `--name NAME` — name suffix for the marker block written into
  `/etc/apk/repositories` (`# >>> yoe-<name>` … `# <<< yoe-<name>`). Default
  `yoe-dev`.
- `--push-key` — copy the project signing pubkey to `/etc/apk/keys/` on the
  target before configuring.
- `--user USER` — default ssh user when the target spec has no `user@` prefix.
  Default `root`. ssh shells out to the user's `ssh` so `~/.ssh/config`,
  ssh-agent, known_hosts, and jump hosts all work.

### `yoe device repo remove`

```
yoe device repo remove <[user@]host[:port]> [--name NAME] [--user USER]
```

Idempotent — missing file is success.

### `yoe device repo list`

```
yoe device repo list <[user@]host[:port]> [--user USER]
```

### `yoe deploy`

```
yoe deploy <unit> <[user@]host[:port]> [--user U] [--port P]
                                        [--host-ip IP] [--machine M]
```

- `<unit>` — must resolve to a non-image unit. Image targets error with a
  pointer to `yoe flash`.
- `<[user@]host[:port]>` — ssh destination, same syntax as `device repo add`.
- `--port` — feed port (default `8765`, same as `yoe serve`).
- `--host-ip` — advertise this IP to the device instead of `<hostname>.local`.
  Use when mDNS resolution fails on the device.
- `--machine` — target machine override.

## Constraints

- mDNS doesn't cross subnets. Cross-subnet deploys need `--feed URL` or
  `--host-ip`.
- A pinned port `8765` collides if something else on the dev host is using it —
  pass `--port` to `yoe serve` and `yoe deploy` to override.
- The dev host needs avahi / systemd-resolved running for `<hostname>.local` to
  resolve from the device. Most Linux distros ship this.
- Concurrent deploys against the same project: one runs the ephemeral feed (or
  reuses `yoe serve`), the other will see the same URL via mDNS reuse. Truly
  parallel ephemeral feeds for the same project on the same dev host collide on
  port `8765`.
