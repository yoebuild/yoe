# Naming and Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement collision detection, module priority for provides, --project
global flag, and per-project APK repo scoping.

**Architecture:** Add module tracking to Engine/Unit, validate uniqueness during
registration, add provides-override logic with module priority, parse --project
before command dispatch, and scope repo paths by project name.

**Tech Stack:** Go, Starlark

---

### Task 1: Add module tracking to Engine and Unit

**Files:**

- Modify: `internal/starlark/types.go:102-138` (Unit struct)
- Modify: `internal/starlark/engine.go:11-30` (Engine struct)

- [ ] **Step 1: Add Module and ModuleIndex fields to Unit**

In `internal/starlark/types.go`, add two fields to the `Unit` struct after the
`Provides` field:

```go
	Provides  string // virtual package name
	Module      string // module that registered this unit (empty = project root)
	ModuleIndex int    // module priority (0 = project root, 1+ = declaration order)
```

- [ ] **Step 2: Add current module tracking to Engine**

In `internal/starlark/engine.go`, add fields and a setter:

```go
type Engine struct {
	mu        sync.Mutex
	project   *Project
	machines  map[string]*Machine
	units     map[string]*Unit
	commands  map[string]*Command
	moduleInfo *ModuleInfo

	// Current module context — set by the loader before evaluating each
	// module's directories so registerUnit can tag units.
	currentModule      string
	currentModuleIndex int

	// globals stores the top-level bindings from the last ExecFile/ExecString,
	// used to retrieve the run() function for custom commands.
	globals starlark.StringDict

	// Predeclared variables set after engine creation (e.g., ARCH).
	vars map[string]starlark.Value

	// load() support
	projectRoot string
	moduleRoots map[string]string
	loadCache   *loadCache
}

// SetCurrentModule sets the module context for subsequent unit registrations.
func (e *Engine) SetCurrentModule(name string, index int) {
	e.currentModule = name
	e.currentModuleIndex = index
}
```

- [ ] **Step 3: Commit**

```bash
git add internal/starlark/types.go internal/starlark/engine.go
git commit -m "add module tracking fields to Unit and Engine"
```

---

### Task 2: Unit name duplicate detection

**Files:**

- Modify: `internal/starlark/builtins.go:506-508` (registerUnit)
- Test: `internal/starlark/engine_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/starlark/engine_test.go`:

```go
func TestEvalUnitDuplicate(t *testing.T) {
	src := `
unit(name = "foo", version = "1.0.0")
unit(name = "foo", version = "2.0.0")
`
	eng := NewEngine()
	err := eng.ExecString("units/foo.star", src)
	if err == nil {
		t.Fatal("expected error for duplicate unit name, got nil")
	}
	if !strings.Contains(err.Error(), "already defined") {
		t.Errorf("error = %q, want it to contain 'already defined'", err)
	}
}
```

Add `"strings"` to the import block in `engine_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run:
`cd /scratch4/yoe/yoe-ng && go test ./internal/starlark/ -run TestEvalUnitDuplicate -v`
Expected: FAIL — no duplicate check yet, second unit silently overwrites.

- [ ] **Step 3: Implement duplicate detection in registerUnit**

In `internal/starlark/builtins.go`, replace lines 506-508:

```go
	e.mu.Lock()
	e.units[name] = r
	e.mu.Unlock()
```

With:

```go
	r.Module = e.currentModule
	r.ModuleIndex = e.currentModuleIndex

	e.mu.Lock()
	if existing, ok := e.units[name]; ok {
		e.mu.Unlock()
		src := "project root"
		if existing.Module != "" {
			src = fmt.Sprintf("module %q", existing.Module)
		}
		return nil, fmt.Errorf("unit %q already defined (first defined in %s)", name, src)
	}
	e.units[name] = r
	e.mu.Unlock()
```

- [ ] **Step 4: Run test to verify it passes**

Run:
`cd /scratch4/yoe/yoe-ng && go test ./internal/starlark/ -run TestEvalUnitDuplicate -v`
Expected: PASS

- [ ] **Step 5: Run all starlark tests to check for regressions**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/starlark/ -v` Expected: All
PASS

