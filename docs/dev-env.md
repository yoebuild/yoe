# Development Environments (planned)

> **Status:** Nothing in this document is implemented yet. `yoe shell` and
> `yoe bundle` do not exist in `cmd/yoe/main.go`, and there is no bundle
> export/import path in the build engine. This file describes the intended model
> so the no-SDK direction is discoverable.

`[yoe]` does not ship a separate SDK. The same tool that builds the OS is the
tool application developers use — `yoe` is small enough (single Go binary +
Docker) that the traditional "OS team hands an SDK to app developers" split
doesn't need to exist.

This document describes two pieces that make the no-SDK model complete:

1. **`yoe shell`** — interactive access to the exact sandbox a unit builds in.
2. **`yoe bundle`** — content-addressed export/import for air-gapped sites and
   CI pinning.

## The No-SDK Model

Traditional embedded systems ship an SDK — a frozen sysroot + cross-toolchain
tarball — because the build system is too heavyweight for app developers to run
directly. The SDK drifts from the OS it was cut from, "it works on my machine"
becomes "it works with my SDK version", and the OS team spends real effort
generating and distributing it.

`[yoe]` removes that split. An app developer installs `yoe` and Docker, clones
the project repo, and runs:

```sh
yoe build myapp           # packages myapp.apk against target libs
yoe shell myapp           # drops into the same sandbox for interactive work
yoe build base-image      # folds myapp into the device image
```

The build environment, the dev environment, and CI are all the same yoe-managed
container. There is no "SDK version" distinct from "OS version" because there is
no SDK artifact.

What makes this work:

- **Native arch everywhere.** `[yoe]` does not cross-compile. QEMU user-mode
  emulation (binfmt_misc) transparently runs the target-arch container on any
  host, so the app developer's workstation runs the same toolchain the target
  device will run.
- **Per-unit containers.** Each unit declares the container it builds in. An app
  developer opening a shell for `myapp` gets the container `myapp` was designed
  to build in, with the resolved `-dev` deps already installed via `apk` — no
  manual sysroot wrangling.
- **Cached packages, not cached environments.** Heavy `.apk` artifacts
  (`qt6-dev`, `chromium-dev`, `glibc-dev`) live in the build cache,
  content-addressed by input hash. An app developer pulls them on first build
  and never rebuilds them unless inputs change. The cache is the SDK's sysroot,
  decomposed into reusable pieces.

## Working on App Code

The no-SDK model gives every developer a uniform toolchain. The other half of
the app-developer loop is editing source and seeing the change on a device.
Three pieces make that work:

### Local-path sources

Units can reference a working tree on disk instead of (or alongside) a git URL:

```python
unit(
    name = "myapp",
    source = path("./"),     # build from this repo's working tree
    class = "go_binary",
    ...
)
```

`path()` sources are not cloned. yoe binds the working tree into the build
sandbox so edits land in the next build immediately, without a commit-tag-fetch
cycle.

### Fast deploy

`yoe deploy <unit> <host>` builds the apk for `<unit>`, exposes the project's
repo over an HTTP feed (reusing a running `yoe serve` if one is up), and runs
`apk add --upgrade <unit>` on the device over SSH. Combined with local-path
sources, the loop is:

```
edit code → yoe deploy myapp dev-pi → service running on the device
```

Pull, not push: apk on the device resolves transitive deps from the same
`APKINDEX.tar.gz` production OTA uses, so adding a runtime dep to a unit doesn't
require updating any deploy machinery. After the first deploy the device's
`/etc/apk/repositories` keeps the dev-feed line in place, so subsequent
`apk add` calls from the device work too. See [feed-server.md](feed-server.md).

### Watch mode

`yoe dev <unit>` watches the source tree and rebuilds (and optionally redeploys)
on save. For app projects this is the inner loop; for upstream units, it's the
patch-and-iterate workflow.

### Three workflow shapes

The pieces above support three repo layouts:

**Single-repo project.** App code and yoe config live in one git repo. Add
`PROJECT.star` and a `unit.star` next to the source tree:

```
my-app/
├── PROJECT.star      # references module-core for the base system
├── unit.star         # source = path("./")
└── src/...
```

`yoe build && yoe deploy` runs from the repo root. Easiest onboarding;
yoe-specific files become part of the project.

**Multi-repo (clean app).** App stays untouched in its own repo. A separate
"system" project references it via a sibling path:

```
~/projects/
├── my-app/                  # plain app repo, no yoe files
└── my-system/
    ├── PROJECT.star
    └── apps/
        └── my-app.star      # source = path("../../my-app")
```

The system project is what gets versioned for production. Mirrors how Rust
workspaces and mono-repos handle service composition.

**In-tree dev of an upstream unit.** `yoe dev openssh` checks out an upstream
unit's source into a working dir; subsequent builds use that dir until you
commit or revert. Distinct from app dev — this is the "patch upstream and try
it" workflow.

### Editor integration

Run language servers and debuggers inside `yoe shell` (or a devcontainer pointed
at the toolchain image) so they see the same headers, libraries, and target arch
as the build:

- VSCode Remote / Dev Containers attaches naturally.
- Neovim's `distant.nvim` works the same way.
- JetBrains Gateway connects via SSH into the container.

There is no SDK to install, no `environment-setup-*` to source. The container
the build runs in is the container the LSP runs in.

## `yoe shell`

`yoe shell` opens an interactive shell inside the build sandbox for a unit —
same container, same environment variables, same mounted sysroot that
`yoe build` uses, but attached to a TTY instead of running build steps.

