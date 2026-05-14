# Feed server and `yoe deploy` — design

## Goal

Give developers a fast inner loop for pushing in-progress builds to running yoe
devices, using `apk` end-to-end so dependency resolution Just Works on the
target. Three commands, layered:

```
yoe deploy <unit> <host>           — build + ephemeral feed + ssh + apk add
yoe serve [flags]                  — long-lived HTTP feed + mDNS advert
yoe device repo {add,remove,list}  — configure the device's apk repo file
```

`deploy` is the convenience wrapper. `serve` and `device repo` are the
primitives the wrapper composes — and are also useful standalone for the
LAN-fleet QA case where one dev host serves several devices.

The pull model (apk on the device fetching from the host) is preferred over push
(`scp` + `apk add /tmp/foo.apk`) specifically because it lets the device resolve
transitive dependencies against the same `APKINDEX.tar.gz` that production OTA
uses.

## Non-goals (v1)

- HTTPS, basic-auth, or mTLS on the feed. `apk` already verifies package and
  index signatures end-to-end (see `docs/signing.md`); transport security is not
  the integrity boundary. Layer HTTPS via reverse proxy if needed.
- A/B rollback or atomic upgrades — that's a separate rootfs-strategy layer,
  noted in `docs/on-device-apk.md`.
- Image (rootfs) deploys. `yoe deploy <image-unit> <host>` errors with "image
  targets are flashed, not deployed". Future: a meta-package mode that installs
  every package in an image's manifest into a running system.
- `yoe dev <unit> --feed` watch loop. Falls out for free once `yoe serve`
  exists; spec'd separately.
- Cross-subnet deploys. mDNS doesn't cross subnets; document this constraint and
  require `--feed URL` / explicit IP for those cases.
- A new SSH library dependency. Shell out to the user's `ssh` and `scp` so
  `~/.ssh/config`, ssh-agent, known_hosts, and jump hosts all work.

## Trust model

The integrity story is unchanged from `docs/on-device-apk.md`:

- Every `.apk` is signed by the project signing key.
- Every `APKINDEX.tar.gz` is signed by the same key.
- Every yoe device has the corresponding `<keyname>.rsa.pub` in `/etc/apk/keys/`
  via `base-files`.
- `apk` verifies signatures unconditionally. A bad signature aborts the install.

Plain HTTP transport on the feed is therefore acceptable for development. The
spec assumes any LAN host can read packages from `yoe serve` — that's a
deliberate trade-off for zero-config dev usage. Production OTA is a separate
deployment with its own hosting and access control.

## `yoe serve`

Long-lived HTTP feed on the developer's workstation, plus an mDNS advertisement
that lets devices and `yoe deploy` discover it.

### CLI

```
yoe serve [--port PORT] [--bind ADDR] [--no-mdns] [--service-name NAME]
```

- `--port`: TCP port. Default `8765`. Pinned (not random) so the URL written to
  `/etc/apk/repositories.d/yoe-dev.list` on a target by `yoe device repo add`
  survives `yoe serve` restarts. Override on collision.
- `--bind`: listen address. Default `0.0.0.0` (LAN-visible).
- `--no-mdns`: skip the mDNS advertisement (multicast-hostile networks,
  containers without host networking).
- `--service-name`: mDNS instance name. Default `yoe-<projectName>`.

On startup the command prints:

```
serving <projectDir>/repo/ at http://<hostname>.local:8765/<project>
mDNS:  _yoe-feed._tcp.local. instance=yoe-<project>
press ctrl-c to stop
```

Hostname comes from `os.Hostname()` with `.local` appended (the standard mDNS
form published by avahi / systemd-resolved). The URL uses that name so devices
resolving via mDNS see a stable identifier even if the dev host's IP changes.

### Wire layout served

The HTTP root is `<projectDir>/repo/` exactly as built by
`internal/repo/local.go`. The on-disk layout:

```
<projectDir>/repo/<project>/<arch>/APKINDEX.tar.gz
<projectDir>/repo/<project>/<arch>/<pkg>-<v>-r<N>.apk
<projectDir>/repo/<project>/keys/<keyname>.rsa.pub
```

This matches Alpine's convention so an `apk` repository line pointed at
`http://<host>.local:<port>/<project>` resolves arch directories automatically.
Same layout the static-host OTA flow in `docs/on-device-apk.md` already
documents — `yoe serve` is just an HTTP front-end on the same tree.