- [ ] **Step 6: Commit**

```bash
git add internal/starlark/builtins.go internal/starlark/engine_test.go
git commit -m "detect duplicate unit names at evaluation time"
```

---

### Task 3: Set module context in loader

**Files:**

- Modify: `internal/starlark/loader.go:97-146` (module root registration and
  phase 1/2 evaluation)

- [ ] **Step 1: Set module context before evaluating each module's directories**

In `internal/starlark/loader.go`, the loader evaluates project root directories
and then module directories. Add `SetCurrentModule` calls.

For phase 1 (machines), before `evalDir(eng, root, "machines")` (line 137):

```go
	// Phase 1: Evaluate all machine definitions (project + modules).
	eng.SetCurrentModule("", 0)
	if err := evalDir(eng, root, "machines"); err != nil {
		return nil, err
	}
	if eng.moduleRoots != nil {
		for i, m := range eng.Project().Modules {
			name := filepath.Base(strings.TrimSuffix(m.URL, ".git"))
			if m.Path != "" {
				name = filepath.Base(m.Path)
			}
			if modulePath, ok := eng.moduleRoots[name]; ok {
				eng.SetCurrentModule(name, i+1)
				if err := evalDir(eng, modulePath, "machines"); err != nil {
					return nil, err
				}
			}
		}
	}
```

This replaces the existing phase 1 machine evaluation loop (lines 134-146).

For phase 2a (units), replace the existing unit evaluation (lines 232-242):

```go
	// Phase 2a: Evaluate all unit definitions (project + modules).
	eng.SetCurrentModule("", 0)
	if err := evalDir(eng, root, "units"); err != nil {
		return nil, err
	}
	if proj := eng.Project(); proj != nil {
		for i, m := range proj.Modules {
			name := filepath.Base(strings.TrimSuffix(m.URL, ".git"))
			if m.Path != "" {
				name = filepath.Base(m.Path)
			}
			if modulePath, ok := eng.moduleRoots[name]; ok {
				eng.SetCurrentModule(name, i+1)
				if err := evalDir(eng, modulePath, "units"); err != nil {
					return nil, err
				}
			}
		}
	}
```

For phase 2b (images), replace the existing image evaluation (lines 266-275):

```go
	// Phase 2b: Evaluate image definitions (project + modules).
	eng.SetCurrentModule("", 0)
	if err := evalDir(eng, root, "images"); err != nil {
		return nil, err
	}
	if proj := eng.Project(); proj != nil {
		for i, m := range proj.Modules {
			name := filepath.Base(strings.TrimSuffix(m.URL, ".git"))
			if m.Path != "" {
				name = filepath.Base(m.Path)
			}
			if modulePath, ok := eng.moduleRoots[name]; ok {
				eng.SetCurrentModule(name, i+1)
				if err := evalDir(eng, modulePath, "images"); err != nil {
					return nil, err
				}
			}
		}
	}
```