```sh
# Drop into the sandbox for myapp (uses myapp's unit + machine defaults)
yoe shell myapp

# For a specific machine (e.g., cross-arch via QEMU)
yoe shell myapp --machine raspberrypi4

# Open a shell without targeting a specific unit — useful for quick experiments
yoe shell --machine beaglebone-black
```

Inside the shell the developer can:

- Edit source in `$SRCDIR` (live-mounted from `build/<arch>/<unit>/src/`).
- Run the unit's build commands manually (`./configure && make`, `go build`,
  `cargo build`) — exactly what `yoe build` would run.
- Add extra deps interactively with `apk add <pkg>` for probing; the next
  `yoe shell` invocation starts fresh so probes don't pollute the recorded
  environment.
- Use `yoe dev extract <unit>` from inside the container to turn local commits
  into patch files for the unit.

**Why this replaces an SDK shell:** the SDK shell in Yocto
(`environment-setup-*`) is a static snapshot of environment variables.
`yoe shell` is a live attach to the sandbox that would run if you typed
`yoe build <unit>` right now — it cannot drift from the OS because it _is_ the
OS build environment.

## `yoe bundle` for Air-Gapped Distribution

Some environments cannot reach the internet: regulated sites, long-lifetime
industrial deployments, offline CI runners. For these, `[yoe]` exports a
**bundle** — a content-addressed archive containing everything needed to build
the declared targets without network access.

```sh
# Export a bundle for a specific image (includes everything transitively needed)
yoe bundle export base-image --out bundle-base-v1.0.tar

# Export everything reachable from PROJECT.star
yoe bundle export --all --out bundle-full.tar

# On the air-gapped machine
yoe bundle import bundle-base-v1.0.tar
yoe build base-image              # all hits from cache — no network
```

A bundle contains:

| Piece            | Source                     | What it's for                              |
| ---------------- | -------------------------- | ------------------------------------------ |
| Built `.apk`s    | `$YOE_CACHE/build/`        | Pre-built packages matching current hash   |
| Source archives  | `$YOE_CACHE/sources/`      | Tarballs + git bundles for rebuild-ability |
| Module checkouts | `$YOE_CACHE/modules/`      | Vendored external modules at their refs    |
| Container images | OCI archives               | Toolchain / build containers as tarballs   |
| Project snapshot | `PROJECT.star` + `units/*` | Optional; for bundles that include source  |

Everything is keyed by content hash, so importing the same bundle on two
machines produces byte-identical build results.

### Why Bundles Beat an SDK Image for Air-Gapped

A monolithic SDK image is a snapshot of what was convenient to pre-bake. A
bundle is a **subset of the cache** that covers exactly the targets the
air-gapped site needs, composed from the same cache layers the OS team already
produces.

- **Reproducible.** Two bundle exports at the same project state produce the
  same bytes. An SDK image bakes in timestamps and layer ordering.
- **Composable.** A site that needs two products ships two bundles; shared
  packages dedupe automatically on import.
- **No separate artifact to maintain.** CI already produces the cache. A bundle
  is `yoe bundle export <targets>` — no separate SDK build.
- **Targeted.** A Go-microservices team gets a bundle with `go`, `glibc-dev`,
  and the libraries their units link against — not the 4 GB everything-image.

### Signed Bundles

Bundles are signed with the project's cache signing key (same key used for
remote cache entries). Import verifies signatures before trusting hashes, so a
tampered bundle is rejected rather than silently polluting the cache.

```sh
yoe bundle export base-image --sign keys/bundle.key --out bundle.tar
yoe bundle import bundle.tar --verify keys/bundle.pub
```

## Devcontainers / Codespaces

For developers who want a one-click cloud or VS Code setup, point the
devcontainer at the project's toolchain container — already a regular `[yoe]`
unit built by `container()`:

```json
{
  "image": "registry.example.com/yoe/toolchain-musl:v1.0.0-arm64",
  "mounts": ["source=${localWorkspaceFolder},target=/src,type=bind"]
}
```

CI produces this image by building the container unit and pushing it:

```sh
yoe build toolchain-musl --machine raspberrypi4
docker tag yoe/toolchain-musl:...-arm64 registry.example.com/yoe/toolchain-musl:v1.0.0-arm64
docker push registry.example.com/yoe/toolchain-musl:v1.0.0-arm64
```

The devcontainer isn't an SDK — it's the build container for the machine the
team is targeting, promoted to a registry image. The app developer inside the
container still runs `yoe build` and `yoe shell` against the project checkout.

## What This Replaces

| Yocto concept                      | `[yoe]` equivalent                            |
| ---------------------------------- | --------------------------------------------- |
| `populate_sdk` / SDK tarball       | _(nothing)_ — app devs install `yoe` directly |
| `environment-setup-*` shell script | `yoe shell`                                   |
| `populate_sdk_ext` extensible SDK  | `yoe` itself (the tool is the extensible SDK) |
| Offline SDK installer              | `yoe bundle export` / `yoe bundle import`     |
| `oe-devshell`                      | `yoe shell <unit>`                            |
| Cross-toolchain tarball            | _(not applicable)_ — `[yoe]` is native-only   |

## See Also

- [The `yoe` Tool](yoe-tool.md) — reference for `yoe shell` and `yoe bundle`
  flags once implemented.
- [Build Environment](build-environment.md) — the container / bwrap sandbox
  model that `yoe shell` attaches to.
- [Unit & Configuration Format](metadata-format.md#tasks-and-per-task-containers-planned)
  — how per-unit and per-task container selection determines what `yoe shell`
  drops you into.
