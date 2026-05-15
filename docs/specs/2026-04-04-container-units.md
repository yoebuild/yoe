# Container Units Design

Container units are a new unit type that produces Docker images for use as build
environments. This replaces the single embedded Dockerfile with per-unit
container definitions that participate in the DAG, caching, and versioning.

## Container Unit Definition

A new top-level Starlark function `container()` loaded from
`//classes/container.star`. It registers a unit with `class = "container"` that
produces a Docker image.

### Module layout

```
modules/module-core/
  MODULE.star
  classes/
  containers/
    toolchain-musl.star
    toolchain-musl/
      Dockerfile
  units/
  images/
  machines/
```

The `containers/` directory is parallel to `units/`, `images/`, and `machines/`.
The `.star` file is a sibling of a directory matching the unit name. The
directory holds the Dockerfile and any supporting files (configs, scripts). This
follows the Yocto pattern of recipes alongside their files directory.

### Starlark definition

```python
load("//classes/container.star", "container")

container(
    name = "toolchain-musl",
    version = "15",
)
```

The `container()` function defaults `dockerfile = "Dockerfile"`. The path is
resolved relative to the `<unit-name>/` directory next to the `.star` file. The
`dockerfile` parameter can be overridden for non-standard names.

### Class implementation

The `container()` class registers a unit and provides tasks that run
`docker build` on the host via `run(host = True)`:

```python
def container(name, version, dockerfile = "Dockerfile"):
    unit(
        name = name,
        version = version,
        class = "container",
        tasks = [
            task("build",
                fn = lambda: run(
                    "docker build -t yoe/%s:%s -f %s/%s %s" % (
                        name, version, name, dockerfile, name),
                    host = True,
                ),
            ),
        ],
    )
```

The `host = True` flag on `run()` executes the command on the host instead of
inside a container. This is a new extension to the `run()` builtin.

## Container Usage by Units

Classes like `autotools()`, `cmake()`, and `go()` set the container internally.
Units look the same as today — no new boilerplate.

### Class-provided container

Each class function internally sets `container`, `container_arch`, and includes
the container in `deps`:

```python
# Inside autotools.star
def autotools(configure_args = [], **kwargs):
    # ... build tasks ...
    # Internally sets:
    #   container = "toolchain-musl"
    #   container_arch = "target"
    #   deps includes "toolchain-musl"
```

A typical unit is unchanged:

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "ncurses",
    version = "6.4",
    source = "https://github.com/mirror/ncurses.git",
    tag = "v6.4",
    license = "MIT",
    description = "Terminal handling library",
    configure_args = ["--with-shared", "--without-debug"],
)
```

### Overriding the container

Units that need a different container override it explicitly:

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "special-lib",
    version = "1.0",
    source = "https://github.com/example/special-lib.git",
    container = "my-custom-container",
    container_arch = "target",
    deps = ["my-custom-container", "zlib"],
)
```

### Deps merging

Class functions merge their deps (including the container dep) with
user-provided deps. A unit declaring `deps = ["openssl", "zlib"]` with
`autotools()` gets effective deps `["openssl", "zlib", "toolchain-musl"]`. This
is handled inside the class function, not via `**` spread.

## Container Architecture

The `container_arch` field determines whether the container runs in the target
architecture or the host architecture:

- `"target"` — container matches the target arch. For cross builds, this uses
  QEMU user-mode emulation. Used by C/C++ builds that need native compilation.
- `"host"` — container runs in the host architecture. Used by languages like Go
  that cross-compile natively via GOARCH/GOOS.

There is no default. `container_arch` must always be set explicitly, either by
the class or by the unit. If missing, evaluation errors:

```
error: unit "zlib" has no container_arch — set it in the unit or class
```

Classes set `container_arch` explicitly:

- `autotools()`, `cmake()` → `container_arch = "target"`
- `go()` → `container_arch = "host"`

Units can override `container_arch` if needed.

## External Container Images

If the `container` value contains `:` or `/` (e.g., `golang:1.23`,
`registry.example.com/myimage:v2`), it is treated as a Docker image reference:

- No DAG dependency — Docker pulls on demand.
- No build step — the image is assumed to exist in a registry or locally.
- No input hash contribution from a Dockerfile (the image tag itself is hashed).

```python
load("//classes/go.star", "go")

go(
    name = "myapp",
    version = "1.0.0",
    source = "https://github.com/example/myapp.git",
    # go() sets container = "golang:1.23", container_arch = "host"
)
```