- [ ] **Step 2: Run all starlark tests**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/starlark/ -v` Expected: All
PASS (existing tests still work, units now have Module/ModuleIndex set)

- [ ] **Step 3: Commit**

```bash
git add internal/starlark/loader.go
git commit -m "set module context before evaluating module directories"
```

---

### Task 4: PROVIDES duplicate detection with module priority

**Files:**

- Modify: `internal/starlark/loader.go:247-253` (PROVIDES population after phase
  2a)
- Test: `internal/starlark/engine_test.go`

- [ ] **Step 1: Write test for same-module provides conflict**

Add to `internal/starlark/engine_test.go`:

```go
func TestEvalProvidesDuplicate(t *testing.T) {
	src := `
unit(name = "foo", version = "1.0.0", provides = "virtual-pkg")
unit(name = "bar", version = "1.0.0", provides = "virtual-pkg")
`
	eng := NewEngine()
	if err := eng.ExecString("units/test.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	// Both units registered — conflict is detected during PROVIDES population,
	// not during registration. We need to simulate that here.
	provides := make(map[string]string) // virtual name -> unit name
	var err error
	for _, u := range eng.Units() {
		if u.Provides == "" {
			continue
		}
		if existing, ok := provides[u.Provides]; ok {
			if u.ModuleIndex == 0 {
				// Same module — error
				err = fmt.Errorf("virtual package %q provided by both %q and %q", u.Provides, existing, u.Name)
				break
			}
		}
		provides[u.Provides] = u.Name
	}
	if err == nil {
		t.Fatal("expected error for duplicate provides in same module, got nil")
	}
	if !strings.Contains(err.Error(), "virtual package") {
		t.Errorf("error = %q, want it to contain 'virtual package'", err)
	}
}
```

Actually, the provides validation happens inside the loader, not the engine
directly. A better approach is to test via `LoadProject` with a test fixture.
Let me revise — we'll test this through the loader with a dedicated test
fixture.

- [ ] **Step 1 (revised): Create test fixture for provides conflict**

Create `testdata/provides-conflict/PROJECT.star`:

```python
project(name = "provides-conflict", version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64"),
)
```

Create `testdata/provides-conflict/machines/qemu.star`:

```python
machine(name = "qemu-x86_64", arch = "x86_64")
```

Create `testdata/provides-conflict/units/foo.star`:

```python
unit(name = "foo", version = "1.0.0", provides = "virtual-pkg")
```

Create `testdata/provides-conflict/units/bar.star`:

```python
unit(name = "bar", version = "1.0.0", provides = "virtual-pkg")
```

- [ ] **Step 2: Write the failing test**

Add to `internal/starlark/loader_test.go`:

```go
func TestLoadProject_ProvidesDuplicate(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "provides-conflict")
	_, err := LoadProject(dir)
	if err == nil {
		t.Fatal("expected error for duplicate provides in same module, got nil")
	}
	if !strings.Contains(err.Error(), "virtual package") {
		t.Errorf("error = %q, want it to contain 'virtual package'", err)
	}
}
```

Add `"strings"` to the import block in `loader_test.go`.

- [ ] **Step 3: Run test to verify it fails**

Run:
`cd /scratch4/yoe/yoe-ng && go test ./internal/starlark/ -run TestLoadProject_ProvidesDuplicate -v`
Expected: FAIL — no conflict detection yet.

- [ ] **Step 4: Implement provides conflict detection in loader**

In `internal/starlark/loader.go`, replace the PROVIDES population block (the
section starting at "Add unit provides to PROVIDES dict"):

```go
	// Add unit provides to PROVIDES dict, checking for conflicts.
	if prov, ok := eng.vars["PROVIDES"].(*starlark.Dict); ok {
		for _, u := range eng.Units() {
			if u.Provides == "" {
				continue
			}
			if existing, found, _ := prov.Get(starlark.String(u.Provides)); found {
				existingName := string(existing.(starlark.String))
				// Look up the existing unit to compare module priority.
				existingUnit := eng.Units()[existingName]
				if existingUnit == nil || u.ModuleIndex == existingUnit.ModuleIndex {
					return nil, fmt.Errorf("virtual package %q provided by both %q and %q",
						u.Provides, existingName, u.Name)
				}
				if u.ModuleIndex > existingUnit.ModuleIndex {
					fmt.Fprintf(os.Stderr, "notice: %q from module %q overrides %q via provides %q\n",
						u.Name, u.Module, existingName, u.Provides)
					_ = prov.SetKey(starlark.String(u.Provides), starlark.String(u.Name))
				}
				// If u.ModuleIndex < existingUnit.ModuleIndex, skip — higher priority already won.
				continue
			}
			_ = prov.SetKey(starlark.String(u.Provides), starlark.String(u.Name))
		}
	}
