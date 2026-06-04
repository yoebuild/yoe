---
name: diagnose
description: >
  This skill should be used when the user asks to "diagnose a build failure",
  "debug a unit", "debug <unit>", "debug some unit", "fix a build", "why did the
  build fail", "/diagnose", or mentions a unit that failed to build. Iteratively
  analyzes build logs, identifies root causes, applies fixes, and rebuilds until
  the unit succeeds.
---

# Diagnose Build Failures

Analyze and fix unit build failures by reading the artifacts the failed build
already left on disk, identifying the root cause, applying a fix to the unit or
source, and only then rebuilding to verify.

## Diagnose from disk — do not rebuild to reproduce

A failed `yoe build` leaves everything you need under
`build/<distro>/<unit>.<arch>/`: the full `build.log`, the per-unit
`executor.log`, the extracted `src/` tree, the `destdir/` staging dir, and the
`sysroot/` that holds the deps that were staged for this unit. **Read those
artifacts first.** Do not re-run the build to "see the error" — the error is
already captured, and a yoe build can take minutes per unit and re-sync modules.
Rebuilding is the *last* step (verification of a fix), never the first step of
diagnosis. If the artifacts are missing or you genuinely can't tell which build
produced them, ask the user rather than kicking off a rebuild to regenerate
them.

## When to Use

- A unit fails to build (`yoe build <unit>` exits with error)
- The user asks to diagnose or debug a build failure
- The user says `/diagnose <unit>` or `/diagnose` (most recent failure)

## Diagnosis Workflow

### Step 1: Find the Build Log First

**Always start by locating the build log in the build directory** — it is the
single most useful artifact and almost always pinpoints the failure. The build
tree is segmented by distro, and each unit directory carries its arch suffix:

```
build/<distro>/<unit>.<arch>/build.log     full build output (configure/make/...)
build/<distro>/<unit>.<arch>/executor.log  per-unit task summary: which task failed
```

where `<distro>` is `alpine`, `debian`, etc. and `<unit>.<arch>` is e.g.
`openssl.x86_64` or `file.arm64`. `executor.log` names the failing task and unit
and tails the build log; `build.log` has the complete output. Read `build.log`'s
end for the actual error, and skim `executor.log` to confirm *which* task and
unit failed (useful when a dep failed rather than the unit you named).

**If you don't know which distro the failure is in, ask the user** before
guessing — the same unit can build under multiple distros, and reading the wrong
log wastes a cycle. Once you know the distro, find the most recent failure:

```
ls -lt build/<distro>/*/build.log | head -5
```

If the user specified a unit name, go straight to its log under the chosen
distro. Read the **end** of the log first — the error is almost always in the
last 100 lines:

```
Read build/<distro>/<unit>.<arch>/build.log (last 100 lines)
```

Shortcut: `yoe log [unit]` prints the build log for the most recent failure (or
a named unit) without hunting for the path, and `yoe log -e [unit]` opens it in
your editor. The underlying file is still `build/<distro>/<unit>.<arch>/build.log`
— reach for it directly when you need a specific distro's log or want to read a
slice with the Read tool.

If the error references earlier output (e.g., a missing header first used
hundreds of lines up), read more context as needed.

### Step 2: Inspect the sysroot when the error is a missing header/library

When the error is a missing dependency — a header not found, or a linker probe
that fails (`checking for gzopen in -lz... no`, `cannot find -lfoo`,
`configure: error: <feature> support requested but not found`) — confirm it
straight from disk instead of rebuilding. The deps that were staged for this
unit live in its own sysroot:

```
ls build/<distro>/<unit>.<arch>/sysroot/
find build/<distro>/<unit>.<arch>/sysroot -iname 'lib<name>*' -o -iname '<name>.h'
```

An **empty sysroot, or one missing the expected `lib<name>.so` / `<name>.h`**,
means the dependency was never staged — the build edge to it was dropped or the
dep never materialized, not that the dep's own build failed. Cross-check against
a sibling unit that built successfully (`ls build/<distro>/<other-unit>.<arch>/sysroot/`)
to see what a populated sysroot looks like. On Debian/apt distros, remember the
split: configure-time link probes need the **`-dev`** package (headers + the
unversioned `lib*.so` symlink), while `distro_runtime_deps` only pull the
runtime `libN` package — a sysroot that has `libfoo1` but not `libfoo-dev` will
fail every `-lfoo` link test. Whether the missing dep has a build directory at
all (`ls -d build/<distro>/<depname>.<arch>` exists?) tells you if it was ever
scheduled.

### Step 3: Read the Unit

Load the unit's `.star` file to understand what's being built, its dependencies,
build class, configure args, and any custom build steps:

```
Find and read modules/**/units/**/<unit>.star
```

### Step 4: Identify the Root Cause

Common failure categories in order of likelihood:

1. **Missing dependency** — compiler error for a missing header or library.
   Check if the required package is in the unit's `deps` list. Check if the dep
   is built and installed to `build/sysroot/`. If the dep has no unit yet,
   **create one** — do not install it in the Dockerfile via `apk add`. Every
   library the system needs must be built from source as a unit.
