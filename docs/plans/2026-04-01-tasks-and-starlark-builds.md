# Tasks, Starlark Build Functions, and Machine-Portable Images

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `build = [...]` with `tasks = [...]`, add `run()` builtin for
Starlark build functions, add `MACHINE_CONFIG`/`PROVIDES` for machine-portable
images, and move image assembly from Go to Starlark.

**Architecture:** Tasks are named build phases with steps that can be shell
strings or Starlark callables. The `run()` builtin executes commands during
build time via thread-local sandbox config. Machine definitions carry packages
and partitions that get merged into images via `MACHINE_CONFIG` and `PROVIDES`
Starlark variables.

**Tech Stack:** Go, go.starlark.net, Docker/bwrap

**Spec:**
`docs/superpowers/specs/2026-04-01-tasks-and-starlark-builds-design.md`

**No backward compatibility.** The `build` field is removed. All units migrate
to `tasks`.

---

### Task 1: Add Task/Step Types and `task()` Builtin

Add the Go types for Task and Step, add the `task()` Starlark builtin, and parse
`tasks` in `registerUnit()`. Remove the `build` field from Unit.

**Files:**

- Modify: `internal/starlark/types.go`
- Modify: `internal/starlark/builtins.go`
- Modify: `internal/resolve/hash.go`

- [ ] **Step 1: Add Step and Task types to types.go**

In `internal/starlark/types.go`, add after the `Partition` struct:

```go
// Step is a single build action — either a shell command or a Starlark function.
type Step struct {
	Command string            // shell command (set when step is a string)
	Fn      starlark.Callable // Starlark function (set when step is callable)
}

// Task is a named build phase containing one or more steps.
type Task struct {
	Name      string
	Container string // optional container image override
	Steps     []Step
}
```

Add import for `"go.starlark.net/starlark"` at the top of types.go.

Remove the `Build` field and `ConfigureArgs` and `GoPackage` fields from Unit
(classes will handle these internally). Add `Tasks`, `Container`, and
`Provides`:

```go
type Unit struct {
	Name        string
	Version     string
	Class       string // "unit", "image", etc.
	Scope       string // "arch" (default), "machine", or "noarch"
	Description string
	License     string

	// Source
	Source  string
	SHA256  string
	Tag     string
	Branch  string
	Patches []string

	// Dependencies
	Deps        []string
	RuntimeDeps []string

	// Build
	Container string // default container for all tasks
	Tasks     []Task
	Provides  string // virtual package name

	// Artifact metadata
	Services    []string
	Conffiles   []string
	Environment map[string]string

	// Image-specific
	Artifacts  []string
	Exclude    []string
	Hostname   string
	Timezone   string
	Locale     string
	Partitions []Partition
}
```

- [ ] **Step 2: Add `task()` builtin to builtins.go**

Add `task()` to the builtins dict:

```go
"task": starlark.NewBuiltin("task", fnTask),
```

Implement `fnTask`. A task takes a positional name arg and keyword args `run`,
`fn`, `steps`, and `container`:

```go
func fnTask(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name starlark.String
	if err := starlark.UnpackPositionalArgs("task", args, nil, 1, &name); err != nil {
		return nil, err
	}

	t := &starlarkstruct.Struct{}
	fields := starlark.StringDict{
		"name": name,
	}

	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		fields[key] = kv[1]
	}

	return starlarkstruct.FromStringDict(starlark.String("task"), fields), nil
}
```

- [ ] **Step 3: Parse `tasks` and `provides` in registerUnit()**

In `registerUnit()`, replace parsing of `Build`, `ConfigureArgs`, `GoPackage`
with parsing of `Tasks`, `Container`, and `Provides`:

```go
r := &Unit{
	Name:        name,
	Version:     kwString(kwargs, "version"),
	Class:       class,
	Scope:       kwString(kwargs, "scope"),
	Description: kwString(kwargs, "description"),
	License:     kwString(kwargs, "license"),
	Source:      kwString(kwargs, "source"),
	SHA256:      kwString(kwargs, "sha256"),
	Tag:         kwString(kwargs, "tag"),
	Branch:      kwString(kwargs, "branch"),
	Patches:     kwStringList(kwargs, "patches"),
	Deps:        kwStringList(kwargs, "deps"),
	RuntimeDeps: kwStringList(kwargs, "runtime_deps"),
	Container:   kwString(kwargs, "container"),
	Provides:    kwString(kwargs, "provides"),
	Services:    kwStringList(kwargs, "services"),
	Conffiles:   kwStringList(kwargs, "conffiles"),
	Artifacts:   kwStringList(kwargs, "artifacts"),
	Exclude:     kwStringList(kwargs, "exclude"),
	Hostname:    kwString(kwargs, "hostname"),
	Timezone:    kwString(kwargs, "timezone"),
	Locale:      kwString(kwargs, "locale"),
}
```

Parse `tasks` kwarg as a list of task structs. Each task struct has fields
`name`, `run`, `fn`, `steps`, `container`. Convert `run`/`fn`/`steps` into a
`[]Step` slice:

```go
// Parse tasks
for _, kv := range kwargs {
	if string(kv[0].(starlark.String)) == "tasks" {
		if list, ok := kv[1].(*starlark.List); ok {
			iter := list.Iterate()
			defer iter.Done()
			var v starlark.Value
			for iter.Next(&v) {
				s, ok := v.(*starlarkstruct.Struct)
				if !ok {
					continue
				}
				t := Task{
					Name:      structString(s, "name"),
					Container: structString(s, "container"),
				}
				// Parse steps: run (single string), fn (single callable),
				// or steps (list of strings/callables)
				if rv, err := s.Attr("run"); err == nil {
					if cmd, ok := rv.(starlark.String); ok {
						t.Steps = []Step{{Command: string(cmd)}}
					}
				}
				if rv, err := s.Attr("fn"); err == nil {
					if fn, ok := rv.(starlark.Callable); ok {
						t.Steps = []Step{{Fn: fn}}
					}
				}
				if rv, err := s.Attr("steps"); err == nil {
					if list, ok := rv.(*starlark.List); ok {
						si := list.Iterate()
						var sv starlark.Value
						for si.Next(&sv) {
							switch val := sv.(type) {
							case starlark.String:
								t.Steps = append(t.Steps, Step{Command: string(val)})
							case starlark.Callable:
								t.Steps = append(t.Steps, Step{Fn: val})
							}
						}
						si.Done()
					}
				}
				r.Tasks = append(r.Tasks, t)
			}
		}
	}
}
```

- [ ] **Step 4: Remove fnUnit build validation, remove
      fnAutotools/fnCMake/fnGoBinary**

The `fnUnit` function currently requires `len(r.Build) == 0`. Remove this check
— units with tasks don't need the old build field. Also remove `fnAutotools`,
`fnCMake`, `fnGoBinary` from builtins.go — these will be pure Starlark classes
that call `unit()` with `tasks`.

Keep `fnImage` for now (it will be migrated to Starlark in Task 5).

Remove the corresponding entries from the `builtins()` dict:

```go
// Remove these:
// "autotools":   starlark.NewBuiltin("autotools", e.fnAutotools),
// "cmake":       starlark.NewBuiltin("cmake", e.fnCMake),
// "go_binary":   starlark.NewBuiltin("go_binary", e.fnGoBinary),
```

- [ ] **Step 5: Update UnitHash to include tasks**

In `internal/resolve/hash.go`, replace the build/configureArgs/goPackage hash
lines with tasks:

```go
// Tasks
for _, t := range unit.Tasks {
	fmt.Fprintf(h, "task:%s:%s\n", t.Name, t.Container)
	for _, s := range t.Steps {
		if s.Command != "" {
			fmt.Fprintf(h, "step:cmd:%s\n", s.Command)
		}
		if s.Fn != nil {
			fmt.Fprintf(h, "step:fn:%s\n", s.Fn.Name())
		}
	}
}
fmt.Fprintf(h, "container:%s\n", unit.Container)
fmt.Fprintf(h, "provides:%s\n", unit.Provides)
```

- [ ] **Step 6: Remove buildCommands() from executor.go**

Delete the `buildCommands()` function (lines 305-348) — classes now generate
tasks in Starlark, not in Go.

- [ ] **Step 7: Build and test**

Run: `go build ./...` Run: `go test ./internal/starlark/ -v` Run:
`go test ./internal/resolve/ -v`

Fix any compilation errors.

