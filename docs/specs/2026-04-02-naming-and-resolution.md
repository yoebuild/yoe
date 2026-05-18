# Naming and Resolution Implementation

Implements the remaining unimplemented features from
[naming-and-resolution.md](../../naming-and-resolution.md): collision detection,
module priority, `--project` flag, and per-project APK repo scoping.

## 1. Unit name duplicate detection

**File:** `internal/starlark/builtins.go` — `registerUnit()`

Currently `e.units[name] = r` silently overwrites. Add a check before the
assignment: if the name already exists, return an error.

Changes:

- Add `Module string` and `ModuleIndex int` fields to the `Unit` struct in
  `types.go`. `Module` is the module name (empty string for project root).
  `ModuleIndex` is the module's position in the project's module list (0 =
  project root, 1+ = modules in declaration order).
- The Engine tracks the current module context during evaluation — set via a new
  `SetCurrentModule(name string, index int)` method called from the loader
  before evaluating each module's directories.
- In `registerUnit()`, before `e.units[name] = r`, set `r.Module` and
  `r.ModuleIndex` from the engine's current context, then check:
  ```go
  if existing, ok := e.units[name]; ok {
      return nil, fmt.Errorf("unit %q already defined (first defined in module %q)", name, existing.Module)
  }
  ```

Error message when module is empty string:
`"unit %q already defined (first defined in project root)"`.

## 2. PROVIDES duplicate detection

**File:** `internal/starlark/loader.go` — after phase 2a (~line 248)

When populating the PROVIDES dict from unit provides, check for conflicts:

```go
for _, u := range eng.Units() {
    if u.Provides == "" {
        continue
    }
    if existing, found, _ := prov.Get(starlark.String(u.Provides)); found {
        existingName := string(existing.(starlark.String))
        // Check module priority — see section 3
    }
    _ = prov.SetKey(starlark.String(u.Provides), starlark.String(u.Name))
}
```

If two units from the **same module** (same `ModuleIndex`) provide the same
virtual name, error:

```
virtual package %q provided by both %q and %q
```

## 3. Module priority for provides override

Module priority follows declaration order — later modules have higher priority
(last wins). When a provides conflict is detected between units from different
modules:

- If the new unit's `ModuleIndex` > existing unit's `ModuleIndex`, emit a notice
  and allow the override:
  ```
  notice: %q from module %q overrides %q via provides %q
  ```
- If the new unit's `ModuleIndex` < existing unit's `ModuleIndex`, skip it (the
  higher-priority module already won).
- If same `ModuleIndex`, error (duplicate within same module).

**Provides overriding a real unit name:** When unit `openssh-vendor` declares
`provides = "openssh"` and a real unit named `openssh` exists, the provides from
a higher-priority module wins. The DAG must resolve names through PROVIDES
before falling back to the unit map. This is already handled by the image class
(`PROVIDES.get(a, None)` in `image.star`) — the provides dict takes precedence.

The shadowed real unit remains registered but is unreachable via the virtual
name. It can still be referenced by its real name if needed.

**Notice output:** Use `fmt.Fprintf(os.Stderr, ...)` for notices, consistent
with other loader diagnostics.

## 4. `--project` global flag

**File:** `cmd/yoe/main.go`

Parse `--project <path>` before command dispatch. Store it in a package-level
variable. All commands that call `LoadProject` / `LoadProjectFromRoot` pass it
through.

Changes:

- Add `WithProjectFile(path string) LoadOption` to
  `internal/starlark/loader.go`. When set, `LoadProjectFromRoot` evaluates the
  specified file instead of `PROJECT.star` at root. The project root remains the
  repo root — only the project definition file changes.
- In `main.go`, parse `--project` from `os.Args` before the command switch.
  Strip it from `args` passed to subcommands.
- `findProjectRoot` behavior unchanged — it still finds the repo root by walking
  up to `PROJECT.star`. The `--project` flag only changes which `.star` file
  defines the project.
- When `--project` is set, the specified file must exist and must call
  `project()`. Error if it doesn't.

## 5. Per-project APK repo scoping

**File:** `internal/repo/local.go` — `RepoDir()`

Change from:

```go
return filepath.Join(projectDir, "repo")
```

To:

```go
return filepath.Join(projectDir, "repo", proj.Name)
```

The project name comes from `project(name = "...")` in the project file. All
callers already pass `proj` — this is signature-compatible.

When `proj.Repository.Path` is explicitly set, use that path as-is (no project
name appended) — the user has opted into manual repo management.

## 6. Documentation updates

Update `docs/naming-and-resolution.md`:

- Move collision detection details from "Open Issues" into the main "Collision
  Detection" section, marking them as implemented behavior rather than open
  questions.
- Move module priority / provides override from "Open Issues" into a new
  subsection under "Virtual packages (PROVIDES)".
- Add the `--project` flag to the "Projects as module scoping" section.
- Add per-project repo path to the "APK repo scoping per project" section.
- Remove the "Open Issues" header if all items are resolved, or keep it with
  only genuinely open items.

Update `CHANGELOG.md` with these features.

## Testing

- Unit test in `builtins_test.go`: register two units with same name, expect
  error.
- Unit test in `loader_test.go`: two units providing same virtual name from same
  module, expect error. Two units from different modules with provides override,
  expect notice + correct resolution.
- Unit test in `repo/local_test.go`: verify `RepoDir` includes project name.
- Integration: existing `testdata/e2e-project` continues to work (no
  regressions).