2. **Missing build tool** — a tool required during the build (e.g., `makeinfo`,
   `help2man`, `bison`) is not in the container. The fix is **never** to install
   it in the Dockerfile. Instead, either disable the feature that needs it
   (e.g., `--disable-docs`) if it's non-essential, or write a new unit that
   builds the tool from source and add it as a `deps` entry so it lands in the
   sysroot before this unit builds. The Dockerfile provides only the minimal
   bootstrap toolchain.
3. **Configure flag issue** — `./configure` or `cmake` can't find a feature or
   path. Check `configure_args` in the unit and verify paths reference
   `/build/sysroot`.
4. **Source/patch conflict** — patch doesn't apply, or source version changed.
   Check `build/<distro>/<unit>/src/` for `.rej` files or git errors in the log.
5. **Toolchain mismatch** — wrong compiler flags, missing tools. Check the build
   environment and Dockerfile.
6. **Parallel build race** — intermittent failure in `make -j`. Look for "No
   rule to make target" or missing generated files. Retry with `make -j1` as a
   diagnostic step.

### Step 5: Apply the Fix

Based on the root cause, apply the appropriate fix:

- **Missing dep**: Add to the unit's `deps` list in the `.star` file. If no unit
  exists for the dependency, create one first. Never install the missing library
  in the Dockerfile.
- **Missing build tool**: If non-essential (docs, man pages), disable via
  configure flags. If essential, create a new unit for the tool and add it as a
  dep. **Never modify the Dockerfile to install artifacts.**
- **Configure flag**: Adjust `configure_args` in the unit
- **Patch conflict**: Update or remove the conflicting patch
- **Source issue**: Check if the source needs updating or the extraction failed

Always explain what was found and what the fix is before applying it.

### Step 6: Rebuild with --force (verification only — the one step that rebuilds)

This is the only step that runs a build, and only *after* a fix is applied — it
verifies the fix, it is not how you reproduce or investigate the failure. Target
the same distro whose log you diagnosed so the rebuild lands in the right tree:

```bash
yoe build --force --distro <distro> <unit>
```

Use `--force` (not `--clean`) to skip the cache but preserve the source tree.
Use `--clean` only if the source tree itself is corrupted or needs a fresh
extract.

### Step 7: Check the Result

Read the build output. If the build succeeds, report the fix. If it fails again,
go back to Step 1 with the new log — the next error may be different (e.g.,
fixing a missing header reveals a missing library).

## Iteration Rules

- **Maximum 5 iterations** before stopping to reassess with the user. If a unit
  fails 5 times with different errors, there may be a deeper issue (wrong source
  version, fundamentally incompatible configuration).
- **Never apply the same fix twice.** If an attempted fix didn't resolve the
  error, revert it and try a different approach.
- **Read the actual error, not just the exit code.** Build systems often print
  the real error hundreds of lines before the final "make: \*\*\* Error 1".
- **Check dependencies first.** Most build failures in this system are missing
  deps — a package needs a header or library that hasn't been built or isn't in
  the sysroot.

## Key Paths

| Path                                       | Contents                                       |
| ------------------------------------------ | ---------------------------------------------- |
| `build/<distro>/<unit>.<arch>/build.log`   | Full build output                              |
| `build/<distro>/<unit>.<arch>/executor.log`| Per-unit task summary: which task/unit failed  |
| `build/<distro>/<unit>.<arch>/build.json`  | Status, hash, timing, error for this unit      |
| `build/<distro>/<unit>.<arch>/src/`        | Extracted source tree                          |
| `build/<distro>/<unit>.<arch>/destdir/`    | Install staging directory                      |
| `build/<distro>/<unit>.<arch>/sysroot/`    | This unit's staged deps (headers/libs)         |
| `modules/**/units/**/<unit>.star`          | Unit definition                                |

`<distro>` is `alpine`, `debian`, etc.; the unit directory carries its arch
suffix (e.g. `openssl.x86_64`, `file.arm64`). The `sysroot/` is per-unit — it
holds exactly the deps staged for that one unit, so an empty or incomplete
sysroot is itself a diagnosis (see Step 2).

## What NOT to Do

- Do not modify files in `build/<distro>/<unit>/sysroot/` directly — it's
  populated automatically from built artifacts.
- Do not modify source files in `build/<distro>/<unit>/src/` as a permanent fix
  — changes there are lost on rebuild. Instead, create a patch in the unit.
- Do not skip the build log. Always read it before proposing a fix.
- Do not rebuild to reproduce the failure. The failed build already wrote
  `build.log`, `executor.log`, `src/`, `destdir/`, and `sysroot/` — diagnose
  from those. The only rebuild is Step 6, to verify a fix you already identified.
- Do not take shortcuts to make the build pass (e.g., disabling features,
  removing configure checks) without explaining the trade-off and getting user
  approval.
- Do not install missing tools or libraries in the Dockerfile. The container
  provides only the minimal bootstrap toolchain. If a unit needs a tool, create
  a unit for it.
