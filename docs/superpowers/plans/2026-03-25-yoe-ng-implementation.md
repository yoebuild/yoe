# Yoe-NG Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [x]`) syntax for tracking.

**Goal:** Build `yoe`, a Go CLI tool that builds packages from Starlark units,
assembles root filesystem images, and manages an embedded Linux distribution — a
simpler alternative to Yocto.

**Architecture:** Single Go binary with stdlib CLI (no framework — switch/case
dispatch like brun), go.starlark.net for unit/config evaluation, bubblewrap for
build isolation, and apk-tools for package management. Two-phase
resolve-then-build model inspired by Bazel/GN. Content-addressed caching at
every level.

**Tech Stack:** Go 1.22+, Go stdlib (CLI — no Cobra), go.starlark.net (Starlark
evaluation), Bubble Tea (TUI), bubblewrap (sandboxing), apk-tools (package
management), systemd-repart (disk images)

---

## Phase Overview

This project is broken into 9 phases. Each phase produces working, testable
software. Phases 1-3 are pure Go with no external system dependencies (testable
on any dev machine). Phases 4+ require Linux with bubblewrap and apk-tools.

| Phase | Name                          | Depends On | Key Deliverable                                                                        |
| ----- | ----------------------------- | ---------- | -------------------------------------------------------------------------------------- |
| 1     | CLI Foundation                | —          | **DONE** — `yoe init/config/clean/module`, Starlark engine, all builtins               |
| 2     | Dependency Resolution         | 1          | **DONE** — DAG, topo sort, hashing, `desc/refs/graph`, `dev`, custom commands, patches |
| 2.5   | Module Management & Engine    | 1          | `yoe module sync`, load() resolution, remove class builtins, recursive discovery       |
| 2.6   | module-core Module (Phase 1)  | 2.5        | Module skeleton, Starlark classes, toolchain units                                     |
| 2.7   | module-core Module (Phase 2)  | 2.6, 6     | Base system units, QEMU machines, base/dev images                                      |
| 2.8   | module-core Module (Phase 3)  | 2.7        | Essential libs, crypto/TLS, networking, debug tools                                    |
| 3     | Source Management             | 1          | `yoe source fetch/list/verify/clean`, content-addressed cache                          |
| 4     | Build Execution               | 2, 3       | `yoe build` with bubblewrap isolation, build step execution                            |
| 5     | Package Creation & Repository | 4          | APK package creation, `yoe repo` commands, local repository                            |
| 6     | Image Assembly                | 5          | Image unit builds — rootfs via apk, overlays, disk image generation                    |
| 7     | Device Interaction            | 6          | `yoe flash`, `yoe run` (QEMU with KVM)                                                 |
| 8     | TUI                           | 2          | `yoe tui` — Bubble Tea interactive interface                                           |
| 9     | Bootstrap                     | 5          | `yoe bootstrap stage0/stage1` — self-hosting toolchain                                 |

---

## Phase 1: CLI Foundation

**Goal:** Establish the Go project, stdlib CLI with switch/case dispatch (brun
pattern), Starlark evaluation engine for units/config, `yoe init` scaffolding,
and `yoe config` — the skeleton everything else builds on.

### File Structure

```
cmd/yoe/main.go                        — entry point, switch/case command dispatch
internal/config/project.go             — project discovery (find PROJECT.star)
internal/starlark/engine.go            — Starlark thread setup, load() handler, evaluation
internal/starlark/builtins.go          — built-in functions: project(), machine(), unit(), image(), layer_info(), etc.
internal/starlark/types.go             — Go types produced by evaluation (Project, Machine, Unit, LayerInfo, etc.)
internal/starlark/loader.go            — walk project tree, evaluate all .star files, return Project
internal/starlark/engine_test.go       — tests for engine + builtins
internal/starlark/loader_test.go       — tests for full project loading
internal/init.go                       — yoe init logic
internal/clean.go                      — yoe clean logic
internal/layer.go                      — yoe layer list logic
internal/configcmd.go                  — yoe config show logic
go.mod
go.sum
testdata/valid-project/                — test fixture: complete valid project
testdata/valid-project/PROJECT.star
testdata/valid-project/machines/*.star
testdata/valid-project/units/*.star
testdata/minimal-project/              — test fixture: minimal valid project
testdata/invalid-project/              — test fixture: various invalid configs
```

---

### Task 1: Go Module and CLI Skeleton

**Files:**

- Create: `go.mod`
- Create: `cmd/yoe/main.go`

- [x] **Step 1: Initialize Go module**

```bash
cd /scratch4/yoe/yoe-ng
go mod init github.com/YoeDistro/yoe-ng
```

- [x] **Step 2: Write the entry point with command dispatch**

Create `cmd/yoe/main.go`:

```go
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	args := os.Args[2:]

	switch command {
	case "init":
		cmdInit(args)
	case "layer":
		cmdLayer(args)
	case "config":
		cmdConfig(args)
	case "clean":
		cmdClean(args)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s COMMAND [OPTIONS]\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "Yoe-NG embedded Linux distribution builder\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  init <project-dir>      Create a new Yoe-NG project\n")
	fmt.Fprintf(os.Stderr, "  build [units...]      Build units (packages and images)\n")
	fmt.Fprintf(os.Stderr, "  flash <device>          Write an image to a device/SD card\n")
	fmt.Fprintf(os.Stderr, "  run                     Run an image in QEMU\n")
	fmt.Fprintf(os.Stderr, "  layer                   Manage external layers (fetch, sync, list)\n")
	fmt.Fprintf(os.Stderr, "  repo                    Manage the local apk package repository\n")
	fmt.Fprintf(os.Stderr, "  cache                   Manage the build cache (local and remote)\n")
	fmt.Fprintf(os.Stderr, "  source                  Download and manage source archives/repos\n")
	fmt.Fprintf(os.Stderr, "  config                  View and edit project configuration\n")
	fmt.Fprintf(os.Stderr, "  desc <unit>           Describe a unit or target\n")
	fmt.Fprintf(os.Stderr, "  refs <unit>           Show reverse dependencies\n")
	fmt.Fprintf(os.Stderr, "  graph                   Visualize the dependency DAG\n")
	fmt.Fprintf(os.Stderr, "  tui                     Launch the interactive TUI\n")
	fmt.Fprintf(os.Stderr, "  clean                   Remove build artifacts\n")
	fmt.Fprintf(os.Stderr, "  version                 Display version information\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  %s init my-project --machine beaglebone-black\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s build openssh\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s build base-image --machine raspberrypi4\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Environment Variables:\n")
	fmt.Fprintf(os.Stderr, "  YOE_PROJECT             Project directory (default: cwd)\n")
	fmt.Fprintf(os.Stderr, "  YOE_CACHE               Cache directory (default: ~/.cache/yoe-ng)\n")
	fmt.Fprintf(os.Stderr, "  YOE_LOG                 Log level: debug, info, warn, error (default: info)\n")
	fmt.Fprintf(os.Stderr, "\n")
}

// Stub command handlers — implemented in subsequent tasks

func cmdInit(args []string) {
	fmt.Fprintf(os.Stderr, "init: not yet implemented\n")
	os.Exit(1)
}

func cmdConfig(args []string) {
	fmt.Fprintf(os.Stderr, "config: not yet implemented\n")
	os.Exit(1)
}

func cmdClean(args []string) {
	fmt.Fprintf(os.Stderr, "clean: not yet implemented\n")
	os.Exit(1)
}
```

