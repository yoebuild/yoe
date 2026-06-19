<!--
Spec: distro_artifacts and core-layer multi-distro images
Date: 2026-06-12
-->

# `distro_artifacts`: one image definition, many distros

## Summary

Image definitions are triplicated today. `base-image`, `dev-image`, and
`ssh-image` each exist as three near-parallel `.star` files — one in
`module-alpine/images/`, one in `module-debian/images/`, one in
`module-ubuntu/images/` — that drift independently and must be kept in sync by
hand (the comments in each already say things like "mirrors module-debian's
ssh-image so the two can be compared apples-to-apples"). The package _names_
differ across distros (musl/openrc/apk-tools vs systemd/glibc/dpkg), so a single
flat `artifacts = [...]` list cannot express "this image, every distro," and the
files were split as the only available option.

This spec adds **`distro_artifacts`** — a `{distro: [names]}` map on `image()`,
the exact shape and naming of the existing `distro_deps` / `distro_runtime_deps`
fields on `unit()` — so one image definition fans out to Alpine, Debian, and
Ubuntu, selected by the project's effective distro. With the artifact lists
expressible in one place, the parallel per-distro image files collapse into a
single set under **`module-core/images/`**, demonstrating yoe's core promise:
the same core module targets multiple distros with no per-distro forking.

The proving case is a Raspberry Pi 5 image that builds as either Alpine or
Debian from one definition, carrying the custom `linux-rpi5` kernel in both.

## Motivation

```
module-alpine/images/ssh-image.star   ┐
module-debian/images/ssh-image.star   ├─ same image, 3 files, hand-synced
module-ubuntu/images/ssh-image.star   ┘
```

Two observations from the current files set the design:

- **Alpine vs Debian artifact lists share almost nothing** — different init
  (openrc vs systemd-sysv), libc (musl vs libc6), package manager (apk-tools vs
  dpkg/apt), network stack (dhcpcd vs network-manager), even the ssh package
  name (`openssh` vs `openssh-server`). Merging them is "one name, one file,"
  not "shared list."
- **Debian vs Ubuntu lists are ~95% identical** — they differ only by the kernel
  helper (`debian_kernel()` vs `ubuntu_kernel()`) and Ubuntu's extra
  `nm-manage-ethernet` drop-in. This pair is genuine, near-total duplication.

`distro_artifacts` addresses both: it gives Alpine its own disjoint branch while
letting Debian and Ubuntu share a factored base list with a one-line delta.

The target image reads:

```python
# module-core/images/ssh-image.star — one definition, three distros
_APT_SSH = [
    "systemd-sysv", "systemd-resolved", "init",
    "libc6", "libc-bin", "base-files", "base-passwd",
    "dash", "diffutils", "coreutils",
    "dpkg", "apt", "openssh-server", "network-manager",
]

image(
    name = "ssh-image",
    artifacts = ["linux", "bash"],          # distro-neutral: kernel via machine, bash
    distro_artifacts = {
        "alpine": [
            "base-files-ssh", "busybox", "busybox-binsh", "musl",
            "kmod", "util-linux", "e2fsprogs", "eudev",
            "openrc", "apk-tools", "network-config", "dhcpcd", "openssh",
        ],
        "debian": _APT_SSH,
        "ubuntu": _APT_SSH + ["nm-manage-ethernet"],
    },
)
```

## Requirements

### R1 — `distro_artifacts` field on `image()`

`image()` gains a `distro_artifacts = {}` keyword: a map from distro name to a
list of artifact names. The selected distro's list is merged into the image's
artifacts using the **already-computed `effective_distro`** (the cascade at
`modules/module-core/classes/image.star:79-85`), before the provides-resolution
loop and `resolve_closure`, so the merged entries inherit provides-resolution,
closure-walking, and rootfs assembly identically to `artifacts`:

```python
all_artifacts = list(artifacts) + list(distro_artifacts.get(effective_distro, []))
if effective_distro == "alpine":
    all_artifacts = all_artifacts + list(ctx.machine_config.packages)
elif _is_apt_distro(effective_distro):
    all_artifacts = all_artifacts + _DEBIAN_ESSENTIAL
```

This is a **pure `image.star` change** — no Go. Unlike `distro_deps`, which is
folded into the dependency graph during the Go closure walk (`DepsForDistro`,
`internal/starlark/loader.go:654`, `internal/resolve/dag.go:90`),
`distro_artifacts` resolves entirely in Starlark because `image()` already
computes its artifact closure there.

### R2 — Only the built distro's branch is consulted; non-selected branches are inert

The whole point of consolidating images into core is that a single shared image
carries branches for distros a given project may never build — an Alpine-only
project should be able to evaluate and build a core `ssh-image` that also has
`"debian"` and `"ubuntu"` branches, **without** loading `module-debian` or
`module-ubuntu`. So the selection must be lazy: the merge consults exactly the
effective distro's branch and nothing else.

```python
all_artifacts = list(artifacts) + list(distro_artifacts.get(effective_distro, []))
```

A non-selected branch (`"ubuntu"` in an Alpine build) is just an unreferenced
list of strings in a dict literal: its names are never provides-resolved, never
walked by `resolve_closure`, and never force their distro's feed module to be
present. Validation of artifact names therefore happens **only for the branch
being built**, through the normal closure resolution that already fails loudly
on an unresolvable name — and only when that distro is actually the build
target.

There is deliberately **no eager check that every key is a "known" distro.**
Such a check would defeat the goal twice over:

- It would make a shared core image fail to evaluate in any project that doesn't
  load every referenced distro's module — i.e. it would _force inclusion_ of
  distros you never build, the exact opposite of lazy materialization.
- The distro set is open — new distro modules can appear — so core cannot own a
  closed universe of valid distro names to validate against.

The accepted cost is that a typo'd key for a distro you never build (`"debain"`)
is silently inert. That is the right trade for an open, lazily-resolved distro
system: a misspelled key costs nothing because the branch is never reached, and
a typo for the distro you _are_ building surfaces when its required packages are
missing from the assembled rootfs. (A `yoe check`-style lint could warn on keys
outside the project's configured distros without gating the build; out of scope
here.)

### R3 — Cache-neutral by construction

`distro_artifacts` needs **no hash gating**. The merged result becomes the
image's existing `artifacts` field, which already participates in the unit hash
(`internal/resolve/hash.go`), and each distro builds into a distinct
`build/<distro>/<image>.<scope>/` directory regardless. There is no shared hash
line to gate — contrast `distro_deps`, which writes a per-distro `fmt.Fprintf`
gated on a non-empty check precisely because it folds into a hash that would
otherwise be shared.

### R4 — Distro-aware machine config (the relocation enabler)

The blocker to moving images into core is the kernel helper: Debian and Ubuntu
images `load("//classes/kernel.star", "debian_kernel")` from their own module
and call `debian_kernel()` / `ubuntu_kernel()` — a load-time dependency that
core **cannot** satisfy, because core is the bottom layer and may not `load()`
from a distro module. (The Alpine images, by contrast, already load only `@core`
and reference plain strings, so they are core-ready today.)

The helpers exist to keep the kernel meta-package **arch-generic**
(`linux-image-amd64` on x86_64, `linux-image-arm64` on arm64, keyed on
`ctx.arch`). Hardcoding a bare `"linux-image-arm64"` string into
`distro_artifacts` would regress multi-arch.

Resolution: make the **machine config distro-aware**, so the names an image
references resolve to the right concrete unit per `(machine, distro)`. This is
the machine-layer member of the same `distro_*` family as `distro_artifacts`
(images) and `distro_deps` (units) — not a kernel one-off. Two parts of a machine
legitimately vary by distro:

- **Kernel** — `qemu-x86_64` wants a from-source kernel on Alpine but the stock
  feed kernel on apt distros; `raspberrypi5` wants the custom `linux-rpi5` on
  every distro.
- **Bootloader / board support** — the machine `packages` list. `qemu-x86_64`'s
  `syslinux` is a from-source musl unit on Alpine and the apt `syslinux` package
  on Debian; board firmware is the same story.

Because the machine is already arch-specific (`raspberrypi5` is arm64,
`qemu-x86_64` is x86_64), arch falls out for free and the `*_kernel()` helpers
are retired.

**Only the unit name varies per distro — `defconfig` belongs to the kernel unit,
`cmdline` is board-level.** A from-source kernel's `defconfig` lives in that
unit's own build (e.g. `linux-rpi5` runs `make bcm2712_defconfig` itself), and a
feed kernel has none; `cmdline` is a property of the board, identical across
distros. So in practice the only thing that varies per distro is *which unit*
`"linux"` resolves to — a `distro_unit` name-map on the `kernel(...)` block. The
flat single-`unit` form stays valid where the kernel is distro-neutral:

```python
# qemu-x86_64 — from-source kernel on Alpine, stock feed kernel on apt distros
kernel = kernel(
    distro_unit = {
        "alpine": "linux-qemu",
        "debian": "linux-image-amd64",
        "ubuntu": "linux-image-generic",
    },
    provides = "linux",
    cmdline = "...",        # board-level, shared across distros
)

# raspberrypi5 — same kernel on every distro: flat form, no map
kernel = kernel(
    unit = "linux-rpi5",
    provides = "linux",
    defconfig = "bcm2712_defconfig",
    cmdline = "...",
)
```

(If a machine ever needed a genuinely per-distro `cmdline`, the whole
`kernel(...)` block could go per-distro via a `distro_kernel({...})` wrapper — but
no known board requires it, so `distro_unit` is the form we ship.)

The machine `packages` list gains the matching per-distro form (a
`distro_packages` map, the machine analog of `distro_artifacts`) for the
bootloader/firmware split.

**Where resolution happens — `image()`, not the global `provides` table.**
`ctx.provides` is built once, from the project's _default_ machine, with no distro
in scope (`internal/starlark/loader.go:300-334` registers a single
`provides["linux"] = machine.Kernel.Unit`). A distro-blind global table cannot
pick a per-distro kernel. So `image()` — the one place the effective distro is
known — does the resolution: it reads the machine's per-distro kernel from
`ctx.machine_config.kernel` and substitutes the unit for `effective_distro` while
resolving the `"linux"` entry in the artifact list. Single-`unit` machines (rpi5)
keep the global registration and need no override; per-distro machines (qemu)
carry no global entry and are resolved entirely in `image()`.

The **rpi5 proving case does not actually exercise per-distro kernels**:
`linux-rpi5` is the kernel on every distro there, so its flat
`kernel(unit = ...)` works unchanged and `distro_kernel` is unnecessary. The
distro-aware machinery earns its keep on `qemu` (and any board mixing source and
feed kernels across distros); rpi5 needs only the image side (`distro_artifacts`
plus `"linux"` resolving through the machine).

**Alternative considered — lean on per-distro `provides` resolution.** Each
kernel unit could declare `provides "linux"`, be distro-tagged, and let the
distro-scoped closure pick the matching one with no new machine field. Rejected
as the primary mechanism because `provides` is global, not per-board: several
units provide `"linux"`, and the machine is precisely what pins "for _this
board_, the linux provider is X." The machine selector still rides `provides` for
the final name resolution; it only adds the board×distro disambiguation that
`provides` alone cannot express.

**Implementation landing sites.** `kernel()` is a Go builtin, not a Starlark
function — `fnKernel` (`internal/starlark/builtins.go:421-423`) is a one-liner
that packs its kwargs into a generic struct via `makeStruct` (`builtins.go:382`)
and validates nothing, so a `distro_unit`/`distro_kernel` kwarg is already
accepted-and-stored today. The real work lands in two places:

- **`fnMachine`, the typed `KernelConfig` extraction (`builtins.go:582-591`).**
  This reads the kernel struct's fields into the Go `KernelConfig`
  (`internal/starlark/types.go`), which gains a `DistroUnit map[string]string`.
  `fnMachine` only **parses, validates, and stores** the per-distro map — the
  build distro is not in scope at machine-parse time, so no selection happens
  here. Fail loud when a kernel sets neither `unit` nor `distro_unit`, or both. The
  same per-distro treatment applies to the machine `packages` extraction
  (`builtins.go:592`) for the `distro_packages` split. The stored map is re-exposed
  to Starlark as `ctx.machine_config.kernel.distro_unit` at
  `internal/starlark/loader.go:1140`. `distro_unit` is a plain dict kwarg on the
  existing `kernel()` builtin — `fnKernel`/`makeStruct` already accept arbitrary
  kwargs, so no new builtin is needed.
- **`image()` (`modules/module-core/classes/image.star`) — the resolution point.**
  Where the artifact list is resolved against `ctx.provides`, `image()` overrides
  the kernel provides-name (`"linux"`): it reads `ctx.machine_config.kernel` and,
  when a `distro_unit` map is present, substitutes the unit for the image's
  `effective_distro` (failing loud if that distro has no entry). This is the only
  place the effective distro is known.

Because `fnKernel` / `makeStruct` accept any kwarg with no validation, a typo'd
kernel field is silently dropped today; per the "explicit over implicit / fail
loud" rule, R4 adds field validation in `fnMachine`'s typed extraction (where the
field set is known), not in the deliberately permissive `fnKernel`.