- [ ] **Step 8: Commit**

```bash
git add internal/starlark/ internal/resolve/ internal/build/
git commit -m "add Task/Step types, task() builtin, remove build field

Tasks replace build=[...] as the unit build mechanism. Each task has a
name, optional container, and steps that are shell strings or Starlark
callables. Removes autotools/cmake/go_binary Go builtins — these move
to pure Starlark classes."
```

---

### Task 2: Migrate Classes to Starlark Tasks

Rewrite autotools, cmake, go classes to generate `tasks` instead of `build`
steps. Migrate all unit .star files.

**Files:**

- Modify: `modules/module-core/classes/autotools.star`
- Modify: `modules/module-core/classes/cmake.star`
- Modify: `modules/module-core/classes/go.star`
- Modify: all `modules/module-core/units/**/*.star`
- Modify: all `modules/module-rpi/units/**/*.star`

- [ ] **Step 1: Rewrite autotools.star**

```python
def autotools(name, version, source, sha256="", deps=[], runtime_deps=[],
              configure_args=[], patches=[], services=[], conffiles=[],
              license="", description="", tasks=[], scope="", **kwargs):
    if not tasks:
        tasks = [
            task("build", steps=[
                "test -f configure || autoreconf -fi",
                "./configure --prefix=$PREFIX " + " ".join(configure_args),
                "make -j$NPROC ACLOCAL=true AUTOCONF=true AUTOMAKE=true AUTOHEADER=true MAKEINFO=true",
                "make DESTDIR=$DESTDIR install ACLOCAL=true AUTOCONF=true AUTOMAKE=true AUTOHEADER=true MAKEINFO=true",
            ]),
        ]
    unit(
        name=name, version=version, source=source, sha256=sha256,
        deps=deps, runtime_deps=runtime_deps, patches=patches,
        tasks=tasks, services=services, conffiles=conffiles,
        license=license, description=description, scope=scope,
        **kwargs,
    )
```

- [ ] **Step 2: Rewrite cmake.star**

```python
def cmake(name, version, source, sha256="", deps=[], runtime_deps=[],
          cmake_args=[], patches=[], services=[], conffiles=[],
          license="", description="", tasks=[], scope="", **kwargs):
    if not tasks:
        tasks = [
            task("build", steps=[
                "cmake -B build -DCMAKE_INSTALL_PREFIX=$PREFIX "
                + "-DCMAKE_BUILD_TYPE=Release " + " ".join(cmake_args),
                "cmake --build build -j $NPROC",
                "DESTDIR=$DESTDIR cmake --install build",
            ]),
        ]
    unit(
        name=name, version=version, source=source, sha256=sha256,
        deps=deps, runtime_deps=runtime_deps, patches=patches,
        tasks=tasks, services=services, conffiles=conffiles,
        license=license, description=description, scope=scope,
        **kwargs,
    )
```

- [ ] **Step 3: Rewrite go.star**

```python
def go_binary(name, version, source, sha256="", deps=[], runtime_deps=[],
              go_package="", patches=[], services=[], conffiles=[],
              license="", description="", tasks=[], scope="",
              container="", **kwargs):
    if not go_package:
        go_package = "./cmd/" + name
    if not tasks:
        tasks = [
            task("build",
                 run="CGO_ENABLED=0 go build -o $DESTDIR$PREFIX/bin/" + name + " " + go_package),
        ]
    unit(
        name=name, version=version, source=source, sha256=sha256,
        deps=deps, runtime_deps=runtime_deps, patches=patches,
        tasks=tasks, services=services, conffiles=conffiles,
        license=license, description=description, scope=scope,
        container=container,
        **kwargs,
    )
```

- [ ] **Step 4: Migrate all unit .star files to use tasks**

Each unit that uses explicit `build = [...]` needs to change to
`tasks = [task("build", steps=[...])]`.

Example — `busybox.star`:

```python
unit(
    name = "busybox",
    version = "1.36.1",
    source = "https://github.com/mirror/busybox.git",
    tag = "1_36_1",
    license = "GPL-2.0",
    description = "Swiss army knife of embedded Linux",
    tasks = [
        task("build", steps=[
            "make defconfig",
            "sed -i 's/# CONFIG_STATIC is not set/CONFIG_STATIC=y/' .config",
            "make -j$NPROC",
            "make CONFIG_PREFIX=$DESTDIR install",
        ]),
    ],
)
```

