# Running Containers on yoe Images (planned)

> **Status:** No container runtime ships in any yoe-built image today. This
> document captures the design discussion and prerequisites for getting a
> container runtime (Docker, Podman, or containerd) running on devices built
> from yoe units. Nothing described here is implemented yet.

Supporting container workloads on yoe-built images is a high-value feature: it
is the single biggest thing that turns a minimal embedded Linux into something
people actually want to deploy on real devices. This document records what it
would take.

## Reference Point: Home Assistant OS

Home Assistant OS (HAOS) is the clearest proof that full Docker on embedded
devices is viable, and it is a useful reference architecture. Key facts:

- **Base:** Buildroot (not Yocto)
- **Container runtime:** full Docker Engine (`dockerd` + `containerd` + `runc`)
- **Orchestration:** their own "Supervisor" — a privileged container that
  manages addon containers and talks to the host via D-Bus
- **Rootfs:** read-only squashfs with A/B partitions for atomic updates (RAUC)
- **Data partition:** separate ext4/btrfs for `/var/lib/docker` and addon state
- **Init:** systemd
- **Networking:** NetworkManager

HAOS images are ~350 MB compressed / ~1 GB installed and run comfortably on a
Raspberry Pi 4 with 2 GB RAM. Source and kernel fragments are public at
<https://github.com/home-assistant/operating-system>.

The takeaway: Buildroot-with-Docker has been a proven path for years. Nothing in
yoe's architecture prevents matching or bettering it.

## Kernel Requirements

A container-capable kernel needs a specific set of CONFIG options. The upstream
`moby/moby` repository ships a `check-config.sh` script that enumerates them and
is worth wiring into the kernel unit's QA step.

Essentials:

- **Namespaces:** `PID`, `NET`, `IPC`, `UTS`, `USER`, `MNT`, `CGROUP`
- **Cgroups v2** (`CONFIG_CGROUPS`, `CONFIG_MEMCG`, `CONFIG_CPUSETS`, etc.) —
  modern Docker and containerd assume v2
- **Storage driver:** `CONFIG_OVERLAY_FS` — without this the engine falls back
  to the `vfs` driver, which is unusably slow
- **Networking:** `CONFIG_BRIDGE`, `CONFIG_VETH`, `CONFIG_NETFILTER*`,
  `CONFIG_NF_NAT`, `CONFIG_NF_TABLES` (or legacy `CONFIG_IP_NF_*`)
- **Security:** `CONFIG_SECCOMP`, `CONFIG_SECCOMP_FILTER`, `CONFIG_KEYS`
- **Misc:** `CONFIG_POSIX_MQUEUE`

The plan is to ship a `kernel-container-host.cfg` config fragment alongside the
kernel unit and add a build-time check that runs `check-config.sh` against the
resulting `.config`.

## Userspace Prerequisites

Container runtimes pull in userspace tools that yoe does not yet package.
Shipping a container-capable image forces the following units from the
[roadmap](roadmap.md) to land first:

- `iptables` or `nftables` — Docker refuses to start without one
- `ca-certificates` — required to pull images over TLS
- `util-linux` — container runtimes use `mount` with flags that busybox mount
  does not handle cleanly
- `kmod` — needed to load `overlay`, `bridge`, and netfilter modules at runtime,
  unless everything is built into the kernel
- `e2fsprogs` — for formatting a dedicated `/var/lib/docker` partition

This is a nice forcing function: these units are all on the roadmap for other
reasons, and shipping a container host is a concrete goal that justifies landing
them.

## libc and Init System

All mainstream container runtimes — Docker, containerd, runc, Podman, nerdctl —
are Go and do not meaningfully care about the host libc. Alpine Linux (musl +
OpenRC) has shipped full Docker for years; Void (musl + runit) and Chimera
(musl + dinit) do the same. yoe currently targets musl, so this is a
well-trodden path with even less friction than the glibc equivalent.

Known musl-specific caveats, all survivable:

- musl's DNS resolver does not honor `/etc/nsswitch.conf` and differs from glibc
  in edge cases. This affects _workloads running in containers_, but most
  container images bring their own libc (Debian, Alpine, distroless), so the
  host's libc rarely reaches the workload.
- Prebuilt Go binaries compiled with `CGO_ENABLED=1` against glibc will not run
  on a musl host. yoe builds everything from source, so this is moot.

None of these runtimes require systemd. Docker ships a SysV-style init script
upstream; Alpine's packaging supplies OpenRC services for `dockerd`,
`containerd`, and Podman. Podman is daemonless and needs no init integration at
all.

Init-system considerations for yoe:

- yoe currently uses **busybox init**, which is fine for `dev-image` but thin
  for a container host — no dependency ordering, no supervision, no auto-restart
  of crashed daemons.
- **OpenRC** is the natural next step: small, well-supported by Alpine's
  packaging, and the path of least resistance for Docker/containerd service
  scripts.
- **s6** or **runit** are lighter alternatives if supervision is the main need
  and OpenRC's dependency machinery feels heavy.
- **systemd** is possible but a large addition and not required. Adopt only if a
  downstream workload genuinely needs it.
- **cgroups v2 without systemd:** mount `cgroup2` at `/sys/fs/cgroup` at boot
  and configure the kernel cmdline accordingly. containerd and Docker handle
  this fine; no systemd-specific glue is needed.

The init choice should be made deliberately before the `container-host-image`
milestone. OpenRC is the default recommendation unless there is a reason to pick
otherwise.

## Runtime Choice

Three credible options, in rough order of embedded-friendliness:

### Option 1: containerd + runc + nerdctl

- Smallest footprint (~50–100 MB installed)
- What Kubernetes and K3s use under the hood
- `nerdctl` provides a `docker`-compatible CLI
- Best pick if the device is a workload runner rather than a developer box
- **Recommended as the first milestone** — smallest surface, proves the concept,
  leaves room for Docker CE later

### Option 2: Podman

- Daemonless, rootless-friendly
- CLI-compatible with `docker`
- Popular in Red Hat ecosystems and increasingly in embedded
- Good middle ground if users expect a `docker`-like UX without the daemon

### Option 3: Docker CE

- Largest footprint (~200–300 MB across `dockerd`, `containerd`, `runc`, CLI)
- Maximum ecosystem compatibility — Compose, Swarm, third-party tooling
- What users ask for by name because of familiarity
- Worth adding after containerd is working, if there is demand

## Building from Source

Docker's prebuilt "static" binaries (from `download.docker.com/linux/static/`)
are not truly static — `dockerd`, `containerd`, and `runc` are linked against
glibc and pull in `libseccomp`/`libdevmapper` dynamically on some releases — so
they will not run on a musl-based yoe rootfs. Building from source is the only
serious path.

The toolchain side is already solved: `modules/module-core/units/dev/go.star`
provides a Go toolchain (currently Go 1.26.2) and `classes/go.star` gives Go
units a build class. The component breakdown:

- **`docker` CLI** — pure Go, `CGO_ENABLED=0`, no system-library deps
- **`containerd`** — mostly pure Go, builds with `CGO_ENABLED=0` for the daemon
  and `ctr`
- **`runc`** — effectively requires cgo + `libseccomp` to be useful; without
  seccomp filtering it is not a serious container runtime, so a `libseccomp`
  unit must land first
- **`dockerd`** — optional cgo paths for graphdrivers (devicemapper, btrfs), all
  avoidable with overlay2 as the default storage driver
- **`tini`** (`docker-init`) — small C program, trivial autotools build

So the work is one C library unit (`libseccomp`), four Go units (`runc`,
`containerd`, `docker`, `dockerd`), and one trivial autotools unit (`tini`). The
genuinely hard pieces are the runtime concerns covered elsewhere in this
document — kernel config, init integration, iptables/nftables, the
`/var/lib/docker` data partition — not the source builds themselves.

Alpine's `aports` tree (`community/docker`, `community/containerd`,
`community/runc`) is the obvious reference: those packages are already
musl-native and the APKBUILDs document the exact configure flags, ldflags, and
patches that work in practice.

