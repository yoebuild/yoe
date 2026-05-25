# Running Containers on yoe Images

> **Status:** Shipped on x86_64 QEMU and Raspberry Pi 5; kernel config also
> merged for Raspberry Pi 4 and BeaglePlay. Docker (engine + CLI + buildx +
> containerd + runc + libseccomp + iptables) ships via Alpine apk passthrough,
> started under OpenRC. The HAOS-style hardening pattern (read-only rootfs,
> separate data partition, A/B atomic updates) and a source-built runtime are
> still on the roadmap — see [What is not yet shipped](#what-is-not-yet-shipped)
> below.

Running container workloads on yoe-built devices turns a minimal embedded Linux
into something people actually want to deploy. Two shipped images cover the
common cases, and the kernel + apk + service plumbing they rely on is reusable
for any other yoe image that wants to add a container runtime.

## Shipped images

### `docker-image`

A `dev-image`-style base plus the Docker userspace.

- Defined in
  [`modules/module-core/images/docker-image.star`](../modules/module-core/images/docker-image.star).
- Adds `docker` and the `docker-init` OpenRC service to the dev-image artifact
  list. The Alpine `docker` meta-apk pulls in `docker-engine`, `docker-cli`,
  `docker-cli-buildx`, `containerd`, `runc`, `libseccomp`, and `iptables`
  transitively.
- Suitable for any machine whose kernel has the container config fragment merged
  in (`linux`, `linux-rpi4`, `linux-rpi5`, `linux-beagleplay` — see
  [Kernel config](#kernel-config) below).

### `selfhost-image`

`docker-image` plus the full yoe build-host toolchain — `yoe`, `go`, `git`,
`bubblewrap`, `qemu-system-x86_64`, and `grow-rootfs` for first-boot partition
expansion.

- Defined in
  [`modules/module-core/images/selfhost-image.star`](../modules/module-core/images/selfhost-image.star).
- Targets the Raspberry Pi 5 self-host workflow described in
  [selfhost.md](selfhost.md): flash, boot, `yoe build` on the device with no
  workstation in the loop.
- The default `user` is added to the `docker` group via `base-files`, so
  `docker run` and yoe's per-unit container builds work without `sudo`.

## Kernel config

The container-runtime kernel options live in a single config fragment:
[`modules/module-core/units/base/linux/container.cfg`](../modules/module-core/units/base/linux/container.cfg).

It is merged into the kernel `.config` via `scripts/kconfig/merge_config.sh`
during the `linux` unit's build, and is referenced from each
container-host-capable kernel unit:

| Kernel unit        | Path                                                      |
| ------------------ | --------------------------------------------------------- |
| `linux`            | `modules/module-core/units/base/linux.star` (x86_64 QEMU) |
| `linux-rpi4`       | `modules/module-bsp/units/bsp/linux-rpi4.star`            |
| `linux-rpi5`       | `modules/module-bsp/units/bsp/linux-rpi5.star`            |
| `linux-beagleplay` | `modules/module-bsp/units/bsp/linux-beagleplay.star`      |

What the fragment turns on, grouped:

- **Namespaces:** `PID`, `NET`, `IPC`, `UTS`, `USER`, `MNT`.
- **Cgroups v2** plus the per-controller flags Docker enumerates at start
  (`MEMCG`, `CPUSETS`, `PIDS`, `DEVICE`, `FREEZER`, `BLK_CGROUP`, `NET_PRIO`,
  `NET_CLASSID`, `CPUACCT`, `HUGETLB`).
- **Storage:** `OVERLAY_FS` so dockerd uses `overlay2` rather than falling back
  to `vfs`.
- **Networking:** `BRIDGE`, `VETH`, `VLAN_8021Q`, `MACVLAN`, `IPVLAN`, `VXLAN`;
  full `NETFILTER` + `NF_NAT` + `NF_TABLES` + `NFT_COMPAT` surface so both the
  iptables-legacy and iptables-nft backends work.
- **Sandboxing:** `SECCOMP`, `SECCOMP_FILTER`.
- **eBPF:** `BPF`, `BPF_SYSCALL`, `BPF_JIT` for cgroup v2 device control and
  runc.
- **Misc:** `KEYS`, `POSIX_MQUEUE`.

Adding a new container-host kernel is one line plus a reference to the fragment
— see the `linux-rpi5.star` recipe for the pattern.

## Userspace

Everything Docker needs at runtime ships via Alpine apk passthrough (see
[apk-passthrough.md](apk-passthrough.md) for how that works):

| Package             | Source                        | Role                                                      |
| ------------------- | ----------------------------- | --------------------------------------------------------- |
| `docker`            | `community/docker` (meta)     | Pulls engine + CLI + tooling                              |
| `docker-engine`     | `community/docker-engine`     | `dockerd`                                                 |
| `docker-cli`        | `community/docker-cli`        | `docker` CLI                                              |
| `docker-cli-buildx` | `community/docker-cli-buildx` | `docker buildx` plugin                                    |
| `containerd`        | `community/containerd`        | Container runtime daemon, pulled in transitively          |
| `runc`              | `community/runc`              | OCI runtime                                               |
| `libseccomp`        | `main/libseccomp`             | Seccomp filtering for runc                                |
| `iptables`          | `main/iptables`               | Required by `dockerd` for the default bridge network      |
| `ca-certificates`   | `main/ca-certificates`        | TLS for pulling images                                    |
| `util-linux`        | `main/util-linux`             | Mount options busybox `mount` does not handle             |
| `kmod`              | `main/kmod`                   | Load `overlay`, `bridge`, and netfilter modules on demand |
| `e2fsprogs`         | `main/e2fsprogs`              | Filesystem tooling                                        |

## Init integration

The `docker` service is wired into OpenRC by the
[`docker-init`](../modules/module-core/units/net/docker-init.star) unit, which
installs `/etc/init.d/docker` and declares `services = ["docker"]` so yoe's
image assembly drops a symlink into the default runlevel.

The default init is **OpenRC** on yoe images that ship Docker. Container
runtimes themselves do not require systemd (Alpine, Void, and Chimera have
shipped Docker on non-systemd inits for years), and OpenRC is the path of least
resistance because Alpine's own `docker-engine` apk ships ready-to-use OpenRC
service scripts.

`cgroups v2` is mounted at `/sys/fs/cgroup` at boot. No systemd glue is needed;
containerd and Docker handle the unified hierarchy directly.

## Reference architecture: Home Assistant OS

Home Assistant OS (HAOS) remains the clearest production reference for "full
Docker on an embedded device" and is where the long-term hardening pattern below
is heading.

- **Base:** Buildroot
- **Container runtime:** Docker Engine (`dockerd` + `containerd` + `runc`)
- **Orchestration:** their privileged "Supervisor" container, talking to the
  host over D-Bus
- **Rootfs:** read-only squashfs + A/B partitions for atomic updates (RAUC)
- **Data partition:** separate ext4/btrfs for `/var/lib/docker`
- **Init:** systemd
- **Networking:** NetworkManager

HAOS images are ~350 MB compressed / ~1 GB installed and run on a Raspberry Pi 4
with 2 GB RAM. Source and kernel fragments are at
<https://github.com/home-assistant/operating-system>.

The takeaway: Buildroot-with-Docker has been a proven path for years. yoe
matches the basic shape today; the read-only + A/B story is where the bulk of
the remaining engineering sits.

## Resource envelope

From running `docker-image` and `selfhost-image` and matching HAOS field
experience:

- **Storage:** Docker engine + CLI + containerd + runc + buildx land around
  200–300 MB installed. Add image and volume storage on top — `/var/lib/docker`
  grows with whatever workloads run. For RPi5 self-host, an NVMe SSD (≥64 GB) is
  the practical target; a microSD fills up quickly once toolchain images cache.
- **RAM:** 512 MB minimum to be non-miserable. 2 GB+ for comfortable
  multi-container workloads. The RPi5 self-host workflow wants 8 GB and benefits
  from 16 GB.
- **Rootfs:** writable `/var` today. `/var/lib/docker` lives on the shared
  rootfs, so there is no second data partition to worry about — with the
  trade-off that the rootfs cannot yet be read-only.

## What is not yet shipped

### Read-only rootfs + separate data partition

Today `/var/lib/docker` is on the shared rootfs. The HAOS pattern — read-only
squashfs rootfs with a dedicated writable data partition for container state —
is the long-term target. This is where the bulk of the remaining engineering
sits, because it touches the image-assembly flow, the bootloader, and the update
mechanism.

### A/B atomic updates

`grow-rootfs` handles first-boot expansion, but there are no A/B partitions and
no rollback on a failed update. Pairing the read-only rootfs change above with
an A/B layout + signed update bundles is the HAOS-style hardening goal.

### `check-config.sh` QA

The fragment is correct today, but if an upstream kernel change drops or renames
a CONFIG, the failure mode is "`dockerd` fails to start on the device." Wiring
`moby/moby`'s `check-config.sh` into the kernel unit's QA step so the build
fails noisily at integration time is the cheapest prevention.

### Source-built Docker / containerd / runc

The Alpine apk passthrough was the right first move — it shipped a working
container host on day one, on top of a glibc-free musl base. A source-built path
is still useful for two cases that the passthrough cannot serve:

- A version newer or older than what is in Alpine's repository at the time of
  build.
- Static or non-Alpine libc bases (e.g. a glibc-flavoured yoe image).

The component breakdown for a source build:

- **`docker` CLI** — pure Go, `CGO_ENABLED=0`, no system-library deps.
- **`containerd`** — mostly pure Go, builds with `CGO_ENABLED=0`.
- **`runc`** — cgo + `libseccomp` required for serious use.
- **`dockerd`** — optional cgo paths for graphdrivers; all avoidable with
  overlay2 as the default storage driver.
- **`tini`** (`docker-init`) — small C program, trivial autotools build.

The Yoe-native shape is one C-library unit (`libseccomp`), four Go units
(`runc`, `containerd`, `docker`, `dockerd`), one trivial autotools unit
(`tini`). For cgo-using units like `runc`, the existing `units/dev/go.star` unit
installs Go into the build sysroot via `deps`, so a unit with
`container = "toolchain-musl"`, `deps = ["go", "libseccomp"]` gets the
toolchain, the C compiler from the container, and the seccomp headers from the
sysroot all in one place. The pattern is reusable for any future cgo unit.

The wrinkle: `classes/go.star::go_binary` currently hardcodes
`container = "golang:1.26"` and `CGO_ENABLED=0`. Adding a `cgo = True` mode that
switches to `toolchain-musl` and relies on `deps` for the Go toolchain is the
right place to land this.

Alpine's `aports` tree (`community/docker`, `community/containerd`,
`community/runc`) is the obvious reference for configure flags, ldflags, and
patches that work in practice — the apks we passthrough today come straight from
there.

### Other runtimes

- **Podman / nerdctl.** No yoe units yet. Podman is daemonless and
  rootless-friendly; nerdctl is the minimal containerd-only path. Both are
  reasonable follow-ons; neither is required while Docker covers the primary use
  cases.

### Container workload orchestration

Shipping a runtime is different from managing workloads on it. A
managed-container story — declarative workload definitions, OTA-aware
pull/restart, health checks — is not in scope here and is the obvious next
layer.

## Why this matters for yoe

- Enabling Docker on Buildroot is famously fiddly; on Yocto it requires the
  large `meta-virtualization` layer. yoe ships a clean, opinionated path that is
  smaller and more approachable than either, in two image recipes and a single
  kernel fragment.
- `selfhost-image` is the dogfood proof: a yoe-built RPi5 builds yoe itself,
  natively, using the same Docker the device hosts for user workloads. The build
  host and the deploy host are the same image.
- It turns yoe from "a nicer way to build a minimal Linux" into "a reasonable
  way to build a production-shaped device OS" — which is the audience that
  actually ships products.

## Related docs

- [Self-Host Builds](selfhost.md) — what `selfhost-image` is for and how to use
  it.
- [Alpine apk passthrough](apk-passthrough.md) — how the Docker apks reach the
  image.
- [Feed Server and `yoe deploy`](feed-server.md) — the OTA + per-package update
  path that complements container workloads.
- [libc, init, and the Rootfs Base](libc-and-init.md) — why OpenRC on musl is
  the current base and what would change for glibc/systemd.