### R5 — Relocate image definitions to `module-core/images/`

With R4 removing the only cross-module load, `base-image`, `dev-image`, and
`ssh-image` move to `module-core/images/` as single definitions using
`distro_artifacts`. The per-module copies in `module-alpine/images/`,
`module-debian/images/`, and `module-ubuntu/images/` are **deleted** — no
re-export stubs, no compatibility shims (`CLAUDE.md`: "No backward compatibility
concerns"). Distro-only leaf images that have no cross-distro counterpart (e.g.
`module-alpine/images/jukebox-image.star`, `qt-image.star`) stay where they are;
this spec consolidates only the images that are currently triplicated.

### R6 — Factor the shared apt closure

Within a consolidated image, the Debian and Ubuntu branches share a module-local
list (e.g. `_APT_SSH`) and express Ubuntu as `base + delta`
(`_APT_SSH + ["nm-manage-ethernet"]`), eliminating the debian↔ubuntu duplication
that the three-file layout could not.

### R7 — The base-files / users seam stays per-distro

User and base-file setup differs by mechanism, not just name: Alpine builds a
`base_files(...)` unit inline with `user(...)` entries; Debian/Ubuntu pull
`base-files` + `base-passwd` from the feed and seed users through maintainer
scripts. This stays inside each distro's `distro_artifacts` branch (plus the
inline `base_files()` call for Alpine). It is correct that it differs and is not
a target for unification.

