---
date: 2026-05-12
topic: app-mode-builds
---

# App-mode builds

## Summary

A new app-mode lets a yoe unit live in its application's own location — either
its own git repo or a subdirectory of a yoe project — and be built into a `.apk`
without invoking a full project + image build. The unit references a yoe project
for its module context; a regular yoe project can pull the same unit into its
image via `app_source(...)`. The same unit definition produces a
content-identical artifact whether built standalone or as part of an image.

---

## Problem Frame

Today every yoe artifact requires a yoe _project_: a `PROJECT.star` with a
module list, `prefer_modules`, default machine, and the surrounding `modules/`,
`cache/`, `build/` directory tree. Even single-file apps like `bun-hello` live
inside `modules/module-core/units/bun/bun-hello/` to be buildable.
Unit-next-to-source ergonomics already work in that layout — but only inside a
yoe project, and only inside a module.

Application developers in a yoe shop hit two pains from this:

1. **Iteration outside the project tree is awkward.** To hack on an app, the
   developer either copies the app's source into `units/` of the surrounding
   project (gross — severs the app from its own git history) or sets up a
   wrapper project with `module(local = "../app")` indirection (heavy,
   duplicative). Neither matches the natural "edit code, build artifact" inner
   loop they'd have with any other build tool.

2. **Monorepos with multiple apps don't have a clean home for unit
   definitions.** The common shape — one project repo containing several apps,
   each in its own subdirectory — has nowhere obvious to put per-app unit
   declarations. They end up under `units/` away from the source they describe.

The `bun_app` class already proves unit-next-to-source works as a pattern.
What's missing is the mode of invocation that turns "unit + source in the same
directory" into a buildable thing without surrounding project ceremony.

---

## Actors

- A1. **App developer**: writes and iterates on a single application; owns its
  `yoe.star` and source. Builds standalone or via the surrounding project's
  image.
- A2. **Project owner**: maintains the yoe project (modules, pins, machine
  defaults). Declares which apps the image includes via `app_source(...)`.
- A3. **yoe CLI**: invoked from any directory; discovers app-mode vs
  project-mode by walking up from CWD; runs the appropriate build path.

---

## Key Flows

- F1. **Standalone build from an app repo**
  - **Trigger:** `yoe build` invoked in a directory containing `yoe.star` and no
    surrounding `PROJECT.star`
  - **Actors:** A1, A3
  - **Steps:** yoe discovers `yoe.star`; resolves the project pointer (from
    `yoe.star`, overridable by `local.star`); materializes a synthesized project
    directory under the local build dir; fetches the project's module set into
    the shared cache under the same build dir; evaluates the unit; runs the
    existing build pipeline; writes the `.apk` to a predictable path in the
    build dir.
  - **Outcome:** `.apk` for that one unit lives at a predictable path; the
    synthesized project directory remains on disk for reuse and inspection.
  - **Covered by:** R1, R2, R5, R6, R8, R9

- F2. **Monorepo build from an app subdir**
  - **Trigger:** `yoe build` invoked from `apps/<name>/` in a repo whose root
    has `PROJECT.star`
  - **Actors:** A1, A3
  - **Steps:** yoe walks up from CWD, finds `yoe.star` first; recognizes the
    project at the repo root; treats the surrounding project as the implicit
    project pointer; uses the project's build dir at the repo root (not a
    per-app one); evaluates and builds the unit; writes the `.apk` into the
    project's shared apk repo.
  - **Outcome:** The `.apk` lands in the same repo cache the project uses for
    image builds.
  - **Covered by:** R1, R3, R4, R6, R8

- F3. **Image bake via `app_source(...)`**
  - **Trigger:** A project's `app_source(local = "./apps/foo")` or
    `app_source(url = ..., ref = ...)` is reached during an image build
  - **Actors:** A2, A3
  - **Steps:** At project startup the unit appears as a stub in the resolved DAG
    (name + source pointer, no deps yet); when the build executor reaches the
    stub, yoe fetches the app's `yoe.star` (if remote and not cached) and
    evaluates it; the stub expands into the full unit; the app's deps are
    checked against the project's already-resolved module set; build proceeds.
  - **Outcome:** The same `.apk` content that standalone build produces lands in
    the image's rootfs.
  - **Covered by:** R7, R10, R11, R12

- F4. **Iterate-and-deploy from a unit dir**
  - **Trigger:** `yoe deploy` invoked from a unit directory (app-mode)
  - **Actors:** A1, A3
  - **Steps:** Same discovery as F1/F2; build the unit; push the resulting
    `.apk` to the developer's registered device.
  - **Outcome:** Running device picks up the new `.apk`; the app dev's
    edit-build-test loop runs without any project-level command.
  - **Covered by:** R13