Do this for every unit that uses `build = [...]`:

- `busybox.star`, `linux.star`, `musl.star`, `base-files.star`
- `syslinux.star`, `network-config.star`
- `linux-rpi4.star`, `linux-rpi5.star`, `rpi-firmware.star`
- `rpi4-config.star`, `rpi5-config.star`

Units using class functions (autotools, cmake) don't need changes — the class
generates tasks internally.

- [ ] **Step 5: Build and test**

Run: `go build ./...` Run: `go test ./...`

- [ ] **Step 6: Commit**

```bash
git add modules/ internal/
git commit -m "migrate all classes and units to tasks

autotools, cmake, go classes generate tasks instead of build steps.
All unit .star files migrated from build=[...] to tasks=[task(...)]."
```

---

### Task 3: Implement `run()` Builtin and Build-Time Starlark Execution

Add the `run()` builtin that executes commands during build time (not eval
time). Update the executor to dispatch task steps.

**Files:**

- Create: `internal/build/starlark_exec.go`
- Modify: `internal/build/executor.go`
- Modify: `internal/build/sandbox.go`

- [ ] **Step 1: Create starlark_exec.go with Execer interface and run() impl**

```go
package build

import (
	"context"
	"fmt"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// Execer abstracts command execution for testability.
type Execer interface {
	Run(ctx context.Context, cfg *SandboxConfig, command string) (ExecResult, error)
}

type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// RealExecer runs commands via RunInSandbox.
type RealExecer struct{}

func (RealExecer) Run(ctx context.Context, cfg *SandboxConfig, command string) (ExecResult, error) {
	cfg.Ctx = ctx
	err := RunInSandbox(cfg, command)
	if err != nil {
		return ExecResult{ExitCode: 1, Stderr: err.Error()}, err
	}
	return ExecResult{ExitCode: 0}, nil
}

// Thread-local keys for build-time Starlark execution.
type sandboxKeyType struct{}
type execerKeyType struct{}
type contextKeyType struct{}

var sandboxKey = sandboxKeyType{}
var execerKey = execerKeyType{}
var contextKey = contextKeyType{}

// NewBuildThread creates a Starlark thread with run() available.
func NewBuildThread(ctx context.Context, cfg *SandboxConfig, execer Execer) *starlark.Thread {
	t := &starlark.Thread{Name: "build"}
	t.SetLocal(sandboxKey, cfg)
	t.SetLocal(execerKey, execer)
	t.SetLocal(contextKey, ctx)
	return t
}

// BuildPredeclared returns the predeclared names available during build execution.
func BuildPredeclared() starlark.StringDict {
	return starlark.StringDict{
		"run": starlark.NewBuiltin("run", fnRun),
	}
}

// fnRun implements the run() builtin for build-time Starlark execution.
func fnRun(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var command starlark.String
	if err := starlark.UnpackPositionalArgs("run", args, nil, 1, &command); err != nil {
		return nil, err
	}

	check := true
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == "check" {
			if b, ok := kv[1].(starlark.Bool); ok {
				check = bool(b)
			}
		}
	}

	cfg := thread.Local(sandboxKey).(*SandboxConfig)
	execer := thread.Local(execerKey).(Execer)
	ctx := thread.Local(contextKey).(context.Context)

	if cfg == nil || execer == nil {
		return nil, fmt.Errorf("run() is only available during build execution")
	}

	result, err := execer.Run(ctx, cfg, string(command))

	// Build result struct
	resultStruct := starlarkstruct.FromStringDict(starlark.String("result"), starlark.StringDict{
		"exit_code": starlark.MakeInt(result.ExitCode),
		"stdout":    starlark.String(result.Stdout),
		"stderr":    starlark.String(result.Stderr),
	})

	if err != nil && check {
		return nil, fmt.Errorf("run(%q) failed: exit code %d\n%s",
			string(command), result.ExitCode, result.Stderr)
	}

	return resultStruct, nil
}
```

- [ ] **Step 2: Update executor.go buildOne() to dispatch tasks**

Replace the command iteration loop in `buildOne()` with task dispatch. The key
change is in the section after source preparation and sysroot assembly.

