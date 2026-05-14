# Starlark Packaging and Image Assembly

**Date:** 2026-04-06 **Status:** Spec

Move packaging (APK creation, repo management) and image assembly (rootfs
population, disk generation) from hardcoded Go into composable Starlark tasks.
This makes packaging format a project-level policy choice and image assembly
fully customizable per-image.

## Motivation

Today, APK packaging and image assembly are hardcoded in Go:

- `internal/artifact/apk.go` — creates .apk via Go tar/gzip
- `internal/repo/` — publishes .apk and generates APKINDEX
- `internal/image/rootfs.go` — installs packages, applies config, overlays
- `internal/image/disk.go` — partitions, formats, installs bootloader
- `internal/bootstrap/` — Stage 0/1 orchestration

This means:

1. **Packaging format is not configurable.** Every unit produces an APK. To
   support deb, rpm, or "no packaging" (direct sysroot install, like Buildroot),
   you'd need to fork the Go code.
2. **Image assembly is opaque.** The disk layout, filesystem types, bootloader
   choice, and rootfs population strategy are all buried in Go. Customizing an
   image (e.g., Home Assistant with Docker on a Raspberry Pi) requires modifying
   Go internals.
3. **Classes can't compose.** A unit calls one class function that does
   everything. There's no way to say "build with cmake, then package with apk."

## Design

### Composable Task Lists

Classes become functions that return task lists, not functions that register
units. Units compose tasks by concatenation:

```python
# Simple: class registers the unit directly (convenience)
cmake(name = "zlib", version = "1.3.1")

# Composable: unit assembles tasks from multiple classes
load("//classes/cmake.star", "cmake_tasks")
load("//classes/apk.star", "apk_tasks")

unit(
    name = "zlib",
    version = "1.3.1",
    tasks = cmake_tasks(cmake_args = ["-DBUILD_SHARED_LIBS=ON"]) + apk_tasks(),
)
```

Each class function (e.g., `cmake_tasks()`, `autotools_tasks()`) returns a list
of `task(...)` entries. The convenience wrappers (`cmake()`, `autotools()`) call
`unit()` internally with the combined tasks.

### Project-Level Packaging Policy

The `unit()` Go builtin auto-appends packaging tasks based on project config:

```python
# PROJECT.star
project(
    name = "my-project",
    packaging = "apk",  # "apk", "deb", "rpm", "none"
    ...
)
```

```go
// In Go registerUnit():
if u.Class == "unit" && proj.Packaging != "none" {
    u.Tasks = append(u.Tasks, packagingTasks(proj.Packaging))
}
```

- `"apk"` — append APK creation + repo publish tasks (default)
- `"none"` — skip packaging, install destdir directly into sysroot (Buildroot
  style)

yoe is intentionally apk-only. The package format, repo index, signing model,
and on-device installer are all wired through `apk` end-to-end (`/etc/apk/keys`
in the rootfs, `apk add` at image-assembly time, the `apk-tools` unit for
on-device OTA). Adding `deb` or `rpm` would mean a parallel pipeline for each
without a real use case — yoe targets embedded Linux, not the desktop / server
distros where those formats live.

Units can opt out: `unit(..., package = False)` skips auto-appended packaging.

### Package Metadata

Package metadata uses existing top-level unit fields — no separate struct
needed:

```python
unit(
    name = "zlib",
    version = "1.3.1",
    description = "Compression library",
    license = "Zlib",
    runtime_deps = ["musl"],
    tasks = cmake_tasks() + apk_tasks(),
)
```

`apk_tasks()` reads `description`, `license`, `version`, and `runtime_deps` from
the unit to generate `.PKGINFO`. `deb_tasks()` would read the same fields to
generate `control`. The metadata is packaging-format-agnostic and already part
of the unit schema.

### Go Builtins for Packaging

APK creation requires tar/gzip/SHA operations that are impractical in pure
Starlark. These stay in Go as builtins callable from Starlark tasks:

| Builtin                                 | Purpose                           |
| --------------------------------------- | --------------------------------- |
| `apk_create(destdir, output, metadata)` | Create .apk from destdir          |
| `apk_publish(apk_path, repo_dir)`       | Copy to repo, regenerate APKINDEX |
| `hash_file(path, algorithm)`            | SHA256/SHA1 of a file             |

These are thin wrappers around the existing `artifact.CreateAPK()` and
`repo.Publish()`. The Starlark task calls the builtin; the builtin does the
heavy lifting:

```python
def apk_tasks():
    return [
        task("package", fn = lambda: apk_create(
            destdir = "${DESTDIR}",
            output = "${OUTPUT}",
        )),
        task("publish", fn = lambda: apk_publish(
            apk = "${OUTPUT}/${NAME}-${VERSION}.apk",
            repo = "${REPO}",
        )),
    ]
```

### Image Assembly in Starlark

The image class becomes a Starlark function that generates tasks for rootfs
population and disk image creation:

```python
# classes/image.star
def image(name, artifacts, hostname = "", timezone = "UTC", **kwargs):
    unit(
        name = name,
        unit_class = "image",
        artifacts = artifacts,
        tasks = [
            task("populate", fn = lambda: _populate_rootfs(artifacts)),
            task("configure", fn = lambda: _configure_rootfs(hostname, timezone)),
            task("partition", fn = lambda: _partition_disk()),
            task("assemble", fn = lambda: _assemble_image()),
        ],
        hostname = hostname,
        timezone = timezone,
        **kwargs,
    )
```

#### Populate (install packages into rootfs)

Currently `installPackages()` in Go — extracts .apk files via `tar xzf`. This is
a shell command per package. The dependency resolution (`resolvePackageDeps()`)
becomes a Starlark builtin or uses the DAG that already resolves deps:

```python
def _populate_rootfs(artifacts):
    rootfs = "${BUILD}/rootfs"
    run("rm -rf " + rootfs)
    run("mkdir -p " + rootfs)
    # apk_install resolves transitive deps and extracts into rootfs
    apk_install(rootfs = rootfs, packages = artifacts, repo = "${REPO}")
```

`apk_install()` is a Go builtin that wraps the existing `installPackages()` +
`resolvePackageDeps()`.

#### Configure (hostname, timezone, services)

Currently `applyConfig()` in Go — writes files and creates symlinks. Trivially
expressible as shell commands or Starlark file operations:

```python
def _configure_rootfs(hostname, timezone):
    rootfs = "${BUILD}/rootfs"
    run("echo '{}' > {}/etc/hostname".format(hostname, rootfs))
    run("ln -sf /usr/share/zoneinfo/{} {}/etc/localtime".format(timezone, rootfs))
```

#### Partition and Assemble (disk image)

Currently `GenerateDiskImage()` in Go — shells out to `sfdisk`, `mkfs.ext4`,
`mkfs.vfat`, `dd`, `mcopy`, `extlinux` via `RunInContainer()`. These are already
shell commands; they map directly to Starlark `run(host = True)` calls (running
in the container):

```python
def _partition_disk():
    run("truncate -s {}M ${{BUILD}}/{}.img".format(total_mb, name))
    run("sfdisk ${{BUILD}}/{}.img <<EOF\n...\nEOF".format(name), host = True)

def _assemble_image():
    run("mkfs.ext4 -d ${BUILD}/rootfs ${BUILD}/rootfs.img", host = True)
    run("dd if=${BUILD}/rootfs.img of=${BUILD}/${NAME}.img bs=1M seek=1 conv=notrunc", host = True)
```

### Per-Task Container Selection

To support mixed toolchains (e.g., build containerd with glibc, CLI with Go),
tasks can override the unit-level container:

```python
unit(
    name = "docker",
    version = "27.0",
    container = "toolchain-glibc",
    tasks = [
        task("build-containerd", steps = ["make -C containerd"]),
        task("build-cli", container = "toolchain-go",
             steps = ["go build ./cmd/docker"]),
    ],
)
```

The executor resolves container image per-task, falling back to the unit-level
default.

### Source Fetching

Currently in `internal/source/` — almost entirely shell commands (`git clone`,
`git checkout`, `tar xf`, `git am`). Moves to Starlark naturally:

```python
# Hypothetical — source prep as tasks on the unit
# Today this is implicit; making it explicit is optional
task("fetch", fn = lambda: git_clone(url = SRC_URI, ref = SRC_REV)),
task("patch", fn = lambda: git_am("${PATCHES}/*.patch")),
```

Source fetching could remain implicit (Go handles it before task execution) or
become explicit tasks. The implicit approach is simpler and avoids boilerplate.
Recommendation: keep source fetching in Go for now, move later if needed.

### Bootstrap

`internal/bootstrap/` orchestrates Stage 0 (host toolchain) and Stage 1
(self-hosted rebuild). This is build sequencing that could become a Starlark
"bootstrap" class or remain in Go. Since bootstrap is run rarely and has complex
ordering requirements, recommendation: keep in Go for now.

## What Stays in Go

| Component                              | Reason                                                   |
| -------------------------------------- | -------------------------------------------------------- |
| Build executor (DAG, caching, hashing) | Graph algorithms, concurrency, content-addressed caching |
| APK tar/gzip/hash operations           | Crypto and archive formats need Go stdlib                |
| Repo index generation                  | Reads APK internals, writes APKINDEX.tar.gz              |
| Source fetching and caching            | Complex caching logic, HTTP client, hash verification    |
| Bootstrap orchestration                | Rarely customized, complex ordering                      |
| bwrap/container invocation             | Security boundary, needs careful Go control              |

These are exposed as **Starlark builtins** (`apk_create`, `apk_install`,
`apk_publish`, `hash_file`) so Starlark tasks can call them.

## What Moves to Starlark

| Component                    | Current Location      | Benefit                                |
| ---------------------------- | --------------------- | -------------------------------------- |
| Packaging task composition   | Hardcoded in executor | Pluggable packaging formats            |
| Image rootfs population      | `image/rootfs.go`     | Custom rootfs strategies               |
| Image disk generation        | `image/disk.go`       | Custom partition layouts, bootloaders  |
| Image configuration          | `image/rootfs.go`     | Per-image hostname, services, overlays |
| Sysroot assembly             | `build/sandbox.go`    | Custom sysroot layouts                 |
| Per-task container selection | N/A (unit-level only) | Mixed toolchain builds                 |

## Implementation Order

1. **Composable task lists** — refactor classes to return task lists, add
   convenience wrappers. No Go changes needed.
2. **Per-task container** — add optional `container` field to `task()`, executor
   resolves per-task.
3. **Packaging builtins** — expose `apk_create` and `apk_publish` as Starlark
   builtins. Add `packaging` field to project config. Auto-append packaging
   tasks in `unit()`.
4. **Image assembly in Starlark** — expose `apk_install` builtin. Rewrite image
   class as Starlark tasks calling builtins + shell commands.
5. **`packaging = "none"` mode** — skip APK, install destdir directly into
   sysroot. Enables Buildroot-style builds.

## Non-Goals

- **Replacing APK with deb/rpm now.** The infrastructure supports it, but the
  immediate goal is making it _possible_, not implementing every format.
- **Moving DAG resolution to Starlark.** Graph algorithms and content-addressed
  caching are Go strengths.
- **Moving source fetching to Starlark.** The caching and hash verification
  logic is complex and rarely needs customization.
