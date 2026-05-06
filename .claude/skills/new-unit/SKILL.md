---
name: new-unit
description: >
  This skill should be used when the user asks to "create a unit", "add a unit",
  "new unit", "package something", "/new-unit", or provides a URL or project
  name to be packaged for Yoe. Generates a complete Starlark .star unit file
  from a source URL or description.
---

# Create a New Unit

Generate a complete Starlark `.star` unit from an upstream source URL or a
natural language description. The output is a ready-to-build unit that follows
existing conventions in the project's modules.

## Workflow

### Step 1: Determine the Source

If the user provides a URL (GitHub repo, tarball, etc.), use it directly. If the
user provides a description ("I need an MQTT broker"), research appropriate
upstream projects and suggest one, confirming with the user before proceeding.

### Step 2: Research Existing Packaging

Before writing the unit from scratch, check how other distributions package the
same software. These are valuable references for configure flags, dependencies,
patches, and known pitfalls:

- **Alpine Linux (APKBUILD)** — search
  `https://gitlab.alpinelinux.org/alpine/aports` for the package. Alpine is
  closest to Yoe's packaging model (apk, musl/glibc, minimal). Pay attention to
  `makedepends`, `depends`, and `configure` flags.
- **Yocto/OpenEmbedded (bitbake units)** — search
  `https://layers.openembedded.org` or the OE-Core module. Yocto units often
  have well-tested configure flags and patch sets for embedded use.
- **Buildroot** — search
  `https://github.com/buildroot/buildroot/tree/master/package` for the package.
  Buildroot units are simple and often reveal minimal configure flags needed for
  embedded targets.

Extract useful information: required dependencies, recommended configure flags,
known patches, and license details. Do not blindly copy — adapt to Yoe's
conventions and verify the information is current.

### Step 3: Inspect the Source

Fetch and inspect the upstream source to determine:

1. **Build system** — look for these files in priority order:
   - `configure.ac` / `Makefile.am` → autotools class
   - `CMakeLists.txt` → cmake class
   - `go.mod` → go class (go_binary)
   - `Makefile` only → custom build steps with `unit()`
   - `meson.build` → custom build steps (no meson class yet)

2. **Version** — latest stable release tag or version string

3. **Dependencies** — scan `configure.ac`, `CMakeLists.txt`, `go.mod`,
   `pkg-config` requires, or `#include` directives to identify build and runtime
   dependencies. Cross-reference against existing units in the project's modules
   and the findings from Step 2.

4. **License** — check `LICENSE`, `COPYING`, or source headers. Use SPDX
   identifiers (e.g., `MIT`, `Apache-2.0`, `GPL-2.0-or-later`).

### Step 4: Check for Existing Units

Before creating a new unit, search all modules for an existing unit:

```
Glob: modules/**/units/**/<name>.star
```

If one exists, inform the user and suggest `/update-unit` instead.

### Step 5: Generate the Unit

Write a `.star` file following the conventions of existing units in the project.
Use the appropriate class:

**Autotools example:**

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "example",
    version = "1.2.3",
    source = "https://github.com/example/example.git",
    tag = "v1.2.3",
    license = "MIT",
    description = "Short description of the package",
    deps = ["zlib", "openssl"],
    runtime_deps = ["zlib", "openssl"],
    configure_args = ["--with-ssl"],
)
```

**CMake example:**

```python
load("//classes/cmake.star", "cmake")

cmake(
    name = "example",
    version = "1.2.3",
    source = "https://github.com/example/example.git",
    tag = "v1.2.3",
    license = "MIT",
    description = "Short description of the package",
    deps = ["zlib"],
    runtime_deps = ["zlib"],
    cmake_args = ["BUILD_SHARED_LIBS=ON"],
)
```

**Go example:**

```python
load("//classes/go.star", "go_binary")

