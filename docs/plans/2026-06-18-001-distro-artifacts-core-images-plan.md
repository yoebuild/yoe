---
title:
  "feat: distro_artifacts + distro-aware machine config + core-layer images"
type: feat
status: active
date: 2026-06-18
origin: docs/specs/2026-06-12-distro-artifacts-core-images.md
---

# distro_artifacts + core-layer images — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one image definition target every distro via a `distro_artifacts`
map, resolve each machine's kernel/board packages per `(machine, distro)`, and
collapse the triplicated `base/dev/ssh` images into one set under
`module-core/images/`.

**Architecture:** Three coupled changes. (1) `image()` gains a
`distro_artifacts` dict merged in Starlark on the already-computed
`effective_distro` — pure `image.star`, no Go. (2) The machine `kernel`
declaration gains a per-distro form; because the global `ctx.provides` is built
once and is distro-blind, the per-distro kernel is resolved **inside `image()`**
(the only place `effective_distro` is known), not via global provides — this
retires the `debian_kernel()`/`ubuntu_kernel()` helpers and removes the only
cross-module `load()` that kept images in distro modules. (3) With that load
gone, the consolidated images move into `module-core/images/` and the per-module
copies are deleted.

**Tech Stack:** Go (`internal/starlark/`), Starlark units (`modules/`,
`testdata/e2e-project/cache/modules/`), `go test`, `yoe build`.

---

## Problem Frame

Full motivation, the money-shot image, and the design rationale live in the
origin spec (`docs/specs/2026-06-12-distro-artifacts-core-images.md`, R1–R8).
This plan covers HOW: exact file boundaries, the Go `KernelConfig` change, the
`image()` resolution point, the in-tree vs external-module split, and the
sequencing that keeps each phase independently testable.

The decisive architectural fact (verified, not in the spec sketch): the
`distro_kernel({...})` authoring surface is fine, but **resolution lands in
`image()` / `ctx.machine_config`, not in the global `provides` table**, because
`internal/starlark/loader.go:300-334` builds `ctx` once from the project default
machine with no distro in scope.

---

## Scope Decisions (resolving the spec's open questions)

- **First cut order:** land `distro_artifacts` + R4 + the **apt-pair**
  consolidation (debian/ubuntu) first (Phases 1–5), then fold Alpine in and
  prove rpi5 (Phase 6). The apt pair is the lowest-risk, highest-dedup slice and
  exercises the full R4 path.
- **qemu apt kernels:** keep the **stock feed kernel** on Debian/Ubuntu
  (`linux-image-amd64` / `linux-image-generic`) and the from-source kernel on
  Alpine. This is the split R4's per-distro machine kernel expresses.
- **rpi5 kernel:** the custom `linux-rpi5` on every distro — the machine keeps
  the flat `kernel(unit = ...)` form and needs no per-distro map.

## In-tree vs external-module split (operational)

- **In-tree** (`modules/module-core`, `modules/module-bsp`, `internal/`): the
  `image.star` change, the Go `KernelConfig`/builtin change, the new core
  images, the qemu + rpi machine kernel edits. Normal commits.
- **External modules** (`module-alpine`, `module-debian`, `module-ubuntu` —
  edited under `testdata/e2e-project/cache/modules/<m>/`): deleting the
  per-module image files and the `classes/kernel.star` helpers. Per `CLAUDE.md`
  and the no-external-pushes memory: **edit the cached copy, surface the paths,
  and the user commits + pushes upstream.** A `yoe build` does
  `git fetch && git checkout FETCH_HEAD` and discards un-pushed cache edits, so
  pause for confirmation that pushes landed before any rebuild.

---

## File Structure

