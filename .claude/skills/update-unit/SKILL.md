---
name: update-unit
description: >
  This skill should be used when the user asks to "update a unit", "bump a
  unit", "upgrade a unit", "update to latest version", "/update-unit", or
  mentions that a unit needs a version bump. Bumps a unit to the latest upstream
  version with a test build.
---

# Update a Unit

Bump an existing unit to the latest upstream release. Updates the version, tag,
and sha256 (if applicable), checks for patch conflicts or dependency changes,
and verifies with a test build.

## Workflow

### Step 1: Read the Current Unit

Find and read the unit's `.star` file:

```
Glob: modules/**/units/**/<name>.star
```

Note the current version, source URL, tag format, and any patches.

### Step 2: Find the Latest Version

Determine the latest stable release from the upstream source:

- **GitHub repos** — check releases/tags via the GitHub API or web
- **Tarball sources** — check the project's download page

Identify the new version number and the corresponding tag name. Match the
existing tag format (e.g., if current is `V_9_9_P1`, the new tag should follow
the same convention like `V_9_9_P2`).

### Step 3: Research Changes

Check how other distributions have handled the version bump. This helps identify
new dependencies, removed features, or required patch updates:

- **Alpine Linux** — check if their APKBUILD has been updated for the new
  version, and note any new `makedepends` or configure flag changes
- **Yocto/OpenEmbedded** — check if the OE-Core unit has been updated, noting
  any new patches or dependency changes
- **Buildroot** — check for configure flag or dependency changes

Also review the upstream changelog/release notes for breaking changes, new
dependencies, or removed features that might affect the build.

### Step 4: Update the Unit

Modify the `.star` file:

- Update `version` to the new version string
- Update `tag` to match the new release tag
- Update `sha256` if the unit uses tarball sources
- Adjust `deps` or `configure_args` if the new version requires changes

### Step 5: Check Patches

If the unit has `patches`, verify they still apply to the new version. Check
`build/<distro>/<unit>/src/` after source preparation for `.rej` files (the
build tree is segmented by distro — `build/alpine/...`, `build/debian/...` — and
`<unit>` carries its arch suffix). If patches conflict:

- Determine if the patch is still needed (the fix may be upstream now)
- If still needed, regenerate the patch against the new version
- If obsolete, remove it from the patches list

### Step 6: Test Build

Build the updated unit:

```bash
yoe build --clean <unit-name>
```

Use `--clean` to ensure a fresh build from the new source. If the unit builds
under more than one distro, add `--distro <distro>` to target each. If the build
fails, read the log with `yoe log <unit-name>` (or
`build/<distro>/<unit>/build.log`) and use the diagnose workflow to fix it
iteratively.

### Step 7: Test Reverse Dependencies

After the unit builds, check if artifacts that depend on it still build:

```bash
yoe refs <unit-name>
```

Build any direct dependents to catch API/ABI breakage:

```bash
yoe build --force <dependent-unit>
```

### Step 8: Report Changes

Summarize what changed:

- Version bump (old → new)
- Any dependency changes
- Any configure flag changes
- Any patches added, removed, or updated
- Build and reverse-dependency test results

## What NOT to Do

- Do not update to pre-release, alpha, beta, or RC versions unless the user
  explicitly requests it.
- Do not remove patches without verifying the fix is in the new version.
- Do not skip the reverse-dependency check — an ABI change in a library can
  break all consumers.
- Do not change configure flags without understanding why — research the
  upstream changelog and other distributions' units first.