- [x] **Step 3: Build and run**

```bash
go build -o yoe ./cmd/yoe
./yoe
./yoe version
```

Expected: Usage text on bare `yoe`, "dev" on `yoe version`.

- [x] **Step 4: Commit**

```bash
git add go.mod cmd/
git commit -m "feat: initialize Go module with stdlib CLI skeleton"
```

---

### Task 2: Starlark Types

**Files:**

- Create: `internal/starlark/types.go`

Define the Go types that Starlark evaluation produces. These are plain Go
structs — no Starlark dependency yet.

- [x] **Step 1: Write the types**

Create `internal/starlark/types.go`:

```go
package starlark

// Project represents an evaluated PROJECT.star.
type Project struct {
	Name       string
	Version    string
	Defaults   Defaults
	Repository RepositoryConfig
	Cache      CacheConfig
	Sources    SourcesConfig
	Layers     []LayerRef
	Machines   map[string]*Machine
	Units    map[string]*Unit
}

type Defaults struct {
	Machine string
	Image   string
}

type RepositoryConfig struct {
	Path string
}

type CacheConfig struct {
	Path      string
	Remote    []CacheRemote
	Retention int // days
	Signing   string
}

type CacheRemote struct {
	Name     string
	Bucket   string
	Endpoint string
	Region   string
	Prefix   string
}

type SourcesConfig struct {
	GoProxy       string
	CargoRegistry string
	NpmRegistry   string
	PypiMirror    string
}

type LayerRef struct {
	URL   string
	Ref   string
	Local string // local path override (like Go's replace directive)
}

// LayerInfo represents an evaluated LAYER.star from an external layer.
type LayerInfo struct {
	Name        string
	Description string
	Deps        []LayerRef
}

// Machine represents an evaluated machine() call.
type Machine struct {
	Name        string
	Arch        string
	Description string
	Kernel      KernelConfig
	Bootloader  BootloaderConfig
	QEMU        *QEMUConfig // nil if not a QEMU machine
}

type KernelConfig struct {
	Repo        string
	Branch      string
	Tag         string
	Defconfig   string
	DeviceTrees []string
	Unit      string
	Cmdline     string
}

type BootloaderConfig struct {
	Type      string
	Repo      string
	Branch    string
	Defconfig string
}

type QEMUConfig struct {
	Machine  string
	CPU      string
	Memory   string
	Firmware string
	Display  string
}

// Unit represents an evaluated unit(), autotools(), image(), etc. call.
type Unit struct {
	Name        string
	Version     string
	Class       string // "package", "autotools", "cmake", "go", "image", etc.
	Description string
	License     string

	// Source
	Source string // URL or git repo
	SHA256 string
	Tag    string
	Branch string

	// Dependencies
	Deps        []string
	RuntimeDeps []string

	// Build
	Build         []string // shell commands (for generic unit())
	ConfigureArgs []string // for autotools/cmake
	GoPackage     string   // for go_binary

	// Package metadata
	Services    []string
	Conffiles   []string
	Environment map[string]string

	// Image-specific (class == "image")
	Packages   []string // artifacts to install in rootfs
	Exclude    []string
	Hostname   string
	Timezone   string
	Locale     string
	Partitions []Partition
}

type Partition struct {
	Label    string
	Type     string // "vfat", "ext4", etc.
	Size     string // "64M", "fill", etc.
	Root     bool
	Contents []string
}

var validArchitectures = map[string]bool{
	"arm64":   true,
	"riscv64": true,
	"x86_64":  true,
}
```

- [x] **Step 2: Commit**

```bash
git add internal/starlark/types.go
git commit -m "feat: add Go types for Starlark evaluation output"
```

---

### Task 3: Starlark Engine and Built-in Functions

**Files:**

- Create: `internal/starlark/engine.go`
- Create: `internal/starlark/builtins.go`
- Create: `internal/starlark/engine_test.go`

The engine evaluates `.star` files and collects the results of built-in function
calls (project(), machine(), unit(), image(), etc.) into Go types.

- [x] **Step 1: Write the failing test**

Create `internal/starlark/engine_test.go`:

```go
package starlark

import (
	"testing"
)

func TestEvalProject(t *testing.T) {
	src := `
project(
    name = "test-project",
    version = "0.1.0",
    defaults = defaults(machine = "qemu-arm64", image = "base-image"),
    repository = repository(path = "/var/cache/yoe-ng/repo"),
    cache = cache(path = "/var/cache/yoe-ng/build"),
)
`
	eng := NewEngine()
	if err := eng.ExecString("PROJECT.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	proj := eng.Project()
	if proj == nil {
		t.Fatal("Project() returned nil")
	}
	if proj.Name != "test-project" {
		t.Errorf("Name = %q, want %q", proj.Name, "test-project")
	}
	if proj.Defaults.Machine != "qemu-arm64" {
		t.Errorf("Defaults.Machine = %q, want %q", proj.Defaults.Machine, "qemu-arm64")
	}
}

func TestEvalMachine(t *testing.T) {
	src := `
machine(
    name = "beaglebone-black",
    arch = "arm64",
    description = "BeagleBone Black",
    kernel = kernel(
        repo = "https://github.com/beagleboard/linux.git",
        branch = "6.6",
        defconfig = "bb.org_defconfig",
        device_trees = ["am335x-boneblack.dtb"],
    ),
    bootloader = uboot(
        repo = "https://github.com/beagleboard/u-boot.git",
        branch = "v2024.01",
        defconfig = "am335x_evm_defconfig",
    ),
)
`
	eng := NewEngine()
	if err := eng.ExecString("machines/bbb.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	machines := eng.Machines()
	m, ok := machines["beaglebone-black"]
	if !ok {
		t.Fatal("machine 'beaglebone-black' not found")
	}
	if m.Arch != "arm64" {
		t.Errorf("Arch = %q, want %q", m.Arch, "arm64")
	}
	if m.Kernel.Defconfig != "bb.org_defconfig" {
		t.Errorf("Kernel.Defconfig = %q, want %q", m.Kernel.Defconfig, "bb.org_defconfig")
	}
	if len(m.Kernel.DeviceTrees) != 1 {
		t.Errorf("Kernel.DeviceTrees = %v, want 1 entry", m.Kernel.DeviceTrees)
	}
}

func TestEvalPackageRecipe(t *testing.T) {
	src := `
unit(
    name = "openssh",
    version = "9.6p1",
    source = "https://cdn.openbsd.org/pub/OpenBSD/OpenSSH/portable/openssh-9.6p1.tar.gz",
    sha256 = "abc123",
    deps = ["zlib", "openssl"],
    runtime_deps = ["zlib", "openssl"],
    build = [
        "./configure --prefix=$PREFIX",
        "make -j$NPROC",
        "make DESTDIR=$DESTDIR install",
    ],
    services = ["sshd"],
    conffiles = ["/etc/ssh/sshd_config"],
)
`
	eng := NewEngine()
	if err := eng.ExecString("units/openssh.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	units := eng.Units()
	r, ok := units["openssh"]
	if !ok {
		t.Fatal("unit 'openssh' not found")
	}
	if r.Class != "package" {
		t.Errorf("Class = %q, want %q", r.Class, "package")
	}
	if r.Version != "9.6p1" {
		t.Errorf("Version = %q, want %q", r.Version, "9.6p1")
	}
	if len(r.Deps) != 2 {
		t.Errorf("Deps = %v, want 2 entries", r.Deps)
	}
	if len(r.Build) != 3 {
		t.Errorf("Build = %v, want 3 entries", r.Build)
	}
}

func TestEvalImageRecipe(t *testing.T) {
	src := `