### mDNS advertisement

Service type `_yoe-feed._tcp.local.`, instance name `yoe-<project>` by default.
TXT records:

```
project=<project>
path=/<project>
arch=<comma-separated archs found under repo/<project>/>
```

`path` lets a discovering client construct the full feed URL without assuming
the project name appears at the URL root (it always does today, but TXT
documents the contract).

Library: `github.com/libp2p/zeroconf/v2` (pure Go, maintained fork of the
unmaintained `grandcat/zeroconf`). No avahi runtime dependency on the dev host.

### File watching

None. `apk update` re-fetches `APKINDEX.tar.gz` on demand, and the server just
streams whatever's on disk. `yoe build` rewrites the index as part of its normal
Publish flow. Adding fsnotify here would be solving a non-problem.

### Lifecycle

`SIGINT` / `SIGTERM` triggers graceful shutdown:

1. Stop the mDNS advertisement first so no new client discovers a feed that's
   about to disappear.
2. `http.Server.Shutdown(ctx)` with a 5-second drain.
3. Exit 0.

### Logging

Every request logs `method path status bytes duration` to stderr at info level.
Useful for the dev to see device activity in real time.

## `yoe deploy`

Build + ephemeral feed + ssh + `apk add` in one verb. The repo file written on
the device is left in place permanently — same file `yoe device repo add` would
have written. So the first `yoe deploy` to a fresh device doubles as the
device's persistent feed configuration; subsequent deploys are idempotent
overwrites.

### CLI

```
yoe deploy <unit> <host> [flags]

  <unit>   build target (must resolve to a single non-image unit)
  <host>   ssh destination — hostname, IP, or user@host (default user: root)

  --user USER      default ssh user when target lacks a user@ prefix (default: root)
  --port PORT      feed port (default: 8765, same as yoe serve)
  --host-ip IP     advertise this IP to the device instead of <hostname>.local
                   (use when mDNS is unreachable from the device)
```

The `<host>` argument accepts `[user@]host[:port]` — e.g. `dev-pi.local`,
`pi@dev-pi.local`, `localhost:2222` (QEMU port forward), or
`pi@dev-pi.local:2200`. `[ipv6]:port` works for literals.

`<unit>` must resolve to a `class != "image"` unit. Image targets error: "image
targets are flashed, not deployed; use `yoe flash <image>`".

### Pipeline

1. **Resolve and build.** `yoe build <unit>` via the existing executor. No-op if
   the cache is warm. Failure surfaces normally and aborts the deploy.

2. **Pick a feed.** Browse mDNS for `_yoe-feed._tcp.local.` for 500 ms. If
   exactly one instance has TXT `project=<this project>`, reuse its URL (skips
   the rest of step 3 and step 6). Otherwise:

3. **Start an ephemeral feed.** Internally the same `internal/feed/Server`
   `yoe serve` uses, on the same pinned port (default `8765`, override via
   `--port`) and **without an mDNS advertisement**. The URL is given to the
   target directly via ssh in step 4; no other consumer needs to discover it.
   Skipping the advertisement avoids a stale registration that other concurrent
   deploys could try to reuse just before it tears down.

   Reusing the same port keeps the URL shape identical to a persistent
   `yoe serve` — so the device sees the same `http://<host>.local:8765/...`
   regardless of whether the feed is long-lived or ephemeral. Step 2 already
   ensures no collision: if `yoe serve` is running, deploy reuses it instead of
   starting its own. If `8765` is in use by something other than yoe,
   `bind: address already in use` surfaces immediately and the user passes
   `--port`.
   - If `--host-ip` is set, advertise `http://<host-ip>:<port>/<project>` to the
     device.
   - Otherwise advertise `http://<hostname>.local:<port>/<project>` where
     `<hostname>` is `os.Hostname()` with a `.local` suffix appended (the
     standard mDNS form published by avahi / systemd-resolved on Linux dev
     workstations).

4. **Configure the target and install.** ssh to `<host>`:

   ```
   mkdir -p /etc/apk/repositories.d
   printf '%s\n' '<feedURL>' > /etc/apk/repositories.d/yoe-dev.list
   apk update
   apk add --upgrade <unit>
   ```

   Run as a single ssh invocation with a heredoc so partial failures still
   report cleanly. Stdout/stderr stream back to the dev's terminal. The
   `yoe-dev.list` file is the same name `yoe device repo add` writes by default
   — running `yoe deploy` is therefore equivalent to running
   `yoe device repo add` plus `apk add`, and the device is left configured to
   keep pulling from this dev host on any future `apk` invocation.

