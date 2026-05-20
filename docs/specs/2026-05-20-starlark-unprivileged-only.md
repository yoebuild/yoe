---
date: 2026-05-20
topic: starlark-unprivileged-only
---

# Restrict Starlark to unprivileged container execution

## Summary

Today the Starlark `run()` builtin exposes two escalation kwargs — `privileged
= True` (root in the privileged container) and `host = True` (shell on the
host outside any container). Any unit author or class author can use them.
This spec removes both kwargs and moves the operations that legitimately
need root-in-container or host execution into Go code in `internal/`, invoked
by the executor based on `unit.Class`. The audit surface for "code that can
run as root on the build machine" shrinks from "every `.star` file across
every imported module" to a single grep — `RunInContainer(... NoUser: true)`
in `internal/`.

The change is bounded by today's reality: only two `.star` files anywhere in
the tree use the escalation kwargs — `modules/module-core/classes/image.star`
(losetup/sfdisk/extlinux for bootloader install) and
`modules/module-core/classes/container.star` (`docker build` on the host).
Both are yoe-shipped classes, not unit-author scripts. Replacing them with
Go-side drivers is a refactor, not a new design.

This spec does **not** remove `--privileged` from the container itself. That
requires migrating image assembly off `losetup` / `mount` / `extlinux` to a
Go image assembler (already discussed in
[Build Environment §"Reducing Dependence on Docker's /dev"](../build-environment.md));
it is independent work and remains tracked there. This spec narrows _who can
request root-in-container_, not _whether the container is privileged_.

---

## Problem Frame

`internal/build/starlark_exec.go` defines the `run()` builtin used inside
unit tasks. `fnRun` parses three kwargs that affect the execution boundary:

- `check` — bool, controls error propagation. Benign.
- `privileged = True` — sets `cfg.NoUser = true` and routes through
  `RunSimple` (no bwrap), so the shell command executes as **uid 0** inside
  the `--privileged` build container. All Linux caps, all of `/dev`, no
  seccomp.
- `host = True` — bypasses the container entirely and runs the command via
  `bash -c` directly on the host, as the host user.

Both escalations are author-controlled at the call site. A unit that ships
in any imported module can write `run("rm -rf $HOME", host = True)` or
`run("dd if=/dev/zero of=/dev/sda", privileged = True)` and the executor
will dutifully comply. The trust boundary today is "every line of Starlark
that flows from an imported module" — `PROJECT.star`, every unit file,
every class file, and any helper they `load(...)`.

A grep of the tree shows the actual user base:

```
modules/module-core/classes/image.star:266     ..., privileged=True)
modules/module-core/classes/container.star:17  host_arch = run("uname -m", host=True).stdout...
modules/module-core/classes/container.star:22  ..., host=True)
modules/module-core/classes/container.star:25  ..., host=True)
```

Two files, both `classes/*.star` in yoe's `module-core`. No unit author has
called these kwargs anywhere in the tree, and the cached external modules
(`module-alpine`, `module-jetson`) don't either. The kwargs exist purely so
the two yoe-shipped classes can drive container assembly and image assembly.

Reading the escalations out of Starlark and into Go has three properties:

1. The set of callers becomes auditable in one place. Every privileged
   operation lives under `internal/` and is reachable via a small number of
   `RunInContainer(cfg.NoUser: true)` and direct host `exec.Command` sites.
2. The trust boundary stops moving with every new module the project imports.
   A new module pin can no longer add a `host = True` escalation in its
   classes by virtue of being loaded.
3. The class-author API for "describe an image" or "describe a container
   unit" becomes a fixed declarative shape (class fields + Go driver) rather
   than "an arbitrary shell script we trust because it ships in module-core
   today."

---

## Actors

- A1. Unit author: writes a `unit(...)` call with tasks/steps that compile,
  package, or test a single piece of software. Should be able to call
  `run("...")` for compilation but never need root-in-container or host
  execution to ship a package.
- A2. Class author: writes a reusable factory in `classes/*.star` —
  `image(...)`, `container(...)`, `go_binary(...)`, etc. Today some classes
  use `privileged = True` / `host = True` because the operations they wrap
  genuinely need them. After this spec, classes are expressive via fields,
  not via shell-out.
- A3. yoe core developer: maintains `internal/build`, `internal/image`,
  `internal/container.go`, and the executor. Owns the Go-side drivers that
  replace the escalation kwargs.
- A4. Build operator: runs `yoe build` on a developer or CI machine. Wants
  the smallest possible blast radius from importing a module they didn't
  read line-by-line.

---

## Key Flows