| File                                                                                 | Responsibility                              | Change                                                                            |
| ------------------------------------------------------------------------------------ | ------------------------------------------- | --------------------------------------------------------------------------------- |
| `modules/module-core/classes/image.star`                                             | `image()` class                             | add `distro_artifacts` merge (R1–R3) + per-distro kernel resolution (R4 consumer) |
| `internal/starlark/types.go`                                                         | `KernelConfig`                              | add `DistroUnit map[string]string`                                                |
| `internal/starlark/builtins.go`                                                      | `fnMachine` kernel extraction               | parse `distro_unit`; fail-loud field validation                                   |
| `internal/starlark/loader.go`                                                        | `buildMachineConfigStruct`, global provides | expose `distro_unit`; keep single-form provides registration                      |
| `modules/module-core/machines/qemu-x86_64.star`, `qemu-arm64.star`                   | qemu machine kernels                        | convert to `distro_unit` form                                                     |
| `modules/module-bsp/machines/raspberrypi5.star`, `raspberrypi4.star`                 | rpi machine kernels                         | stay flat; verify `provides = "linux"`                                            |
| `modules/module-core/images/ssh-image.star`, `dev-image.star`, `base-image.star`     | consolidated images                         | new, using `distro_artifacts`                                                     |
| `testdata/e2e-project/cache/modules/module-{debian,ubuntu,alpine}/images/*.star`     | per-module images                           | **delete** (external; user pushes)                                                |
| `testdata/e2e-project/cache/modules/module-{debian,ubuntu}/classes/kernel.star`      | kernel helpers                              | **delete** (external; user pushes)                                                |
| `docs/image-config.md` (or the existing image reference), machine `kernel(...)` docs | reference docs                              | rewrite to target state (Phase 0)                                                 |
| `CHANGELOG.md`                                                                       | user-facing entry                           | one bullet                                                                        |
| `docs/SPEC_PLAN_INDEX.md`                                                            | index                                       | set this plan + flip status                                                       |

---

## Phase 0 — Docs first (target-state, reviewable artifact)

Per `CLAUDE.md` ("write docs in final form once a plan commits"), land the
reference docs describing the finished shape before code.

### U0: Target-state reference docs

**Files:**

- Modify: the image-configuration reference doc under `docs/` (the one
  documenting `image()` kwargs) — add `distro_artifacts`.
- Modify: the machine reference doc under `docs/` documenting
  `kernel(...)`/`machine(...)` — add the per-distro `kernel` form and the
  `distro_packages` note.
- Modify: `docs/SPEC_PLAN_INDEX.md` — set the Plan cell for the 2026-06-12 row
  to this file.

- [ ] **Step 1:** Document `distro_artifacts` next to `artifacts` in the image
      reference: a `{distro: [names]}` map, only the built distro's branch is
      consulted, non-selected branches are inert (no module load), no
      closed-distro validation. Mirror the spec R1–R2 wording but self-contained
      (no R-numbers, per `CLAUDE.md` "no plan vocabulary in docs/").
- [ ] **Step 2:** Document the machine per-distro `kernel` form (`distro_unit`)
      and the `"linux"`-via-machine resolution, replacing any mention of
      `debian_kernel()`/`ubuntu_kernel()`.
- [ ] **Step 3:** Update the SPEC_PLAN_INDEX row: Plan →
      `[distro-artifacts-core-images](plans/2026-06-18-001-distro-artifacts-core-images-plan.md)`.
- [ ] **Step 4: Commit**

```bash
git add docs/
git commit -m "docs: target-state for distro_artifacts + per-distro machine kernel"
```

---

## Phase 1 — `distro_artifacts` field (R1–R3)

Pure `image.star`. Tested through the Go loader, which evaluates Starlark and
exposes the resolved closure.

### U1: `distro_artifacts` merge

**Files:**

- Modify: `modules/module-core/classes/image.star` (the `image()` signature
  ~line 49 and the merge block ~lines 95-99)
- Test: `internal/starlark/distro_views_test.go` (add a case; this file already
  loads projects and asserts per-distro resolution)

- [ ] **Step 1: Write the failing test.** Add to
      `internal/starlark/distro_views_test.go` a test that loads a tiny project
      whose image sets
      `distro_artifacts = {"alpine": ["a-only"], "debian": ["d-only"]}` and
      asserts the resolved artifacts for an alpine build contain `a-only` and
      not `d-only`, and vice-versa. Use the existing test's project-fixture
      helper in that file as the template for constructing the project.

