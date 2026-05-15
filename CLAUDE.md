# CLAUDE.md

This file provides guidance to Claude Code when working with code in this
repository. It contains rules that shape behavior; project overview and
architecture description live under `docs/` (start with `docs/intro.md` and
`docs/architecture.md`).

## Key Design Decisions

- **No installing packages in the container.** If a build fails because a tool
  or library is missing from the container, the fix is to write a unit that
  builds it from source (and add it as a `deps` entry), not to install it via
  `apk add` in the Dockerfile. This applies to both build tools (makeinfo,
  bison, help2man) and libraries (zlib, ncurses, libffi). The Dockerfile
  provides only the minimal bootstrap toolchain (gcc, binutils, make, etc.);
  everything else is a unit. For non-essential features (docs, man pages),
  disabling via configure flags is also acceptable.
- **Container units set arch explicitly.** Classes set `container` and
  `container_arch` explicitly; units inherit these from their class. Do not let
  container selection happen by implicit default.
- **Prefer git sources over tarballs.** Shallow clone with tag pinning. Enables
  the `yoe dev` workflow (edit, commit, extract patches).
- **Native builds only — no cross-compilation.** Cross-arch is handled by
  foreign-arch containers under QEMU user-mode (binfmt_misc); never propose a
  cross-compile toolchain instead.
- **Content-addressed caching.** Input hash determines output. When adding a new
  unit field that participates in the hash (`internal/resolve/hash.go`), gate
  the `fmt.Fprintf` on a non-empty/non-zero check so units that don't set the
  field stay cache-neutral. An unconditional write invalidates every unit's hash
  the moment the line lands, forcing a full rebuild. Follow the pattern used for
  `Extra` and the image-only block.
- **Hardware-bootable images.** Images must boot on real hardware, not just
  QEMU. Never suggest QEMU-only shortcuts like `-kernel` direct boot that bypass
  the bootloader. QEMU is a development convenience; the real target is always
  physical boards.
- **No intermediate code generation.** Avoid generating shell scripts, config
  files, or other intermediate artifacts that are then executed or parsed. When
  something fails, the user should be looking at the code they wrote, not
  machine-generated output. Prefer direct execution over code generation.
- **One unit, one .apk; resolve variation at runtime.** A unit produces a single
  binary artifact that every project and every machine shares. When two images
  need different behavior from the same package, prefer runtime mechanisms —
  init scripts that detect what's installed, conditional config files,
  alternative selection at boot, `replaces:` annotations that declare ownership
  of shared paths — over per-project or per-machine build configurations.
  Forking a unit's build flags into machine- or project- scoped variants is a
  last resort: it multiplies the cache surface, breaks binary reuse across
  projects, and pushes complexity from a few clean conditionals into N parallel
  build configurations. Reach for it only when runtime resolution is genuinely
  impossible (e.g., kernel defconfig, bootloader target).
- **Explicit over implicit.** Values in Starlark units and configuration should
  not have hidden defaults. Require fields to be set explicitly — this makes it
  easier for AI to reason about the system and for humans to understand what a
  unit does without reading class internals. If a value is required, error when
  it is missing rather than silently defaulting.
- **Installed packages run their services.** If a package ships an init script
  (`/etc/init.d/<svc>`) or systemd unit
  (`/usr/lib/systemd/system/<svc>.service`), installing the package means the
  service runs at boot. Image assembly discovers services by scanning the
  assembled rootfs and wires the appropriate runlevel/target symlinks
  automatically. Do not add a `services = [...]` field on units and do not add
  an `enable_services = [...]` field on images — both push policy to the wrong
  place. The right way to "not run a service" is to not install the package; if
  a project genuinely needs a package installed but a service disabled, that's
  an explicit per-image opt-out (not an opt-in), and the bar for adding one is
  high.
- **No backward compatibility concerns.** The project is pre-1.0. Do not add
  compatibility shims, legacy conversion paths, or deprecated-but-still- working
  code. When a design changes, update everything to the new design and delete
  the old code.

## Plugin output directories

All plugin-generated planning artifacts go to two directories at the repo root,
regardless of which plugin produced them:

- `docs/specs/` — requirements documents, brainstorming outputs, design docs,
  feature briefs. Naming: `YYYY-MM-DD-<topic>.md` (drop the `-requirements` /
  `-design` suffix; the directory name already classifies the artifact).
- `docs/plans/` — implementation plans, step-by-step execution plans. Naming:
  `YYYY-MM-DD-<topic>-plan.md` or `YYYY-MM-DD-NNN-<topic>-plan.md` if multiple
  plans land on the same day.