Replace the current command loop (the `for i, cmd := range commands` loop) with:

```go
// Execute tasks
for _, t := range unit.Tasks {
	fmt.Fprintf(logW, "  task: %s (%d steps)\n", t.Name, len(t.Steps))

	for i, step := range t.Steps {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("build cancelled")
		}

		if step.Command != "" {
			fmt.Fprintf(logW, "    [%d/%d] %s\n", i+1, len(t.Steps), step.Command)
			cfg := &SandboxConfig{
				Ctx:        ctx,
				Arch:       opts.Arch,
				SrcDir:     srcDir,
				DestDir:    destDir,
				Sysroot:    sysroot,
				Env:        env,
				ProjectDir: opts.ProjectDir,
				Stdout:     logW,
				Stderr:     logW,
			}
			if err := RunInSandbox(cfg, step.Command); err != nil {
				if !opts.Verbose {
					fmt.Fprintf(w, "  build log: %s\n", logPath)
				}
				return err
			}
		} else if step.Fn != nil {
			fmt.Fprintf(logW, "    [%d/%d] fn: %s\n", i+1, len(t.Steps), step.Fn.Name())
			cfg := &SandboxConfig{
				Ctx:        ctx,
				Arch:       opts.Arch,
				SrcDir:     srcDir,
				DestDir:    destDir,
				Sysroot:    sysroot,
				Env:        env,
				ProjectDir: opts.ProjectDir,
				Stdout:     logW,
				Stderr:     logW,
			}
			thread := NewBuildThread(ctx, cfg, RealExecer{})
			// Merge build predeclared into thread
			thread.Load = func(_ *starlark.Thread, module string) (starlark.StringDict, error) {
				return nil, fmt.Errorf("load() not available during build")
			}
			if _, err := starlark.Call(thread, step.Fn, nil, nil); err != nil {
				if !opts.Verbose {
					fmt.Fprintf(w, "  build log: %s\n", logPath)
				}
				return fmt.Errorf("task %s: %w", t.Name, err)
			}
		}
	}
}
```

Also remove the image special-case at the top of `buildOne()` — images will use
tasks like everything else once the image class is Starlark (Task 5). For now,
keep it but also allow images with tasks to use the task path.

- [ ] **Step 3: Build and test**

Run: `go build ./...` Run: `go test ./internal/build/ -v`

- [ ] **Step 4: Commit**

```bash
git add internal/build/
git commit -m "implement run() builtin and task-based build execution

run() executes shell commands during Starlark build functions via
thread-local sandbox config. Executor dispatches task steps as either
shell commands or Starlark callables. Execer interface enables testing."
```

---

### Task 4: Add MACHINE_CONFIG, PROVIDES, Machine Packages/Partitions

Add machine-level `packages` and `partitions` fields, expose them as
`MACHINE_CONFIG` Starlark struct, and build `PROVIDES` dict from units.

**Files:**

- Modify: `internal/starlark/types.go` (Machine, KernelConfig)
- Modify: `internal/starlark/builtins.go` (parse machine packages/partitions,
  kernel provides)
- Modify: `internal/starlark/loader.go` (set MACHINE_CONFIG and PROVIDES)

- [ ] **Step 1: Add Packages, Partitions to Machine and Provides to
      KernelConfig**

In `internal/starlark/types.go`:

```go
type Machine struct {
	Name        string
	Arch        string
	Description string
	Kernel      KernelConfig
	Bootloader  BootloaderConfig
	QEMU        *QEMUConfig
	Packages    []string    // packages added to every image for this machine
	Partitions  []Partition // default partition layout for images
}

type KernelConfig struct {
	Repo        string
	Branch      string
	Tag         string
	Defconfig   string
	DeviceTrees []string
	Unit        string
	Cmdline     string
	Provides    string // virtual package name (e.g., "linux")
}
```

- [ ] **Step 2: Parse packages, partitions, provides in builtins.go**

In `fnMachine`, parse the new `packages` and `partitions` kwargs. The
`partitions` parsing is the same pattern already used in `registerUnit()` —
extract it into a helper `parsePartitions()` and reuse.

In `fnKernel`, add `provides` parsing:

```go
func fnKernel(...) {
	// ... existing parsing ...
	provides := kwString(kwargs, "provides")
	// Add to kernel struct
}
```

