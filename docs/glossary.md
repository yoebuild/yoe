# Glossary

This page defines the vocabulary of yoe: the core concepts, the fields you
write in unit `.star` files, the build-time environment variables your build
steps can rely on, and the Starlark builtins that tie them together. Terms are
grouped by what they describe rather than alphabetically, so related ideas sit
together. For the full format with worked examples, see
[Unit & Configuration Format](metadata-format.md); for how names are looked up
and shadowed, see [Naming and Resolution](naming-and-resolution.md).

## Core concepts

**Unit.** The single definition of how to build one piece of software, written
as a `.star` file. A unit declares its source, dependencies, and build steps;
yoe turns it into one or more packages. The unit is the one place a package's
build is defined — its output is content-addressed and keyed by
`(distro/libc, arch, machine scope)`, so every project and machine that lands on
the same key shares the same artifact. The same source unit therefore builds
more than once whenever those keys differ (most commonly: once for musl/Alpine
and once for glibc/Debian).

**Package** / **artifact.** The binary output of a build — an `.apk` on Alpine,
a `.deb` on Debian or Ubuntu. Units are the version-controlled inputs; packages
are the built outputs. A single unit may emit several packages where split
packages (`-dev`, `-doc`, `-libs`) apply, while remaining the single source of
truth for the build.