```

Add `"os"` to the import block in `loader.go` if not already present.

- [ ] **Step 5: Run test to verify it passes**

Run:
`cd /scratch4/yoe/yoe-ng && go test ./internal/starlark/ -run TestLoadProject_ProvidesDuplicate -v`
Expected: PASS

- [ ] **Step 6: Run all starlark tests**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/starlark/ -v` Expected: All
PASS

- [ ] **Step 7: Commit**

```bash
git add testdata/provides-conflict/ internal/starlark/loader.go internal/starlark/loader_test.go
git commit -m "detect PROVIDES conflicts with module priority override"
```

---

### Task 5: Module priority provides override test

**Files:**

- Create: `testdata/provides-override/` (test fixture with two modules)
- Test: `internal/starlark/loader_test.go`

- [ ] **Step 1: Create test fixture**

Create `testdata/provides-override/PROJECT.star`:

```python
project(name = "provides-override", version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64"),
    modules = [
        module("https://example.com/base.git", local = "modules/base"),
        module("https://example.com/override.git", local = "modules/override"),
    ],
)
```

Create `testdata/provides-override/machines/qemu.star`:

```python
machine(name = "qemu-x86_64", arch = "x86_64")
```

Create `testdata/provides-override/modules/base/MODULE.star`:

```python
module_info(name = "base")
```

Create `testdata/provides-override/modules/base/units/base-files.star`:

```python
unit(name = "base-files", version = "1.0.0")
```

Create `testdata/provides-override/modules/override/MODULE.star`:

```python
module_info(name = "override")
```

Create
`testdata/provides-override/modules/override/units/base-files-custom.star`:

```python
unit(name = "base-files-custom", version = "1.0.0", provides = "base-files")
```

- [ ] **Step 2: Write the test**

Add to `internal/starlark/loader_test.go`:

```go
func TestLoadProject_ProvidesOverride(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "provides-override")
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	// Both units should exist
	if _, ok := proj.Units["base-files"]; !ok {
		t.Error("expected unit 'base-files'")
	}
	if _, ok := proj.Units["base-files-custom"]; !ok {
		t.Error("expected unit 'base-files-custom'")
	}
	// base-files-custom should have higher module index than base-files
	bf := proj.Units["base-files"]
	bfc := proj.Units["base-files-custom"]
	if bfc.ModuleIndex <= bf.ModuleIndex {
		t.Errorf("base-files-custom ModuleIndex=%d should be > base-files ModuleIndex=%d",
			bfc.ModuleIndex, bf.ModuleIndex)
	}
}
```

- [ ] **Step 3: Run test**

Run:
`cd /scratch4/yoe/yoe-ng && go test ./internal/starlark/ -run TestLoadProject_ProvidesOverride -v`
Expected: PASS (notice printed to stderr about the override)

- [ ] **Step 4: Commit**

```bash
git add testdata/provides-override/ internal/starlark/loader_test.go
git commit -m "add test for module priority provides override"
```

---

### Task 6: --project global flag

**Files:**

- Modify: `internal/starlark/loader.go` (add WithProjectFile option)
- Modify: `cmd/yoe/main.go` (parse --project, pass to loader)

- [ ] **Step 1: Add WithProjectFile LoadOption**

In `internal/starlark/loader.go`, add the option to the `loadConfig` struct and
a constructor:

```go
type loadConfig struct {
	moduleSync  func([]ModuleRef, io.Writer) error
	machine     string // override default machine before evaluating units/images
	projectFile string // alternative project file (instead of PROJECT.star)
}

// WithProjectFile specifies an alternative project file to evaluate instead
// of PROJECT.star at the project root.
func WithProjectFile(path string) LoadOption {
	return func(c *loadConfig) { c.projectFile = path }
}
```

- [ ] **Step 2: Use projectFile in LoadProjectFromRoot**

In `LoadProjectFromRoot`, replace the PROJECT.star evaluation (lines 82-85):