### R8 — Raspberry Pi 5 proving case

A consolidated `rpi5` image (or the shared `dev-image`/`ssh-image` built for the
`raspberrypi5` machine) builds as both Alpine and Debian from one definition,
carrying the custom `linux-rpi5` kernel in both — the same flat
`kernel(unit = ...)` on every distro, so the board needs no per-distro kernel map
(R4). The Debian build
packages `linux-rpi5` as a `.deb` (the from-source → `.deb` → local repo →
mmdebstrap path is already implemented), installs it, and lays down
`/boot/kernel_2712.img`, the `bcm2712-rpi-5-b.dtb`, and overlays for the
VideoCore firmware. Because Debian does not merge machine `packages`
(`image.star:96-99`), the board-support packages (`rpi-firmware`, `rpi5-config`)
are listed explicitly in the Debian branch of `distro_artifacts`.

## Decisions

- **Name: `distro_artifacts`.** Matches the existing `distro_deps` /
  `distro_runtime_deps` prefix convention. `artifacts_distro` would be the only
  `*_distro` field in the codebase; rejected for consistency.
- **Per-distro selection is a machine-level `distro_*` family, not a core
  `apt_kernel()` helper.** A distro-aware machine config (a per-distro kernel
  `distro_unit` map plus a `distro_packages` list) keeps arch implicit
  (the machine is already arch-specific), handles both "custom kernel on every
  distro" (rpi5) and "stock feed kernel per distro" (qemu), and extends to the
  bootloader/firmware split — all with one mechanism. A core helper would
  re-derive arch, solve only the kernel, and could not express the rpi5 "custom
  everywhere" case as cleanly. The mechanism is general (the same shape as
  `distro_artifacts` on images, `distro_deps` on units), not kernel-specific.