- F1. Unit task calls `run("make && make install")`.
  - **Trigger:** Build executes a unit's task step.
  - **Actors:** A1, A3.
  - **Steps:** The Starlark `run()` builtin routes through `RunInSandbox`
    with `cfg.NoUser = false`. The command lands in the build container as
    the host user, with the standard mounts (`/project`, `/build/src`,
    `/build/destdir`, `/build/sysroot`).
  - **Outcome:** Compilation succeeds; no privilege escalation is possible
    from this call site.
  - **Covered by:** R1, R6.

- F2. Image-class unit builds (e.g. `jukebox-image`).
  - **Trigger:** Build executes a unit whose `Class == "image"`.
  - **Actors:** A3 (Go executor), A2 (image class declares image shape via
    fields only — packages, hostname, services).
  - **Steps:** The executor runs the unit's tasks (`run("apk add ...")` and
    similar, all unprivileged) to populate `destdir/rootfs/`. After tasks
    complete, the executor invokes a Go-side image-assembly driver
    (`internal/image/assemble.go` or equivalent) that reads the machine's
    partition spec, creates partitions, formats filesystems, installs the
    bootloader, and writes `destdir/<name>.img`. Each step that needs root
    in the privileged container is a direct `RunInContainer(... NoUser:
    true)` call in Go.
  - **Outcome:** A bootable image, assembled without any Starlark code path
    that requests root-in-container.
  - **Covered by:** R2, R4, R5.

- F3. Container-class unit builds (e.g. `toolchain-musl`).
  - **Trigger:** Build executes a unit whose `Class == "container"`.
  - **Actors:** A3 (Go executor), A2 (container class declares the
    Dockerfile path).
  - **Steps:** The executor reads the unit's Dockerfile location from
    fields (`unit.DefinedIn` + `dockerfile = "..."` field today), resolves
    the target arch and tag, and invokes `docker build` (or `docker buildx
    build --platform linux/<arch> --load` for cross-arch) directly from Go
    via `exec.Command`. No Starlark step participates.
  - **Outcome:** The container image is tagged and available locally; no
    Starlark code path reaches the host shell.
  - **Covered by:** R3, R5.

- F4. Unit author writes `run("...", privileged = True)`.
  - **Trigger:** Starlark thread tries to call `run` with the kwarg.
  - **Actors:** A1, the parser.
  - **Steps:** `fnRun` rejects the kwarg with a clear error pointing at
    `Class = "image"` (if the user is trying to do image-assembly work) or
    asking them to file an issue (if a legitimate use case exists).
  - **Outcome:** Build fails at task-resolve time, not silently.
  - **Covered by:** R1.

- F5. Unit author writes `run("...", host = True)`.
  - **Trigger:** Same.
  - **Actors:** Same.
  - **Steps:** `fnRun` rejects the kwarg with a clear error explaining that
    `host = True` no longer exists and suggesting `Class = "container"` if
    they are trying to build a Docker image.
  - **Outcome:** Build fails at task-resolve time.
  - **Covered by:** R1.

---

## Requirements

**Starlark surface**

- R1. The `privileged` and `host` kwargs are removed from the `run()`
  builtin in `internal/build/starlark_exec.go`. Calls that pass either kwarg
  fail with a clear error that names the kwarg, names the calling unit, and
  points at the class-based replacement (image / container) or invites a
  bug report. The error fires at task resolution / parse time, not when the
  step is reached.

**Go-side image assembly**

- R2. Image-class unit assembly is driven from Go. The executor, on
  `unit.Class == "image"`, runs the unit's unprivileged tasks to populate
  `destdir/rootfs/` and then calls a Go-side assembler that performs every
  operation requiring root-in-container (`mkfs.ext4 -d`, `sfdisk`,
  `losetup`, `mount`, `extlinux`/bootloader install, raw-byte writes) via
  `RunInContainer(... NoUser: true)`. No `run(..., privileged = True)`
  step participates. The current contents of
  `_install_syslinux` in `modules/module-core/classes/image.star` move into
  Go, parameterized by the same machine partition spec.

**Go-side container build**

- R3. Container-class unit build is driven from Go. The executor, on
  `unit.Class == "container"`, calls `docker build` (or `docker buildx
  build --platform linux/<arch> --load` when cross-arch) directly via
  `exec.Command`, using the Dockerfile path resolved from
  `unit.DefinedIn + unit.Dockerfile` (or equivalent class fields). No
  `run(..., host = True)` step participates. The current contents of
  `_build_container` in `modules/module-core/classes/container.star` move
  into Go.

**Audit surface**

- R4. After the change, the set of code paths that run as root in the
  privileged container is exactly the set already enumerated in
  `docs/security.md` — image assembly (now in Go), ownership recovery
  (`chownDirToHost`), QEMU device runner, bootstrap stage 1
  (`createBuildRoot`) — plus any new Go-side image-assembly call sites
  introduced under R2. No new categories are added.
- R5. A repo-wide grep for `host = True` and `privileged = True` returns
  zero matches in any `.star` file under `modules/` and `testdata/`
  (excluding `build/` and `cache/` artifacts). A grep for `NoUser: true`
  returns only Go files under `internal/`.

