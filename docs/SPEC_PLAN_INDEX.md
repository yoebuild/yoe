# Spec and Plan Index

Status of every spec in `docs/specs/` and every plan in `docs/plans/`.

**Status values:**

- **Done** — implementation has landed; behavior matches the spec/plan.
- **Partial** — some pieces shipped, others outstanding.
- **Spec only** — design exists, no implementation plan or code yet.
- **Not started** — design and/or plan exists; no code yet.
- **Superseded** — replaced by a later spec or plan.

| Date       | Topic                                 | Spec                                                                                               | Plan                                                                                        | Status      | Notes                                                                                       |
| ---------- | ------------------------------------- | -------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- | ----------- | ------------------------------------------------------------------------------------------- |
| 2026-03-25 | yoe-ng implementation                 | [recipes-core-layer](specs/2026-03-26-recipes-core-layer.md)                                       | [yoe-ng-implementation](plans/2026-03-25-yoe-ng-implementation.md)                          | Done        | Core CLI, Starlark, build, image, TUI all working.                                          |
| 2026-03-26 | QEMU x86 bootable image               | —                                                                                                  | [qemu-x86-bootable-image](plans/2026-03-26-qemu-x86-bootable-image.md)                      | Done        | QEMU boot, flash, image assembly all working.                                               |
| 2026-03-27 | Container as build worker             | [container-as-build-worker](specs/2026-03-27-container-as-build-worker.md)                         | [container-as-build-worker](plans/2026-03-27-container-as-build-worker.md)                  | Done        | RunInContainer API, host CLI, container sandbox.                                            |
| 2026-03-30 | TUI redesign                          | [tui-redesign](specs/2026-03-30-tui-redesign.md)                                                   | [tui-redesign](plans/2026-03-30-tui-redesign.md)                                            | Done        | Unit list, build status, detail view, setup view.                                           |
| 2026-03-31 | Cross-arch QEMU user-mode             | [cross-arch-qemu-usermode](specs/2026-03-31-cross-arch-qemu-usermode.md)                           | [cross-arch-qemu-usermode](plans/2026-03-31-cross-arch-qemu-usermode.md)                    | Done        | binfmt registration, per-arch containers, qemu-arm64 machine.                               |
| 2026-03-31 | Raspberry Pi BSP module               | [units-rpi-layer](specs/2026-03-31-units-rpi-layer.md)                                             | —                                                                                           | Done        | module-rpi shipped.                                                                         |
| 2026-04-01 | Tasks and Starlark builds             | [tasks-and-starlark-builds](specs/2026-04-01-tasks-and-starlark-builds.md)                         | [tasks-and-starlark-builds](plans/2026-04-01-tasks-and-starlark-builds.md)                  | Done        | task(), run(), MACHINE_CONFIG, PROVIDES all implemented.                                    |
| 2026-04-02 | Naming and resolution                 | [naming-and-resolution](specs/2026-04-02-naming-and-resolution.md)                                 | [naming-and-resolution](plans/2026-04-02-naming-and-resolution.md)                          | Done        | Collision detection, `--project` flag, per-project APK repo.                                |
| 2026-04-04 | Container units                       | [container-units](specs/2026-04-04-container-units.md)                                             | —                                                                                           | Done        | Container class, toolchain-musl unit, `run(host=True)`, per-unit container.                 |
| 2026-04-06 | Starlark packaging and image assembly | [starlark-packaging-and-image-assembly](specs/2026-04-06-starlark-packaging-and-image-assembly.md) | —                                                                                           | Spec only   | No Starlark image class yet; packaging and assembly remain hardcoded in Go.                 |
| 2026-04-07 | Unit services                         | [unit-services](specs/2026-04-07-unit-services.md)                                                 | —                                                                                           | Superseded  | Now overridden by 2026-05-13 "Installed packages run their services" rule in CLAUDE.md.     |
| 2026-04-23 | File templates                        | —                                                                                                  | [file-templates](plans/2026-04-23-file-templates.md)                                        | Done        | `install_file` / `install_template` step values shipped; units migrated.                    |
| 2026-04-28 | Flash command                         | [flash-command](specs/2026-04-28-flash-command.md)                                                 | —                                                                                           | Done        | Implementation in `internal/device/flash*.go`; CLI and TUI wired.                           |
| 2026-04-29 | `binary` class                        | [binary-class](specs/2026-04-29-binary-class.md)                                                   | [binary-class](plans/2026-04-29-binary-class.md)                                            | Not started | Prebuilt-binary class + Go-side zip and bare-binary source prep.                            |
| 2026-04-29 | apk compatibility                     | —                                                                                                  | [apk-compat](plans/2026-04-29-apk-compat.md)                                                | Partial     | Passthrough path shipped (see `docs/apk-passthrough.md`); 5-phase upgrade plan not started. |
| 2026-04-30 | Feed server and deploy                | [feed-server-and-deploy](specs/2026-04-30-feed-server-and-deploy.md)                               | [feed-server-and-deploy](plans/2026-04-30-feed-server-and-deploy.md)                        | Done        | `yoe serve`, `yoe deploy`, `yoe device repo {add,remove,list}`.                             |
| 2026-05-04 | TUI unit query                        | [tui-unit-query](specs/2026-05-04-tui-unit-query.md)                                               | [tui-unit-query](plans/2026-05-04-tui-unit-query.md)                                        | Done        | Parser, match, closure, completion in `internal/tui/query/`; live in TUI.                   |
| 2026-05-08 | Source dev mode                       | [source-dev-mode](specs/2026-05-08-source-dev-mode.md)                                             | [feat-source-dev-mode-toggle](plans/2026-05-08-001-feat-source-dev-mode-toggle-plan.md)     | Done        | State detection, toggle, fsnotify, SRC column, `u`/`P` bindings shipped.                    |
| 2026-05-12 | App-mode builds                       | [app-mode-builds](specs/2026-05-12-app-mode-builds.md)                                             | —                                                                                           | Spec only   | Requirements comprehensive; no `app_source()` or `project_ref()` in codebase yet.           |
| 2026-05-13 | Dev-mode branch tracking              | —                                                                                                  | [feat-dev-mode-branch-tracking](plans/2026-05-13-001-feat-dev-mode-branch-tracking-plan.md) | Done        | Extends source-dev-mode; branch-aware toggle landed in recent dev-mode fixes.               |
| 2026-05-13 | Feeds as modules                      | [feeds-as-modules](specs/2026-05-13-feeds-as-modules.md)                                           | —                                                                                           | Not started | Spec landed today; no `alpine_feed()` builtin or synthetic-module machinery yet.            |

## Undated plans

These plans predate the date-prefixed convention. Topic, status, and notes are
inherited from the original superpowers INDEX.

| Plan                                                            | Status      | Notes                                                        |
| --------------------------------------------------------------- | ----------- | ------------------------------------------------------------ |
| [content-addressed-cache](plans/content-addressed-cache.md)     | Partial     | Hash-based cache markers working; full object store not yet. |
| [host-image-building-bwrap](plans/host-image-building-bwrap.md) | Not started | Image assembly still runs in container.                      |
| [per-recipe-sysroots](plans/per-recipe-sysroots.md)             | Done        | `AssembleSysroot` / `StageSysroot` wired into build path.    |

## Maintenance

This index is hand-maintained. When you add a spec or plan, append a row here in
the same commit. When implementation lands, flip the status. When a spec is
superseded, mark it and link to the replacement.

`docs/specs/superpowers-INDEX.md` and `docs/plans/superpowers-INDEX.md` are
retired in favor of this consolidated view.