```go
func TestDistroArtifactsSelectsBuiltBranch(t *testing.T) {
    // ... build project fixture with an image:
    //   image(name="img", distro="alpine",
    //         distro_artifacts={"alpine":["a-only"], "debian":["d-only"]})
    // load for alpine, assert resolved artifacts include "a-only", exclude "d-only"
}
```

- [ ] **Step 2: Run, verify it fails.**

Run:
`go test ./internal/starlark/ -run TestDistroArtifactsSelectsBuiltBranch -v`
Expected: FAIL — `distro_artifacts` is an unexpected kwarg / not merged.

- [ ] **Step 3: Implement.** In `image.star`, add `distro_artifacts={}` to the
      `image()` signature, and merge it before the machine-packages branch:

```python
all_artifacts = list(artifacts) + list(distro_artifacts.get(effective_distro, []))
if effective_distro == "alpine":
    all_artifacts = all_artifacts + list(ctx.machine_config.packages)
elif _is_apt_distro(effective_distro):
    all_artifacts = all_artifacts + _DEBIAN_ESSENTIAL
```

No key-validation loop, no `_KNOWN_DISTROS` (R2: non-selected branches inert; no
closed universe). Non-selected branches are never read.

- [ ] **Step 4: Run, verify it passes.**

Run:
`go test ./internal/starlark/ -run TestDistroArtifactsSelectsBuiltBranch -v`
Expected: PASS

- [ ] **Step 5: Inert-branch test.** Add
      `TestDistroArtifactsNonSelectedBranchIsInert`: an alpine build whose
      `distro_artifacts["debian"]` names a unit that does **not** exist anywhere
      must still resolve cleanly (proves the debian branch is never walked). Run
      it; expected PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/module-core/classes/image.star internal/starlark/distro_views_test.go
git commit -m "feat: distro_artifacts on image() — per-distro artifact lists"
```

---

## Phase 2 — Per-distro machine kernel: Go (R4 producer)

### U2: `KernelConfig.DistroUnit` + `fnMachine` parsing

**Files:**

- Modify: `internal/starlark/types.go:208-217` (`KernelConfig`)
- Modify: `internal/starlark/builtins.go:582-593` (`fnMachine` kernel
  extraction)
- Test: `internal/starlark/builtins_test.go`

- [ ] **Step 1: Write the failing test.** In `builtins_test.go`, load a
      `machine(...)` whose
      `kernel = kernel(distro_unit = {"alpine":"k-alp","debian":"k-deb"}, provides="linux")`
      and assert the parsed `Machine.Kernel.DistroUnit["debian"] == "k-deb"`.

```go
func TestMachineKernelDistroUnit(t *testing.T) {
    // load machine with kernel(distro_unit={...}, provides="linux")
    // assert m.Kernel.DistroUnit["alpine"]=="k-alp" && ["debian"]=="k-deb"
}
```

- [ ] **Step 2: Run, verify it fails.**

Run: `go test ./internal/starlark/ -run TestMachineKernelDistroUnit -v`
Expected: FAIL — `DistroUnit` field does not exist.

- [ ] **Step 3: Implement.** Add to `KernelConfig`:

```go
DistroUnit  map[string]string // per-distro kernel unit; empty for single-form machines
```

In `fnMachine` (builtins.go), after the existing `Kernel: KernelConfig{...}`
block, populate `DistroUnit` from the kernel struct and add fail-loud validation
in the typed extraction (not in the permissive `fnKernel`):

```go
kc := KernelConfig{
    Repo: structString(kernelS, "repo"), Branch: structString(kernelS, "branch"),
    Tag: structString(kernelS, "tag"), Defconfig: structString(kernelS, "defconfig"),
    DeviceTrees: structStringList(kernelS, "device_trees"),
    Unit: structString(kernelS, "unit"), Cmdline: structString(kernelS, "cmdline"),
    Provides: structString(kernelS, "provides"),
    DistroUnit: structStringMap(kernelS, "distro_unit"), // new helper, mirrors structStringList
}
if kernelS != nil {
    if kc.Unit == "" && len(kc.DistroUnit) == 0 {
        return nil, fmt.Errorf("machine %q: kernel needs unit or distro_unit", name)
    }
    if kc.Unit != "" && len(kc.DistroUnit) > 0 {
        return nil, fmt.Errorf("machine %q: kernel sets both unit and distro_unit", name)
    }
}
m := &Machine{ /* ... */ Kernel: kc, /* ... */ }
```

Add `structStringMap(s *starlarkstruct.Struct, field string) map[string]string`
next to `structStringList` in builtins.go (iterate a `*starlark.Dict`,
string→string).

- [ ] **Step 4: Run, verify it passes.** Run the same test → PASS. Add a
      negative test `TestMachineKernelUnitAndDistroUnitConflict` asserting the
      both-set error; run → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/starlark/types.go internal/starlark/builtins.go internal/starlark/builtins_test.go
git commit -m "feat: KernelConfig.DistroUnit + fail-loud kernel field validation"
```