---

## Requirements

**Discovery and mode selection**

- R1. `yoe build` and `yoe deploy` walk up from CWD looking for `yoe.star` or
  `PROJECT.star`, taking the first match. Finding `yoe.star` first selects
  app-mode; finding `PROJECT.star` first selects project-mode (existing
  behavior). When both files exist at the same level, `yoe.star` wins for mode
  selection and the same-level `PROJECT.star` becomes the implicit project
  pointer (see R3).
- R2. App-mode requires a project context. When `yoe.star` is discovered with no
  `PROJECT.star` upstream in the tree, it must declare a project pointer
  (working name `project_ref(...)`) naming a yoe project by git URL + ref or
  local path. Absence of both is an error at evaluation time, not a silent
  fallback to `module-core`.
- R3. When `yoe.star` is discovered _inside_ a yoe project tree (a
  `PROJECT.star` exists at the same level or higher up in the same tree), the
  project pointer is implicit — the surrounding project is the project. An
  explicit `project_ref(...)` in that `yoe.star` is allowed and, if present,
  overrides the implicit pointer.
- R4. `local.star` next to `yoe.star` (or in the project root for monorepos) may
  override the project pointer for the developer's local checkout. The override
  is gitignored and not committed.

**Build state and caching**

- R5. App-mode builds materialize a real synthesized project directory on disk,
  gitignored, in the app repo (or in the shared build root when multiple repos
  share one). The directory is inspectable, debuggable, and reusable across runs
  — not held in memory.
- R6. The build directory holds everything yoe needs locally: synthesized
  project state, module clones, source cache, build artifacts, and output
  `.apk`. No global per-user cache directory.
- R7. Standalone build and project-side `app_source(...)` produce the same
  `.apk` content hash for the same unit definition + source. Both paths hit the
  same cache entry; the unit does not build twice.

**Project pointer and module inheritance**

- R8. The unit's `yoe.star` does not enumerate modules. Modules, version pins,
  `prefer_modules`, and default machine are inherited from the referenced
  project.
- R9. The class API on the app side is unchanged. Existing classes (`bun_app`,
  `autotools`, `go`, `cmake`, etc.) work in app-mode without modification.

**Project-side integration**

- R10. A yoe project may declare a unit using `app_source(...)` that points at
  an app repo by git URL + ref or by local path. The declaration produces a
  regular unit in the project's DAG.
- R11. `app_source(...)` units are resolved lazily. At project startup the unit
  is a stub in the DAG with enough metadata to satisfy upfront validation (name,
  source pointer); the unit's deps and tasks are loaded only when the build
  executor reaches the stub or the unit is explicitly targeted
  (`yoe build <app>`, `yoe desc <app>`). The two-phase resolve-then-build
  guarantee continues to hold for project-local content; external app refs trade
  resolve-time validation for cheaper startup.
- R12. Lazy resolution is cached. Once an external `app_source` repo's
  `yoe.star` is fetched and evaluated for a given ref, the resolved unit
  metadata is reused until the source hash changes.

**Deploy, sharing, and monorepo**

- R13. `yoe deploy` works from any unit directory using the same discovery rules
  as `yoe build`.
- R14. Multiple app repos may share one build directory by pointing `local.star`
  at a common path. Per-app synthesized project state lives in its own subtree
  within the shared root; modules and source downloads are the shared content.
  The unrelated-app-repos case and the monorepo case (project + multiple apps in
  subdirs) both use this mechanism.
- R15. In a monorepo with `PROJECT.star` at the root and `apps/<name>/yoe.star`
  in subdirs, all apps share the project root's build dir by default (no per-app
  `local.star` needed). `app_source(local = "./apps/foo")` in the project's
  image declaration hits the same cache entry as `yoe build` from inside
  `apps/foo/`.

**Scaffolding**

- R16. `yoe init --app` scaffolds the in-source layout for a standalone app: a
  `yoe.star` skeleton with a `project_ref(...)` placeholder and a `.gitignore`
  covering the build directory. For monorepo apps under an existing project,
  developers add `yoe.star` files under `apps/<name>/` manually.

**First target: yoe builds itself**

- R17. The yoe repo itself is the first concrete target of app-mode. A
  `yoe.star` at the repo root declares the yoe Go binary as a unit using the
  existing `go` class. `yoe build` from anywhere in the repo produces a `.apk`
  containing the statically-linked `yoe` binary, exercising the same flags as
  `yoe_build` in `envsetup.sh` (CGO_ENABLED=0, static linking compatible with
  the musl toolchain container).