image(
    name = "base-image",
    version = "1.0.0",
    artifacts = ["openssh", "myapp"],
    hostname = "yoe",
    services = ["sshd"],
    partitions = [
        partition(label="boot", type="vfat", size="64M"),
        partition(label="rootfs", type="ext4", size="fill", root=True),
    ],
)
`
	eng := NewEngine()
	if err := eng.ExecString("units/base-image.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	units := eng.Units()
	r, ok := units["base-image"]
	if !ok {
		t.Fatal("unit 'base-image' not found")
	}
	if r.Class != "image" {
		t.Errorf("Class = %q, want %q", r.Class, "image")
	}
	if len(r.Packages) != 2 {
		t.Errorf("Packages = %v, want 2 entries", r.Packages)
	}
	if len(r.Partitions) != 2 {
		t.Errorf("Partitions = %v, want 2 entries", r.Partitions)
	}
	if !r.Partitions[1].Root {
		t.Error("Partitions[1].Root = false, want true")
	}
}

func TestEvalInvalidArch(t *testing.T) {
	src := `
machine(name = "bad", arch = "mips")
`
	eng := NewEngine()
	err := eng.ExecString("machines/bad.star", src)
	if err == nil {
		t.Fatal("expected error for invalid arch, got nil")
	}
}

func TestEvalPackageRequiresBuild(t *testing.T) {
	src := `
unit(name = "broken", version = "1.0.0")
`
	eng := NewEngine()
	err := eng.ExecString("units/broken.star", src)
	if err == nil {
		t.Fatal("expected error for package with no build steps, got nil")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

```bash
go get go.starlark.net@latest
go test ./internal/starlark/ -v
```

Expected: FAIL — types exist but `NewEngine`, `ExecString`, etc. not defined.

- [x] **Step 3: Write the engine**

Create `internal/starlark/engine.go`:

```go
package starlark

import (
	"fmt"
	"sync"

	"go.starlark.net/starlark"
)

// Engine evaluates .star files and collects results.
type Engine struct {
	mu       sync.Mutex
	project  *Project
	machines map[string]*Machine
	units  map[string]*Unit
}

func NewEngine() *Engine {
	return &Engine{
		machines: make(map[string]*Machine),
		units:  make(map[string]*Unit),
	}
}

func (e *Engine) Project() *Project   { return e.project }
func (e *Engine) Machines() map[string]*Machine { return e.machines }
func (e *Engine) Units() map[string]*Unit   { return e.units }

// ExecString evaluates Starlark source code with built-in functions available.
func (e *Engine) ExecString(filename, src string) error {
	thread := &starlark.Thread{Name: filename}
	predeclared := e.builtins()

	_, err := starlark.ExecFile(thread, filename, src, predeclared)
	return err
}

// ExecFile evaluates a .star file from disk.
func (e *Engine) ExecFile(path string) error {
	thread := &starlark.Thread{Name: path}
	predeclared := e.builtins()

	_, err := starlark.ExecFile(thread, path, nil, predeclared)
	return err
}
```

- [x] **Step 4: Write the built-in functions**

Create `internal/starlark/builtins.go`:

```go
package starlark

import (
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// builtins returns the predeclared names available in all .star files.
func (e *Engine) builtins() starlark.StringDict {
	return starlark.StringDict{
		"project":    starlark.NewBuiltin("project", e.fnProject),
		"defaults":   starlark.NewBuiltin("defaults", fnDefaults),
		"repository": starlark.NewBuiltin("repository", fnRepository),
		"cache":      starlark.NewBuiltin("cache", fnCache),
		"s3_cache":   starlark.NewBuiltin("s3_cache", fnS3Cache),
		"sources":    starlark.NewBuiltin("sources", fnSources),
		"layer":      starlark.NewBuiltin("layer", fnLayer),
		"layer_info": starlark.NewBuiltin("layer_info", e.fnLayerInfo),
		"machine":    starlark.NewBuiltin("machine", e.fnMachine),
		"kernel":     starlark.NewBuiltin("kernel", fnKernel),
		"uboot":      starlark.NewBuiltin("uboot", fnUboot),
		"qemu_config": starlark.NewBuiltin("qemu_config", fnQEMUConfig),
		"package":    starlark.NewBuiltin("package", e.fnPackage),
		"autotools":  starlark.NewBuiltin("autotools", e.fnAutotools),
		"cmake":      starlark.NewBuiltin("cmake", e.fnCMake),
		"go_binary":  starlark.NewBuiltin("go_binary", e.fnGoBinary),
		"image":      starlark.NewBuiltin("image", e.fnImage),
		"partition":  starlark.NewBuiltin("partition", fnPartition),
	}
}

// --- Helper: extract keyword args ---

func kwString(kwargs []starlark.Tuple, key string) string {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if s, ok := kv[1].(starlark.String); ok {
				return string(s)
			}
		}
	}
	return ""
}

func kwStringList(kwargs []starlark.Tuple, key string) []string {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if list, ok := kv[1].(*starlark.List); ok {
				var result []string
				iter := list.Iterate()
				defer iter.Done()
				var v starlark.Value
				for iter.Next(&v) {
					if s, ok := v.(starlark.String); ok {
						result = append(result, string(s))
					}
				}
				return result
			}
		}
	}
	return nil
}

func kwBool(kwargs []starlark.Tuple, key string) bool {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if b, ok := kv[1].(starlark.Bool); ok {
				return bool(b)
			}
		}
	}
	return false
}

func kwInt(kwargs []starlark.Tuple, key string) int {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if n, ok := kv[1].(starlark.Int); ok {
				v, _ := n.Int64()
				return int(v)
			}
		}
	}
	return 0
}

func kwStruct(kwargs []starlark.Tuple, key string) *starlarkstruct.Struct {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if s, ok := kv[1].(*starlarkstruct.Struct); ok {
				return s
			}
		}
	}
	return nil
}

func kwStructList(kwargs []starlark.Tuple, key string) []*starlarkstruct.Struct {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if list, ok := kv[1].(*starlark.List); ok {
				var result []*starlarkstruct.Struct
				iter := list.Iterate()
				defer iter.Done()
				var v starlark.Value
				for iter.Next(&v) {
					if s, ok := v.(*starlarkstruct.Struct); ok {
						result = append(result, s)
					}
				}
				return result
			}
		}
	}
	return nil
}

func structString(s *starlarkstruct.Struct, field string) string {
	if s == nil {
		return ""
	}
	v, err := s.Attr(field)
	if err != nil {
		return ""
	}
	if str, ok := v.(starlark.String); ok {
		return string(str)
	}
	return ""
}