### U3: Expose `distro_unit` in `ctx.machine_config`; keep single-form provides

**Files:**

- Modify: `internal/starlark/loader.go:1139-1146` (`buildMachineConfigStruct`)
- Modify: `internal/starlark/loader.go:329-334` (global provides — no behavior
  change for single-form)
- Test: `internal/starlark/loader_test.go`

- [ ] **Step 1: Write the failing test.** Assert that a machine with
      `distro_unit` exposes `ctx.machine_config.kernel.distro_unit["debian"]` to
      Starlark (load a unit/image that reads it, or assert the built struct).

- [ ] **Step 2: Run, verify it fails.**
      `go test ./internal/starlark/ -run TestMachineConfigDistroUnit -v` → FAIL.

- [ ] **Step 3: Implement.** In `buildMachineConfigStruct`, add the
      `distro_unit` key to the kernel sub-struct (convert `map[string]string` →
      `*starlark.Dict`). Leave the `m.Kernel.Unit != ""` guard so single-form
      machines are unchanged. At loader.go:329, leave the global registration
      as-is (`Unit == ""` for per-distro machines means no global
      `provides["linux"]` — image() does that resolution in U4).

- [ ] **Step 4: Run, verify it passes.** → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/starlark/loader.go internal/starlark/loader_test.go
git commit -m "feat: expose kernel.distro_unit in ctx.machine_config"
```

### U4: `image()` resolves `"linux"` per effective distro

**Files:**

- Modify: `modules/module-core/classes/image.star` (the provides-resolution loop
  ~lines 101-105)
- Test: `internal/starlark/distro_views_test.go`

- [ ] **Step 1: Write the failing test.** Project with a machine that sets
      `kernel(distro_unit={"alpine":"linux-qemu","debian":"linux-image-amd64"}, provides="linux")`
      and an image listing `"linux"`. Assert the alpine build resolves `"linux"`
      → `linux-qemu`, the debian build → `linux-image-amd64`.

- [ ] **Step 2: Run, verify it fails.** → FAIL (today `"linux"` resolves to the
      single global provides or is unresolved for per-distro machines).

- [ ] **Step 3: Implement.** In `image()`, compute the per-distro kernel and
      override the kernel provides-name during artifact resolution:

```python
# Resolve the machine kernel for this image's distro. Single-form machines
# register provides["linux"] globally (distro-neutral); per-distro machines
# carry no global entry, so image() picks the unit for effective_distro here —
# the only place the effective distro is known.
kernel_provides = None
kernel_unit = None
mc = getattr(ctx, "machine_config", None)
if mc != None and getattr(mc, "kernel", None) != None:
    k = mc.kernel
    kernel_provides = getattr(k, "provides", None)
    du = getattr(k, "distro_unit", None)
    if du:
        if effective_distro not in du:
            fail("image %s: machine kernel has no entry for distro %r" % (name, effective_distro))
        kernel_unit = du[effective_distro]

explicit = []
for a in all_artifacts:
    if kernel_provides and a == kernel_provides and kernel_unit != None:
        explicit.append(kernel_unit)
        continue
    r = ctx.provides.get(a, None)
    explicit.append(r if r != None else a)
