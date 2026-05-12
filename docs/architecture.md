# Architecture

This page introduces the four core concepts in `[yoe]` — **project**,
**module**, **unit**, and **package** — and how they relate. Use it as a map
before diving into the reference docs.

![Project, modules, units, and packages](assets/project-module-unit-package.png)

## Project

The **project** is the top of the tree. It is a directory containing a
`PROJECT.star` file, which declares:

- which **modules** the project uses (and at what Git ref),
- the **machine** to build for,
- any `prefer_modules` resolution pins,
- project-local **units** under `units/` and machines under `machines/`.

A project is what you check into version control for a specific product. It is
what `yoe init` scaffolds and what the `yoe` CLI operates on from your working
directory.

See [Unit & Configuration Format](metadata-format.md) for the full
`PROJECT.star` surface and [Naming and Resolution](naming-and-resolution.md) for
how module references and prefer-rules work.

## Modules

A **module** is a Git repository (or a subdirectory of one) that provides
reusable building blocks: classes, units, machine definitions, container
definitions, and image definitions. Projects compose modules to get the pieces
they need.

Typical modules:

- `module-core` — base classes (autotools, cmake, go), common units (busybox,
  openssl, openssh, kernel), and reference images.
- A BSP module (e.g. `module-jetson`, `module-rpi`) — machine definitions and
  hardware-specific units for a board family.
- `module-alpine` — passthrough access to upstream Alpine `.apk` packages.

Modules are referenced by URL and Git ref in `PROJECT.star`. The `[yoe]` CLI
clones them into the project's cache. See
[Naming and Resolution](naming-and-resolution.md) for module naming, directory
structure, and load-path semantics.

## Units

A **unit** is a `.star` file describing _how to build_ a single piece of
software. Units live in a module's `units/` directory (or the project's own
`units/`), and they call into a **class** (`autotools`, `cmake`, `go`, …) that
encodes the build pattern.

A unit declares its source, version, dependencies, and any build-time
configuration. The `[yoe]` build system resolves the DAG of units, runs each in
its own sandboxed build environment, and installs results into the build sysroot
so downstream units can find them.

Units are inputs to the build system: developer-edited, version-controlled, and
a CI concern. See [Unit & Configuration Format](metadata-format.md) for the
unit, class, and machine API.

## Packages

A **package** is the build output — an `.apk` file that the unit produces.
Packages are content-addressed, cached, signed, and published to a repository.
They are consumed by `apk` at image-assembly time and by the on-device package
manager for over-the-air updates.

One unit produces one `.apk` today. A small set of subpackage splits (`-dev`,
`-dbg`) is planned for cases where the runtime image should not carry headers or
debug info. See
[metadata-format.md#units-vs-packages](metadata-format.md#units-vs-packages) for
the contract between units and packages, and [apk Signing](signing.md) /
[Feed Server](feed-server.md) for how packages get published and deployed.

## How they fit together

The build flow is **unit → build → .apk → repository → image / device**. The
conceptual flow is **project references modules, modules provide units, units
produce packages, packages assemble into images**:

| Concept | Lives in            | Produced by              | Consumed by                 |
| ------- | ------------------- | ------------------------ | --------------------------- |
| Project | Your product repo   | You                      | The `yoe` CLI               |
| Module  | A Git repo          | Module authors           | Projects                    |
| Unit    | A module or project | Module / project authors | The build system            |
| Package | A package repo      | The build system         | `apk` (image and on-device) |

For an explanation of why this split exists — versus Yocto's recipe/layer model
— see [Comparisons](comparisons.md). For the language used to express units and
configuration, see [Build & Configuration Languages](build-languages.md).

## Where units build

Builds run on the host through a tiered environment. The host provides only
`yoe` and a container runtime; everything else is nested inside the container
that `yoe` spawns:

![Build environment tiers](assets/build-environment-tiers.png)

Each unit builds inside its own bwrap sandbox with only its declared deps
visible. See [Build Environment](build-environment.md) for the tier-by-tier
details, the bootstrap process, and the rationale behind bwrap-over-Docker for
per-unit isolation.

## What feeds a unit build

A unit pulls inputs from four independent sources, each managed by the right
tool for the job:

![Build dependencies](assets/build-dependencies.png)

Host tools (compilers, language runtimes) come from Docker containers; library
deps from the apk sysroot built up by other yoe units; distro packages (full
libraries, runtime services, applications) come from prebuilt upstream apks via
`module-alpine`; and language-native deps (Go modules, Cargo crates, pip wheels)
are handled by each language's own package manager inside the container. See
[Build Dependencies and Caching](build-dependencies-and-caching.md) for why this
split exists and how it interacts with the build cache, and
[Alpine apk Passthrough](apk-passthrough.md) for the prebuilt-apk path.

## How packages reach a running device

Built packages flow from the workstation to a running yoe device through a small
set of orthogonal channels: mDNS for discovery, HTTP for the apk pull, and SSH
for orchestration. The same apk repo, signing key, and `APKINDEX` serve
image-time installs, the dev loop, and on-device OTA:

![Feed server topology](assets/feed-server-topology.png)

`yoe serve` is the long-lived HTTP + mDNS server, `yoe device repo add` does the
one-time `/etc/apk/repositories` setup, and `yoe deploy` orchestrates the whole
"build → ship → install" round trip. See
[Feed Server and yoe deploy](feed-server.md) for the workflows, command
reference, and trust model.

## How distro packages enter the pipeline

`module-alpine` units don't rebuild Alpine packages — they repack each upstream
`.apk`, swapping the signature so the device's apk-tools verifies against the
project key like any other yoe-built package:

![apk passthrough repack pipeline](assets/apk-passthrough-repack.png)

The control segment (PKGINFO, install scripts, file checksums) and data segment
pass through byte-for-byte, so apk-tools on the device sees Alpine's metadata,
install behavior, and shared-library deps unchanged. Only the signature changes.
See [Alpine apk Passthrough](apk-passthrough.md) for the two-metadata-systems
story (`.star` fields drive the yoe resolver; PKGINFO drives apk-tools at
install time) and the noarch routing details.