func structStringList(s *starlarkstruct.Struct, field string) []string {
	if s == nil {
		return nil
	}
	v, err := s.Attr(field)
	if err != nil {
		return nil
	}
	if list, ok := v.(*starlark.List); ok {
		var result []string
		iter := list.Iterate()
		defer iter.Done()
		var item starlark.Value
		for iter.Next(&item) {
			if str, ok := item.(starlark.String); ok {
				result = append(result, string(str))
			}
		}
		return result
	}
	return nil
}

// --- Built-in functions that return structs (data constructors) ---

func makeStruct(name string, kwargs []starlark.Tuple) *starlarkstruct.Struct {
	d := make(starlark.StringDict, len(kwargs))
	for _, kv := range kwargs {
		d[string(kv[0].(starlark.String))] = kv[1]
	}
	return starlarkstruct.FromStringDict(starlark.String(name), d)
}

func fnDefaults(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("defaults", kwargs), nil
}

func fnRepository(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("repository", kwargs), nil
}

func fnCache(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("cache", kwargs), nil
}

func fnS3Cache(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("s3_cache", kwargs), nil
}

func fnSources(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("sources", kwargs), nil
}

func fnLayer(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("layer() requires a URL argument")
	}
	url, ok := args[0].(starlark.String)
	if !ok {
		return nil, fmt.Errorf("layer() URL must be a string")
	}
	d := starlark.StringDict{"url": url}
	for _, kv := range kwargs {
		d[string(kv[0].(starlark.String))] = kv[1]
	}
	return starlarkstruct.FromStringDict(starlark.String("layer"), d), nil
}

func fnKernel(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("kernel", kwargs), nil
}

func fnUboot(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("uboot", kwargs), nil
}

func fnQEMUConfig(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("qemu_config", kwargs), nil
}

func fnPartition(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("partition", kwargs), nil
}

// --- Built-in functions that register targets (side-effecting) ---

func (e *Engine) fnProject(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.project != nil {
		return nil, fmt.Errorf("project() called more than once")
	}

	defs := kwStruct(kwargs, "defaults")
	repo := kwStruct(kwargs, "repository")
	cacheS := kwStruct(kwargs, "cache")

	e.project = &Project{
		Name:    kwString(kwargs, "name"),
		Version: kwString(kwargs, "version"),
		Defaults: Defaults{
			Machine: structString(defs, "machine"),
			Image:   structString(defs, "image"),
		},
		Repository: RepositoryConfig{
			Path: structString(repo, "path"),
		},
		Cache: CacheConfig{
			Path: structString(cacheS, "path"),
		},
	}

	return starlark.None, nil
}

func (e *Engine) fnMachine(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	name := kwString(kwargs, "name")
	arch := kwString(kwargs, "arch")

	if name == "" {
		return nil, fmt.Errorf("machine() requires name")
	}
	if !validArchitectures[arch] {
		return nil, fmt.Errorf("machine %q: invalid arch %q (valid: arm64, riscv64, x86_64)", name, arch)
	}

	kernelS := kwStruct(kwargs, "kernel")
	bootS := kwStruct(kwargs, "bootloader")
	if bootS == nil {
		// Also accept uboot() as bootloader
		bootS = kwStruct(kwargs, "uboot") // won't match kwStruct key, handled below
	}

	m := &Machine{
		Name:        name,
		Arch:        arch,
		Description: kwString(kwargs, "description"),
		Kernel: KernelConfig{
			Repo:        structString(kernelS, "repo"),
			Branch:      structString(kernelS, "branch"),
			Tag:         structString(kernelS, "tag"),
			Defconfig:   structString(kernelS, "defconfig"),
			DeviceTrees: structStringList(kernelS, "device_trees"),
			Unit:      structString(kernelS, "unit"),
			Cmdline:     structString(kernelS, "cmdline"),
		},
	}

	// Handle bootloader — could be passed as bootloader= or as uboot()
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		if key == "bootloader" || key == "uboot" {
			if s, ok := kv[1].(*starlarkstruct.Struct); ok {
				m.Bootloader = BootloaderConfig{
					Type:      structString(s, "type"),
					Repo:      structString(s, "repo"),
					Branch:    structString(s, "branch"),
					Defconfig: structString(s, "defconfig"),
				}
				if key == "uboot" {
					m.Bootloader.Type = "u-boot"
				}
			}
		}
		if key == "qemu" {
			if s, ok := kv[1].(*starlarkstruct.Struct); ok {
				m.QEMU = &QEMUConfig{
					Machine:  structString(s, "machine"),
					CPU:      structString(s, "cpu"),
					Memory:   structString(s, "memory"),
					Firmware: structString(s, "firmware"),
					Display:  structString(s, "display"),
				}
			}
		}
	}

	e.mu.Lock()
	e.machines[name] = m
	e.mu.Unlock()

	return starlark.None, nil
}

func (e *Engine) registerRecipe(class string, kwargs []starlark.Tuple) (*Unit, error) {
	name := kwString(kwargs, "name")
	if name == "" {
		return nil, fmt.Errorf("%s() requires name", class)
	}

	r := &Unit{
		Name:          name,
		Version:       kwString(kwargs, "version"),
		Class:         class,
		Description:   kwString(kwargs, "description"),
		License:       kwString(kwargs, "license"),
		Source:        kwString(kwargs, "source"),
		SHA256:        kwString(kwargs, "sha256"),
		Tag:           kwString(kwargs, "tag"),
		Branch:        kwString(kwargs, "branch"),
		Deps:          kwStringList(kwargs, "deps"),
		RuntimeDeps:   kwStringList(kwargs, "runtime_deps"),
		Build:         kwStringList(kwargs, "build"),
		ConfigureArgs: kwStringList(kwargs, "configure_args"),
		GoPackage:     kwString(kwargs, "package"),
		Services:      kwStringList(kwargs, "services"),
		Conffiles:     kwStringList(kwargs, "conffiles"),
		// Image-specific
		Packages:   kwStringList(kwargs, "packages"),
		Exclude:    kwStringList(kwargs, "exclude"),
		Hostname:   kwString(kwargs, "hostname"),
		Timezone:   kwString(kwargs, "timezone"),
		Locale:     kwString(kwargs, "locale"),
	}

	// Parse partitions if present
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == "partitions" {
			if list, ok := kv[1].(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var v starlark.Value
				for iter.Next(&v) {
					if s, ok := v.(*starlarkstruct.Struct); ok {
						r.Partitions = append(r.Partitions, Partition{
							Label:    structString(s, "label"),
							Type:     structString(s, "type"),
							Size:     structString(s, "size"),
							Root:     false, // handled below
							Contents: structStringList(s, "contents"),
						})
						// Check root flag
						if rv, err := s.Attr("root"); err == nil {
							if b, ok := rv.(starlark.Bool); ok {
								r.Partitions[len(r.Partitions)-1].Root = bool(b)
							}
						}
					}
				}
			}
		}
	}

	e.mu.Lock()
	e.units[name] = r
	e.mu.Unlock()

	return r, nil
}

func (e *Engine) fnPackage(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	r, err := e.registerRecipe("package", kwargs)
	if err != nil {
		return nil, err
	}
	if len(r.Build) == 0 {
		return nil, fmt.Errorf("unit(%q): build steps required", r.Name)
	}
	return starlark.None, nil
}

func (e *Engine) fnAutotools(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	_, err := e.registerRecipe("autotools", kwargs)
	return starlark.None, err
}

