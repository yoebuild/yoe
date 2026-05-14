# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

## Project Overview

`[yoe]` is a next-generation embedded Linux distribution builder — a simpler
alternative to Yocto. The project has a working Go CLI (`yoe`) that builds
artifacts from Starlark units inside a Docker container, creates bootable disk
images, and runs them in QEMU. A `module-core` module provides Starlark classes
and units for a minimal Linux system (busybox, kernel, openssl, openssh, etc.).

Core design: Go CLI (`yoe`) + Starlark units/config + apk artifacts + bubblewrap
sandbox inside Docker. Native builds only (no cross-compilation).

## Container as Build Worker

**The `yoe` CLI always runs on the host. The container is a stateless build
worker invoked only when container-provided tools (gcc, bwrap, mkfs, etc.) are
needed.**

- The host runs: CLI dispatch, Starlark evaluation, DAG resolution, source
  fetch, APK packaging, cache management, all query commands
- The container runs: bwrap-sandboxed compilation, image disk tool operations
  (mkfs, sfdisk, bootloader install), Stage 0 bootstrap
- `RunInContainer()` is the single entry point -- called from the build
  executor, image assembly, and bootstrap
- The container runs with `--privileged` for bwrap namespaces and disk tools
- Build output uses `--user uid:gid` so files are owned by the host user
- Container images are built by container units (e.g., `toolchain-musl`) as part
  of the DAG — no embedded Dockerfile
- Developers need only Git, Docker/Podman, and the `yoe` binary

## Repository Structure

- `cmd/yoe/main.go` — CLI entry point with command dispatch
- `internal/` — core Go packages (starlark, build, resolve, source, image,
  artifact, repo, device, tui, bootstrap, module, config)
- `modules/module-core/` — base module with classes, units, machines, images,
  containers