go_binary(
    name = "example",
    version = "1.2.3",
    source = "https://github.com/example/example.git",
    tag = "v1.2.3",
    license = "Apache-2.0",
    description = "Short description of the package",
)
```

**Custom build (no class):**

```python
unit(
    name = "example",
    version = "1.2.3",
    source = "https://github.com/example/example.git",
    tag = "v1.2.3",
    license = "MIT",
    description = "Short description of the package",
    deps = ["zlib"],
    runtime_deps = ["zlib"],
    build = [
        "./configure --prefix=$PREFIX",
        "make -j$NPROC",
        "make DESTDIR=$DESTDIR install",
    ],
)
```

### Step 6: Choose the File Location

Place the unit in the appropriate category directory within the project's module
or the module-core module:

| Category    | Directory            | Examples               |
| ----------- | -------------------- | ---------------------- |
| Libraries   | `units/libs/`        | zlib, openssl, ncurses |
| Networking  | `units/net/`         | openssh, curl          |
| Base system | `units/base/`        | busybox, linux         |
| Debug tools | `units/debug/`       | strace, vim            |
| Bootloaders | `units/bootloaders/` | syslinux               |

If no existing category fits, create a new one (e.g., `units/multimedia/`).

### Step 7: Confirm and Write

Present the complete unit to the user for review before writing the file. Show
the file path and contents. Only write after confirmation.

### Step 8: Test Build

After writing the unit, build it to verify:

```bash
yoe build --force <unit-name>
```

If the build fails, use the diagnose workflow to fix it iteratively.

## Unit Conventions

- **Prefer git sources** over tarballs — use `source` with a `.git` URL and
  `tag` for version pinning
- **Tag format** varies by project — inspect the upstream repo's tags (e.g.,
  `v1.2.3`, `release-1.2.3`, `openssl-3.4.1`)
- **deps vs runtime_deps** — `deps` are build-time only (headers, static libs);
  `runtime_deps` are needed at runtime. Most libraries are both.
- **configure_args** — only add flags that differ from defaults. Do not add
  `--prefix=$PREFIX` (the class handles it).
- **license** — use SPDX identifiers. Check the upstream project carefully.
- **description** — one sentence, lowercase start, no trailing period
- **provides** — almost always omit. `provides` is a `[]string` of virtual
  package names this unit satisfies; it is reserved for **leaf artifacts** that
  get swapped per machine or per project: kernel, base-files, init, bootloader.
  Do **not** set `provides` on a build-time library, a generic tool (less, htop,
  file, etc.), or a daemon that has a busybox alternative — those should ship
  side-by-side and be selected at boot from init scripts. Misusing `provides`
  forks every transitive consumer into a machine-specific apk variant. See
  `docs/naming-and-resolution.md` §"When NOT to use provides".
- **replaces** — `[]string` listing packages whose files this unit may overwrite
  at install time. Set this only when the unit ships a path that is also owned
  by another package and you want apk to accept the shadow rather than fail.
  Example: `util-linux` ships real `dmesg`/`mount`/`umount` etc. at paths
  busybox also claims, so its unit declares `replaces = ["busybox"]`. Without
  the annotation, `apk add` rejects the conflict at image-assembly time.
- **Generic units must not depend on machine-flavored units.** A library or
  tool's `deps` and `runtime_deps` should reference only other libraries and
  tools. Never add `linux`, `base-files`, or any unit that varies by machine to
  a generic unit's deps — it will fork that unit's apk per machine.

## Dependency Policy

**Never install missing dependencies in the Dockerfile.** The container provides
only the minimal bootstrap toolchain (gcc, binutils, make, etc.). Every library
and build tool the unit needs must exist as a unit:

- If a dependency already has a unit, add it to `deps` (and `runtime_deps` if
  it's a shared library needed at runtime).
- If no unit exists for the dependency, **create one first** before writing the
  unit that depends on it. Use this same workflow recursively.
- For non-essential build-time features (doc generation, man pages, GUI
  bindings), prefer disabling them via configure flags over adding deps.

## What NOT to Do

- Do not guess dependencies — inspect the build system files and reference other
  distributions' units to find them.
- Do not hardcode absolute paths in build commands — use `$PREFIX`, `$DESTDIR`,
  `$NPROC` environment variables.
- Do not add a unit to `module-core` unless it's truly a core system component.
  Project-specific units go in the project's own module.
- Do not skip the test build step.
- Do not install missing tools or libraries in the Dockerfile — create units for
  them instead.