### Building cgo Units (runc, libseccomp consumers)

The pure-Go components (`docker` CLI, `containerd`) drop into the existing
`go_binary` class without ceremony — that class already pulls the upstream
`golang:1.24` container and builds with `CGO_ENABLED=0`. The interesting case is
`runc`, which needs cgo + a working C compiler + `libseccomp` headers and
libraries, all in the same build environment.

The Yoe-native answer is to use the existing `units/dev/go.star` as a build-time
dep rather than introducing a new "Go + GCC" container:

- A unit's `deps` are installed into the build sysroot before that unit builds.
  The Go toolchain unit installs to `$PREFIX/lib/go` with `/usr/bin/{go,gofmt}`
  symlinks, so a unit with `deps = ["go"]` gets `go` on `PATH` at build time.
- The same mechanism lands a `libseccomp` unit's headers and `.so` in the
  sysroot, where `pkg-config --cflags --libs libseccomp` finds them.
- The existing `toolchain-musl` container already provides `gcc`, `binutils`,
  `make`, etc.

So a `runc` unit is: `container = "toolchain-musl"`,
`deps = ["go", "libseccomp"]`, and a build task that runs the upstream Makefile.
`go build` invokes `gcc` from the container, links against `libseccomp` from the
sysroot, and uses `go` from the sysroot. One container, three pieces, all native
to the Yoe model.

The one wrinkle: `classes/go.star::go_binary` currently hardcodes
`container = "golang:1.24"` and `CGO_ENABLED=0`, which is fine for pure-Go units
but cannot express the cgo + musl + sysroot-deps combination above. The class
should grow a `cgo = True` mode that switches the container to `toolchain-musl`,
drops the `CGO_ENABLED=0`, and relies on `deps` for the Go toolchain instead of
the upstream Go image. This same path will be reused by anything else needing
cgo (devmapper, btrfs, AppArmor consumers), so it is worth making first-class
rather than hand-rolling tasks per unit.

## Resource Envelope

From HAOS experience and general rules of thumb:

- **Storage:** ~100 MB (containerd-only) to ~300 MB (Docker CE) for the engine
  itself, plus whatever images and volumes the workloads need. A dedicated data
  partition for `/var/lib/containerd` or `/var/lib/docker` is strongly
  recommended.
- **RAM:** 256 MB minimum for the daemon to be non-miserable; 512 MB+ for
  anything real; 2 GB+ for comfortable multi-container workloads.
- **Rootfs:** writable `/var` (or a writable overlay) is required. A read-only
  rootfs with a separate writable data partition — HAOS-style — is the right
  long-term pattern.

## Suggested Path

1. Land the roadmap units `util-linux`, `kmod`, `iptables`/`nftables`,
   `ca-certificates`, and `e2fsprogs`. These are needed for other reasons too.
2. Add a `kernel-container-host.cfg` fragment and wire `check-config.sh` into
   the kernel unit's QA step.
3. Package `runc`, `containerd`, and `nerdctl` as the first milestone.
4. Ship a `container-host-image` alongside `dev-image` that pulls it all
   together — kernel config, userspace, engine, and a writable data partition.
5. Consider Podman and/or Docker CE as follow-on units once the containerd path
   is solid.
6. Longer term: mirror HAOS's update architecture (A/B partitions, read-only
   rootfs, signed update bundles). That is where HAOS spent its engineering
   budget, and it is the real differentiator against ad-hoc Buildroot images.

## Why This Matters for yoe

- Enabling Docker on Buildroot is famously fiddly; on Yocto it requires the
  large `meta-virtualization` layer. yoe can ship a clean, opinionated path that
  is smaller and more approachable than either.
- A `container-host-image` is a credible, demo-able milestone that proves the
  machine-portability claims in `docs/metadata-format.md` are real.
- It turns yoe from "a nicer way to build a minimal Linux" into "a reasonable
  way to build a production-shaped device OS" — a much larger audience.
