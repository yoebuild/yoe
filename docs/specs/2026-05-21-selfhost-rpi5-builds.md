# Self-host yoe builds on a Raspberry Pi 5

> **Status:** Requirements only. No implementation yet. This document scopes the
> components needed to turn a Raspberry Pi 5 into a standalone yoe build host
> that can run `yoe build`, `yoe flash`, `yoe deploy`, and developer workflows
> (edit source, rebuild, ship) without a separate workstation.

## Goal

A Raspberry Pi 5 boots a yoe-built image, and from that image a user can:

1. Clone a yoe project (or use one baked into `/home/user/`).
2. Run `yoe build <image>` and produce a working image, packages, and an apk
   repo — exactly as a workstation does today.
3. Edit unit `.star` files and project source in a real editor (helix), browse
   the tree (yazi), use multiplexed shells (zellij), commit and push (git).
4. Flash an SD/USB to a target device with `yoe flash`, and serve apks /
   `yoe deploy` to a separate yoe device on the same network.

The dogfood-stretch case (build the `yoe` binary itself on the device and
`apk upgrade yoe` from the local repo) falls out of this naturally once the
above works.

## Why now

- The RPi5 BSP (`linux-rpi5`, `rpi5-config`, `rpi-firmware`, `raspberrypi5`
  machine) is shipped and known-good; the kernel already carries the
  `container.cfg` fragment for cgroups v2, overlayfs, bridge, veth, and
  netfilter.
- Cross-arch QEMU user-mode is shipped, so a workstation can build the ARM64
  packages that the RPi5 image needs in the first place.
- `module-alpine` passthrough is shipped and already wraps every userspace piece
  this image needs (`docker`, `docker-engine`, `docker-cli`, `docker-openrc`,
  `containerd`, `runc`, `git`, `bubblewrap`, `libseccomp`, `nftables`,
  `iptables`, `helix`).
- The Go toolchain unit (`modules/module-core/units/dev/go.star`) is shipped for
  both `x86_64` and `arm64`.
- `dev-image` is shipped and already includes helix, yazi, zellij, bash,
  openssh, ca-certificates, openrc, curl, htop, strace, apk-tools.

The unshipped pieces are: the `yoe` binary as a unit, the Docker engine as an
enabled service on a yoe image, a writable `/var/lib/docker`, and a new image
manifest that ties it all together.

## Non-goals

- Multi-arch builds _from_ the RPi5 (binfmt registration plus qemu-user-static
  so the device can build x86_64 or RISC-V packages). Native ARM64 first;
  foreign-arch comes later if needed.
- A/B updates, read-only rootfs, or signed image bundles. The rootfs is writable
  and grown on first boot.
- A remote-runner / workstation-orchestrator mode where a workstation dispatches
  build steps to the RPi5. Self-hosted = the RPi5 is the orchestrator.
- Container workloads orchestration (running user containers on yoe-shipped
  devices). That's [`docs/containers.md`](../containers.md); this spec uses
  Docker as a build tool, not as a workload runtime.
