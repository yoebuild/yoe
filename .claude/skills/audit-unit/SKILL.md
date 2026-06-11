---
name: audit-unit
description: >
  This skill should be used when the user asks to "audit a unit", "review a
  unit", "check a unit", "/audit-unit", or wants to verify that a unit follows
  best practices and has no common issues. Reviews a unit for correctness,
  completeness, and quality.
---

# Audit a Unit

Review an existing unit for common issues: missing dependencies, incorrect
license, unnecessary build dependencies, suboptimal configure flags, missing
sub-package splits, and deviation from project conventions.

## Workflow

### Step 1: Read the Unit

Find and read the unit's `.star` file:

```
Glob: modules/**/units/**/<name>.star
```

### Step 2: Cross-Reference with Other Distributions

Check how Alpine, Yocto, and Buildroot package the same software. Compare:

- **Dependencies** — are any build or runtime deps missing? Are any listed deps
  unnecessary?
- **Configure flags** — are there important flags that other distros use that
  this unit is missing? Are there flags enabled here that are unusual?
- **Patches** — do other distros carry patches that might be needed here?
- **License** — does the SPDX identifier match what other distros declare?

### Step 3: Check Dependencies

Verify the unit's dependency lists:

- **Missing build deps** — if `configure.ac` or `CMakeLists.txt` requires a
  library via `pkg-config` or `find_package`, it should be in `deps`.
- **Missing runtime deps** — if the built binary links against a shared library,
  that library's unit should be in `runtime_deps`.
- **Dep has no unit** — every dependency must be built from source as a unit. If
  a dep is listed but has no corresponding `.star` unit file, flag it. If a dep
  is satisfied only because it happens to be in the container's base image
  (Alpine artifacts), that is a bug — it needs its own unit. Never rely on
  `apk add` in the Dockerfile for library dependencies.
- **Unnecessary deps** — check if any listed deps are actually unused by the
  build.
- **Circular deps** — verify no dependency cycles exist via `yoe graph`.
- **Machine-flavored dep in a generic unit** — flag as a **warning** if a
  generic library or tool depends on a unit that varies by machine (anything
  with `provides` set, or a unit reachable only via `MACHINE_CONFIG`). It forks
  the generic unit's apk per machine. Generic units' deps should be other
  generic libraries/tools only.

### Step 3b: Check `provides` usage

`provides` is a `[]string` of virtual package names. If the unit sets it, check
that it's appropriate:

- `provides` is reserved for **leaf artifacts** that get swapped per machine or
  project: kernel, base-files, init, bootloader. Flag as a **warning** if a
  build-time library, a generic tool, or a daemon with a busybox alternative
  declares `provides` — every transitive consumer's apk forks per swap.
- For runtime alternatives (mdev/eudev, udhcpc/dhcpcd, busybox-ntpd/ntp-client),
  recommend installing both side-by-side and selecting which daemon runs from
  the init script in a config unit instead of using `provides`.
- See `docs/naming-and-resolution.md` §"When NOT to use provides" for details.

### Step 3c: Check `replaces` usage

`replaces` is a `[]string` of package names whose files this unit may overwrite
at install time. Verify:

- The unit only sets `replaces` if it actually ships a file path that another
  package also claims. Flag as a **warning** if `replaces` is set but no
  shadowed paths can be identified — likely a copy-paste from another unit.
- The replaced package(s) are real units in the project (`busybox`, `ncurses`,
  etc.). Typos silently degrade to "no shadow declared" and apk will fail the
  install.
- The shadow direction is correct: if pkg A overwrites pkg B's files, A declares
  `replaces = ["B"]`, not the other way around.

To check linked libraries after a successful build:

```bash
# Inside the container, check what the built binaries link against. The build
# tree is segmented by distro: build/<distro>/<unit>/ (e.g. build/alpine/...,
# build/debian/...), and <unit> carries its arch suffix.
find build/<distro>/<unit>/destdir -type f -executable | head -5
```

### Step 3d: Check for per-distro / per-machine build forks

yoe's rule is **one unit, one artifact; resolve variation at runtime**. A unit
should produce a single binary that every project and machine shares.

- Flag as a **warning** any field that forks the build by machine or project
  (per-machine `configure_args`, per-project flags) where a runtime mechanism
  would do — an init script that detects what's installed, a conditional config
  file, a `replaces:` annotation. Forking multiplies the cache surface and
  breaks binary reuse.
- Building **twice along the distro axis is expected, not a fork.** A source
  unit consumed by both an Alpine (musl) image and a Debian (glibc) image
  necessarily builds once per libc — musl- and glibc-linked binaries cannot
  share at the ABI level. That is the one legitimate split; do not flag it.
- If the same package is also pulled from an Alpine feed for some images (via
  `prefer_modules`) while built from source for others, confirm that is
  intentional (usually to get a shared-library soversion Alpine ships) rather
  than an accident.

### Step 4: Check Build Configuration

Review configure flags and build steps:

- **Security flags** — for network-facing software, ensure TLS/crypto is enabled
  and linked against openssl (not a bundled copy).
- **Unnecessary features** — for embedded targets, disable features that add
  bloat (e.g., GUI support, test suites, documentation generation).
- **Hardcoded paths** — build commands should use `$PREFIX`, `$DESTDIR`,
  `$NPROC`, not hardcoded values.
- **Parallel build** — verify `make -j$NPROC` is used (the classes handle this,
  but custom `build` steps might not).

### Step 5: Check Metadata

Verify unit metadata:

- **license** — must be a valid SPDX identifier. Cross-check against the
  upstream `LICENSE`/`COPYING` file.
- **description** — should be a clear, concise one-liner.
- **version** — check if a newer stable version exists upstream.
- **source** — prefer git URLs over tarballs. If using git, verify `tag` matches
  the version.

### Step 6: Check for Known Issues

- **Version staleness** — is the unit significantly behind upstream? Note any
  known CVEs in the current version.
- **Patch applicability** — if patches exist, are they still needed or have they
  been merged upstream?
- **Build reproducibility** — are there any non-deterministic elements (embedded
  timestamps, random ordering)?

### Step 7: Report Findings

Present findings organized by severity:

**Errors** (must fix):

- Missing runtime dependencies (will cause runtime failures)
- Incorrect license
- Security issues (e.g., using bundled crypto instead of system openssl)

**Warnings** (should fix):

- Missing build dependencies (build may work by accident via sysroot)
- Stale version with known CVEs
- Suboptimal configure flags

**Suggestions** (nice to have):

- Version bump available
- Patches that could be dropped
- Configure flags to reduce image size

For each finding, explain what the issue is, why it matters, and how to fix it.

## What NOT to Do

- Do not modify the unit during an audit — only report findings. The user
  decides what to fix.
- Do not flag style issues that match existing project conventions (e.g., if all
  units omit description periods, don't flag that).
- Do not recommend changes without checking how other distributions handle the
  same package — there may be good reasons for the current configuration.
- Do not recommend installing missing dependencies in the Dockerfile — every
  library and build tool must be a unit built from source.