```go
	// Evaluate project file (PROJECT.star or --project override)
	projFile := filepath.Join(root, "PROJECT.star")
	if cfg.projectFile != "" {
		projFile = cfg.projectFile
		if !filepath.IsAbs(projFile) {
			projFile = filepath.Join(root, projFile)
		}
	}
	if err := eng.ExecFile(projFile); err != nil {
		return nil, fmt.Errorf("evaluating %s: %w", projFile, err)
	}
```

- [ ] **Step 3: Parse --project in main.go**

In `cmd/yoe/main.go`, add a package-level variable and parse `--project` before
command dispatch. Replace the `main()` function:

```go
var globalProjectFile string

func main() {
	// Parse global flags before command dispatch
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--project" && i+1 < len(args) {
			globalProjectFile = args[i+1]
			args = append(args[:i], args[i+2:]...)
			break
		}
	}

	if len(args) < 1 {
		cmdTUI(nil)
		return
	}

	command := args[0]
	cmdArgs := args[1:]

	switch command {
	case "version":
		fmt.Println(version)
	case "update":
		cmdUpdate()
	case "init":
		cmdInit(cmdArgs)
	case "container":
		cmdContainer(cmdArgs)
	case "module":
		cmdModule(cmdArgs)
	case "build":
		cmdBuild(cmdArgs)
	case "bootstrap":
		cmdBootstrap(cmdArgs)
	case "image":
		cmdImage(cmdArgs)
	case "list":
		cmdList(cmdArgs)
	case "flash":
		cmdFlash(cmdArgs)
	case "run":
		cmdRun(cmdArgs)
	case "dev":
		cmdDev(cmdArgs)
	case "repo":
		cmdRepo(cmdArgs)
	default:
		cmdCustom(command, cmdArgs)
	}
}
```

Note: Read the existing `main()` switch statement carefully before applying
this. Make sure all cases are preserved exactly as they exist.

- [ ] **Step 4: Pass globalProjectFile to loadProject**

In `cmd/yoe/main.go`, update `loadProjectWithMachine`:

```go
func loadProjectWithMachine(machineName string) *yoestar.Project {
	dir := os.Getenv("YOE_PROJECT")
	if dir == "" {
		dir = "."
	}
	opts := []yoestar.LoadOption{
		yoestar.WithModuleSync(module.SyncIfNeeded),
	}
	if machineName != "" {
		opts = append(opts, yoestar.WithMachine(machineName))
	}
	if globalProjectFile != "" {
		opts = append(opts, yoestar.WithProjectFile(globalProjectFile))
	}
	proj, err := yoestar.LoadProject(dir, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return proj
}
```

- [ ] **Step 5: Run all tests**

Run: `cd /scratch4/yoe/yoe-ng && go test ./... 2>&1 | tail -20` Expected: All
PASS

- [ ] **Step 6: Commit**

```bash
git add internal/starlark/loader.go cmd/yoe/main.go
git commit -m "add --project global flag for alternate project files"
```

---

### Task 7: Per-project APK repo scoping

**Files:**

- Modify: `internal/repo/local.go:18-22` (RepoDir)

- [ ] **Step 1: Write the failing test**

Create `internal/repo/local_test.go`:

```go
package repo

import (
	"path/filepath"
	"testing"

	yoestar "github.com/YoeDistro/yoe-ng/internal/starlark"
)

func TestRepoDir_IncludesProjectName(t *testing.T) {
	proj := &yoestar.Project{Name: "my-product"}
	got := RepoDir(proj, "/home/user/project")
	want := filepath.Join("/home/user/project", "repo", "my-product")
	if got != want {
		t.Errorf("RepoDir = %q, want %q", got, want)
	}
}

func TestRepoDir_ExplicitPath(t *testing.T) {
	proj := &yoestar.Project{
		Name:       "my-product",
		Repository: yoestar.RepositoryConfig{Path: "/custom/repo"},
	}
	got := RepoDir(proj, "/home/user/project")
	want := "/custom/repo"
	if got != want {
		t.Errorf("RepoDir = %q, want %q", got, want)
	}
}

func TestRepoDir_NilProject(t *testing.T) {
	got := RepoDir(nil, "/home/user/project")
	want := filepath.Join("/home/user/project", "repo")
	if got != want {
		t.Errorf("RepoDir = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/repo/ -run TestRepoDir -v`
