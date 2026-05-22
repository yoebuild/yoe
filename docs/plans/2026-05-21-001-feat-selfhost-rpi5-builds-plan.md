---
title: "feat: Self-host yoe builds on a Raspberry Pi 5"
type: feat
status: active
date: 2026-05-21
origin: docs/specs/2026-05-21-selfhost-rpi5-builds.md
---

# feat: Self-host yoe builds on a Raspberry Pi 5

## Summary

Ship a `selfhost-image` for the `raspberrypi5` machine that bundles the Docker
build runtime, the Go toolchain, git, bwrap, and a new source-built `yoe` unit
on top of the existing `dev-image` content — so a freshly flashed RPi5 can clone
a yoe project, run `yoe build`, and develop yoe itself without a workstation.
Most of the lift is composition (alpine passthrough + an image manifest) plus
three small units: the `yoe` binary, a first-boot rootfs grow service, and a
`base-files-selfhost` that adds `user` to the `docker` group.

---

## Problem Frame

Today a yoe build requires a developer workstation with Docker and the yoe
binary. ARM64 native iteration means owning an x86_64 laptop and paying QEMU
emulation cost, owning two machines, or renting Graviton time — ergonomically
painful and a poor dogfood story for a tool that already ships an excellent RPi5
BSP. The RPi5 has the hardware (8/16 GB RAM, PCIe NVMe), the kernel already
carries the container CONFIG fragment, and alpine passthrough already wraps
every required userspace piece. The missing pieces are small and additive; this
plan delivers them as a single new image and a handful of supporting units. See
origin:
[docs/specs/2026-05-21-selfhost-rpi5-builds.md](../specs/2026-05-21-selfhost-rpi5-builds.md).

---

## Requirements

- R1. A `selfhost-image` artifact builds for `--machine raspberrypi5` and
  produces a bootable SD/NVMe image. (origin: Goal §1–4)
- R2. The image ships the `yoe` binary as an installed apk so a fresh flash can
  run `yoe build` without sideloading a binary. (origin: Bootstrap §1)
- R3. The image ships a working `dockerd` (Docker engine + CLI + buildx,
  containerd, runc, libseccomp, nftables) enabled at boot via OpenRC. (origin:
  Components → Pulled through `module-alpine`)