- R18. The yoe repo grows a minimal `PROJECT.star` at the repo root alongside
  the new `yoe.star`. The PROJECT.star references this repo's own
  `modules/module-core/` and `modules/module-rpi/` via `local = "."` +
  `path = "modules/..."` so the root-level `yoe.star` discovers it as the
  implicit project pointer (per R3 same-level rule).

---

## Acceptance Examples

- AE1. **Covers R1, R2, R3.** Given an app repo with `yoe.star` at the root, no
  `PROJECT.star`, and a `project_ref(url = "...", ref = "main")` declaration,
  when the developer runs `yoe build` from the repo root, yoe selects app-mode,
  fetches the named project, and builds the unit. Given the same repo _without_
  a `project_ref`, `yoe build` fails with a clear error rather than silently
  building against `module-core`.
- AE2. **Covers R3, R15.** Given a project repo with `PROJECT.star` at the root
  and `apps/foo/yoe.star` in a subdir, when the developer runs `yoe build` from
  inside `apps/foo/`, yoe selects app-mode, recognizes the surrounding project
  as the implicit project pointer, and writes the build artifact into the
  project root's build dir.
- AE3. **Covers R7.** Given the same `yoe.star` evaluated standalone
  (`yoe build` from the app repo) and via `app_source(local = "./apps/foo")` in
  a project's image, the produced `.apk` has the same content hash and is served
  from the same cache entry.
- AE4. **Covers R11.** Given a project with twenty `app_source(...)`
  declarations pointing at external app repos, when `yoe build base-image` runs,
  yoe does not fetch or evaluate the external repos until each app's unit is
  actually reached during build execution.
- AE5. **Covers R4, R14.** Given two unrelated app repos, each with a
  `local.star` pointing at the same shared build directory, when the developer
  builds both in sequence, the second build reuses module clones and source
  downloads from the first.
- AE6. **Covers R1, R3, R17, R18.** Given the yoe repo with a new root-level
  `PROJECT.star` and `yoe.star`, when a developer runs `yoe build` from the repo
  root (or from any subdirectory like `cmd/yoe/` or `internal/`), app-mode
  discovery picks `yoe.star`, treats the same-level `PROJECT.star` as the
  implicit project pointer, evaluates the unit against this repo's own
  `modules/module-core/`, builds the yoe Go binary, and writes the resulting
  `.apk` to the project's build dir.

---

## Success Criteria

- An app developer in a yoe shop can drop a `yoe.star` into their app repo, run
  `yoe build`, and get a `.apk` without writing a `PROJECT.star`, `machines/`
  directory, or module list.
- A monorepo with a project at the root and apps in subdirs builds any
  individual app from its subdir (app-mode) and bakes those apps into the image
  from the root (project-mode) using one unit definition per app.
- An image build using `app_source(...)` does not pay startup-time cost for
  fetching every external app repo; cost is paid only when each app is actually
  built or explicitly targeted.
- The same unit definition produces the same `.apk` whether built standalone or
  as part of an image; cache hits move artifacts between the two contexts
  without rebuilding.
- A `ce-plan` reader can pick this up and decide implementation specifics
  (on-disk layout for the synthesized project directory, exact API of
  `project_ref` and `app_source`, how stub resolution participates in the cache)
  without inventing product behavior, scope, or success criteria.
- The yoe repo dogfoods app-mode: `yoe build` invoked from the repo root (or any
  subdirectory) produces a `.apk` containing the yoe binary using the new
  root-level `yoe.star` + `PROJECT.star`, validating discovery, the same-level
  implicit project pointer, module inheritance against this repo's own modules,
  and the existing `go` class against a real Go binary target. Replacing the
  `yoe_build` shell function with `yoe build` is feasible once this works.

---

## Scope Boundaries

- **Persona B (pure app dev, no yoe project anywhere) not addressed.** The
  design intentionally requires a project pointer. A future workstream may
  address this with module discovery and a project-less build path.
- **No image builds from app-mode.** Building an image still requires a full
  `PROJECT.star`. App-mode produces `.apk`s only.
- **No new manifest format.** A new `yoe_app(...)` builtin or parallel manifest
  file was rejected in favor of reusing the existing class API.
- **No image-only knobs in app-mode.** `prefer_modules`, `machines/`, and
  similar project-scope settings have no effect in `yoe.star`.
- **No cross-arch matrix builds from the app repo.** One target per invocation;
  multi-arch matrices remain project-mode.
- **No multiple units per `yoe.star`.** One `yoe.star` = one unit. Monorepos use
  multiple `yoe.star` files in subdirs.