## Container Resolution

A unit's container is set explicitly, either by the class function or directly
on the unit. There is no project-level default or fallback chain.

If a unit with tasks has no `container`, it errors at evaluation time:

```
error: unit "zlib" has no container — set container in the unit or class
```

Container units themselves (`class = "container"`) are exempt — they build via
`docker build` on the host, outside any container.

Units with no tasks (metadata-only units like machine definitions) do not
require a container.

## DAG Integration

Container dependencies are explicit in `deps`. No implicit DAG edges. If a unit
uses `toolchain-musl` as its container, `toolchain-musl` must appear in the
unit's `deps` list. Class functions handle this automatically.

Container units build before any unit that depends on them, same as any other
dependency.

## Build Execution

### Container unit builds

Container unit builds are driven by Starlark, not special Go code. The
`container()` class provides tasks that use `run(host = True)` to execute
`docker build` on the host — no sandbox, no container-inside-container.

The build flow:

1. Cache check: input hash (includes Dockerfile content and all files in the
   unit directory) determines rebuild. If hash matches `.yoe-hash` marker and
   the Docker image exists, skip the build.
2. The class's Starlark task runs `docker build` (host arch) or
   `docker buildx build` (cross-arch) with the unit directory as build context.
3. The image is tagged as `yoe/<unit-name>:<version>` or
   `yoe/<unit-name>:<version>-<arch>` for cross-arch.

### Regular unit builds

The execution path changes from the current hardcoded `containerTag(arch)`:

1. Resolve the unit's `container` field
2. Resolve `container_arch` to determine host or target arch
3. If container is a unit name: use `yoe/<name>:<version>[-<arch>]` as image tag
4. If container is an external reference: use it directly
5. Pass the resolved image tag to `RunInContainer`

## Input Hashing

Container unit input hash includes:

- Unit name, version
- SHA256 of Dockerfile content
- SHA256 of all files in the unit's files directory
- Docker build args (if any)

Downstream units include their container's input hash as part of their own hash.
A Dockerfile change cascades rebuilds to all units using that container.

The version in the Docker tag is for human readability. The input hash drives
correctness. Changing the Dockerfile without bumping the version works — the tag
gets overwritten and downstream units rebuild due to hash change.

## What Gets Removed

- `containers/Dockerfile.build` — the embedded Dockerfile
- `containers/embed.go` — the Go embed directive
- `containerVersion` constant in `internal/container.go`
- `EnsureImage()` lazy-build logic — replaced by container unit builds in the
  DAG

## What Changes

### Go code

- `internal/container.go` — remove `EnsureImage()`, `containerVersion`,
  `containerTag()`. `RunInContainer()` takes the image tag as a parameter
  instead of computing it.
- `internal/build/sandbox.go` — `SandboxConfig` gains a `Container` field (the
  resolved image tag). `RunInSandbox` uses it instead of hardcoded
  `containerTag(arch)`.
- `internal/build/executor.go` — resolves the unit's container and
  `container_arch` before calling `RunInSandbox`.
- `internal/starlark/builtins.go` — extend `run()` builtin with `host = True`
  flag to execute commands on the host instead of inside a container.
- `internal/starlark/loader.go` — validate that units with tasks have a
  `container` and `container_arch`. Container deps merging in class functions.
- `internal/resolve/hash.go` — include container hash in unit hash computation.
- `internal/resolve/dag.go` — no changes needed (deps are explicit).

### Starlark (module-core module)

- New: `classes/container.star` — the `container()` class function
- New: `containers/toolchain-musl.star` + `containers/toolchain-musl/Dockerfile`
- Updated: `classes/autotools.star`, `classes/cmake.star` — add `container`,
  `container_arch`, container dep
- Updated: `classes/go.star` — add `container`, `container_arch = "host"`,
  container dep

### Discovery

The Starlark loader's recursive file discovery currently scans `units/`,
`images/`, and `machines/`. It must also scan `containers/` for `.star` files.

## Breaking Change

Existing external projects must use the updated `module-core` module that
includes `toolchain-musl`. Older module versions without the container unit will
fail at evaluation time.

## Future Work

- **Deployable containers** — container units that produce OCI tarballs packaged
  as `.apk` for installation on the target device (running application
  containers on edge hardware).
- **Registry caching** — push container images to a registry for team sharing.
- **Toolchain variants** — `toolchain-glibc-gcc`, `toolchain-musl-clang`, etc.
  Default container derived from machine/distro configuration.