This rule overrides each plugin's default path. Specifically:

- `compound-engineering:ce-brainstorm` writes to `docs/specs/`, not
  `docs/brainstorms/`.
- `compound-engineering:ce-plan` writes to `docs/plans/` (unchanged).
- `superpowers:brainstorming` writes to `docs/specs/`, not
  `docs/superpowers/specs/`.
- `superpowers:writing-plans` writes to `docs/plans/`, not
  `docs/superpowers/plans/`.

If a plugin's skill instructions reference its default directory anywhere
(search hints, "look for existing docs in …"), redirect those lookups to
`docs/specs/` and `docs/plans/` as well. The goal is one location per artifact
kind; consumers should never have to check three places.

`docs/SPEC_PLAN_INDEX.md` is the canonical index of every spec and plan with
implementation status. When a new spec or plan lands in `docs/specs/` or
`docs/plans/`, append a row to the index in the same commit. When implementation
lands or status changes, flip the row's Status column. When a spec is
superseded, mark it and link to the replacement. Before starting work on a
topic, check the index for the prior spec/plan and its status — that's faster
than scanning the directories.

## Working on This Codebase

- **No shortcuts.** Build systems are fragile. Always implement the correct fix,
  not a workaround that happens to make things pass. If the correct fix is
  significantly harder, explain the trade-off and ask before taking a shortcut.
- **Understand before changing.** Read the relevant code paths end-to-end before
  proposing changes. Build failures often have non-obvious root causes — trace
  the actual problem rather than patching symptoms.
- **Silent failures are bugs.** If something can fail, it should fail loudly
  with a clear error. Never swallow errors or degrade silently in ways that make
  debugging harder later.
- **Mark unimplemented docs as (planned).** Any design, feature, command, class,
  builtin, kwarg, or subcommand described in `docs/` that does not yet exist in
  the code must be marked `(planned)` in its section heading and carry a
  `> **Status:** …` blockquote under the heading that describes what exists
  today (with file/path pointers where useful) and what the section is
  describing as future work. When adding a new design-ahead section, mark it
  planned from the start; when implementation lands, remove the `(planned)`
  suffix and the Status blockquote in the same change that ships the code. The
  goal: a reader of `docs/` can never confuse aspirational design with what
  `yoe` actually does today.
- **Changelog entries stay simple and user-focused.** Write for the user of
  `yoe`, not the engineer changing it. One or two short sentences, leading with
  the user-visible benefit (what they see, what they can now do, what was broken
  and is now fixed). Do not enumerate file paths, function names, or the
  mechanism of the fix — those belong in the commit message and the code itself.
  Past entries are immutable history; never edit them during refactors. Do not
  put blank lines between bullet entries — entries sit directly under each
  other, and the blank line separates one version section from the next.
- **Update `docs/` whenever you add a CHANGELOG entry.** A changelog bullet is a
  promise that user-facing behavior changed; the matching reference doc under
  `docs/` (and any key-binding/option table) must reflect that change in the
  same commit. New flag → document the flag; new TUI tab or keybinding →
  document it next to the existing ones; new subcommand → describe it where the
  others live. If a feature is intentionally undocumented (internal,
  experimental), say so in the changelog entry rather than skipping the doc pass
  silently.
- **`yoe init` mirrors the e2e-project template.** The PROJECT.star generated by
  `RunInit` (`internal/init.go`) is the canonical "what does a fresh project
  look like" answer — it must stay in sync with
  `testdata/e2e-project/PROJECT.star`, including the module list,
  `prefer_modules` pins (e.g. `xz` → `alpine`), and the comments explaining why
  those choices exist. When the e2e project changes for a real-world reason,
  update the init template in the same commit so users running `yoe init` get a
  project that builds out of the box. The two diverge only in module references
  — e2e uses `local = "../.."` to test the in-tree modules, while init uses
  upstream URLs.
- **External-module fixes go in the cached copy and must be pushed.** When the
  right place to change something is an external module (e.g. `module-alpine`,
  `module-jetson`), edit the file in place under
  `testdata/<project>/cache/modules/<module>/...` rather than creating a local
  override in `module-core`. Surface the full file path, then remind the user to
  commit AND push upstream — the next `yoe build` does
  `git fetch && git checkout FETCH_HEAD` and silently discards any uncommitted
  or un-pushed local edits in the cache. Pause and wait for confirmation that
  the push landed before re-running any command that triggers a module sync.
  Never do the upstream commit/push yourself — the user manages those repos.