```

(Single-form machines keep working through `ctx.provides` unchanged; per-distro
machines take the override branch.)

- [ ] **Step 4: Run, verify it passes.** → PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/module-core/classes/image.star internal/starlark/distro_views_test.go
git commit -m "feat: image() resolves machine kernel per effective distro"
```

---

## Phase 3 — Convert qemu machines to per-distro kernels

### U5: qemu-x86_64 / qemu-arm64 distro_unit

**Files:**

- Modify: `modules/module-core/machines/qemu-x86_64.star`
- Modify: `modules/module-core/machines/qemu-arm64.star`

- [ ] **Step 1:** Read each file's current
      `kernel(unit = "<X>", provides = "linux", ...)`. Replace `unit = "<X>"`
      with the per-distro map, moving the current value to the `alpine` key:

```python
# qemu-x86_64
kernel = kernel(
    distro_unit = {
        "alpine": "<current unit>",        # e.g. linux-qemu / linux-virt — keep existing
        "debian": "linux-image-amd64",
        "ubuntu": "linux-image-generic",
    },
    provides = "linux",
    cmdline = "<keep existing cmdline>",
)
```

For `qemu-arm64`, the apt names are `linux-image-arm64` (debian) and
`linux-image-generic` (ubuntu); alpine keeps its current arm64 kernel unit.

- [ ] **Step 2: Run loader tests.** `go test ./internal/starlark/...` → PASS
      (machines still parse; validation from U2 accepts the new form).
- [ ] **Step 3: Commit**

```bash
git add modules/module-core/machines/qemu-x86_64.star modules/module-core/machines/qemu-arm64.star
git commit -m "feat: qemu machines select kernel per distro (stock feed on apt)"
```

---

## Phase 4 — Consolidate the apt pair into core (debian + ubuntu)

### U6: Core `ssh-image` / `base-image` / `dev-image` (apt branches)

**Files:**

- Create: `modules/module-core/images/ssh-image.star`, `base-image.star`,
  `dev-image.star`
- Read (source of truth for the lists): the existing
  `testdata/e2e-project/cache/modules/module-{debian,ubuntu}/images/<name>.star`

- [ ] **Step 1:** For each of `ssh/base/dev`, create the core image. Carry the
      debian and ubuntu artifact lists verbatim from the existing per-module
      files into `distro_artifacts`, factoring the shared apt closure (Ubuntu =
      debian list + delta), and replacing `debian_kernel()`/`ubuntu_kernel()`
      with the plain string `"linux"` (now resolved by the machine via U4).
      Exemplar (`ssh-image`):

```python
load("@core//classes/image.star", "image")

_APT_SSH = [
    "systemd-sysv", "systemd-resolved", "init",
    "libc6", "libc-bin", "base-files", "base-passwd",
    "dash", "diffutils", "coreutils",
    "dpkg", "apt", "openssh-server", "network-manager",
]

image(
    name = "ssh-image",
    artifacts = ["linux", "bash"],
    distro_artifacts = {
        "debian": _APT_SSH,
        "ubuntu": _APT_SSH + ["nm-manage-ethernet"],
        # "alpine" branch added in Phase 6 (U8)
    },
)
```

For `base-image` and `dev-image`, take their existing debian/ubuntu lists from
the cached module files and apply the identical transform (shared `_APT_*`
base + Ubuntu delta; `debian_kernel()`/`ubuntu_kernel()` → `"linux"`). The
kernel meta-package (`linux-image-amd64`, etc.) is NOT listed in the image
anymore — it arrives via the machine.

- [ ] **Step 2:** Point the e2e project at the core images. In
      `testdata/e2e-project/PROJECT.star`, ensure the image references resolve
      to the new `module-core/images/` (and mirror any needed change into
      `internal/init.go` per the "`yoe init` mirrors e2e" rule — see U10).

- [ ] **Step 3: Build-verify (user runs).** Ask the user to run, on x86_64:

```
yoe build -distro debian ssh-image
yoe build -distro ubuntu ssh-image
```