- R4. The image includes the full `dev-image` tool set: helix, yazi, zellij,
  openssh, bash, htop, strace, curl, ca-certificates, apk-tools, git, go,
  bubblewrap. (origin: Goal §3; user clarification "all the dev stuff in the dev
  image")
- R5. On first boot, the rootfs partition grows to fill the underlying block
  device so `/var/lib/docker` and the project tree have room. (origin: Storage
  and resource envelope)
- R6. Logging in as `user` allows running `docker run …` and `yoe build …`
  without `sudo`. (origin: Runtime service wiring)
- R7. On a fresh flash, `yoe build base-image --machine raspberrypi5` completes
  end-to-end and produces a bootable second image. (origin: Verification §1)
- R8. `yoe build yoe` produces a yoe apk on the device, and `apk upgrade yoe`
  from the project repo replaces the baked-in binary. (origin: Verification §3)
- R9. Documentation explains the boot path, NVMe-vs-SD trade-off, and first-run
  steps. (origin: Why now, Bootstrap)

**Origin acceptance examples:** four scenarios in origin's `## Verification`
section (build base-image on-device, second-image boots, `apk upgrade yoe` swap,
day-to-day dev tools work over SSH). They map to R7 and R8.

---

## Scope Boundaries

- No multi-arch builds **from** the RPi5 (no binfmt registration, no
  `qemu-user-static` in the image). Native ARM64 only.
- No A/B partitions, no read-only rootfs, no signed image bundles.
- No remote-runner / workstation-orchestrator wiring.
- No source-built Docker / containerd / runc. The plan uses alpine passthrough;
  a future migration to source builds is a unit swap.
- No CI gate that builds yoe-on-yoe.
- No HDMI/kiosk variant. Headless + serial + ssh only.

### Deferred to Follow-Up Work

- Source-built Docker stack: defer to a separate plan once
  [`docs/containers.md`](../containers.md) lands its full design.
- CI gate (yoe-on-yoe build per PR): tracked in
  [roadmap.md → Self-Hosting](../roadmap.md#self-hosting).
- Backport `selfhost-image` to `raspberrypi4`: same image manifest +
  `linux-rpi4`; a few-line follow-up.
- Foreign-arch builds from RPi5 (`qemu-user-static` + binfmt).

---

## Context & Research

### Relevant Code and Patterns

- `modules/module-core/images/dev-image.star` — the baseline this plan extends.
  New `selfhost-image.star` follows the same shape: `base_files()` call,
  `image(name=…, artifacts=[…])`.
- `modules/module-core/images/docker-image.star` — already adds `docker` +
  `docker-init` on top of dev-image. The status note ("kernel currently lacks
  the CONFIG fragment") is stale for `linux-rpi5`, which has `container.cfg`
  merged.
- `modules/module-core/units/net/docker-init.star` — module-core's own OpenRC
  service unit for `dockerd`. Declares `services = ["docker"]`, so the image
  automatically enables the runlevel symlink at install time (project rule:
  "Units declare their own services").
- `modules/module-core/classes/go.star` —
  `go_binary(name, version, source, tag, …)`. Defaults `CGO_ENABLED=0`,
  `go_package="./cmd/<name>"`, container `golang:1.24`. Used by `simpleiot.star`
  as a clean reference for an external Go binary (sets `binary="siot"` to
  override).
- `modules/module-core/units/dev/go.star` — already shipped as the Go toolchain
  unit for arm64. Useful when a build-time `deps = ["go"]` is needed; not used
  by the `yoe` unit since `go_binary` already provides the toolchain via its
  build container.
- `modules/module-core/units/net/simpleiot.star` — reference for a go-binary
  unit that ships an OpenRC service script alongside the binary via
  `install_file(…)` and `services = ["simpleiot"]`.
- `modules/module-bsp/machines/raspberrypi5.star` — 1 GB rootfs default to bump.
  Partition table is declared via `partition(label=, type=, size=, root=)` in a
  Starlark list.
- `modules/module-bsp/units/bsp/linux-rpi5.star` and the shared `container.cfg`
  fragment — already enables namespaces, cgroups v2, overlayfs, bridge, veth,
  netfilter, seccomp.
- `modules/module-core/units/base/base-files.star` plus `classes/users.star` —
  `base_files(name=, users=[user(name=, uid=, gid=, password=, groups=[…])])`.
  The `groups` field exists; verify it accepts `docker`.

### Institutional Learnings

- Project rule: "Units declare their own services; images do not." The
  `services = [...]` field on a unit materializes the runlevel symlink into the
  apk. Image artifact lists never declare per-unit enablement.
  `docker-init.star` already follows this.
- Project rule: "Need a tool Alpine already packages? Pull it through
  `module-alpine`, don't build from source." This plan uses alpine passthrough
  for docker, containerd, runc, git, bubblewrap, libseccomp, iptables, nftables.
- Project rule: "Update `docs/` whenever you add a CHANGELOG entry." U7 bundles
  both.
- Project rule: "Test builds for new/changed units always go in
  `testdata/e2e-project`." Verification steps build the new units from that
  project tree.
- Origin spec calls out a stale `docker-image.star` status comment ("kernel
  currently lacks the CONFIG fragment Docker needs"). This is no longer accurate
  for `linux-rpi5`. Fix in passing.

### External References

- `moby/moby` repo ships `contrib/check-config.sh`. Used by Alpine and HAOS as a
  CONFIG audit. Vendor or `curl` at build time in U6.
- HAOS architecture (referenced in `docs/containers.md`) is the proof point that
  musl + OpenRC + Docker on RPi-class hardware is solid.

---

## Key Technical Decisions

- **Use module-alpine passthrough for the entire container stack.** Source-built
  Docker (per `docs/containers.md`) is the right long-term answer for a shipped
  product, but alpine's musl-native packages already work on `linux-rpi5` and
  avoid blocking on a `libseccomp` source unit and a `cgo = True` extension to
  the `go_binary` class. Migration later is a unit swap with no image-shape
  change.
- **One image manifest, not a layered hierarchy.** `selfhost-image.star`
  duplicates the `dev-image` artifact list rather than introducing a
  `compose`/`inherits` mechanism (which doesn't exist in the image class yet).
  The duplication is small (~25 lines) and the image class can grow composition
  later without breaking this manifest.
- **First-boot rootfs grow lives in a dedicated module-core unit, not in the
  machine descriptor.** The same `grow-rootfs` unit will be reusable by other
  images (jetson, beagleplay) and keeps machine descriptors focused on hardware
  topology.
- **Default rootfs partition size bumps to 4 GB.** Big enough to install the
  image (~1 GB), pull the toolchain Docker images (~1 GB), and have headroom
  before the first-boot grow runs. Small enough to flash to a 4 GB SD card if
  necessary.
- **`/var/lib/docker` lives on the rootfs partition, not a separate data
  partition.** Simpler image assembly, no third partition, no A/B bookkeeping.
  The first-boot grow makes this viable. A dedicated `/var/lib/docker` partition
  is a sensible follow-up but out of scope.
- **`docker-cli-buildx` is in; `docker-compose` is out for v1.** Buildx is small
  and useful for multi-arch workloads; compose is heavier and not on the
  critical path.
- **The `yoe` unit pins its Go toolchain implicitly via the `go_binary` class's
  `golang:1.24` container.** Letting `go.mod` enforce the minimum keeps the unit
  from drifting against the repo's Go version. Revisit if the build needs Go ≥
  1.26 features.
- **`dtparam=nvme` is unconditional in `rpi5-config`.** No-op on SD-only setups;
  enables PCIe HAT users without an extra knob.

---

## Open Questions

### Resolved During Planning

- **Docker package set.** `docker`, `docker-cli`, `docker-cli-buildx`,
  `containerd`, `runc`, `libseccomp`, `nftables`, plus module-core's
  `docker-init`. Drop `docker-compose`.
- **NVMe vs SD as documented default.** Document both; recommend NVMe via the
  PCIe HAT for serious work, note SD is acceptable for demos but slow.
- **yoe Go version pin.** Use the `go_binary` class default container; let
  `go.mod` enforce the minimum.
- **Source-built Docker vs alpine passthrough.** Alpine passthrough for v1.
- **Trust model for project apk repo on device.** Use per-project RSA key in
  `cache/keys/` (workstation default). Documented in U7.
- **GUI vs headless.** Headless + serial + ssh only.
- **First-boot grow mechanism.** Use `sfdisk` (from `util-linux`, already in
  image) to resize partition 2 in place, followed by `resize2fs` (from
  `e2fsprogs`, already in image), wrapped in an OpenRC one-shot service that
  disables itself after success.

### Deferred to Implementation

- **Exact behaviour of `check-config.sh` against `linux-rpi5`'s resolved
  `.config`.** It may flag missing CONFIG options not yet in `container.cfg`
  (e.g., `CONFIG_USER_NS`, `CONFIG_POSIX_MQUEUE`). Discover during U6
  implementation; either extend `container.cfg` or document the gap.
- **Dependency closure of `docker` apk on aarch64.** The alpine cached `docker`
  meta-package may pull in unexpected runtime dependencies (systemd-bits if any,
  glibc-only packages). Resolve during U3 build.
- **Does `nftables` need explicit service-ordering wrt `docker`?** Docker's
  runtime sets up its own iptables/nftables rules. If `nftables` flushes them on
  its own start, we may need the docker service to depend on `nftables`. Verify
  on first boot.
- **Partition reread after `sfdisk` resize.** May require either `partprobe`,
  `kpartx`, or a reboot. Determine empirically.
- **Whether the `groups = ["docker"]` field on `user()` works as expected** with
  the current `base_files` class implementation, or whether `/etc/group` needs
  to be hand-built in a task step.

---

## Implementation Units

### U1. New `yoe` source unit

**Goal:** Package the `yoe` binary as an installable apk so any image can add
`yoe` to its artifact list and any running device can `apk add yoe` /
`apk upgrade yoe`.

**Requirements:** R2, R8

**Dependencies:** None.

**Files:**

- Create: `modules/module-core/units/yoe.star`

**Approach:**

- Wrap the `go_binary` class. Set `source` to this repo's URL,
  `tag = "v" + version`, `go_package = "./cmd/yoe"`, `license`, `description`.
- Pin `version` to the latest tagged release (today: read
  `git tag --sort=-v:refname | head -1`). Document the bump procedure alongside
  the file.
- No `runtime_deps` required — `yoe` is a static Go binary. (Add `docker` as a
  `runtime_deps` candidate during U3 if a built-in postinstall sanity check
  needs it; otherwise omit so the unit is useful in non-container contexts too.)
- Leave `services = []`. `yoe` is a CLI, not a daemon.

**Patterns to follow:**

- `modules/module-core/units/net/simpleiot.star` — the cleanest existing example
  of an external go-binary unit.

**Test scenarios:**

- Happy path: `yoe build yoe` (built from `testdata/e2e-project`) produces
  `cache/packages/yoe-<version>-r0.apk` and the apk's `usr/bin/yoe` is an
  aarch64 ELF that runs `yoe --version` to match the unit's pinned version.
  _Covers AE3._
- Happy path: an image including `"yoe"` in its `artifacts` list installs the
  binary at `/usr/bin/yoe` with mode 0755.
- Edge case: when `tag` does not exist upstream, build fails with a clear "tag
  not found" message from `git clone`, not a silent fallback to `HEAD`.

**Verification:** `yoe build yoe` succeeds inside `testdata/e2e-project`; the
resulting apk is signed; `yoe --version` inside the apk's destdir runs.

---

### U2. First-boot rootfs grow service unit

**Goal:** On first boot, expand the rootfs partition to fill the SD/NVMe, resize
the ext4 filesystem to match, then mark itself complete so the service does not
re-run.

**Requirements:** R5

**Dependencies:** None.

**Files:**

- Create: `modules/module-core/units/base/grow-rootfs.star`
- Create: `modules/module-core/units/base/grow-rootfs/grow-rootfs.init` (OpenRC
  service script, installed via `install_file`)
- Create: `modules/module-core/units/base/grow-rootfs/grow-rootfs.sh` (the
  actual resize logic, installed under `/usr/libexec/`)

**Approach:**

- Unit declares `services = ["grow-rootfs"]` and
  `runtime_deps = ["openrc", "util-linux", "e2fsprogs"]`.
- The OpenRC init script runs at `boot` runlevel before `localmount`, detects
  the root device from `/proc/cmdline`'s `root=` arg, calls
  `sfdisk --no-reread --force <disk> -N <partno>` with the same start sector and
  no size limit, runs `partprobe`, then `resize2fs <rootdev>`.
- On success, the init writes a sentinel `/var/lib/grow-rootfs.done` and
  `rc-update del grow-rootfs boot`. Subsequent boots no-op.
- Failure is logged loudly to the console and to syslog. The script exits
  non-zero but does not block boot.

**Technical design:** _(directional; not implementation specification)_

```
start():
  test -f /var/lib/grow-rootfs.done && exit 0
  rootdev := readlink -f $(awk 'parse root= from /proc/cmdline')
  disk, partno := derive(rootdev)         # e.g. /dev/mmcblk0, 2
  sfdisk --no-reread --force "$disk" -N "$partno" <<< ',+'
  partprobe "$disk"
  resize2fs "$rootdev"
  touch /var/lib/grow-rootfs.done
  rc-update del grow-rootfs boot
```

**Patterns to follow:**

- `modules/module-core/units/net/simpleiot.star` — `install_file` for init
  scripts and `services = [...]` declaration.
- `modules/module-core/units/net/docker-init.star` — minimal unit that ships
  only an init script and declares the service.

**Test scenarios:**

- Happy path: fresh image on an 8 GB SD card boots, rootfs grows from 4 GB to ~8
  GB; `df -h /` shows the new size. _Covers AE1._
- Edge case: second boot is a no-op (sentinel present); rootfs size unchanged.
- Edge case: rootdev is already the size of the disk → script exits cleanly
  without invoking sfdisk's destructive path; sentinel still written.
- Error path: rootdev cannot be determined from `/proc/cmdline` → exit non-zero,
  log error, do not write sentinel (so a fix-and-reboot retries).
- Integration: with `e2fsprogs` and `util-linux` declared as `runtime_deps`,
  `apk add grow-rootfs` brings them in.

**Verification:** After first boot on a fresh flash, `df -h /` shows the
expanded size; `/var/lib/grow-rootfs.done` exists; service is removed from the
`boot` runlevel.

---

### U3. `selfhost-image` manifest and base-files

**Goal:** Compose the new image: dev-image baseline + Docker stack + yoe + go +
git + grow-rootfs + a base-files variant that adds `user` to the `docker` group.

**Requirements:** R1, R3, R4, R6

**Dependencies:** U1, U2.

**Files:**

- Create: `modules/module-core/images/selfhost-image.star`

**Approach:**

- Manifest mirrors `dev-image.star`'s shape: a `base_files()` call followed by
  an `image()` call.
- `base_files(name="base-files-selfhost", users=[...])` with `user` carrying
  `groups = ["docker", "wheel"]`.
- `image(name="selfhost-image", artifacts=[...])` — dev-image list plus: `yoe`,
  `go`, `git`, `bubblewrap`, `libseccomp`, `nftables`, `docker`, `docker-cli`,
  `docker-cli-buildx`, `docker-init`, `containerd`, `runc`, `grow-rootfs`.
- Verify the apk closure resolves on aarch64 before declaring done
  (`yoe build selfhost-image --machine raspberrypi5`).

**Patterns to follow:**

- `modules/module-core/images/dev-image.star` (artifact list shape)
- `modules/module-core/images/docker-image.star` (docker + docker-init
  precedent)

**Test scenarios:**

- Happy path: `yoe build selfhost-image --machine raspberrypi5` completes from a
  clean cache in `testdata/e2e-project` and produces
  `build/selfhost-image.raspberrypi5/disk.img`.
- Happy path: the produced `disk.img` boots in a real RPi5; serial console shows
  `dockerd` reaching `ready` state and `sshd` listening on port 22. _Covers AE1,
  AE4._
- Edge case: omit `nftables` and rebuild → `dockerd` either fails to start or
  refuses to set up the bridge. This is the negative case that justifies the
  artifact being on the list; capture as a documented gotcha rather than a
  regression test.
- Integration: log in as `user` over ssh; `docker run --rm hello-world` succeeds
  without `sudo`. This exercises:
  - kernel namespaces / cgroups v2 (linux-rpi5)
  - storage driver (overlay2)
  - networking (bridge + nftables)
  - user-in-docker-group membership (base-files-selfhost) _Covers AE4._
- Integration:
  `yoe init demo && cd demo && yoe build base-image --machine raspberrypi5`
  completes on the device. _Covers AE1._

**Verification:** The four `## Verification` items in the origin spec all pass
on real hardware.

---

### U4. Raspberry Pi 5 machine: rootfs partition default

**Goal:** Bump the default `raspberrypi5` rootfs partition from 1 GB to 4 GB so
a freshly-flashed image has enough room to install Docker and hold the toolchain
images before the first-boot grow runs.

**Requirements:** R5

**Dependencies:** None (but lands together with U2 to make sense).

**Files:**

- Modify: `modules/module-bsp/machines/raspberrypi5.star`

**Approach:**

- Change `partition(label = "rootfs", type = "ext4", size = "1G", root = True)`
  to `size = "4G"`.
- Leave the boot partition (64M vfat) unchanged.
- Same change applies to `raspberrypi4.star` once the backport-to-RPi4 follow-up
  lands; defer.

**Patterns to follow:**

- Existing partition declarations in `raspberrypi5.star` and
  `raspberrypi4.star`.

**Test scenarios:**

- Happy path: `yoe build base-image --machine raspberrypi5` (the small image)
  still completes and the produced `disk.img` is approximately `64M + 4G` in
  size. The base-image footprint inside the rootfs is unchanged.
- Happy path: `yoe build selfhost-image --machine raspberrypi5` fits within the
  4 GB rootfs at flash time (before grow).

**Verification:** `ls -l build/base-image.raspberrypi5/disk.img` shows ~4.1 GB;
`sfdisk -d` on the image confirms partition 2 is 4 GB.

---

### U5. RPi5 config: NVMe-friendly defaults

**Goal:** Add `dtparam=nvme` to the default `config.txt` so RPi5 users with a
PCIe NVMe HAT boot off NVMe without extra configuration. No-op for SD-only
setups.

**Requirements:** R9

**Dependencies:** None.

**Files:**

- Modify: `modules/module-bsp/units/bsp/rpi5-config.star` (or its `config.txt`
  template — check whether the file is inline or external)

**Approach:**

- Append `dtparam=nvme` to the rendered `config.txt`.
- Document in U7 docs that boot media is decided by what `cmdline.txt`'s `root=`
  points at: `/dev/mmcblk0p2` for SD, `/dev/nvme0n1p2` for NVMe. The user
  updates `cmdline.txt` after imaging if they want to boot from NVMe; baking
  NVMe-by-default in would break SD users.

**Patterns to follow:**

- Existing `rpi5-config.star` `config.txt` content.

**Test scenarios:**

- Happy path: built image's `/boot/config.txt` contains `dtparam=nvme`.
- Edge case: on an SD-only RPi5 (no HAT), boot is unchanged — the parameter is
  silently ignored by firmware.

**Verification:** `mount` the boot partition of the produced image,
`grep dtparam=nvme config.txt`.

---

### U6. Kernel container-config QA gate

**Goal:** Add a post-build verification task to `linux-rpi5` that runs
`moby/moby`'s `check-config.sh` against the resolved `.config` and fails the
build if a required CONFIG is missing.

**Requirements:** R3 (prevents silent regression of the container fragment)

**Dependencies:** None; lands independently. **Optional for the v1 self-host
milestone** — useful preventive QA, but if it surfaces existing gaps, fix in a
follow-up rather than blocking the rest of the plan.

**Files:**

- Modify: `modules/module-bsp/units/bsp/linux-rpi5.star`
- Optionally: shared task definition in `modules/module-bsp/units/bsp/` reused
  by `linux-rpi4` and `linux-beagleplay`.

**Approach:**

- Add a new task that runs after `build`:
  `curl -fsSL https://raw.githubusercontent.com/moby/moby/<pinned-tag>/contrib/check-config.sh | bash -s — .config`
  (or vendor the script as a `conffile`).
- Pin the script tag to a known-good moby release. Document the pin in a
  comment.
- Fail the task on any "missing" CONFIG; allow "optional but missing" to pass.

**Patterns to follow:**

- Existing `tasks = [...]` lists in BSP kernel units (look for `merge_tasks` or
  inline `task(…)` calls).

**Test scenarios:**

- Happy path: current `linux-rpi5` .config passes
  `check-config.sh --strict-essential`. _(If it fails, the failing CONFIG is the
  next deliverable; treat as discovery, not blocker.)_
- Error path: deliberately strip `CONFIG_OVERLAY_FS=y` from a local build;
  verify the task fails with a clear pointer to the missing option.

**Verification:** `yoe build linux-rpi5` succeeds and includes a visible
"container-config check passed" line in the build log.

---

### U7. Documentation and changelog

**Goal:** Document the new image, the first-run flow, and the NVMe-vs-SD
trade-off; link from the existing RPi BSP page, the roadmap, and the spec/plan
index.

**Requirements:** R9

**Dependencies:** U1–U5 (so the docs can describe what shipped).

**Files:**

- Create: `docs/selfhost-rpi5.md` (the canonical "build yoe on yoe on RPi5"
  page; first-run, NVMe setup, user changes, security warnings)
- Modify: `docs/intro.md` (add a paragraph: yoe can now self-host on RPi5)
- Modify: `docs/machine-rpi.md` (link to selfhost-rpi5.md under "Image assembly"
  or a new section)
- Modify: `docs/roadmap.md` (flip the relevant items under "Self-Hosting";
  remove items that are now done)
- Modify: `docs/SPEC_PLAN_INDEX.md` (flip status from "Spec only" → "Partial"
  once first units land, → "Done" when verification passes)
- Modify: `docs/CHANGELOG.md` (user-facing entry: "Raspberry Pi 5 images can now
  self-host yoe builds — see selfhost-rpi5.md")

**Approach:**

- The new doc is short — first-run boot path, login, change password / add ssh
  key, where the project tree lives, how to point `cmdline.txt` at NVMe, how
  `apk upgrade yoe` works once a feed server is available.
- Changelog entry follows the project rule: one or two sentences,
  user-benefit-first, no internal paths or function names. Past entries
  unchanged.
- Update `docker-image.star`'s stale comment ("kernel currently lacks the CONFIG
  fragment Docker needs") in passing.

**Test scenarios:**

- Doc renders cleanly in the mdbook/yoebuild.org build (no broken links, no
  missing images).
- `docs/CHANGELOG.md` parses by the existing changelog QA, if any.

**Verification:** A reader landing on `docs/intro.md` and following links can
flash a self-host image and reach `yoe build` on the device without consulting
the spec or this plan.

---

## System-Wide Impact

- **Interaction graph:** `selfhost-image` is the only new image artifact. No
  existing image is modified. The shared kernel unit (`linux-rpi5`) gains a QA
  task (U6) but its build output is unchanged. `raspberrypi5` machine descriptor
  (U4) is mutated; any image built `--machine raspberrypi5` will get the new 4
  GB rootfs default — verify that doesn't break existing flash workflows on 2 GB
  SD cards. (Document the bump in U7.)
- **Error propagation:** Docker startup failures show up on the serial console
  via OpenRC's standard logging. The first-boot grow logs to syslog and to
  console; failures do not block the rest of boot.
- **State lifecycle risks:** First-boot grow could theoretically race with
  `localmount`; the unit's runlevel placement (`boot`, before `localmount`)
  handles this. Sentinel file on the resized rootfs becomes load-bearing — if
  it's lost, the grow runs again, but `sfdisk -N` against an already-max-sized
  partition is a no-op, so damage potential is low.
- **API surface parity:** None — no public API changes. The new image is
  additive.
- **Integration coverage:** The only cross-layer behavior is "user can run
  docker without sudo" (U3 base-files-selfhost + alpine docker group
  provisioning). This is the integration test in U3.
- **Unchanged invariants:** `base-image`, `dev-image`, `docker-image` manifests
  are not modified. Existing machine descriptors for non-RPi targets are
  unchanged. The `go_binary` class is unchanged.

---

## Risks & Dependencies

| Risk                                                                                                                                                              | Mitigation                                                                                                                                                                                                            |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Alpine `docker` apk closure on aarch64 may pull in surprising or conflicting deps                                                                                 | U3 surfaces the closure on the first build attempt. If a conflict appears (e.g. a util-linux split with `prefer_modules` pinning), document the pin and add it to the project template. Worst case: trim the closure. |
| `linux-rpi5`'s `.config` may fail `moby/moby`'s `check-config.sh` (U6) — the `container.cfg` fragment may not cover every CONFIG the script considers "essential" | Treat U6 as discovery, not a blocker. If CONFIGs are missing, decide per-CONFIG whether to extend `container.cfg` or accept the gap. Either way, U3 (selfhost-image) lands first.                                     |
| `groups = ["docker"]` on `user()` may not generate `/etc/group` entries the way Alpine's `docker-engine` post-install expects                                     | U3 verification step exercises non-root `docker run`. If broken, drop in a small `setup-docker-group` task that appends to `/etc/group` directly.                                                                     |
| Booting from NVMe requires editing `cmdline.txt`'s `root=` after flashing — easy to miss                                                                          | U7 covers it explicitly; consider a `yoe flash --target nvme` flag as follow-up (out of scope here).                                                                                                                  |
| `sfdisk` partition reread may require a reboot                                                                                                                    | U2 calls `partprobe` and falls back to logging "reboot required" if `resize2fs` can't open the device. Sentinel is not written, so the next boot retries.                                                             |
| Docker daemon needs writable `/var/lib/docker`; if grow-rootfs fails, the daemon may start with too little room                                                   | OpenRC service ordering: `grow-rootfs` runs in the `boot` runlevel; `docker` runs in `default`. Docker fails loudly if disk fills, but won't corrupt.                                                                 |

---

## Documentation / Operational Notes

- Add `docs/selfhost-rpi5.md` to the mdbook SUMMARY when the docs land (U7).
- The changelog entry should mention NVMe as the recommended boot medium (U7).
- Operationally: a user flashing an existing 1 GB-rootfs-expecting workflow will
  see a different image size. Call this out in the changelog and in
  machine-rpi.md (U7).

---

## Sources & References

- **Origin document:**
  [docs/specs/2026-05-21-selfhost-rpi5-builds.md](../specs/2026-05-21-selfhost-rpi5-builds.md)
- Related design docs:
  - [docs/containers.md](../containers.md) (kernel reqs, future source-built
    path)
  - [docs/machine-rpi.md](../machine-rpi.md) (RPi BSP, boot chain, flashing)
  - [docs/roadmap.md → Self-Hosting](../roadmap.md#self-hosting)
- Related code:
  - `modules/module-core/images/dev-image.star`
  - `modules/module-core/images/docker-image.star`
  - `modules/module-core/units/net/docker-init.star`
  - `modules/module-core/classes/go.star`
  - `modules/module-bsp/machines/raspberrypi5.star`
  - `modules/module-bsp/units/bsp/rpi5-config.star`
  - `modules/module-bsp/units/bsp/linux-rpi5.star`
- External: `https://github.com/moby/moby/blob/master/contrib/check-config.sh`