- **No pre-fetching of external `app_source` repos at startup.** Lazy resolution
  is the default; an opt-in flag to force eager resolution could be added later
  but is not part of v1.

---

## Key Decisions

- **App-mode is a derivative of project-mode, not a new evaluation path.** The
  synthesized project directory is a real project on disk; yoe's existing
  resolve/build pipeline runs against it unchanged. Avoids two evaluation paths
  and preserves the content-hashing invariant across standalone and image
  builds.
- **Project pointer in `yoe.star`, not module list.** The unit declares its
  project context, not its modules. Ensures cache compatibility with the
  project's eventual image build and prevents version-pin drift between
  standalone and image-mode `.apk`s.
- **Discovery walks up from CWD; first match wins.** A single rule covers
  standalone app repos, monorepos with a project at the root, and existing
  project workflows. A `yoe.star` _inside_ a project tree treats the surrounding
  project as its implicit pointer.
- **Lazy resolution for external `app_source` only.** Project-local content
  (including `app_source(local = ...)`) still resolves at startup; remote
  `app_source(url = ..., ref = ...)` stubs out at startup and expands on demand.
  Preserves "fail fast at startup" for the common monorepo shape while making
  "image with N external apps" cheap to start.
- **One build dir at the project root in monorepos.** Per-app build dirs would
  split caches and break the content-hash equivalence across standalone and
  image builds. Sharing the project's build dir is the default; `local.star` can
  override.
- **Approach A (auto-discovery + existing class API) over Approach B (new
  manifest format) or C (unit-as-project, no synthesized project).** A has the
  smallest carrying cost — one new evaluation entry point, no new builtin, no
  parallel mental model.
- **Yoe builds itself as the first target.** Dogfooding forces the design
  through end-to-end: real Go binary, real existing `go` class, real modules
  from this repo, real `.apk` output. Cheaper feedback than a contrived demo
  app, and the resulting `yoe.star` + `PROJECT.star` at the repo root become the
  canonical reference for how app-mode is used in practice.

---

## Dependencies / Assumptions

- The existing two-phase resolve-then-build model and content-addressed caching
  (`internal/resolve/hash.go`) are extended, not replaced. Stub `app_source`
  units must hash cleanly under the existing scheme.
- `local.star` already exists in the project-mode flow as a per-developer
  override file. App-mode reuses the same concept and discovery semantics next
  to `yoe.star`. The build-dir override is a new field added to `local.star`'s
  schema.
- `yoe deploy` already operates on a single unit and a registered device.
  App-mode discovery is the only change needed to make it work from a unit
  directory.
- The `bun_app` class's adjacent-source convention (the source dir lives next to
  the `.star` file) carries over to app-mode unchanged.

---

## Outstanding Questions

### Resolve Before Planning

(none — all product decisions surfaced during this brainstorm)

### Deferred to Planning

- [Affects R5, R6][Technical] Exact on-disk layout for the synthesized project
  directory and shared module/source cache within the build dir. Planning should
  validate against the existing project-mode layout (current projects have
  separate `build/` and `cache/` trees; app-mode consolidates under one root).
- [Affects R11][Technical] How stub `app_source` units participate in the
  resolved DAG's hash. The stub's hash at resolve time vs. the resolved unit's
  hash at build time — what changes vs. what's stable — is a planning-phase
  concern.
- [Affects R11][Needs research] Whether `yoe build --dry-run` and `yoe graph`
  should force resolution of external `app_source` stubs. Trade-off between
  completeness of the output and the cost of fetching every external repo.
- [Affects R12][Technical] Cache key for resolved `app_source` metadata (source
  URL + ref + commit hash + upstream `yoe.star` hash). Planning specifies
  exactly which inputs the cache key includes and how invalidation cascades.
- [Affects R10][Technical] Whether `app_source(...)` is a class function, a
  builtin, or its own unit-declaration form. The synthesis kept the name as a
  working title; planning picks the shape.
- [Affects R16][Technical] Exact contents of the `yoe init --app` scaffold —
  placeholder values, comment text, default class choice (or no class, just the
  unit skeleton).
- [Affects R17][Technical] Exact build flags and class invocation for the yoe
  binary unit — CGO_ENABLED=0, build tags (if any), linker flags for static
  linking under musl, where the binary lands inside the `.apk` (likely
  `/usr/bin/yoe`). Reconcile with the existing `yoe_build` function in
  `envsetup.sh`.
- [Affects R18][Technical] Whether the new root-level `PROJECT.star` should also
  satisfy the e2e test fixture's needs (so `testdata/e2e-project` could collapse
  into it) or remain a separate file. Affects how `prefer_modules` and machine
  defaults are set at the root.