Expected: both resolve and assemble; the kernel in the rootfs is the stock feed
kernel. (Per the no-builds memory, the assistant does not run this — request it
and read `build/<distro>/ssh-image.machine/build.json` on failure.)

- [ ] **Step 4: Commit (in-tree only at this point)**

```bash
git add modules/module-core/images/ testdata/e2e-project/PROJECT.star
git commit -m "feat: consolidate debian+ubuntu base/dev/ssh images into module-core"
```

### U7: Delete the per-module debian/ubuntu images + kernel helpers (external)

**Files (external — cached copies; user pushes):**

- Delete:
  `testdata/e2e-project/cache/modules/module-debian/images/{base,dev,ssh}-image.star`
- Delete:
  `testdata/e2e-project/cache/modules/module-ubuntu/images/{base,dev,ssh}-image.star`
- Delete: `testdata/e2e-project/cache/modules/module-debian/classes/kernel.star`
- Delete: `testdata/e2e-project/cache/modules/module-ubuntu/classes/kernel.star`

- [ ] **Step 1:** Remove the files in the cached copies. Surface the full paths
      to the user and note these are `module-debian` / `module-ubuntu` upstream
      repos.
- [ ] **Step 2:** Per the external-module memory: **do not commit/push these
      upstream.** Print the exact list and ask the user to commit + push in
      `module-debian` and `module-ubuntu`, then confirm before any rebuild (a
      build `git checkout FETCH_HEAD` would otherwise restore the deleted
      files).
- [ ] **Step 3:** Once the user confirms pushes landed, request a re-run of the
      U6 build-verify to confirm nothing still references the deleted helpers.

---

## Phase 5 — Verify apt pair end-to-end

### U8a: apt-pair regression gate

- [ ] **Step 1:** `go test ./internal/starlark/... ./internal/resolve/...`
      Expected: PASS.
- [ ] **Step 2:** Ask the user to run the debian + ubuntu dev-image boot path
      that the nightly e2e matrix uses (build → QEMU boot → SSH) and confirm
      green. This is the gate before touching Alpine.
- [ ] **Step 3:** Checkpoint commit if any in-tree fixups were needed.

---

## Phase 6 — Fold Alpine in + rpi5 proving case (R5–R8)

### U8: Add the Alpine branch to the core images

**Files:**

- Modify: `modules/module-core/images/{ssh,base,dev}-image.star`
- Delete (external; user pushes):
  `testdata/e2e-project/cache/modules/module-alpine/images/{base,dev,ssh}-image.star`

- [ ] **Step 1:** Add the `"alpine"` branch to each core image, carrying the
      Alpine list verbatim from the existing `module-alpine/images/<name>.star`
      (the disjoint openrc/musl/apk closure). For `ssh-image` include the inline
      `base_files(name="base-files-ssh", users=[...])` and its
      `load("@core//classes/users.star", "user")` / `base-files.star` loads,
      moved from the Alpine file (R7: the base-files/users seam stays in the
      Alpine branch).

```python
distro_artifacts = {
    "alpine": [
        "base-files-ssh", "busybox", "busybox-binsh", "musl",
        "kmod", "util-linux", "e2fsprogs", "eudev",
        "openrc", "apk-tools", "network-config", "dhcpcd", "openssh",
    ],
    "debian": _APT_SSH,
    "ubuntu": _APT_SSH + ["nm-manage-ethernet"],
}
```

- [ ] **Step 2: Build-verify (user runs).**
      `yoe build -distro alpine ssh-image`. Expected: resolves + assembles;
      kernel resolves via the machine (single global provides for rpi, or
      `distro_unit["alpine"]` for qemu).
- [ ] **Step 3:** Delete the `module-alpine` per-module image files (cached);
      surface paths; user commits + pushes `module-alpine`; confirm before
      rebuild.
- [ ] **Step 4: Commit (in-tree)**

```bash
git add modules/module-core/images/
git commit -m "feat: fold Alpine branch into core base/dev/ssh images; retire per-module copies"
```

### U9: rpi5 proving build

**Files:**

- Verify: `modules/module-bsp/machines/raspberrypi5.star` (kernel stays flat
  `unit = "linux-rpi5"`, `provides = "linux"`)
