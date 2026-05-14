# Implementation Plans

Status of implementation plans.

| Plan                                   | Status      | Notes                                                                                                     |
| -------------------------------------- | ----------- | --------------------------------------------------------------------------------------------------------- |
| yoe-ng implementation (2026-03-25)     | Done        | Core CLI, Starlark, build, image, TUI all working                                                         |
| QEMU x86 bootable image (2026-03-26)   | Done        | QEMU boot, flash, image assembly all working                                                              |
| Container as build worker (2026-03-27) | Done        | RunInContainer API, host CLI, container sandbox                                                           |
| TUI redesign (2026-03-30)              | Done        | Unit list, build status, detail view, setup view working                                                  |
| Cross-arch QEMU user-mode (2026-03-31) | Done        | binfmt registration, per-arch containers, qemu-arm64 machine                                              |
| Tasks and Starlark builds (2026-04-01) | Done        | task() builtin, run(), MACHINE_CONFIG, PROVIDES all implemented                                           |
| Naming and resolution (2026-04-02)     | Done        | Collision detection, --project flag, per-project APK repo                                                 |
| Content-addressed cache                | In progress | Hash-based cache markers working; full object store not yet                                               |
| Host image building with bwrap         | Not started | Image assembly still runs in container                                                                    |
| Per-unit sysroots                      | Done        | AssembleSysroot/StageSysroot implemented and wired into build path                                        |
| Container units (2026-04-04)           | Done        | Container class, toolchain-musl unit, run(host=True), per-unit container                                  |
| apk compatibility (2026-04-29)         | Not started | 5-phase plan: round-trip compat, apk-driven assembly, signing, on-device apk, provides/replaces alignment |
| `binary` class (2026-04-29)            | Not started | Prebuilt-binary class + Go-side zip and bare-binary source prep                                           |
| Feed server and deploy (2026-04-30)    | Done        | yoe serve, yoe deploy, yoe device repo {add,remove,list}                                                  |