- [ ] **Step 3: Set MACHINE_CONFIG and PROVIDES in loader.go**

After setting `ARCH` and `MACHINE` in the phase 1/2 boundary, add:

```go
// Set MACHINE_CONFIG struct for Starlark
if proj := eng.Project(); proj != nil {
	if m, ok := eng.Machines()[proj.Defaults.Machine]; ok {
		machineDict := starlark.StringDict{
			"name":       starlark.String(m.Name),
			"arch":       starlark.String(m.Arch),
			"packages":   toStarlarkList(m.Packages),
			"partitions": toStarlarkPartitionList(m.Partitions),
		}
		if m.Kernel.Unit != "" {
			machineDict["kernel"] = starlarkstruct.FromStringDict(
				starlark.String("kernel"),
				starlark.StringDict{
					"unit":      starlark.String(m.Kernel.Unit),
					"provides":  starlark.String(m.Kernel.Provides),
					"defconfig": starlark.String(m.Kernel.Defconfig),
					"cmdline":   starlark.String(m.Kernel.Cmdline),
				},
			)
		}
		eng.SetVar("MACHINE_CONFIG", starlarkstruct.FromStringDict(
			starlark.String("machine_config"), machineDict))
	}
}

// Set PROVIDES dict — built from kernel.provides on the active machine
// (units with provides fields contribute after phase 2 — see below)
provides := starlark.NewDict(4)
if proj := eng.Project(); proj != nil {
	if m, ok := eng.Machines()[proj.Defaults.Machine]; ok {
		if m.Kernel.Provides != "" {
			provides.SetKey(starlark.String(m.Kernel.Provides),
				starlark.String(m.Kernel.Unit))
		}
	}
}
eng.SetVar("PROVIDES", provides)
```

Add helper functions `toStarlarkList` and `toStarlarkPartitionList` that convert
Go slices to Starlark values.

After phase 2 (unit loading), scan all units for `provides` fields and add them
to the PROVIDES dict. This requires a post-phase-2 step:

```go
// After phase 2, add unit provides
if provides, ok := eng.vars["PROVIDES"].(*starlark.Dict); ok {
	for _, u := range eng.Units() {
		if u.Provides != "" {
			provides.SetKey(starlark.String(u.Provides), starlark.String(u.Name))
		}
	}
}
```

- [ ] **Step 4: Update machine .star files**

Update machine definitions to include `packages`, `partitions`, and
`kernel.provides`:

`qemu-x86_64.star`:

```python
machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    description = "QEMU x86_64 virtual machine (KVM)",
    kernel = kernel(
        unit = "linux",
        provides = "linux",
        defconfig = "x86_64_defconfig",
        cmdline = "console=ttyS0 root=/dev/vda2 rw",
    ),
    packages = ["syslinux"],
    partitions = [
        partition(label="rootfs", type="ext4", size="128M", root=True),
    ],
    qemu = qemu_config(
        machine = "q35",
        cpu = "host",
        memory = "1G",
        firmware = "seabios",
        display = "none",
    ),
)
```

Similarly update `qemu-arm64.star`, `raspberrypi4.star`, `raspberrypi5.star`.

- [ ] **Step 5: Build and test**

Run: `go build ./...` Run: `go test ./internal/starlark/ -v`

- [ ] **Step 6: Commit**

```bash
git add internal/starlark/ modules/
git commit -m "add MACHINE_CONFIG, PROVIDES, machine packages/partitions

Machines define packages and partitions that get merged into images.
MACHINE_CONFIG exposes the machine as a Starlark struct. PROVIDES maps
virtual package names to concrete units (e.g., linux → linux-rpi4)."
```

---

### Task 5: Rewrite Image Class in Starlark

Move image assembly from Go (`internal/image/`) to a Starlark class that uses
`run()` for disk operations. Delete the Go image code.

**Files:**

- Rewrite: `modules/module-core/classes/image.star`
- Modify: `modules/module-core/images/base-image.star`
- Modify: `modules/module-core/images/dev-image.star`
- Delete: `modules/module-rpi/images/rpi-image.star` (no longer needed)
- Modify: `internal/build/executor.go` (remove image special case)
- Delete: `internal/image/rootfs.go` (moved to Starlark)
- Delete: `internal/image/disk.go` (moved to Starlark)