func (e *Engine) fnCMake(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	_, err := e.registerRecipe("cmake", kwargs)
	return starlark.None, err
}

func (e *Engine) fnGoBinary(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	_, err := e.registerRecipe("go", kwargs)
	return starlark.None, err
}

func (e *Engine) fnImage(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	_, err := e.registerRecipe("image", kwargs)
	return starlark.None, err
}
```

- [x] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/starlark/ -v
```

Expected: All 6 tests PASS.

- [x] **Step 6: Commit**

```bash
git add internal/starlark/
git commit -m "feat: add Starlark evaluation engine with built-in functions"
```

---

### Task 4: Test Fixtures

**Files:**

- Create: `testdata/valid-project/PROJECT.star`
- Create: `testdata/valid-project/machines/beaglebone-black.star`
- Create: `testdata/valid-project/machines/qemu-x86_64.star`
- Create: `testdata/valid-project/units/openssh.star`
- Create: `testdata/valid-project/units/myapp.star`
- Create: `testdata/valid-project/units/base-image.star`
- Create: `testdata/minimal-project/PROJECT.star`
- Create: `testdata/invalid-project/bad-arch.star`

- [x] **Step 1: Create valid project fixtures**

Create `testdata/valid-project/PROJECT.star`:

```python
project(
    name = "test-distro",
    version = "0.1.0",
    description = "Test distribution",
    defaults = defaults(machine = "qemu-x86_64", image = "base-image"),
    repository = repository(path = "/var/cache/yoe-ng/repo"),
    cache = cache(
        path = "/var/cache/yoe-ng/build",
        remote = [
            s3_cache(name="team", bucket="yoe-cache",
                     endpoint="https://minio.internal:9000", region="us-east-1"),
        ],
    ),
    sources = sources(go_proxy = "https://proxy.golang.org"),
    layers = [
        layer("github.com/yoe/module-core", ref = "v1.0.0"),
    ],
)
```

Create `testdata/valid-project/machines/beaglebone-black.star`:

```python
machine(
    name = "beaglebone-black",
    arch = "arm64",
    description = "BeagleBone Black (AM3358)",
    kernel = kernel(
        repo = "https://github.com/beagleboard/linux.git",
        branch = "6.6",
        defconfig = "bb.org_defconfig",
        device_trees = ["am335x-boneblack.dtb"],
    ),
    bootloader = uboot(
        repo = "https://github.com/beagleboard/u-boot.git",
        branch = "v2024.01",
        defconfig = "am335x_evm_defconfig",
    ),
)
```

Create `testdata/valid-project/machines/qemu-x86_64.star`:

```python
machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyS0 root=/dev/vda2 rw"),
    qemu = qemu_config(machine = "q35", cpu = "host", memory = "1G", firmware = "ovmf", display = "none"),
)
```

Create `testdata/valid-project/units/openssh.star`:

```python
unit(
    name = "openssh",
    version = "9.6p1",
    description = "OpenSSH client and server",
    license = "BSD",
    source = "https://cdn.openbsd.org/pub/OpenBSD/OpenSSH/portable/openssh-9.6p1.tar.gz",
    sha256 = "aaaa1111bbbb2222",
    deps = ["zlib", "openssl"],
    runtime_deps = ["zlib", "openssl"],
    build = [
        "./configure --prefix=$PREFIX --sysconfdir=/etc/ssh",
        "make -j$NPROC",
        "make DESTDIR=$DESTDIR install",
    ],
    services = ["sshd"],
    conffiles = ["/etc/ssh/sshd_config"],
)
```

Create `testdata/valid-project/units/myapp.star`:

```python
go_binary(
    name = "myapp",
    version = "1.2.3",
    description = "Edge data collection service",
    license = "Apache-2.0",
    source = "https://github.com/example/myapp.git",
    tag = "v1.2.3",
    package = "./cmd/myapp",
    services = ["myapp"],
    conffiles = ["/etc/myapp/config.toml"],
)
```

Create `testdata/valid-project/units/base-image.star`:

```python
image(
    name = "base-image",
    version = "1.0.0",
    description = "Minimal bootable system",
    artifacts = ["openssh", "myapp"],
    hostname = "yoe",
    timezone = "UTC",
    services = ["sshd", "myapp"],
    partitions = [
        partition(label="boot", type="vfat", size="64M"),
        partition(label="rootfs", type="ext4", size="fill", root=True),
    ],
)
```

Create `testdata/minimal-project/PROJECT.star`:

```python
project(name = "minimal", version = "0.1.0")
```

Create `testdata/invalid-project/bad-arch.star`:

```python
machine(name = "bad-machine", arch = "mips")
```

- [x] **Step 2: Commit**

```bash
git add testdata/
git commit -m "feat: add Starlark test fixtures for valid and invalid projects"
```

---

### Task 5: Project Discovery and Loader

**Files:**

- Create: `internal/config/project.go`
- Create: `internal/starlark/loader.go`
- Create: `internal/starlark/loader_test.go`

- [x] **Step 1: Write project discovery**

Create `internal/config/project.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

func FindProjectRoot(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}

	for {
		candidate := filepath.Join(dir, "PROJECT.star")
		if _, err := os.Stat(candidate); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("no PROJECT.star found in %s or any parent directory", startDir)
}
```

- [x] **Step 2: Write the loader**

Create `internal/starlark/loader.go`:

```go
package starlark

import (
	"fmt"
	"path/filepath"

	"github.com/YoeDistro/yoe-ng/internal/config"
)

// LoadProject finds the project root, evaluates all .star files, and returns
// a fully populated Project.
func LoadProject(startDir string) (*Project, error) {
	root, err := config.FindProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	eng := NewEngine()

	// Evaluate PROJECT.star first
	projFile := filepath.Join(root, "PROJECT.star")
	if err := eng.ExecFile(projFile); err != nil {
		return nil, fmt.Errorf("evaluating PROJECT.star: %w", err)
	}

	// Evaluate all machine definitions
	if err := evalDir(eng, root, "machines"); err != nil {
		return nil, err
	}

	// Evaluate all units
	if err := evalDir(eng, root, "units"); err != nil {
		return nil, err
	}

	proj := eng.Project()
	if proj == nil {
		return nil, fmt.Errorf("PROJECT.star did not call project()")
	}

	proj.Machines = eng.Machines()
	proj.Units = eng.Units()

	return proj, nil
}

func evalDir(eng *Engine, root, subdir string) error {
	pattern := filepath.Join(root, subdir, "*.star")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("globbing %s: %w", pattern, err)
	}
	for _, path := range matches {
		if err := eng.ExecFile(path); err != nil {
			return fmt.Errorf("evaluating %s: %w", path, err)
		}
	}
	return nil
}
```

- [x] **Step 3: Write the failing test**

Create `internal/starlark/loader_test.go`:

```go
package starlark

import (
	"path/filepath"
	"testing"
)

func TestLoadProject(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "valid-project")
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	if proj.Name != "test-distro" {
		t.Errorf("Name = %q, want %q", proj.Name, "test-distro")
	}
	if proj.Defaults.Machine != "qemu-x86_64" {
		t.Errorf("Defaults.Machine = %q, want %q", proj.Defaults.Machine, "qemu-x86_64")
	}

	// Machines
	if len(proj.Machines) != 2 {
		t.Errorf("got %d machines, want 2", len(proj.Machines))
	}
	if m, ok := proj.Machines["beaglebone-black"]; !ok {
		t.Error("expected machine 'beaglebone-black'")
	} else if m.Arch != "arm64" {
		t.Errorf("bbb arch = %q, want %q", m.Arch, "arm64")
	}
	if m, ok := proj.Machines["qemu-x86_64"]; !ok {
		t.Error("expected machine 'qemu-x86_64'")
	} else if m.QEMU == nil {
		t.Error("expected QEMU config on qemu-x86_64")
	}

	// Units
	if len(proj.Units) != 3 {
		t.Errorf("got %d units, want 3", len(proj.Units))
	}
	if r, ok := proj.Units["openssh"]; !ok {
		t.Error("expected unit 'openssh'")
	} else if r.Class != "package" {
		t.Errorf("openssh class = %q, want %q", r.Class, "package")
	}
	if r, ok := proj.Units["myapp"]; !ok {
		t.Error("expected unit 'myapp'")
	} else if r.Class != "go" {
		t.Errorf("myapp class = %q, want %q", r.Class, "go")
	}
	if r, ok := proj.Units["base-image"]; !ok {
		t.Error("expected unit 'base-image'")
	} else if r.Class != "image" {
		t.Errorf("base-image class = %q, want %q", r.Class, "image")
	}
}

func TestLoadMinimalProject(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "minimal-project")
	proj, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if proj.Name != "minimal" {
		t.Errorf("Name = %q, want %q", proj.Name, "minimal")
	}
}
```

- [x] **Step 4: Run tests**

```bash
go test ./internal/starlark/ -run TestLoadProject -v
```

Expected: All tests PASS.

- [x] **Step 5: Commit**

```bash
git add internal/config/project.go internal/starlark/loader.go internal/starlark/loader_test.go
git commit -m "feat: add project discovery and Starlark project loader"
```

---

### Task 6: `yoe init` Command

**Files:**

- Create: `internal/init.go`
- Create: `internal/init_test.go`
- Modify: `cmd/yoe/main.go` — wire up cmdInit

- [x] **Step 1: Write the failing test**

Create `internal/init_test.go`:

```go
package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test-project")

	if err := RunInit(dir, ""); err != nil {
		t.Fatalf("RunInit: %v", err)
	}

	for _, path := range []string{
		"PROJECT.star",
		"machines",
		"units",
		"classes",
		"overlays",
	} {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); os.IsNotExist(err) {
			t.Errorf("expected %s to exist after init", path)
		}
	}

	// Verify PROJECT.star is valid Starlark
	content, err := os.ReadFile(filepath.Join(dir, "PROJECT.star"))
	if err != nil {
		t.Fatalf("reading PROJECT.star: %v", err)
	}
	if len(content) == 0 {
		t.Error("PROJECT.star is empty")
	}
}

func TestRunInit_WithMachine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test-project")

	if err := RunInit(dir, "qemu-x86_64"); err != nil {
		t.Fatalf("RunInit with machine: %v", err)
	}

	machineFile := filepath.Join(dir, "machines", "qemu-x86_64.star")
	if _, err := os.Stat(machineFile); os.IsNotExist(err) {
		t.Errorf("expected machine file %s to exist", machineFile)
	}
}

func TestRunInit_ExistingProject(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "PROJECT.star"), []byte("project(name=\"exists\")\n"), 0644)

	if err := RunInit(dir, ""); err == nil {
		t.Fatal("expected error when init into existing project, got nil")
	}
}
```

- [x] **Step 2: Write the implementation**

Create `internal/init.go`:

```go
package internal

import (
	"fmt"
	"os"
	"path/filepath"
)

func RunInit(projectDir string, machine string) error {
	if _, err := os.Stat(filepath.Join(projectDir, "PROJECT.star")); err == nil {
		return fmt.Errorf("project already exists at %s (PROJECT.star found)", projectDir)
	}

	dirs := []string{"machines", "units", "classes", "overlays"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(projectDir, dir), 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	name := filepath.Base(projectDir)
	defaultMachine := machine
	if defaultMachine == "" {
		defaultMachine = "qemu-x86_64"
	}

	projectContent := fmt.Sprintf(`project(
    name = %q,
    version = "0.1.0",
    defaults = defaults(machine = %q, image = "base-image"),
    repository = repository(path = "/var/cache/yoe-ng/repo"),
    cache = cache(path = "/var/cache/yoe-ng/build"),
    sources = sources(go_proxy = "https://proxy.golang.org"),
)
`, name, defaultMachine)

	if err := os.WriteFile(filepath.Join(projectDir, "PROJECT.star"), []byte(projectContent), 0644); err != nil {
		return fmt.Errorf("writing PROJECT.star: %w", err)
	}

	if machine != "" {
		if err := createMachineFile(projectDir, machine); err != nil {
			return err
		}
	}

	fmt.Printf("Created Yoe-NG project at %s\n", projectDir)
	return nil
}

func createMachineFile(projectDir, name string) error {
	var content string

	switch {
	case name == "qemu-x86_64" || name == "x86_64":
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "x86_64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyS0 root=/dev/vda2 rw"),
    qemu = qemu_config(machine = "q35", cpu = "host", memory = "1G", firmware = "ovmf", display = "none"),
)
`, name)
	case name == "qemu-arm64" || name == "aarch64":
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "arm64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyAMA0 root=/dev/vda2 rw"),
    qemu = qemu_config(machine = "virt", cpu = "host", memory = "1G", firmware = "aavmf", display = "none"),
)
`, name)
	case name == "qemu-riscv64" || name == "riscv64":
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "riscv64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyS0 root=/dev/vda2 rw"),
    qemu = qemu_config(machine = "virt", cpu = "host", memory = "1G", firmware = "opensbi", display = "none"),
)
`, name)
	default:
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "arm64",
    description = "",
)
`, name)
	}

	path := filepath.Join(projectDir, "machines", name+".star")
	return os.WriteFile(path, []byte(content), 0644)
}
```

- [x] **Step 3: Wire up in main.go**

Update `cmd/yoe/main.go` — replace the `cmdInit` stub with a call to
`internal.RunInit`. Parse `--machine` flag from args.

- [x] **Step 4: Run tests**

```bash
go test ./internal/ -run TestRunInit -v
```

Expected: All 3 tests PASS.

- [x] **Step 5: Commit**

```bash
git add internal/init.go internal/init_test.go cmd/yoe/main.go
git commit -m "feat: implement yoe init with Starlark project scaffolding"
```

---

### Task 7: `yoe config` Command

**Files:**

- Create: `internal/config/show.go`
- Modify: `cmd/yoe/main.go` — wire up cmdConfig

- [x] **Step 1: Write the implementation**

`yoe config show` loads the project via the Starlark loader and prints the
resolved configuration. `yoe config set` is deferred — Starlark files are not
trivially patchable like key-value config files. For now, `config set` prints a
message directing the user to edit `PROJECT.star` directly.

Create `internal/config/show.go`:

```go
package config

import (
	"fmt"

	yoestar "github.com/YoeDistro/yoe-ng/internal/starlark"
)

func ShowConfig(dir string) error {
	proj, err := yoestar.LoadProject(dir)
	if err != nil {
		return err
	}

	fmt.Printf("Project:    %s %s\n", proj.Name, proj.Version)
	fmt.Printf("Machine:    %s (default)\n", proj.Defaults.Machine)
	fmt.Printf("Image:      %s (default)\n", proj.Defaults.Image)
	fmt.Printf("Repository: %s\n", proj.Repository.Path)
	fmt.Printf("Cache:      %s\n", proj.Cache.Path)
	fmt.Printf("Machines:   %d defined\n", len(proj.Machines))
	fmt.Printf("Units:    %d defined\n", len(proj.Units))

	if len(proj.Machines) > 0 {
		fmt.Println("\nMachines:")
		for name, m := range proj.Machines {
			fmt.Printf("  %-20s %s\n", name, m.Arch)
		}
	}

	if len(proj.Units) > 0 {
		fmt.Println("\nRecipes:")
		for name, r := range proj.Units {
			fmt.Printf("  %-20s [%s] %s\n", name, r.Class, r.Version)
		}
	}

	return nil
}
```

- [x] **Step 2: Wire up in main.go and test manually**

```bash
go build -o yoe ./cmd/yoe
cd testdata/valid-project && ../../yoe config show
```

Expected: prints project name, machines, units.

- [x] **Step 3: Commit**

```bash
git add internal/config/show.go cmd/yoe/main.go
git commit -m "feat: implement yoe config show via Starlark project loader"
```

---

### Task 8: `yoe clean` Command

**Files:**

- Create: `internal/clean.go`
- Create: `internal/clean_test.go`
- Modify: `cmd/yoe/main.go` — wire up cmdClean

- [x] **Step 1: Write the implementation**

Create `internal/clean.go`:

```go
package internal

import (
	"fmt"
	"os"
	"path/filepath"
)

func RunClean(projectDir string, all bool, units []string) error {
	buildDir := filepath.Join(projectDir, "build")

	if len(units) > 0 {
		for _, r := range units {
			dir := filepath.Join(buildDir, r)
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("removing %s: %w", dir, err)
			}
			fmt.Printf("Cleaned %s\n", r)
		}
		return nil
	}

	if all {
		dirs := []string{buildDir, filepath.Join(projectDir, "repo")}
		for _, dir := range dirs {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("removing %s: %w", dir, err)
			}
		}
		fmt.Println("Cleaned all build artifacts, packages, and sources")
	} else {
		if err := os.RemoveAll(buildDir); err != nil {
			return fmt.Errorf("removing %s: %w", buildDir, err)
		}
		fmt.Println("Cleaned build intermediates (packages preserved)")
	}

	return nil
}
```

- [x] **Step 2: Write test, wire up, commit**

```bash
git add internal/clean.go internal/clean_test.go cmd/yoe/main.go
git commit -m "feat: implement yoe clean command"
```

---

### Task 9: Run All Phase 1 Tests

- [x] **Step 1: Run full test suite**

```bash
go test ./... -v
```

Expected: All tests PASS across all packages.

- [x] **Step 2: Build and smoke test**

```bash
go build -o yoe ./cmd/yoe
./yoe version
./yoe init /tmp/test-yoe-project --machine qemu-x86_64
cat /tmp/test-yoe-project/PROJECT.star
ls /tmp/test-yoe-project/machines/
cd /tmp/test-yoe-project && /scratch4/yoe/yoe-ng/yoe config show
rm -rf /tmp/test-yoe-project
```

Expected: All commands succeed.

- [x] **Step 3: Commit any fixes from integration testing**

```bash
git add -A
git commit -m "fix: integration test fixes for phase 1"
```

(Skip if no fixes needed.)

---

## Phase 2.5: Module Management & Engine Changes (detailed plan TBD)

**Goal:** Fetch, cache, and resolve external modules declared in `PROJECT.star`
and their transitive dependencies from `MODULE.star` files. Also make the engine
changes required to support Starlark-based classes in modules.

See
[module-core Module Design](../specs/2026-03-26-recipes-core-layer-design.md)
for the full specification of what the base module contains and the engine
changes needed.

**Key components:**

- `internal/module/fetch.go` — Git clone/fetch modules into
  `$YOE_CACHE/modules/`
- `internal/module/resolve.go` — resolve transitive deps from MODULE.star,
  version conflict resolution (PROJECT.star wins, then highest semver)
- `internal/module/cache.go` — bare Git repo caching with worktree checkouts at
  pinned refs
- `internal/module/local.go` — local override support (skip fetch, use local
  dir)
- `cmd/yoe/main.go` — `yoe module sync`, `yoe module list --tree`,
  `yoe module info`, `yoe module check-updates`
- Update `internal/starlark/loader.go` — resolve `@module-name//...` load()
  references to cached module paths

**Engine changes for class support:**

- Remove Go builtins for classes (`autotools()`, `cmake()`, `go_binary()`) —
  these become Starlark functions in module `.star` files loaded via `load()`
- Implement `load()` resolution: `//path` relative to current file's module
  root, `@module-name//path` relative to named module's root
- Add `package_extend()` primitive for modifier classes like `systemd_service()`
- Add `bootstrap` flag to `unit()` for stage 0/1 toolchain units
- Change unit discovery from `units/*.star` to `units/**/*.star` for categorized
  subdirectories

**Depends on:** Phase 1 (ModuleRef/ModuleInfo types, Starlark engine, project
loader)

**v1 behavior:** `yoe module sync` reads MODULE.star from each module and errors
if transitive deps are missing from PROJECT.star. This is explicit and
debuggable.

**v2 behavior (future):** Transitive deps are fetched automatically when not
overridden by PROJECT.star. Diamond dependencies resolve to the highest semver.

---

## Phase 2: Dependency Resolution — DONE

**Implemented:**

- `internal/resolve/dag.go` — DAG construction from units, Kahn's algorithm
  topological sort with cycle detection, transitive dep/rdep queries
- `internal/resolve/hash.go` — SHA256 content-addressed cache key computation
  (unit + source + patches + dep hashes + arch)
- `internal/resolve/describe.go` — `yoe desc`, `yoe refs`, `yoe graph` (text and
  DOT format)
- 11 tests for DAG, topo sort, cycles, hashing, hash cascading

**Also implemented beyond original plan:**

- `yoe dev` — source modification workflow (extract patches, diff, status)
- `yoe` custom commands — extensible CLI via `commands/*.star` with `command()`,
  `arg()`, `run(ctx)` pattern
- Patches support — `patches` field in units, included in cache hash

---

## Phase 3: Source Management (detailed plan TBD)

**Goal:** Download, cache, and verify source archives and git repos. Source
directories are git repos with an `upstream` tag so that `yoe dev` can detect
local modifications and extract patches.

**Key components:**

- `internal/source/fetch.go` — HTTP downloads + git clones
- `internal/source/cache.go` — content-addressed source cache
  ($YOE_CACHE/sources/)
- `internal/source/verify.go` — SHA256 verification
- `internal/source/patch.go` — apply unit patches as git commits on top of
  upstream tag
- `cmd/yoe/main.go` — add `source` command with subcommands to switch statement