**Backward compatibility within yoe**

- R6. Every existing image-class and container-class unit in `module-core`
  (and any cached external module that uses these classes) continues to
  build with no `.star` changes from unit authors. The migration is
  implementation-side: class field shapes stay the same, class bodies
  shrink because the privileged work moves to Go.

**Documentation**

- R7. `docs/security.md` is updated in the same change: the "What a rogue
  unit can do" list drops the `run(host = True)` and `run(privileged =
  True)` bullets, the "Code paths that run as root" table reflects Go-side
  image assembly, and the "Known weaknesses we'd accept patches for"
  section flips the "Sandbox or remove `run(host = True)`" and "Drop
  `--privileged` for non-image steps" entries (the first is now done, the
  second is now genuinely the next step). A CHANGELOG entry leads with the
  user-visible effect ("yoe units can no longer escalate to root on the
  host or to root in the build container — privileged operations are now
  driven from yoe's Go code, not from Starlark").

---

## Acceptance Examples

- AE1. **Covers R1.** Given a unit that contains `run("dd ...", privileged
  = True)`, when `yoe build <unit>` runs, the build fails before executing
  the step with an error that names the kwarg, the unit, and the
  recommended replacement.
- AE2. **Covers R1.** Same as AE1 but with `host = True`. Same failure mode.
- AE3. **Covers R2, R6.** Given `jukebox-image` and `qemu-image` (or
  whichever image-class units are in `module-core` at change time), when
  `yoe build <image>` runs, the resulting `.img` boots in QEMU exactly as
  before. No `.star` changes were required in the image units.
- AE4. **Covers R3, R6.** Given `toolchain-musl` (the canonical
  container-class unit), when `yoe build toolchain-musl` runs, the
  resulting container image is tagged as before and works as the build
  container for downstream units.
- AE5. **Covers R5.** A grep `git grep -nE 'host *= *True|privileged *=
  True' -- '*.star'` returns no matches anywhere in the tree (modules,
  testdata, examples).
- AE6. **Covers R5.** A grep `git grep -n 'NoUser: true'` returns matches
  only under `internal/`.
- AE7. **Covers R4.** The "Code paths that run as root in the privileged
  container" table in `docs/security.md` lists the same logical operations
  as before — `mkfs.ext4 -d`, `losetup`, `mount`, `extlinux`, ownership
  recovery, QEMU runner, bootstrap — but with file pointers into
  `internal/`, not `modules/module-core/classes/`.

---

## Success Criteria

- A unit author cannot reach root-in-container or the host shell through
  any Starlark API. The only kwargs `run()` accepts are `check`.
- Every operation that requires root in the privileged container lives in
  Go and is reachable via grep in a fixed set of `internal/` files.
- `jukebox-image`, `qemu-image`, and the container-class units in
  `module-core` build with no `.star` changes from unit authors.
- `docs/security.md` and the CHANGELOG reflect the narrower attack surface
  in the same commit.
- The container itself stays `--privileged`; tracking that as separate
  work (Go image assembler / go-diskfs) is preserved in
  `docs/build-environment.md` and is _unblocked_ but not gated by this
  change.

---

## Scope Boundaries

- **Does not remove `--privileged` from the container.** That work depends
  on migrating `mkfs.ext4 -d` / `losetup` / `mount` / `extlinux` off
  kernel-loop semantics and into pure-Go partition+filesystem assembly. It
  is tracked separately in `docs/build-environment.md` and remains a
  prerequisite for dropping `--privileged` entirely.
- **Does not add a project-level allowlist for `privileged`/`host`.** An
  allowlist is policy in two places (Starlark + Go) and creates pressure to
  relax. Removing the kwargs entirely is irreversible without a code
  change, which matches the security intent.
- **Does not add seccomp/AppArmor profiles to the container.** Hardening
  inside the container is independent of where escalation requests come
  from. After this spec a hostile build step still has all the kernel
  attack surface a privileged container exposes; it just cannot _ask_ for
  more than that.
- **Does not change the `unit.Class` mechanism.** The class field is still
  author-controlled and still selects executor behavior; what changes is
  that the executor implements `image` and `container` behavior in Go
  rather than delegating to `.star` shell-out.
- **Does not introduce a paranoid mode.** A `--paranoid` flag that refuses
  all root-in-container operations (e.g. for CI) is downstream of this
  change and lives in its own spec when needed.
- **Does not retire `module-core/classes/image.star` and
  `container.star` outright.** They remain as thin wrappers that translate
  user-friendly Starlark calls into the new class fields the executor
  reads; the privileged shell-out portions of their current bodies are
  what move to Go.

---

## Key Decisions