5. **Tear down the ephemeral feed** (skipped if step 2 reused an existing
   `yoe serve`). The repo file on the device is left in place — that's the
   point. To remove the persistent config explicitly, run
   `yoe device repo remove <host>`.

6. Exit code propagates from step 4. Step 5 errors are logged but do not
   override a non-zero exit from step 4.

### Failure modes

| Failure                      | Behavior                                                                                              |
| ---------------------------- | ----------------------------------------------------------------------------------------------------- |
| Build fails                  | Report; no feed started; exit non-zero.                                                               |
| ssh dial fails               | Tear down feed; exit non-zero.                                                                        |
| `apk add` fails              | Tear down feed; propagate exit code. Repo file remains on the device for inspection / retry.          |
| mDNS unreachable from device | Device fails to resolve URL during `apk update`. Hint in error message: re-run with `--host-ip <ip>`. |

### Why pull, not push

Push (`scp pkg.apk; ssh apk add /tmp/pkg.apk`) is simpler in lines of code but
doesn't help with transitive dependencies. If `myapp` gains a new runtime dep,
push needs to scp that dep too — which means duplicating apk's resolver logic.
Pull lets the device's apk resolve from the same `APKINDEX.tar.gz` production
OTA uses, with no parallel resolver.

## `yoe device repo`

Configure the apk repo file on a target device. Mostly used standalone for the
"configure once, then iterate with bare `apk add` from the device" flow.

### CLI

```
yoe device repo add <target> [flags]

  <target>          ssh destination — hostname, IP, or user@host

  --feed URL        explicit feed URL (e.g. http://laptop.local:8765/myproj).
                    if omitted, browses mDNS for _yoe-feed._tcp on the LAN
                    and picks the unique result; errors if 0 or >1 results.
  --name NAME       basename for the repo file written under
                    /etc/apk/repositories.d/<name>.list (default: "yoe-dev")
  --push-key        copy <repoDir>/keys/<keyname>.rsa.pub to
                    /etc/apk/keys/ on the target before configuring
  --user USER       default ssh user when target lacks user@ (default: root)

yoe device repo remove <[user@]host[:port]> [--name NAME] [--user USER]
yoe device repo list   <[user@]host[:port]> [--user USER]
```

### `add` behavior

1. **Resolve feed URL.**
   - If `--feed` set, use it verbatim.
   - Otherwise browse mDNS for `_yoe-feed._tcp.local.` for 1 s. Filter to
     instances whose TXT `project=` matches the current project (if invoked
     inside a project) or to all instances (if invoked outside). Result:
     - 0 matches → error: "no yoe feed discovered on the LAN — pass --feed URL".
     - 1 match → use `http://<host>.local:<port>/<path>` from the SRV+TXT.
     - > 1 matches → error: "found N feeds: <list>; pass --feed to
       > disambiguate".
2. **Push key (if `--push-key`).** Source: `<repoDir>/keys/<keyname>.rsa.pub`,
   resolved via `repo.KeysDir(repo.RepoDir(proj, projectDir))`. Destination:
   `/etc/apk/keys/<keyname>.rsa.pub` on the target. Use `scp` (shelling out) so
   user's ssh config is respected.
3. **Write the repo file.** ssh to target:
   ```
   mkdir -p /etc/apk/repositories.d
   printf '%s\n' '<feedURL>' > /etc/apk/repositories.d/<name>.list
   apk update
   ```
4. **Report.** On success, print `configured <name> -> <feedURL> on <target>`.
   On `apk update` failure, print the apk error verbatim — usually it means the
   URL isn't reachable from the device or the signing key isn't trusted (suggest
   `--push-key`).

### Idempotency

- `add` overwrites an existing `/etc/apk/repositories.d/<name>.list`
  unconditionally. The verb means "make it so". Use `device repo list` to
  inspect first.
- `remove` is `rm -f`; missing file is success.
- `list` cats `/etc/apk/repositories` and `/etc/apk/repositories.d/*.list`,
  printing each line prefixed with the source filename for clarity.

### Examples