- [ ] **Step 1: Write the Starlark image class**

`modules/module-core/classes/image.star`:

```python
def image(name, artifacts=[], hostname="", timezone="", locale="",
          services=[], partitions=[], scope="machine", **kwargs):
    """Create a bootable disk image from packages.

    Merges machine-specific packages and partitions from MACHINE_CONFIG.
    Resolves virtual package names via PROVIDES.
    """
    # Merge machine packages
    all_artifacts = list(artifacts) + list(MACHINE_CONFIG.packages)

    # Resolve provides (e.g., "linux" → "linux-rpi4")
    resolved = [PROVIDES.get(a, a) for a in all_artifacts]

    # Use machine partitions if image doesn't specify its own
    all_partitions = partitions if partitions else list(MACHINE_CONFIG.partitions)

    unit(
        name = name,
        scope = scope,
        artifacts = resolved,
        partitions = all_partitions,
        tasks = [
            task("rootfs", fn=lambda: _assemble_rootfs(resolved, hostname, timezone, locale, services)),
            task("disk", fn=lambda: _create_disk_image(name, all_partitions)),
        ],
        **kwargs,
    )

def _assemble_rootfs(packages, hostname, timezone, locale, services):
    run("mkdir -p $DESTDIR/rootfs")
    for pkg in packages:
        result = run("ls $REPO/%s-*.apk 2>/dev/null | head -1" % pkg, check=False)
        if result.exit_code != 0 or result.stdout.strip() == "":
            run("echo 'warning: package %s not found, skipping' >&2" % pkg)
            continue
        apk = result.stdout.strip()
        run("tar xzf %s -C $DESTDIR/rootfs --exclude=.PKGINFO" % apk)

    if hostname:
        run("mkdir -p $DESTDIR/rootfs/etc")
        run("echo %s > $DESTDIR/rootfs/etc/hostname" % hostname)

    if timezone:
        run("mkdir -p $DESTDIR/rootfs/etc")
        run("echo %s > $DESTDIR/rootfs/etc/timezone" % timezone)

    # Enable services
    for svc in services:
        run("test -f $DESTDIR/rootfs/etc/init.d/%s && "
            "ln -sf ../init.d/%s $DESTDIR/rootfs/etc/init.d/S50%s || true" % (svc, svc, svc))

def _create_disk_image(name, partitions):
    if not partitions:
        return

    # Calculate total size
    total_mb = 1  # MBR overhead
    for p in partitions:
        total_mb += _parse_size_mb(p.size)

    img = "$DESTDIR/%s.img" % name

    # Create sparse image
    run("dd if=/dev/zero of=%s bs=1M count=0 seek=%d" % (img, total_mb))

    # Generate sfdisk script
    sfdisk_lines = ["label: dos", ""]
    for p in partitions:
        size_mb = _parse_size_mb(p.size)
        ptype = "c" if p.type == "vfat" else "83"
        sfdisk_lines.append("- %dM %s" % (size_mb, ptype))

    run("printf '%s\\n' | sfdisk %s" % ("\\n".join(sfdisk_lines), img))

    # Create and populate each partition
    offset = 1
    for p in partitions:
        size_mb = _parse_size_mb(p.size)
        part_img = img + "." + p.label + ".part"

        run("dd if=/dev/zero of=%s bs=1M count=0 seek=%d" % (part_img, size_mb))

        if p.type == "vfat":
            run("mkfs.vfat -n %s %s" % (p.label.upper(), part_img))
            # Copy boot files from rootfs
            for pattern in (p.contents if hasattr(p, "contents") else []):
                run("mcopy -sQi %s $DESTDIR/rootfs/boot/%s ::/ 2>/dev/null || true" % (part_img, pattern))
        elif p.type == "ext4":
            run("mkfs.ext4 -d $DESTDIR/rootfs -L %s %s %dM" % (p.label, part_img, size_mb))

        run("dd if=%s of=%s bs=1M seek=%d conv=notrunc" % (part_img, img, offset))
        run("rm -f %s" % part_img)
        offset += size_mb

    run("echo 'Disk image: %s (%dMB)'" % (img, total_mb))

def _parse_size_mb(size_str):
    """Parse a size string like '64M' or '256M' into megabytes."""
    s = str(size_str)
    if s.endswith("M"):
        return int(s[:-1])
    if s.endswith("G"):
        return int(s[:-1]) * 1024
    return int(s)
```