- The Debian rpi5 image lists `"linux"`, `"rpi-firmware"`, `"rpi5-config"` in
  its `distro_artifacts["debian"]` (machine packages are not auto-merged on
  apt).

- [ ] **Step 1:** Confirm `raspberrypi5.star` keeps the flat kernel form (no
      `distro_unit`) — `provides = "linux"` resolves to `linux-rpi5` on every
      distro via the global registration.
- [ ] **Step 2:** Ensure the consolidated image's `distro_artifacts["debian"]`
      includes `rpi-firmware` and `rpi5-config` (explicit, since
      `image.star:96-99` does not merge machine packages for apt).
- [ ] **Step 3: Build-verify (user runs), targeting the rpi5 machine:**

```
yoe build -machine raspberrypi5 -distro alpine dev-image
yoe build -machine raspberrypi5 -distro debian dev-image
```

Expected: both assemble; the Debian build packages `linux-rpi5` as a `.deb`,
installs it, and the boot partition carries `/boot/kernel_2712.img`,
`bcm2712-rpi-5-b.dtb`, overlays. Verify on real hardware (the spec's R8
acceptance — QEMU does not exercise the Pi 5 firmware boot).

- [ ] **Step 4: Commit** any image fixups.

---

## Phase 7 — Reconcile (docs ↔ code) + changelog + index

### U10: `yoe init` / e2e template + changelog + index + docs verify

**Files:**

- Modify: `internal/init.go` (if the init template references the moved images /
  qemu kernel shape — keep `yoe init` projects building)
- Modify: `CHANGELOG.md`
- Modify: `docs/SPEC_PLAN_INDEX.md`
- Verify: the Phase 0 docs match the shipped behavior

- [ ] **Step 1:** Reconcile `internal/init.go` with
      `testdata/e2e-project/PROJECT.star` so a fresh `yoe init` project resolves
      the core images and per-distro kernels (per the "init mirrors e2e" rule).
- [ ] **Step 2: Changelog.** One user-facing bullet, no file paths/mechanism:

```
- One image definition can now target multiple distros: set `distro_artifacts`
  to give Alpine, Debian, and Ubuntu their own package lists, and reference
  `linux` so each machine picks the right kernel per distro.
```

- [ ] **Step 3:** Flip the SPEC_PLAN_INDEX row Status to `Partial` (apt pair
      done; rpi5 awaiting hardware) or `Done` once the rpi5 hardware boot is
      confirmed.
- [ ] **Step 4:** Re-read the Phase 0 docs against the final code; fix any drift
      (no `(planned)` flags should remain — the feature shipped).
- [ ] **Step 5: Commit**

```bash
git add internal/init.go CHANGELOG.md docs/
git commit -m "docs: reconcile distro_artifacts feature; changelog; index status"
```

---

## Self-Review

- **Spec coverage:** R1 → U1; R2 (inert branches) → U1 Step 5; R3
  (cache-neutral, no hash change) → no hash work needed (merged into existing
  `artifacts`); R4 → U2–U5 (with the verified resolution-in-`image()`
  correction); R5 → U6/U7/U8; R6 (factored apt closure) → U6 Step 1; R7
  (base-files seam) → U8 Step 1; R8 → U9.
- **External-module hazard:** U7/U8 deletions are in external repos — every such
  step pauses for user push + confirmation before rebuild (no-external-pushes
  memory; `git checkout FETCH_HEAD` discard risk).
- **No-builds rule:** every `yoe build` step is explicitly "user runs"; the
  assistant edits and inspects `build/<distro>/<unit>/` artifacts on failure.
- **Type consistency:** `KernelConfig.DistroUnit map[string]string` (U2) is the
  same field read in U3 (`buildMachineConfigStruct`) and consumed in U4
  (`mc.kernel.distro_unit`); the new `structStringMap` helper is defined in U2.
- **Open risk to watch during U4:** `getattr` guards on `ctx.machine_config` —
  imageless/machineless evaluation must not crash; the existing `image.star`
  already guards `machine_config` access, so mirror that pattern.