```bash
# Autodiscover the running yoe serve on the LAN, configure dev-pi.local
yoe device repo add dev-pi.local

# Same, but also push the signing key (freshly flashed device built before
# the project key existed)
yoe device repo add dev-pi.local --push-key

# Explicit URL — colleague's serve, or non-mDNS network
yoe device repo add 192.168.4.30 --feed http://laptop.local:8765/myproj

# Non-root ssh user
yoe device repo add pi@dev-pi.local

# QEMU vm started with `yoe run` (default 2222→22 forward)
yoe device repo add localhost:2222

# Tear it down
yoe device repo remove dev-pi.local

# Inspect
yoe device repo list dev-pi.local
```

## File layout

```
internal/feed/feed.go      — Server type with Start/Stop, used by serve + deploy
internal/feed/server.go    — http.Server + handler for <repoDir> static tree
internal/feed/mdns.go      — zeroconf advertise + browse helpers
internal/feed/feed_test.go — unit tests over a tmp repo tree, no network

internal/device/repo.go        — Add/Remove/List ops; ssh + scp shellouts
internal/device/repo_test.go   — fakes the ssh shellout via PATH override

internal/device/deploy.go      — Deploy() orchestration: build → feed → ssh → cleanup
internal/device/deploy_test.go — unit tests with a fake feed + fake ssh

cmd/yoe/main.go            — wire `serve`, `deploy`, `device repo {add,remove,list}`

docs/feed-server.md        — user-facing doc, links from on-device-apk.md
                             and dev-env.md
```

`internal/device/` already exists for `flash` — `repo.go` and `deploy.go` slot
in alongside.

## Dependency choices

| Concern     | Choice                          | Reason                                                                                               |
| ----------- | ------------------------------- | ---------------------------------------------------------------------------------------------------- |
| HTTP server | `net/http` stdlib               | No new deps.                                                                                         |
| mDNS        | `github.com/libp2p/zeroconf/v2` | Pure Go, maintained, server + browser in one.                                                        |
| ssh / scp   | shell out to `ssh` / `scp`      | Picks up `~/.ssh/config`, agent, known_hosts, jump hosts. Same approach `yoe flash` uses for `sudo`. |
| File watch  | none                            | apk handles freshness via `apk update`.                                                              |

## Testing

- `feed_test.go` builds a tmp repo tree, starts `Server` on port 0, hits it with
  `net/http.Client`, asserts the index and a sample apk roundtrip. mDNS is
  exercised via a same-process browse (zeroconf supports loopback).
- `repo_test.go` stubs `ssh` and `scp` by prepending a `t.TempDir()` to `PATH`
  containing fake binaries that record their argv. Verifies the exact commands
  assembled, repo file contents, and key push paths.
- `deploy_test.go` composes a fake feed (real `feed.Server`) with stub ssh,
  drives `Deploy()`, asserts the cleanup runs even on simulated apk failure.
- Manual end-to-end with a Raspberry Pi: `yoe serve` on laptop;
  `yoe device repo add dev-pi.local`; `apk add htop` from the device; then
  `yoe deploy myapp dev-pi.local`.

## Documentation updates

- `docs/feed-server.md` (new): user guide for `yoe serve`, `yoe device repo`,
  `yoe deploy`. Walk-through of the dev loop.
- `docs/on-device-apk.md`: add a "Pointing at a yoe-served feed" subsection that
  links to the new doc.
- `docs/dev-env.md`: replace the placeholder "Fast deploy" section with the real
  semantics (pull, not push) and link to the new doc.
- `docs/roadmap.md`: move "Fast deploy" and "Feed server" entries into the Done
  section once implemented.
- Changelog entry: one to two sentences, user-focused —
  "`yoe deploy <unit> <host>` builds, ships, and installs a unit on a running
  yoe device with full apk dependency resolution. Pair with `yoe serve` and
  `yoe device repo add` for a persistent dev feed."

## Open follow-ups (deferred)

- `yoe dev <unit>` watch mode that loops `build → deploy` on file save. Trivial
  once this lands.
- HTTPS / basic-auth on the feed. Add when someone needs it; until then,
  document that production OTA layers a reverse proxy.
- `yoe deploy <image>` meta-package mode that installs every package in an
  image's manifest into a running system. Needs design for how that interacts
  with kernel/bootloader packages.
- `yoe device repo add --interactive` picker when multiple mDNS results exist.
  v1 errors and asks for `--feed`.