- `testdata/` — test fixtures including e2e-project
- `envsetup.sh` — shell functions (source it, don't execute)
- `docs/` — design documents (README.md, yoe-tool.md, metadata-format.md,
  build-environment.md, build-languages.md, sdk.md, comparisons.md)

### Module structure

The `module-core` module lives at `modules/module-core/` in this repo. Projects
reference it with `path = "modules/module-core"`:

```python
module("https://github.com/yoebuild/yoe.git",
      ref = "main",
      path = "modules/module-core")
```

The `path` field tells yoe the module's `MODULE.star` is in a subdirectory of
the cloned repo, not at the root.

## Commands

### Building yoe

```bash
source envsetup.sh
yoe_build        # builds static binary (CGO_ENABLED=0 for Alpine container)
yoe_test         # run all tests
```

CGO_ENABLED=0 is required because `net/http` pulls in cgo's DNS resolver by
default, producing a dynamically linked binary that won't run inside the Alpine
(musl) container. `yoe_build` handles this automatically.

### Formatting (markdown)

```bash
source envsetup.sh
yoe_format        # format all markdown with prettier
yoe_format_check  # check formatting compliance
```

### CI

The GitHub Actions workflow (`doc-check.yaml`) runs `prettier --check` on all
`**/*.md` files using Node.js 20. Prettier config: `proseWrap: always`
(`.prettierrc`).

## Key Design Decisions

- **Container units** — build containers are Starlark units (e.g.,
  `toolchain-musl`) that produce Docker images via `run(host = True)`. The
  Dockerfile lives in the module at `containers/toolchain-musl/Dockerfile`.
  Classes set `container` and `container_arch` explicitly; units inherit these
  from their class.
- **Container-only builds** — host provides only `yoe` + Git + Docker; all tools
  live in the container
- **No installing packages in the container** — if a build fails because a tool
  or library is missing from the container, the fix is to write a unit that
  builds it from source (and add it as a `deps` entry), not to install it via
  `apk add` in the Dockerfile. This applies to both build tools (makeinfo,
  bison, help2man) and libraries (zlib, ncurses, libffi). The Dockerfile
  provides only the minimal bootstrap toolchain (gcc, binutils, make, etc.);
  everything else is a unit. For non-essential features (docs, man pages),
  disabling via configure flags is also acceptable.
- **Cross-architecture builds** — foreign-arch containers via QEMU user-mode
  emulation (binfmt_misc). Target arch comes from the machine definition. Build
  directories include arch: `build/<arch>/<unit>/`.
- **Per-unit build environment** — architecture is determined at the unit/task
  level, not globally. Each unit runs in its own build environment — currently a
  Docker container with optional bwrap sandbox. The build environment strategy
  (Docker, bwrap, sysroot tools, future options) is an implementation detail
  that can vary per unit or per architecture as needed.
- **Build sysroot** — after each unit builds, its output is installed into
  `build/sysroot/` so subsequent units can find deps' headers/libraries
- **Starlark** for all units and config (Python-like, deterministic, sandboxed)
- **Classes as functions** — build patterns (autotools, cmake, go) are Starlark
  functions in the module, not Go builtins. Autotools class auto-runs
  `autoreconf` for git sources missing `./configure`.
- **Prefer git sources over tarballs** — shallow clone with tag pinning. Enables
  `yoe dev` workflow (edit, commit, extract patches).
- **apk** package manager (same as Alpine). Currently targets musl libc; glibc
  support may be added in the future.
- **bubblewrap** for per-unit build isolation inside the container
- **Module path** — modules can live in a subdirectory of a repo via the `path`
  field on `module()`. Module name is derived from the path's last component.
- **Image deps in DAG** — image units' `artifacts` list is treated as
  dependencies so `yoe build dev-image` automatically builds all required
  artifacts first
- **Native builds only** — no cross-compilation
- **Label-based references** —
  `load("@module-core//classes/autotools.star", "autotools")`, `//` relative to
  module root when inside a module
- **Two-phase build** — resolve DAG then execute (inspired by GN)
- **Content-addressed caching** — input hash determines output. When adding a
  new unit field that participates in the hash (`internal/resolve/hash.go`),
  gate the `fmt.Fprintf` on a non-empty/non-zero check so units that don't set
  the field stay cache-neutral. An unconditional write invalidates every unit's
  hash the moment the line lands, forcing a full rebuild. Follow the pattern
  used for `Extra` and the image-only block.
- **Hardware-bootable images** — images must boot on real hardware, not just
  QEMU. Never suggest QEMU-only shortcuts like `-kernel` direct boot that bypass
  the bootloader. QEMU is a development convenience; the real target is always
  physical boards.

- **No intermediate code generation.** Avoid generating shell scripts, config
  files, or other intermediate artifacts that are then executed or parsed. When
  something fails, the user should be looking at the code they wrote, not
  machine-generated output. Prefer direct execution over code generation.

- **Tasks, not build step lists** — units define `tasks = [task(...)]` with
  named phases. Steps are shell strings or Starlark callables. `run()` executes
  commands during build with full error traces to `.star` source lines.
- **Machine-portable images** — images list abstract requirements ("linux",
  "base-files"). Machines provide concrete implementations via `provides` and
  inject hardware-specific packages/partitions via `ctx.machine_config`.
- **One unit, one .apk; resolve variation at runtime.** A unit produces a single
  binary artifact that every project and every machine shares. When two images
  need different behavior from the same package, prefer runtime mechanisms —
  init scripts that detect what's installed, conditional config files,
  alternative selection at boot, `replaces:` annotations that declare ownership
  of shared paths — over per-project or per-machine build configurations.
  Forking a unit's build flags into machine- or project-scoped variants is a
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
  compatibility shims, legacy conversion paths, or deprecated-but-still-working
  code. When a design changes, update everything to the new design and delete
  the old code.

## Plugin output directories

All plugin-generated planning artifacts go to two directories at the repo root,
regardless of which plugin produced them:

- `docs/specs/` — requirements documents, brainstorming outputs, design docs,
  feature briefs. Naming: `YYYY-MM-DD-<topic>.md` (drop the
  `-requirements` / `-design` suffix; the directory name already classifies
  the artifact).
- `docs/plans/` — implementation plans, step-by-step execution plans.
  Naming: `YYYY-MM-DD-<topic>-plan.md` or
  `YYYY-MM-DD-NNN-<topic>-plan.md` if multiple plans land on the same day.

This rule overrides each plugin's default path. Specifically:

- `compound-engineering:ce-brainstorm` writes its requirements doc to
  `docs/specs/`, not `docs/brainstorms/`.
- `compound-engineering:ce-plan` writes its plan to `docs/plans/` (unchanged).
- `superpowers:brainstorming` writes its design doc to `docs/specs/`,
  not `docs/superpowers/specs/`.
- `superpowers:writing-plans` writes its plan to `docs/plans/`, not
  `docs/superpowers/plans/`.

If a plugin's skill instructions reference its default directory anywhere
(search hints, "look for existing docs in …"), redirect those lookups to
`docs/specs/` and `docs/plans/` as well. The goal is one location per
artifact kind; consumers should never have to check three places.

`docs/SPEC_PLAN_INDEX.md` is the canonical index of every spec and plan
with implementation status. When a new spec or plan lands in `docs/specs/`
or `docs/plans/`, append a row to the index in the same commit. When
implementation lands or status changes, flip the row's Status column.
When a spec is superseded, mark it and link to the replacement. Before
starting work on a topic, check the index for the prior spec/plan and
its status — that's faster than scanning the directories.

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
  those choices exist. When the e2e project changes for a real-world reason (a
  new module-core unit conflicts with an Alpine prebuilt, a module is
  added/removed), update the init template in the same commit so users running
  `yoe init` get a project that builds out of the box. The two diverge only in
  module references — e2e uses `local = "../.."` to test the in-tree modules,
  while init uses upstream URLs.
- **External-module fixes go in the cached copy — and must be pushed before the
  next `yoe build`.** When the right place to change something is an external
  module (e.g. `module-alpine`, `module-jetson`), edit the file in place under
  `testdata/<project>/cache/modules/<module>/...` rather than creating a local
  override in `module-core`. After making the edit, surface this clearly to the
  user with three pieces of information:
  1. **Which file(s) changed** (full path so the user can `cd` to the module's
     git repo and confirm the diff).
  2. **An explicit reminder to commit AND push** the change to the module's
     upstream remote — not just commit locally.
  3. **A warning that `yoe build` will overwrite the cached edit on its next
     sync** if the upstream hasn't been updated yet. The sync does
     `git fetch && git checkout FETCH_HEAD`, which silently discards
     uncommitted/un-pushed local edits in the cache. So uncommitted-but-not-
     pushed isn't enough — the change must be on the upstream's tracked ref
     (typically `main`) before the next build, or the build will fail again with
     the same error.

  Never do the upstream commit/push yourself — the user manages those repos.
  Pause and wait for confirmation that the push landed before re-running any
  command that triggers a module sync.

## Diagrams

Diagrams (architecture, DAG, build flow, etc.) for `docs/` and presentations
should follow these guidelines. **draw.io is the preferred tool** — author
`.drawio` files and export to PNG/SVG for embedding.

### Composition

- No title in the diagram — the surrounding heading or caption provides it.
- Prefer a simple drawing over a block diagram when possible; boxes-with-text is
  the fallback, not the goal.
- Keep the image simple enough that a reader can glance and decide if they're
  interested. Resist packing in all the details.
- Embed images/icons (downloading if needed) where they convey more than a
  labeled box would.
- Default size: 900x500 points.

### Style

- Rounded rectangles with colored fills for categories/groups.
- Clear, readable fonts: 14pt for titles, 11-12pt for content.
- Emojis in section headers for visual interest.
- Arrows to show relationships/flow.
- Color palette: blue (#dae8fc), green (#d5e8d4), yellow (#fff2cc), purple
  (#e1d5e7), red (#f8cecc), orange (#ffe6cc).
- Keep text concise — bullet points, not paragraphs.
- For vertical arrows using `mxgraph.arrows2.arrow`, add `direction=north;` to
  the style and use a wide width (the shape is natively horizontal, so
  width/height effectively swap when direction changes). For a downward arrow,
  also add `rotation=180;`.

### Sources for free icons and images

- Icons: <https://icon-icons.com/>, <https://heroicons.com/>,
  <https://react-icons.github.io/react-icons/>
- Images: <https://unsplash.com>, <https://www.pexels.com/>,
  <https://pixabay.com/>
