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
- **Need a tool Alpine already packages? Pull it through `module-alpine`, don't
  build from source.** See the `pulling-alpine-packages` skill for the workflow,
  the `module-alpine` cache layout, and the push-upstream rules.
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
  impossible — kernel defconfig, bootloader target, and libc family
  (musl-built and glibc-built binaries cannot share at the ABI level, so a
  unit consumed by both Alpine and Debian images builds twice along the
  distro axis; see `docs/specs/2026-05-25-module-debian.md` R14a).
- **Explicit over implicit.** Values in Starlark units and configuration should
  not have hidden defaults. Require fields to be set explicitly — this makes it
  easier for AI to reason about the system and for humans to understand what a
  unit does without reading class internals. If a value is required, error when
  it is missing rather than silently defaulting.
- **Units declare their own services; images do not.** When a unit ships an
  OpenRC init script (`/etc/init.d/<svc>`) or systemd service unit, the unit
  decides whether installing it also enables the service at boot, via a
  `services = [...]` field that materializes the runlevel/target symlinks into
  the package itself. This is yoe's analog to Alpine's `setup-<pkg>` helpers:
  the package author — not the image, not the project — knows which init scripts
  represent the package's intended runtime. Alpine's own packages deliberately
  ship init scripts unenabled because they assume a human installer running
  `rc-update add`; yoe has no such human, so the unit takes that responsibility.
  **Do not add an `enable_services = [...]` (or equivalent) field on images.**
  Per-image enablement multiplies the cache surface, fragments runtime behavior
  across projects, and pushes policy to the wrong place. If a project genuinely
  needs a package installed but a service disabled, that's an explicit per-image
  opt-out (not an opt-in), and the bar for adding one is high.
- **Distro modules ship a feed + a companion enable layer.** A module that wraps
  an upstream distro (Alpine, Debian, …) declares its packages via one
  `alpine_feed(...)` / `debian_feed(...)` call per repo section, not via
  thousands of per-package `.star` files. Each feed registers a synthetic module
  (`alpine.main`, `alpine.community`) whose units materialize lazily — working
  memory tracks closure size, not catalog size. Service-enable units
  (`<svc>-enable.star`) are hand-curated companions that depend on the upstream
  `-openrc` package and set `services = [...]` to bake the runlevel symlink. Do
  not write a from-source unit for a package the feed already exposes; pulling
  from `module-alpine` via the feed is the default for the cases listed in
  [docs/module-alpine.md](docs/module-alpine.md). Do not scan the rootfs for
  init scripts as an enable mechanism — explicit companion units are how a
  package's services become enabled.
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
- **Preserve and reuse source trees and local work, especially in dev mode.**
  yoe's job in dev mode is to set up connectivity (origin, branch tracking,
  unshallow) and get out of the way; it is never to forcibly normalize the
  user's working tree. Concretely:
  - Don't `git checkout -B <branch> <ref>` if `refs/heads/<branch>` already
    exists — `-B` resets the branch and discards any local commits the user has
    made. Check first; if the local branch exists, plain `git checkout <branch>`
    it. Only use `-B` to create a missing branch.
  - Don't `git clean -fdx` after a checkout. Untracked files (build output,
    editor state, exploratory edits) often represent in-progress work.
  - Don't `os.RemoveAll(srcDir)` and re-clone when an in-place reset is
    possible. The existing clone has full history, the configured remote, and
    any local branches the user created — re-cloning throws all of that away.
  - Where a transition is destructive by design (e.g., `dev → pin` discards
    commits past the pin), require an explicit `force=true` from the caller and
    surface a confirmation in the TUI prompt.
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
- **When a plan commits to implementation, write docs in final form.** Once a
  plan under `docs/plans/` exists and we are committed to building it, the
  matching reference docs under `docs/` are rewritten to describe the
  post-implementation state — no `(planned)` flags, no `> Status:` blockquotes,
  no "today's flat shape" or "wiring incomplete" caveats. A reviewer should
  read the docs as if the work were already done; a single coherent
  target-state doc is faster to comprehend than a verbose plan with
  disconnected steps. The plan's **first step** lands the target-state docs
  (reviewable artifact); the plan's **last step** verifies docs and code agree
  and closes any gaps that surfaced during implementation. This rule is
  disjoint from `(planned)` above: `(planned)` covers design-ahead sections
  with no implementation plan (brainstorming, future-looking writing without
  commitment to ship); final-form-during-plan covers everything once a plan
  exists. Switch framings the moment a plan commits.
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
- **Test builds for new/changed units always go in `testdata/e2e-project`.**
  When a unit (new or modified) needs a test build, run it inside the existing
  `testdata/e2e-project/` checkout — do not create another test project
  directory under `testdata/` for one-off builds. `e2e-project` is the shared
  scratch space: its `cache/modules/` already has the live `module-alpine` and
  friends, its PROJECT.star already wires the standard module set, and reusing
  it keeps the module cache warm across builds. If the unit needs project-level
  configuration that e2e doesn't have, add the configuration to
  `testdata/e2e-project/PROJECT.star` (and propagate to `internal/init.go` per
  the "`yoe init` mirrors the e2e-project template" rule), rather than spinning
  up a parallel project.
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