- **Alpine keeps a disjoint branch; no forced shared base.** The Alpine and apt
  artifact lists share almost nothing, so the value of consolidation is one file
  and one name, not a shared list. We do not contort the lists to manufacture
  overlap.
- **Hard cutover, no stubs.** Per `CLAUDE.md`, the old per-module image files
  are deleted in the same change that lands the core versions.

## Open questions

- **Scope of the first cut.** Move all three consolidated images
  (`base/dev/ssh`) at once, or land `distro_artifacts` + the apt-only
  consolidation (debian/ubuntu) first and fold Alpine in afterward? The apt pair
  is the lowest-risk, highest-dedup slice.
- **qemu apt kernels: stock vs from-source.** R4's per-distro machine kernel lets
  a qemu Debian/Ubuntu image keep the stock feed kernel (full driver tree) while
  Alpine uses a from-source kernel. Confirm that is the desired split, versus
  unifying qemu on a from-source kernel across all distros for parity with the
  rpi5 case.
- **`yoe init` / e2e template.** The `e2e-project` PROJECT.star and
  `internal/init.go` reference images by name; consolidating image locations
  must keep both building out of the box (`CLAUDE.md`: "`yoe init` mirrors the
  e2e-project template").

## Phasing

Per `CLAUDE.md`'s "write docs in final form once a plan commits," the first step
lands target-state reference docs; the last verifies docs and code agree.

1. **Docs first.** Rewrite the image-configuration reference and the machine
   `kernel(...)` / `packages` docs to the post-implementation shape
   (`distro_artifacts`, the per-distro machine kernel `distro_unit` and
   `distro_packages`); add this spec's row to `docs/SPEC_PLAN_INDEX.md`.
2. **`distro_artifacts` field** — R1–R3: the `image.star` merge, key validation,
   docstring, and a `CHANGELOG` entry.
3. **Distro-aware machine config** — R4: per-distro machine kernel
   (`distro_unit`) and `distro_packages`, resolved in `image()`; retire
   `debian_kernel()` / `ubuntu_kernel()`.
4. **Relocate + factor** — R5–R7: move `base/dev/ssh` images to
   `module-core/images/`, factor the shared apt closure, delete the per-module
   copies.
5. **rpi5 proving build** — R8: consolidated rpi5 image; verify it builds and
   boots as both Alpine and Debian on real hardware.
6. **Reconcile** — confirm docs and code agree; update `yoe init` / e2e
   template.