**Class.** A reusable build pattern expressed as a Starlark function (for
example `autotools`, `cmake`, `go_binary`, `image`). A class encapsulates the
common task sequence for a kind of software and ultimately calls `unit(...)`
with the right configuration. Classes live in a module's `classes/` directory
and are composed by forwarding `**kwargs`. See
[Classes](metadata-format.md#classes).

**Image.** A unit whose class is `image`. It assembles a root filesystem from a
set of packages and produces a bootable disk image. Images carry their own
[distro](#distro), select packages through the `artifacts` field, and resolve
the full runtime closure of those packages at build time. An image — and any
deployable container — is the artifact that carries a distro.

**Machine.** A target board or platform definition (under `machines/<name>.star`).
It pins the architecture, kernel, bootloader, QEMU settings, default packages,
and partition layout. Machines drive target-specific build selection, such as
which kernel unit a given distro uses. Supported machines include QEMU, the
Raspberry Pi family, and BeaglePlay.

**Distro.** A distribution family that fixes the libc, init system, and
packaging format: `alpine` (musl + apk), `debian` (glibc + dpkg), `ubuntu`
(glibc + dpkg). The distro is chosen per image-bearing artifact through an
effective-distro cascade (`image.distro` → project default override → project
default). A unit tagged with a non-empty `distro` is only visible to closures of
that distro; an untagged unit is visible to all. See
[Yoe and distributions](distro.md).

**Module.** A git repository (or a subdirectory of one) that supplies units,
classes, and machine definitions. Modules are declared in `PROJECT.star` with
`module(...)` and evaluated in declaration order, which establishes priority for
unit shadowing and for resolving virtual-package providers. A module may carry a
`MODULE.star` manifest that names it and declares feeds.

**Feed** (synthetic module). A lazily materialized source for an upstream
distro's package index — Alpine's `APKINDEX` or Debian/Ubuntu's `Packages`.
Declared with `alpine_feed(...)` or `apt_feed(...)` in a module's `MODULE.star`,
one call per repository section. Units for individual packages materialize on
first reference during the closure walk, so working memory tracks closure size,
not catalog size. A distro module ships a feed plus hand-curated
service-enable companion units. See [module-alpine](module-alpine.md).

**Container.** The OCI image in which build steps run. yoe provides only a
minimal bootstrap toolchain in its default container; everything else is built
by a unit. A unit (or an individual task) can override the container with the
`container` field. Cross-architecture builds run a foreign-arch container under
QEMU user-mode rather than a cross-compiler.

**Sysroot.** The per-unit, read-only build environment assembled from the
unit's declared `deps` — the headers and libraries each dependency staged.
Mounted at `/build/sysroot` inside the container. An empty or incomplete
sysroot when a build fails on a missing header or library is itself the
diagnosis: a build edge was dropped or a dependency never materialized.

**Destdir.** The per-unit output staging directory, `/build/destdir` inside the
container and the value of `$DESTDIR`. Build steps install into it (the usual
`make DESTDIR=$DESTDIR install`); its contents become the package.

## Unit fields

These are the keyword arguments accepted by `unit(...)` and `image(...)`. Any
keyword not listed here is captured into the unit's template context (and
participates in the cache hash) rather than mapping to a typed field.

### Identity and versioning

| Field | Type | Meaning |
|-------|------|---------|
| `name` | str | Unit identifier. Required; unique within its module in a flat namespace. |
| `version` | str | Upstream version (e.g. `"3.2.1"`), used for changelog and package metadata. |
| `release` | int | Packaging revision (the apk `-r<N>` field); bumped when repackaging the same upstream version. |
| `scope` | str | Applicability of the build output: `arch` (per target arch, the default), `machine` (one machine), or `noarch` (shared across machines). |
| `unit_class` | str | Overrides the class name. Set internally by `image(...)` to `image`. |

### Metadata

| Field | Type | Meaning |
|-------|------|---------|
| `description` | str | Human-readable summary, shown in the TUI and written to package metadata. |
| `license` | str | SPDX license identifier(s), e.g. `"Apache-2.0 OR MIT"`. Informational. |
| `distro` | str | Distro compatibility tag. Empty means visible to all distros; otherwise the unit is only visible to matching-distro closures. |

### Source

| Field | Type | Meaning |
|-------|------|---------|
| `source` | str | A tarball URL or a git repository to fetch. Git sources are preferred (shallow clone with tag pinning). |
| `sha256` | str | Checksum for a tarball source. Mutually exclusive with `apk_checksum`. |
| `apk_checksum` | str | The `APKINDEX` `C:` checksum for an upstream Alpine package. Mutually exclusive with `sha256`. |
| `tag` | str | Git tag to check out, pinning the version. |
| `branch` | str | Git branch to check out. |
| `patches` | list[str] | Patch files (relative to the unit's directory) applied after fetch and before build. |
| `passthrough_apk` | str | An `.apk` filename to republish verbatim, used by Alpine feeds. |

### Dependencies

| Field | Type | Meaning |
|-------|------|---------|
| `deps` | list[str] | Build-time dependencies, resolved transitively and staged into the sysroot. |
| `runtime_deps` | list[str] | Runtime dependencies recorded in package metadata and resolved by apk/apt at install time. |
| `distro_deps` | dict[str, list[str]] | Per-distro additions to `deps`, keyed by distro name; merged in during the closure walk. |
| `distro_runtime_deps` | dict[str, list[str]] | Per-distro additions to `runtime_deps`, same shape as `distro_deps`. |

### Build execution

| Field | Type | Meaning |
|-------|------|---------|
| `container` | str | Default container image for the unit's tasks; individual tasks can override it. |
| `container_arch` | str | Whether the container runs as the build host's arch or the target arch. Container units set this explicitly. |
| `sandbox` | bool | Run build steps under additional sandboxing inside the container. |
| `shell` | str | Shell used for build commands: `sh` (default) or `bash`. |
| `tasks` | list[task] | Named build phases, each a sequence of steps. Classes generate these; units may override. |

### Virtual packages and file ownership

| Field | Type | Meaning |
|-------|------|---------|
| `provides` | list[str] | Virtual package names this unit satisfies (e.g. `["linux"]`), used for provider resolution. |
| `replaces` | list[str] | Package names whose files this unit may legitimately overwrite at install time, declaring ownership of shared paths. |

### Services, config, and environment

| Field | Type | Meaning |
|-------|------|---------|
| `services` | list[str] | Init services (OpenRC scripts, systemd units) the package enables at boot by baking the runlevel/target symlink into itself. The unit — not the image — decides this. |
| `conffiles` | list[str] | Config-file paths preserved across package upgrades, recorded in package metadata. |
| `environment` | dict[str, str] | Environment-variable definitions embedded in the unit's package metadata. |
| `cache_dirs` | dict[str, str] | Build-time cache mounts: container path → host cache subdirectory. |

### Image fields

These apply to units built with `image(...)`.

| Field | Type | Meaning |
|-------|------|---------|
| `artifacts` | list[str] | Packages to install into the root filesystem; expanded to the full runtime closure at build time. |
| `exclude` | list[str] | Packages to keep out of the root filesystem. |
| `hostname` | str | Default hostname written to `/etc/hostname`; defaults to the machine name. |
| `timezone` | str | Timezone (e.g. `"UTC"`), symlinked to `/etc/localtime`. |
| `locale` | str | Default locale, e.g. `"en_US.UTF-8"`. |
| `partitions` | list[partition] | Disk partition layout the image builder uses to lay out storage. |

## Build environment variables

These are set in the build environment for every unit's tasks, so build steps
and templates can reference them. Any names you declare in the `environment`
field are added alongside these.

| Variable | Value / meaning |
|----------|-----------------|
| `DESTDIR` | `/build/destdir` — where build steps install files that become the package. |
| `PREFIX` | `/usr` — the installation prefix. |
| `NPROC` | CPU count, for parallel builds (`make -j$NPROC`). |
| `ARCH` | Target architecture of this build. |
| `MACHINE` | Target machine name. |
| `DISTRO` | The consuming image's effective distro, so a build-twice unit can branch on it. |
| `CONSOLE` | The machine's console device, derived from the kernel command line. |
| `REPO` | Path to the project's package repository for this distro. |
| `PKG_CONFIG_PATH`, `CFLAGS`, `CPPFLAGS`, `LDFLAGS`, `LD_LIBRARY_PATH`, `PYTHONPATH` | Point compilation and linking at the unit's sysroot (`/build/sysroot`), including Debian multiarch paths so feed-provided `.pc` files, headers, and libraries resolve. |

## Starlark builtins

The functions available in `.star` files. Some declare entities (a unit, an
image, a machine); others are data constructors used to build up an argument; a
few run only inside build tasks.

### Declaring entities

| Builtin | Purpose |
|---------|---------|
| `unit(...)` | Define a package unit. The foundation every class builds on. |
| `image(...)` | Define a root-filesystem image (a unit with class `image`). |
| `machine(...)` | Define a target board or platform. |
| `project(...)` | Declare top-level project metadata in `PROJECT.star` (name, version, defaults, cache, sources, modules). |
| `module_info(...)` | Declare a module's name, description, and dependencies in its `MODULE.star`. |
| `command(...)` | Register a custom CLI subcommand from a `commands/*.star` file. |
| `alpine_feed(...)` | Register a synthetic Alpine package feed (one per repo section) in a `MODULE.star`. |
| `apt_feed(...)` | Register a synthetic Debian/Ubuntu package feed in a `MODULE.star`. |

### Data constructors

These return a struct for use as an argument to one of the declarations above.

| Builtin | Used in | Purpose |
|---------|---------|---------|
| `module(...)` | `project(modules=...)` | A module reference (url, ref, path, or local override). |
| `defaults(...)` | `project(defaults=...)` | Default machine, image, and distro for the project. |
| `cache(...)` / `s3_cache(...)` | `project(cache=...)` | Local and S3 remote cache configuration. |
| `sources(...)` | `project(sources=...)` | Language-ecosystem proxy/mirror endpoints (Go, Cargo, npm, PyPI). |
| `kernel(...)` | `machine(kernel=...)` | Kernel source, defconfig, device trees, and command line. |
| `uboot(...)` | `machine(...)` | U-Boot bootloader source and defconfig. |
| `qemu_config(...)` | `machine(qemu=...)` | QEMU machine, CPU, memory, firmware, and port settings. |
| `partition(...)` | `machine`/`image` | A disk partition (label, type, size, contents, root flag). |
| `task(...)` | `unit(tasks=...)` | A named build phase: a shell command, a Starlark function, or a list of steps. |
| `arg(...)` | `command(args=...)` | A command-line argument descriptor for a custom command. |

### Build steps and task-time helpers

| Builtin | Purpose |
|---------|---------|
| `install_file(src, dest, mode=...)` | Install a static file into the destdir during a build task. |
| `install_template(src, dest, mode=...)` | Render a Go-template file and install the result. |
| `run(command)` | Execute a shell command; callable only inside a task function. |
| `dir_size_mb(path)` | Measure a destdir subdirectory's size in MB; callable only inside a task function. |
| `resolve_closure(artifacts, distro=...)` | Walk the runtime-dependency graph and return the topologically ordered closure; used by image classes. |