- [ ] **Step 2: Update base-image.star**

```python
load("//classes/image.star", "image")

image(
    name = "base-image",
    artifacts = ["base-files", "busybox", "linux"],
    hostname = "yoe",
)
```

- [ ] **Step 3: Update dev-image.star**

```python
load("//classes/image.star", "image")
load("//classes/users.star", "user")
load("//units/base/base-files.star", "base_files")

base_files(
    name = "base-files-dev",
    users = [
        user(name = "root", uid = 0, gid = 0, home = "/root"),
        user(name = "user", uid = 1000, gid = 1000, password = "password"),
    ],
)

image(
    name = "dev-image",
    artifacts = [
        "base-files-dev",
        "busybox",
        "musl",
        "kmod",
        "util-linux",
        "linux",
        "network-config",
        "openssh",
        "curl",
        "strace",
        "vim",
    ],
    hostname = "yoe-dev",
    services = ["sshd"],
)
```

- [ ] **Step 4: Delete rpi-image.star**

`modules/module-rpi/images/rpi-image.star` is no longer needed — `base-image`
works for RPi because MACHINE_CONFIG provides the kernel, firmware, and
partition layout.

```bash
rm modules/module-rpi/images/rpi-image.star
```

- [ ] **Step 5: Remove Go image special case from executor.go**

In `buildOne()`, remove the image class special case that calls
`image.Assemble()`. Images now use tasks like any other unit.

Remove the import of `"github.com/YoeDistro/yoe-ng/internal/image"` from
executor.go.

- [ ] **Step 6: Update executor to set $REPO env var**

The image Starlark class needs `$REPO` to find APK files. Add it to the
environment in `buildOne()`:

```go
env["REPO"] = repo.RepoDir(nil, opts.ProjectDir)
```

- [ ] **Step 7: Build and test**

Run: `go build ./...` Run: `go test ./...`

Verify that building base-image still works end-to-end.

- [ ] **Step 8: Delete Go image assembly code**

```bash
rm internal/image/rootfs.go internal/image/disk.go
```

Keep `internal/image/` directory only if there are test files that still need
it. Otherwise delete the entire directory.

- [ ] **Step 9: Commit**

```bash
git add modules/ internal/
git commit -m "rewrite image assembly in Starlark, delete Go image code

image() is now a Starlark class that uses run() for disk operations.
MACHINE_CONFIG provides packages and partitions. PROVIDES resolves
virtual package names. base-image works across all machines.
Deletes internal/image/ — assembly logic is now in classes/image.star."
```

---

### Task 6: Update Documentation and Changelog

- [ ] **Step 1: Update CHANGELOG.md**

Add under `[Unreleased]`:

```markdown
- **Tasks replace build steps** — `build = [...]` replaced by `tasks = [...]`
  with named build phases. Each task has `run` (shell string), `fn` (Starlark
  function), or `steps` (mixed list). Classes (autotools, cmake, go) are now
  pure Starlark.
- **`run()` builtin** — Starlark functions can execute shell commands directly
  during builds. Errors show `.star` file and line number, not generated shell.
- **Machine-portable images** — images no longer hard-code machine-specific
  packages or partitions. `MACHINE_CONFIG` and `PROVIDES` inject machine
  hardware specifics automatically.
- **`MACHINE_CONFIG` and `PROVIDES`** — Starlark variables exposing machine
  packages/partitions and virtual-to-concrete package mapping.
```

- [ ] **Step 2: Update CLAUDE.md**

Add to design decisions:

```markdown
- **Tasks, not build step lists** — units define `tasks = [task(...)]` with
  named phases. Steps are shell strings or Starlark callables. `run()` executes
  commands during build with full error traces to `.star` source lines.
- **Machine-portable images** — images list abstract requirements ("linux",
  "base-files"). Machines provide concrete implementations via `provides` and
  inject hardware-specific packages/partitions via `MACHINE_CONFIG`.
```

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md CLAUDE.md
git commit -m "update docs for tasks, run(), and machine-portable images"
```