Expected: FAIL — `TestRepoDir_IncludesProjectName` fails (returns
`/home/user/project/repo` without project name).

- [ ] **Step 3: Update RepoDir**

In `internal/repo/local.go`, replace the `RepoDir` function:

```go
// RepoDir returns the local package repository path for a project.
// Repos are scoped per project: repo/<project-name>/. This prevents stale
// packages from one project contaminating another's APKINDEX.
func RepoDir(proj *yoestar.Project, projectDir string) string {
	if proj != nil && proj.Repository.Path != "" {
		return proj.Repository.Path
	}
	if proj != nil && proj.Name != "" {
		return filepath.Join(projectDir, "repo", proj.Name)
	}
	return filepath.Join(projectDir, "repo")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /scratch4/yoe/yoe-ng && go test ./internal/repo/ -run TestRepoDir -v`
Expected: All PASS

- [ ] **Step 5: Fix callers that pass nil**

There is one caller that passes `nil` for proj: `internal/build/executor.go:322`
— `repo.RepoDir(nil, opts.ProjectDir)`. This is in the build executor where proj
isn't available. This call should be updated to pass the project. Check the
context:

Read `internal/build/executor.go` around line 322 to understand what project is
available. If a project is available in the function scope, pass it. If not, the
nil fallback (returns `repo/` without project name) is acceptable for now — it's
only used in a context where the project-scoped path isn't critical.

- [ ] **Step 6: Run all tests**

Run: `cd /scratch4/yoe/yoe-ng && go test ./... 2>&1 | tail -20` Expected: All
PASS

- [ ] **Step 7: Commit**

```bash
git add internal/repo/local.go internal/repo/local_test.go
git commit -m "scope APK repo directory per project name"
```

---

### Task 8: Update documentation

**Files:**

- Modify: `docs/naming-and-resolution.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update naming-and-resolution.md**

The "Open Issues" section should be rewritten. The "Unit replacement via
provides" and "Projects as module scoping" and "APK repo scoping per project"
sections should be updated to reflect implemented behavior rather than open
questions.

Key changes:

1. In "Collision Detection" section, add text confirming duplicate unit names
   error at evaluation time.
2. In "Collision Detection" > "PROVIDES duplicates", update to describe module
   priority behavior: same module = error, different module = higher priority
   wins with notice.
3. In "Unit replacement via provides" (currently under Open Issues), move to a
   subsection under "Virtual packages (PROVIDES)" and describe the implemented
   module priority approach. Remove "open issue" framing.
4. In "Projects as module scoping", add the `--project` flag:
   `yoe build --project projects/customer-a.star`.
5. In "APK repo scoping per project", update to show `repo/<project>/` is the
   implemented path. Remove "open issue" framing.
6. Remove the "Open Issues" header entirely if all items are resolved.

- [ ] **Step 2: Update CHANGELOG.md**

Add under `## [Unreleased]`:

```markdown
## [Unreleased]

- **Unit name collision detection** — duplicate unit names now error at
  evaluation time with a clear message showing which module first defined the
  unit.
- **PROVIDES collision detection** — two units providing the same virtual name
  in the same module now error. Units from higher-priority modules (later in the
  module list) override lower-priority ones with a notice.
- **`--project` flag** — `yoe build --project projects/customer-a.star` selects
  an alternate project file. Available on all subcommands.
- **Per-project APK repo** — package repositories are now scoped per project
  name (`repo/<project>/`) to prevent stale packages across project switches.
```

- [ ] **Step 3: Format markdown**

Run: `cd /scratch4/yoe/yoe-ng && source envsetup.sh && yoe_format`

- [ ] **Step 4: Commit**

```bash
git add docs/naming-and-resolution.md CHANGELOG.md
git commit -m "update docs for naming-and-resolution implementation"
```