**Integration with `yoe dev`:** After fetching source, the build directory
(`build/<unit>/src/`) is a git repo with the `upstream` tag. Existing patches
from the unit are applied as individual commits. If the developer has local
commits beyond upstream, `yoe build` uses them directly (skips re-fetch).
`yoe dev extract` runs `git format-patch upstream..HEAD` to produce patch files.

**Depends on:** Phase 1 (unit types for source URLs), Phase 2 (for patch field
in units)

---

## Phase 4: Build Execution (detailed plan TBD)

**Goal:** Execute unit build steps inside bubblewrap sandboxes. Detect local
source modifications (`yoe dev`) and skip re-fetch when present.

**Key components:**

- `internal/build/sandbox.go` — bubblewrap wrapper (namespace setup, bind
  mounts)
- `internal/build/environment.go` — build environment assembly (apk install of
  build deps)
- `internal/build/executor.go` — build step execution with logging; detect
  `build/<unit>/src/` with local commits and skip fetch/patch when present
- `internal/build/cache.go` — content-addressed build cache using hashes from
  Phase 2
- `cmd/yoe/main.go` — add `build` command to switch statement

**Depends on:** Phase 2 (DAG + hashing), Phase 3 (source fetching + patching)
**System requirements:** Linux with bubblewrap, apk-tools

---

## Phase 5: Package Creation, Repository & Cache (detailed plan TBD)

**Goal:** Create .apk artifacts from build output, manage a local repository,
and provide S3-compatible remote cache for sharing builds across CI/team.

**Key components:**

- `internal/packaging/apk.go` — .apk archive creation (.PKGINFO generation,
  tar.gz packaging)
- `internal/packaging/sign.go` — package signing
- `internal/repo/local.go` — local repository management (index, add, remove)
- `internal/repo/remote.go` — S3-compatible push/pull for repo
- `internal/cache/local.go` — local content-addressed build cache
- `internal/cache/remote.go` — S3-compatible remote cache (push/pull/gc)
- `internal/cache/sign.go` — cache package signing and verification
- `cmd/yoe/main.go` — add `repo` and `cache` commands to switch statement

**Depends on:** Phase 4 (build output in $DESTDIR)

---

## Phase 6: Image Assembly (detailed plan TBD)

**Goal:** Implement the image unit build path — when `yoe build` encounters a
unit with `image()` class, assemble a bootable disk image from packages instead
of compiling source code. No separate `yoe image` command; images are built
through the same `yoe build` pipeline.

**Key components:**

- `internal/image/rootfs.go` — rootfs creation via apk add --root
- `internal/image/configure.go` — hostname, timezone, locale, service enablement
- `internal/image/overlay.go` — overlay file copying
- `internal/image/disk.go` — partition table creation, filesystem formatting
- `internal/image/kernel.go` — kernel + bootloader installation
- `internal/build/executor.go` — extend to dispatch image units to image
  assembly instead of sandbox build

**Depends on:** Phase 5 (populated package repository) **System requirements:**
user namespaces (bubblewrap), mkfs.ext4, mkfs.vfat, systemd-repart

---

## Phase 7: Device Interaction (detailed plan TBD)

**Goal:** Flash images to devices and run in QEMU.

**Key components:**

- `internal/device/flash.go` — block device writing with safety checks (mounted?
  system disk?)
- `internal/device/qemu.go` — QEMU launch configuration (KVM, port forwarding,
  serial console)
- `cmd/yoe/main.go` — add `flash` and `run` commands to switch statement

**Depends on:** Phase 6 (disk images to flash/run)

---

## Phase 8: TUI (detailed plan TBD)

**Goal:** Interactive terminal UI for common workflows.

**Key components:**

- `internal/tui/app.go` — Bubble Tea application model
- `internal/tui/views/` — machine selector, build progress, log viewer
- `cmd/yoe/main.go` — add `tui` command to switch statement

**Depends on:** Phase 2 (unit/machine listing), can start after Phase 2

---

## Phase 2.6: module-core Module Phase 1 — Skeleton + Classes + Toolchain (detailed plan TBD)

**Goal:** Create the `modules/module-core/` directory with the module manifest,
all Starlark class files, and the toolchain/build-tool units. This is the
foundation that all other units build on.

See
[module-core Module Design](../specs/2026-03-26-recipes-core-layer-design.md)
for the full specification.

**Key deliverables:**

- `modules/module-core/MODULE.star` — module manifest
- 10 class files: `autotools`, `cmake`, `meson`, `go`, `rust`, `python`, `node`,
  `image`, `sdk`, `systemd`
- Toolchain units: `gcc`, `binutils`, `glibc`, `linux-headers`
- Build tool units: `make`, `pkg-config`, `autoconf`, `automake`, `libtool`,
  `cmake` (the package), `meson`, `ninja`

**Depends on:** Phase 2.5 (load() resolution, class builtins removed, recursive
unit discovery)

**Deliverable:** Can build C/C++ packages from source inside a Yoe-NG build
root.

---

## Phase 2.7: module-core Module Phase 2 — Base System + QEMU Machines (detailed plan TBD)

**Goal:** Add the base system packages, kernel, bootloaders, QEMU machine
definitions, and image units needed to produce a bootable system.

**Key deliverables:**

- Base units: `busybox`, `systemd`, `util-linux`, `kmod`, `apk-tools`,
  `bubblewrap`
- Kernel unit: `linux`
- Bootloader units: `u-boot`, `ovmf`, `opensbi`
- Machine definitions: `qemu-x86_64`, `qemu-arm64`, `qemu-riscv64`
- Image units: `base-image`, `dev-image`

**Depends on:** Phase 2.6 (toolchain units), Phase 6 (image assembly engine)

**Deliverable:** `yoe build base-image --machine qemu-x86_64` produces a
bootable image.

---

## Phase 2.8: module-core Module Phase 3 — Essential Libs + Networking (detailed plan TBD)

**Goal:** Add the libraries and networking packages that most embedded projects
need.

**Key deliverables:**

- Compression: `zlib`, `xz`, `zstd`, `bzip2`
- Crypto/TLS: `openssl`, `ca-certificates`
- Core libs: `libffi`, `ncurses`, `readline`, `expat`, `gmp`, `mpfr`
- Networking: `openssh`, `curl`, `networkmanager`, `dbus`, `iproute2`
- Debug tools: `gdb`, `strace`, `tcpdump`, `vim`

**Depends on:** Phase 2.7 (base system)

**Deliverable:** A practical embedded image with SSH, network management, and
TLS.

---

## Phase 9: Bootstrap (detailed plan TBD)

**Goal:** Self-hosting bootstrap from Alpine — build glibc, gcc, binutils from
an existing toolchain, then rebuild with own toolchain.

**Key components:**

- `internal/bootstrap/stage0.go` — cross-pollination from Alpine
- `internal/bootstrap/stage1.go` — self-hosting rebuild
- `cmd/yoe/main.go` — add `bootstrap` command to switch statement
- Bootstrap unit set from `modules/module-core/units/toolchain/` — units with
  `bootstrap = True` (glibc, binutils, gcc, linux-headers) plus base units
  (busybox, apk-tools, bubblewrap)

**Depends on:** Phase 5 (package creation and repository), Phase 2.6 (toolchain
units exist in module-core)

**This is the most complex phase** — building a C library and compiler toolchain
is non-trivial. Consider starting with pre-built packages and implementing
bootstrap last.