- A CI gate that builds yoe-on-yoe on every PR. Useful but separable; tracked in
  [roadmap.md](../roadmap.md#self-hosting).

## Users and shape

The user is a yoe developer or yoe-distro developer who wants to iterate on
units, machines, or yoe itself directly on the device — typically because the
workstation is x86_64 and they want native ARM64 iteration without QEMU
emulation slowdown, or because they're traveling with a tiny rig instead of a
laptop, or because they want to dogfood the distro.

Shape: a single new image, `selfhost-image`, that is `dev-image` plus the
build-host pieces.

## Components

### Already shipped

| Component                                    | Source                                              | Notes                                            |
| -------------------------------------------- | --------------------------------------------------- | ------------------------------------------------ |
| `linux-rpi5` kernel                          | `module-bsp`                                        | container.cfg merged; check-config not yet wired |
| `rpi-firmware`, `rpi5-config`                | `module-bsp`                                        | boot chain works                                 |
| `raspberrypi5` machine                       | `module-bsp`                                        | flash + boot proven                              |
| busybox, musl, base-files, openrc, apk-tools | `module-core`                                       | dev-image baseline                               |
| openssh, ca-certificates, curl, mdnsd        | `module-core`                                       | dev-image baseline                               |
| bash, less, file, procps-ng, htop, strace    | `module-core`                                       | dev-image baseline                               |
| `helix`, `yazi`, `zellij`                    | `module-core` (`zellij`, `yazi`) + alpine (`helix`) | dev-image baseline                               |
| `go` 1.26.2 toolchain                        | `module-core/units/dev/go.star`                     | arm64 prebuilt                                   |
| `util-linux`, `e2fsprogs`, `eudev`, `kmod`   | `module-core` + alpine pins                         | dev-image baseline                               |

### Net-new units (build from source)

| Unit  | Class                  | Rationale                                                                                                                                                                                                   |
| ----- | ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `yoe` | `go_binary` (existing) | The yoe binary itself. Pure Go, `CGO_ENABLED=0`, single-shot `go build ./cmd/yoe`. Pulls source from this repo at the tag matching the unit's `version`. Source-of-truth: the same repo this spec lives in. |

That's the only new from-source unit. Everything else comes through
`module-alpine`. Source-built Docker/containerd/runc is listed as a future
option in [`docs/containers.md`](../containers.md) but is not required to hit
the goal — Alpine's musl-native packages already work.

### Pulled through `module-alpine`

| Unit                                                         | Alpine origin                 | Purpose                                                                                                    |
| ------------------------------------------------------------ | ----------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `docker`, `docker-engine`, `docker-cli`, `docker-cli-buildx` | `community/docker*`           | Docker engine + CLI                                                                                        |
| `docker-openrc`                                              | `community/docker-openrc`     | OpenRC init script for `dockerd`                                                                           |
| `containerd`, `runc`                                         | `community/{containerd,runc}` | Pulled in transitively by docker-engine                                                                    |
| `git`                                                        | `main/git`                    | Clone modules, project source, format-patches                                                              |
| `bubblewrap`                                                 | `main/bubblewrap`             | Per-unit sandboxing inside the build container                                                             |
| `libseccomp`                                                 | `main/libseccomp`             | Required by runc for seccomp filtering                                                                     |
| `nftables` + `nftables-openrc`                               | `main/nftables*`              | Docker needs a packet filter; nftables preferred (see [roadmap.md](../roadmap.md#networking-and-security)) |
| `iptables` (fallback)                                        | `main/iptables`               | If `dockerd` cannot drive nftables on a given kernel build, fall back                                      |

A `prefer_modules` pin for these names is not expected to be needed — nothing in
`module-core` currently builds them.

### New image manifest

`modules/module-core/images/selfhost-image.star`:

```python
load("//classes/image.star", "image")
load("//classes/users.star", "user")
load("//units/base/base-files.star", "base_files")

base_files(
    name = "base-files-selfhost",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000,
             password = "password",
             groups = ["docker", "wheel"]),
    ],
)

# Everything dev-image has, plus a working Docker engine and the yoe
# binary, so the device can run `yoe build` on its own checkout.
image(
    name = "selfhost-image",
    artifacts = [
        # dev-image baseline
        "base-files-selfhost", "busybox", "busybox-binsh", "musl", "kmod",
        "util-linux", "e2fsprogs", "eudev", "linux", "openrc",
        "network-config", "iproute2",
        "dhcpcd", "ntp-client", "mdnsd", "openssh", "ca-certificates",
        "curl", "simpleiot", "bash", "less", "file", "procps-ng",
        "htop", "strace", "apk-tools",
        "yazi", "zellij", "helix",
        # build-host additions
        "yoe", "go", "git", "bubblewrap",
        "docker", "docker-cli", "docker-cli-buildx", "docker-init",
        "containerd", "runc", "libseccomp",
        "nftables",
    ],
)
```

### Kernel / image-assembly changes

- **`linux-rpi5` container-config QA.** Add an upstream `check-config.sh`
  invocation against the resolved `.config` so missing CONFIG options break the
  build instead of failing silently at `dockerd` start time. This is the work
  item listed under [roadmap.md → Testing → Kernel QA](../roadmap.md#testing).
  The fragment is already merged; this just gates regressions.
- **Rootfs partition layout.** The current `raspberrypi5` machine declares a 1
  GB ext4 rootfs partition. Bump to a sensible default (4 GB) and add an OpenRC
  `growpart`-style one-shot that resizes the rootfs to fill the SD/NVMe on first
  boot. `/var/lib/docker` lives on rootfs — no second data partition for now.
  (A/B + dedicated data partitions are out-of-scope per
  [Non-goals](#non-goals).)
- **`config.txt` tweaks.** `dtparam=nvme` for NVMe HAT users;
  `usb_max_current_enable=1` is already documented in
  [machine-rpi.md](../machine-rpi.md). Both can be added through `rpi5-config`
  without a new unit.

### Runtime service wiring

- `dockerd` runs as the `docker` OpenRC service from `docker-openrc`. Default
  runlevel: `default`. The image must include the runlevel symlink per the
  [unit-services rule](../../CLAUDE.md#L122-L132) — i.e., the unit's
  `services = [...]` field materializes `/etc/runlevels/default/docker`, so
  first boot starts the daemon without user intervention.
- `nftables` service starts before `docker` (Alpine ships the dependency
  declaration upstream; verify it survives passthrough).
- `dhcpcd`, `sshd`, `mdnsd` come from dev-image inheritance.
- User `user` is added to the `docker` group so non-root `yoe build` and
  `docker` commands work without `sudo`.

### Storage and resource envelope

| Resource     | Minimum      | Recommended                     | Notes                                                              |
| ------------ | ------------ | ------------------------------- | ------------------------------------------------------------------ |
| RAM          | 4 GB         | 8 GB / 16 GB                    | Some unit builds (kernel, llvm) push >4 GB.                        |
| Boot media   | SD card      | NVMe via PCIe HAT               | SD card builds are _very_ slow; document but don't recommend.      |
| Image rootfs | 4 GB on disk | 32–256 GB after first-boot grow | Holds project tree, module cache, apk repo, and `/var/lib/docker`. |
| Network      | DHCP         | DHCP                            | Needed for module clones and Alpine package pulls.                 |

The default `selfhost-image` configures the rootfs to grow on first boot and
prints the resulting size to the serial console.

### Bootstrap and first-run

1. Workstation builds the image once:
   `yoe build --machine raspberrypi5 selfhost-image`
2. Workstation flashes:
   `yoe flash --machine raspberrypi5 selfhost-image /dev/sdN`
3. Boot the RPi5. Rootfs grows to fill the device. `dockerd` starts. ssh listens
   on port 22.
4. Log in as `user` / `password`. **Change the password**, add an SSH public
   key, then disable password auth — same warning as
   [machine-rpi.md → First boot](../machine-rpi.md#first-boot).
5. `yoe init my-project` or `git clone <project>` somewhere under `/home/user/`.
6. `cd my-project && yoe build base-image` — first build pulls modules into the
   project cache and the toolchain container into Docker, then compiles native
   ARM64. Subsequent builds use the local Docker image cache.

### Updating the `yoe` binary on-device

Once an `apk` repo is reachable (either the project's own repo served by
`yoe serve` from another machine, or the upstream `yoebuild` repo when that
exists), `apk upgrade yoe` replaces the baked-in binary. The image ships a sane
`/etc/apk/repositories` pointing at:

- the rootfs-local repo (for offline fallback), and
- a placeholder commented-out upstream URL the user fills in.

This is the standard apk update path — no special yoe machinery.

## Open questions

1. **Which Docker package set?** Alpine ships `docker` (meta), `docker-cli`,
   `docker-engine`, `docker-cli-buildx`, `docker-init`, `docker-openrc`,
   `docker-compose`. The artifact list above takes the conservative superset;
   trimming `buildx` and `compose` saves ~50 MB if size matters. Likely answer:
   keep buildx (multi-arch builds for Docker workloads), drop compose for v1.
2. **NVMe vs SD as the documented default.** NVMe via the PCIe HAT is
   dramatically faster (10–20×) and is the only reasonable choice for real work,
   but it requires a HAT purchase. SD works for a quick demo. Doc should
   recommend NVMe but show both paths.
3. **`yoe` unit Go version pin.** `units/dev/go.star` is 1.26.2; the yoe repo's
   `go.mod` decides the minimum. Either pin yoe's unit to the same Go version
   unit, or version-bump in lockstep. Recommend `deps = ["go"]` and let `go.mod`
   enforce the minimum.
4. **Source-built Docker vs Alpine passthrough.** This spec uses Alpine
   passthrough. Source-built Docker is documented in
   [`docs/containers.md`](../containers.md) and is the right answer for a
   shipped product, but not for the first cut of this spec. Migrating later is a
   unit swap; no image-shape change.
5. **Trust model for the project's apk repo on-device.** When the device builds
   packages, the signing key has to live somewhere. Default: the per-project RSA
   key under the project tree's `cache/keys/`, same as workstation behavior.
   Document that the key is sensitive and survives `yoe flash` only because it's
   inside the project tree, not the image.
6. **GUI vs headless.** Headless + serial + ssh is the documented path. A future
   kiosk variant (a yoe TUI on the HDMI output via `cage` + wlroots, per
   [roadmap.md → Hardware Access](../roadmap.md#hardware-access)) is interesting
   but out of scope.
7. **`yoe deploy` from a self-host device to another yoe device.** Should "just
   work" once mDNS on RPi5 is fixed (open bug in
   [roadmap.md](../roadmap.md#next)). Worth confirming as a smoke test but not a
   blocker.

## Suggested staging

A reasonable implementation plan would split the work along the seams above:

1. **Land `yoe` unit.** `modules/module-core/units/yoe.star`, `go_binary` class,
   source = this repo, tag pinned. Verify `yoe build yoe` produces a working
   aarch64 apk. (Tiny PR; useful on its own — workstations get `apk add yoe`
   too.)
2. **Land Docker stack as installed artifacts in a new image.** Either extend
   `docker-image.star` or land `selfhost-image.star` directly. Boot on real RPi5
   hardware, confirm `dockerd` comes up, `docker run hello-world` works
   (image-pull exercises iptables/nftables, dns, namespaces, overlay storage).
3. **Wire `check-config.sh` into `linux-rpi5`.** Block regressions.
4. **Add the first-boot rootfs-grow.** Small OpenRC service unit; runs once,
   removes itself.
5. **`yoe build base-image` on the device.** End-to-end smoke test. First
   failures here surface the rest of the gap list.
6. **Docs.** New page `docs/selfhost-rpi5.md` linked from
   [machine-rpi.md](../machine-rpi.md), plus a paragraph in
   [intro.md](../intro.md) saying yoe can self-host.
7. **Backport to RPi4.** The same image likely works on RPi4 with the
   `linux-rpi4` swap. Worth confirming; useful demo on cheaper hardware.

Plan-level breakdown (file edits, task ordering, dependencies between steps) is
intentionally left for the `/ce-plan` follow-up — this document captures _what_
must be true and _why_, not _how_.

## Verification

A run is "self-hosting works" when, on a fresh `selfhost-image` flash:

- `yoe init demo && cd demo && yoe build base-image --machine raspberrypi5`
  completes and produces a bootable `disk.img`.
- That `disk.img` flashed to a second RPi boots to a login prompt.
- `yoe build yoe` produces an `.apk` for `yoe` itself, and `apk upgrade yoe`
  replaces the baked-in copy without a reboot.
- `git clone`, `helix`, `yazi`, `zellij`, `ssh` all work as a day-to-day
  development environment over SSH from a laptop.

## References

- [machine-rpi.md](../machine-rpi.md) — RPi4/RPi5 BSP, boot chain, flashing
- [containers.md](../containers.md) — kernel reqs, runtime choice, source-built
  Docker
- [roadmap.md → Self-Hosting](../roadmap.md#self-hosting) — original goals
- [feed-server.md](../feed-server.md) — `yoe serve`, `yoe deploy`, trust model
- [build-environment.md](../build-environment.md) — why Docker is the host
  requirement and what runs inside it
