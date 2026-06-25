# Machine package feed

**Date:** 2026-06-24
**Status:** Spec only

## Problem

A unit with `scope = "machine"` genuinely differs per machine but keeps a single
canonical package name. `base-files` is the live example: it is the boot-time
baseline every image inherits, and it bakes machine-specific content —
`/boot/extlinux/extlinux.conf`, `/etc/os-release`, `/etc/inittab`, the FHS
skeleton — so each machine needs its own build. But every machine emits the
**same filename** into the **same directory**.

Today all packages are published per project / per distro / per arch:

```
repo/<proj>/<distro>/<arch>/<pkg>-<ver>-r<rel>.apk          # apk
repo/<proj>/debian/pool/<component>/<initial>/<src>/...deb  # apt
```

There is no machine axis. So an rpi5 build and an rpi4 build in one tree both
write `base-files-14.0-r14.apk` into `repo/<proj>/alpine/aarch64/` — last writer
wins, and the APKINDEX references only one of them. Two machines cannot coexist
in one tree. This is the blocker called out in `docs/roadmap.md`
("base-files is modified by machine … this needs to be solved before building
multiple machines in one tree").

Kernels escape the collision because they carry distinct names (`linux-rpi5` vs
`linux-rpi4`, resolved per-machine through the `linux` virtual). `base-files`
cannot be renamed: on the apt distros, `libc6` carries
`Breaks: base-files (< 13.3~)` and `dbus` carries `Depends: base-files (>= 13.4~)`,
so the package the rest of the closure constrains must be literally named
`base-files`.

## Why not name-qualification + `provides`

The tempting cheap fix is to do for `base-files` what kernels do: rename it
`base-files-rpi5` and declare `provides = ["base-files"]`. The machinery exists
and emits versioned self-provides on both backends:

- apk (`internal/artifact/apk.go`): bare `provides = base-files` is auto-stamped
  to `provides = base-files=14.0-r14` (apk rejects an unversioned provide for a
  versioned dep).
- apt (`internal/build/executor.go` `debProvides`): bare `Provides: base-files`
  becomes `Provides: base-files (= 14.0)`.

`14.0` satisfies both `dbus`'s `>= 13.4~` and `libc6`'s `Breaks: < 13.3~`, so the
constraints are met. **But this is rejected**, for a decisive reason:

In a shared feed, `base-files-rpi5` and `base-files-rpi4` advertise the
**identical** virtual `base-files=14.0-r14`. Anything that resolves the *bare
virtual* freely — an on-device `apk add` of a package depending on `base-files`,
an `apk fix`, an apt solve that has not already pinned the concrete name — is
free to pull the **wrong machine's** package. Image assembly avoids this only
because the closure pins the concrete name; the guarantee is "correct as long as
every consumer pins the concrete name and never touches the virtual," which is
fragile, especially on-device. yoe also has no `conflicts` field today (only
`replaces`), so the two variants cannot even declare mutual exclusion. The
problem is **visibility**, and the right fix removes the wrong machine's
packages from view rather than hoping the solver picks right.

## Design

Add a **machine axis** to the package feed. Machine-scoped packages publish into
a per-machine subtree; the shared per-arch feed is unchanged. An image (and the
device it produces) is assembled against *shared feed + its own machine feed*
and never sees another machine's packages. Canonical names are preserved — no
`provides` hack, no rename — because a given canonical name is unique **within**
each machine feed.

Routing is driven entirely by `unit.Scope`: `scope == "machine"` publishes to the
machine subtree, everything else to the shared feed. This reuses the
`ScopeDir`/`RepoArchDir` seam already in `internal/build/executor.go`; the only
new decision at publish time is *which repo root* (shared vs machine), based on
scope.

### apk layout

`<machine>` slots in as a new directory level **above** the existing `<arch>`
seam:

```
repo/<proj>/alpine/<arch>/                       # shared (musl libs, apps)
  APKINDEX.tar.gz, *.apk
repo/<proj>/alpine/<machine>/<arch>/             # machine-scoped only
  base-files-14.0-r14.apk
  APKINDEX.tar.gz
```

The `<arch>` level under `<machine>` is **redundant but required**: a machine
maps to exactly one arch, so it carries no information, but apk-tools
unconditionally appends `/$(apk --print-arch)/APKINDEX.tar.gz` to any repo URL
(confirmed by the shipped `/etc/apk/repositories` template and the `RepoArchDir`
comment). yoe cannot tell apk "arch is implied here," so the directory must
exist. Because `<machine>` sits above `<arch>`, the existing `RepoArchDir` /
`Publish` / `ArchDirs` / `GenerateIndex` logic keeps working **unchanged** inside
each machine subtree — the new code only chooses the machine-scoped repo root vs
the shared root.

Device `/etc/apk/repositories` gains a second line, the machine-level URL; apk
fills in the single `<arch>`:

```
<feed>/<proj>/alpine                 # shared
<feed>/<proj>/alpine/<machine>       # this machine
```

### apt layout

apt's repo has axes **suite × component × arch** (`internal/repo/deb_emitter.go`),
with `Components` already a first-class field defaulting to `["main"]`. The
natural mapping is **machine = component** — not a new directory dimension:

```
dists/<suite>/main/binary-<arch>/Packages         # shared
pool/main/<initial>/<src>/...deb
dists/<suite>/<machine>/binary-<arch>/Packages     # machine-scoped
pool/<machine>/<initial>/<src>/base-files_14.0_arm64.deb
```

Device `sources.list`: **one line, two components** —

```
deb <base> <suite> main <machine>
```

Machine = component is strictly lighter than machine = suite, and reuses
mechanisms apt already has:

1. **The pool separates for free.** Pool path embeds the component
   (`pool/<component>/...`), so rpi4's and rpi5's `base-files_14.0_arm64.deb`
   land in distinct paths — no filename collision while keeping the canonical
   name.
2. **Isolation is exactly apt's component model.** An rpi5 device lists component
   `rpi5` and never sees `rpi4`'s `base-files` — the same guarantee as the apk
   machine feed, via a mechanism apt already understands.
3. **No second repo line, no extra signature.** Everything stays under one
   `dists/<suite>/`, so it is one `InRelease`, one suite — just an added
   component subtree. (Machine = suite was rejected: a separate `Release` /
   `InRelease` per machine and shared packages duplicated or carried on a second
   `deb` line, for no benefit over a component.)

### Backend asymmetry (intended)

| | shared | machine-scoped | device config |
|---|---|---|---|
| **apk** | `repo/<proj>/alpine/<arch>/` | `repo/<proj>/alpine/<machine>/<arch>/` | extra repo **root** line in `/etc/apk/repositories` |
| **apt** | component `main` | component `<machine>` (`pool/<machine>/…`) | extra **component** token on the `deb` line |

The two backends express one concept ("machine feed") two ways because their
repo formats differ — apk forces machine to be a directory level above `<arch>`;
apt expresses it as a component. Both keep canonical names and isolate by
visibility.

## Components and consumers to update

- **Publish routing** (`internal/build/executor.go`, `internal/repo`): choose the
  machine-scoped repo root for `scope == "machine"` units; shared root
  otherwise. apk: publish under `<machine>/<arch>/`. apt: publish into
  `pool/<machine>/...` and add `<machine>` to the component set passed to
  `GenerateDebianIndex`.
- **Repo walkers** (`internal/repo/local.go`): `List`, `Info`, `Remove`,
  `Clean`, `ArchDirs` must descend into machine subtrees. `Clean`'s keep-set
  must account for machine-scoped units landing under `<machine>/<arch>/` rather
  than `<arch>/`.
- **Image assembly**: add the machine feed to the apk/apt search path for the
  image's machine (shared + `<machine>`). apk: a second `-X`/repository entry;
  apt: append `<machine>` to the components.
- **Feed server** (`docs/feed-server.md`): serve the machine subtree; the URL
  scheme already nests under `/<proj>/<distro>/…`, so `/<machine>/<arch>/` falls
  out of the existing static serving.
- **Device repo config** (`yoe device repo add`, the `base-files` `repositories`
  template, `internal/device/deploy.go`): write both the shared and machine
  lines (apk) or both components (apt). The machine is known at image build
  time.
- **`base-files` `repositories` template** (`modules/module-core/units/base/base-files/`):
  document the two-URL (apk) / two-component (apt) shape.

## Scope

In scope: both backends (apk per-arch APKINDEX, apt component/pool), publish
routing by `unit.Scope`, repo walkers, image assembly, feed server, device repo
config, docs.

Out of scope:

- A `conflicts` unit field. The machine feed removes the cross-machine
  visibility that would have motivated it; not needed for this design.
- Machine-qualified package names / `provides`-based disambiguation. Explicitly
  rejected above.
- Multiple machines sharing one image (N/A — one image targets one machine).

## Open questions

- **Machine token spelling.** Machines are spelled `raspberrypi5`, `qemu-arm64`,
  `qemu-x86_64` today. The directory/component token should be the machine name
  verbatim. Confirm no machine name needs sanitizing for an apt component (apt
  components are path segments; current names are already path-safe).
- **`apk --print-arch` vs machine arch.** The `<machine>/<arch>/` dir uses the
  apk arch token (`aarch64`), same translation as the shared feed via
  `RepoArchDir`. No new translation, but verify the machine→arch lookup is
  available at publish time (it is, via build `Options`).