- **Remove the kwargs, do not gate them.** A project-level
  `allow_privileged_starlark = True` flag was considered and rejected.
  Once a flag exists, projects flip it to unblock a single module and
  never flip it back. Hard-removing the kwargs forces every legitimate
  privileged operation through a code review and a Go change, which is
  exactly the friction the security model wants.
- **Class-driven Go behavior, not new Starlark builtins.** An alternative
  shape is to keep Starlark expressive and expose `install_bootloader(...)`,
  `mkfs_ext4(...)`, etc. as constrained builtins that route to Go. This was
  rejected because each new builtin re-creates the audit problem at a
  finer grain: every new privileged builtin is a new piece of API that has
  to be vetted, and the Starlark-level call site remains the apparent
  author of the operation. Driving everything from `unit.Class` keeps the
  Starlark side declarative ("this is an image with this rootfs and these
  partitions") and the Go side imperative ("here is how an image gets
  assembled").
- **`docker build` runs from Go, not from a `host = True` shell-out under a
  new name.** Any builtin shaped like `host_run` reintroduces the original
  problem under a new name. The container build is one specific operation
  (`docker build -t <tag> -f <path> <dir>` or `docker buildx ...`), and Go
  can call it directly given the class fields.
- **Unit-class field set stays declarative and minimal.** Image-class
  units already declare packages, hostname, partition spec (via machine).
  Container-class units already declare a Dockerfile path. The Go drivers
  consume those fields; the spec does not propose new fields except where
  R2/R3 surface a missing one.

---

## Dependencies / Assumptions

- The only `.star` callers of `host = True` and `privileged = True` in the
  tree today are `modules/module-core/classes/image.star` (one
  `privileged` call site) and `modules/module-core/classes/container.star`
  (three `host` call sites). Verified by grep against `modules/` and
  `testdata/` at spec date.
- `internal/image/disk.go` already implements the underlying primitives
  (mkfs.ext4 via `RunInContainer(NoUser: true)`, mkfs.vfat, mcopy, raw
  image writes); the image-class assembler in R2 mostly orchestrates them.
  The remaining piece — `losetup` + `mount` + `extlinux` for syslinux
  install — exists today as the shell snippet in `image.star:256-266`.
- `internal/container.go` already exposes `RunInContainer(... NoUser:
  true)` and the platform/binfmt machinery needed for cross-arch
  `docker build`. The container-class driver in R3 is a thin Go wrapper.
- The build container retains `--privileged` and remains the execution
  environment for the Go-side privileged operations. This spec does not
  hinge on changes to `containerRunArgs`.
- No external (unfetched) modules are assumed to use `host = True` or
  `privileged = True`. Confirmed by grep of cached modules
  (`module-alpine`, `module-jetson`); spec is contingent on this remaining
  true at planning time.
- `docs/build-environment.md` §"Reducing Dependence on Docker's /dev"
  documents the long-term direction (go-diskfs / option 1). This spec
  unblocks that work but does not depend on it.

---

## Outstanding Questions

### Resolve Before Planning

- [Affects R2][User decision] Should image-class units retain any escape
  hatch for "do something the standard assembler doesn't cover" — e.g. a
  pre-assembly or post-assembly Starlark hook that runs unprivileged in
  the container — or should every image-class need be expressible via
  class fields? Affects the size and shape of the Go assembler API.
- [Affects R3][User decision] When `docker build` fails (network error,
  Dockerfile typo, daemon down), should the executor surface raw docker
  output or interpret/wrap it? Determines how much of `_build_container`'s
  current error handling needs equivalents in Go.

### Deferred to Planning

- [Affects R2][Technical] Enumerate every distinct privileged step in
  `_install_syslinux` and equivalent bootloader installs across machines
  (BeaglePlay TI K3 boot chain, RPi U-Boot, QEMU SeaBIOS) and confirm each
  has a Go-side primitive or needs one added.
- [Affects R3][Technical] Cross-arch `docker buildx` requires `binfmt`
  registration on the host; confirm whether the executor should auto-register
  on first cross-arch container build or continue requiring `yoe container
  binfmt` as a manual step.
- [Affects R1][Technical] Error-surface shape for AE1/AE2 — choose whether
  the rejection happens at Starlark load (cleanest, but earlier than the
  task is reached) or at step execution (closer to the offending line but
  later in the build). Decision determines whether the parse-time check
  walks every task's steps or whether `fnRun` checks at call time.
- [Affects R6][Technical] Cached external modules
  (`module-alpine`, `module-jetson`) need a re-verify pass at planning
  time to confirm none has added `host = True` / `privileged = True` since
  spec date. If any has, the migration plan needs an upstream-coordination
  step.
- [Affects security.md][Documentation] Decide whether to mark the existing
  "Drop `--privileged` for non-image steps" weakness as _next step_ or
  _spec'd in <link>_ once the Go image assembler work picks up.
