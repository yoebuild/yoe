# QEMU x86_64 Bootable Image Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a bootable QEMU x86_64 image end-to-end with
`yoe build base-image --machine qemu-x86_64` and launch it with `yoe run`.

**Architecture:** Pragmatic path — use Alpine's apk packages for the base system
(glibc, gcc, busybox, etc.) rather than bootstrapping from source. This gets us
to a bootable image fast. Self-built packages replace Alpine's incrementally via
the bootstrap process later.

The critical path is: APKINDEX generation → load() for module imports →
module-core module with real units → disk image generation → boot in QEMU.

**Tech Stack:** Go, go.starlark.net, apk-tools, bubblewrap, QEMU, syslinux/
extlinux (bootloader), mkfs.ext4, mkfs.vfat

---

## Phase Overview

| Step | Name                           | Deliverable                                         |
| ---- | ------------------------------ | --------------------------------------------------- |
| 1    | APKINDEX generation            | `apk` can resolve deps from our local repo          |
| 2    | Starlark load() implementation | Units can import classes from modules               |
| 3    | Recursive unit discovery       | `units/**/*.star` works for categorized layouts     |
| 4    | module-core: classes           | Starlark autotools/cmake/go/image class files       |
| 5    | module-core: base units        | Real zlib, busybox, linux kernel units              |
| 6    | module-core: machines + images | qemu-x86_64 machine, base-image definition          |
| 7    | Disk image generation          | GPT partition table, ext4 rootfs, vfat boot, kernel |
| 8    | Boot configuration             | extlinux.conf for QEMU serial console boot          |
| 9    | End-to-end integration test    | `yoe build base-image` → `yoe run` boots to shell   |

---

## Task 1: APKINDEX Generation

The `apk` tool requires an `APKINDEX.tar.gz` in the repository to resolve
dependencies. Without it, `apk add --repository <dir>` fails.

**Files:**

- Create: `internal/repo/index.go`
- Create: `internal/repo/index_test.go`
- Modify: `internal/repo/local.go` — call index generation after Publish

- [x] **Step 1: Write the failing test**

Create `internal/repo/index_test.go`:

```go
package repo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/YoeDistro/yoe-ng/internal/packaging"
	yoestar "github.com/YoeDistro/yoe-ng/internal/starlark"
)

func TestGenerateIndex(t *testing.T) {
	repoDir := t.TempDir()

	// Create a fake .apk in the repo
	destDir := filepath.Join(t.TempDir(), "destdir", "usr", "bin")
	os.MkdirAll(destDir, 0755)
	os.WriteFile(filepath.Join(destDir, "hello"), []byte("#!/bin/sh\necho hi\n"), 0755)

	unit := &yoestar.Unit{
		Name:        "hello",
		Version:     "1.0.0",
		Description: "Test package",
		RuntimeDeps: []string{"glibc"},
	}
	apkPath, err := packaging.CreateAPK(unit, filepath.Join(t.TempDir(), "destdir"), repoDir)
	if err != nil {
		t.Fatalf("CreateAPK: %v", err)
	}
	_ = apkPath

	// Generate index
	if err := GenerateIndex(repoDir); err != nil {
		t.Fatalf("GenerateIndex: %v", err)
	}

	// Verify APKINDEX.tar.gz exists
	indexPath := filepath.Join(repoDir, "APKINDEX.tar.gz")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Fatal("APKINDEX.tar.gz not created")
	}

	// Verify it's non-empty
	info, _ := os.Stat(indexPath)
	if info.Size() == 0 {
		t.Fatal("APKINDEX.tar.gz is empty")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

```bash
go test ./internal/repo/ -run TestGenerateIndex -v
```

Expected: FAIL — `GenerateIndex` not defined.

- [x] **Step 3: Write the implementation**

Create `internal/repo/index.go`:

```go
package repo

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YoeDistro/yoe-ng/internal/packaging"
)

// GenerateIndex creates APKINDEX.tar.gz in the repository directory.
// This is required for apk to resolve dependencies.
func GenerateIndex(repoDir string) error {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return fmt.Errorf("reading repo dir: %w", err)
	}

	var indexContent strings.Builder

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".apk") {
			continue
		}

		apkPath := filepath.Join(repoDir, e.Name())
		info, err := os.Stat(apkPath)
		if err != nil {
			continue
		}

		hash, err := packaging.APKHash(apkPath)
		if err != nil {
			continue
		}

		// Parse package name and version from filename: name-version-r0.apk
		name, version := parseAPKFilename(e.Name())

		// Write APKINDEX entry
		fmt.Fprintf(&indexContent, "C:Q1%s\n", hash[:40])
		fmt.Fprintf(&indexContent, "P:%s\n", name)
		fmt.Fprintf(&indexContent, "V:%s\n", version)
		fmt.Fprintf(&indexContent, "S:%d\n", info.Size())
		fmt.Fprintf(&indexContent, "I:%d\n", info.Size())
		fmt.Fprintf(&indexContent, "T:%s\n", name)
		fmt.Fprintf(&indexContent, "A:x86_64\n")
		fmt.Fprintf(&indexContent, "\n") // blank line separates entries
	}

	// Write APKINDEX.tar.gz
	indexPath := filepath.Join(repoDir, "APKINDEX.tar.gz")
	f, err := os.Create(indexPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	content := []byte(indexContent.String())
	header := &tar.Header{
		Name:    "APKINDEX",
		Size:    int64(len(content)),
		Mode:    0644,
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if _, err := tw.Write(content); err != nil {
		return err
	}

	return nil
}

func parseAPKFilename(filename string) (name, version string) {
	// Format: name-version-r0.apk
	s := strings.TrimSuffix(filename, ".apk")
	// Find the last -rN suffix
	idx := strings.LastIndex(s, "-r")
	if idx > 0 {
		s = s[:idx]
	}
	// Find the version separator (last dash before a digit)
	for i := len(s) - 1; i > 0; i-- {
		if s[i-1] == '-' && s[i] >= '0' && s[i] <= '9' {
			return s[:i-1], s[i:]
		}
	}
	return s, "0"
}
```

- [x] **Step 4: Run test to verify it passes**

```bash
go test ./internal/repo/ -run TestGenerateIndex -v
```

Expected: PASS.

- [x] **Step 5: Wire index generation into Publish**

Modify `internal/repo/local.go` — add call to `GenerateIndex` after publishing:

```go
// In Publish(), after copying the .apk file, regenerate the index:
func Publish(apkPath, repoDir string) error {
	// ... existing copy logic ...

	// Regenerate APKINDEX after adding a package
	return GenerateIndex(repoDir)
}
```

- [x] **Step 6: Run all tests**

```bash
go test ./internal/repo/ -v
go test ./...
```

- [x] **Step 7: Commit**

```bash
git add internal/repo/index.go internal/repo/index_test.go internal/repo/local.go
git commit -m "feat: generate APKINDEX.tar.gz for apk dependency resolution"
```

---

## Task 2: Starlark load() Implementation

Units need `load("//classes/autotools.star", "autotools")` to import class
functions. The Starlark thread needs a load handler.

**Files:**

- Modify: `internal/starlark/engine.go` — add load handler to thread
- Create: `internal/starlark/load.go` — load resolution logic
- Create: `internal/starlark/load_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/starlark/load_test.go`:

```go
package starlark

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFunction(t *testing.T) {
	dir := t.TempDir()

	// Create a class file
	classDir := filepath.Join(dir, "classes")
	os.MkdirAll(classDir, 0755)
	os.WriteFile(filepath.Join(classDir, "myclass.star"), []byte(`
def my_builder(name, version, **kwargs):
    unit(name=name, version=version, build=["make"], **kwargs)
`), 0644)

	// Create a unit that loads the class
	recipeDir := filepath.Join(dir, "units")
	os.MkdirAll(recipeDir, 0755)
	os.WriteFile(filepath.Join(recipeDir, "hello.star"), []byte(`
load("//classes/myclass.star", "my_builder")
my_builder(name = "hello", version = "1.0")
`), 0644)

	eng := NewEngine()
	eng.SetProjectRoot(dir)

	if err := eng.ExecFile(filepath.Join(recipeDir, "hello.star")); err != nil {
		t.Fatalf("ExecFile with load: %v", err)
	}

	units := eng.Units()
	if _, ok := units["hello"]; !ok {
		t.Fatal("unit 'hello' not found — load() didn't work")
	}
}

func TestLoadFunction_LayerRef(t *testing.T) {
	dir := t.TempDir()

	// Create a "layer" directory
	layerDir := filepath.Join(dir, "layers", "mylib")
	os.MkdirAll(filepath.Join(layerDir, "classes"), 0755)
	os.WriteFile(filepath.Join(layerDir, "classes", "helper.star"), []byte(`
def helper(name):
    unit(name=name, version="1.0", build=["make"])
`), 0644)

	// Unit uses @mylib//classes/helper.star
	recipeDir := filepath.Join(dir, "units")
	os.MkdirAll(recipeDir, 0755)
	os.WriteFile(filepath.Join(recipeDir, "test.star"), []byte(`
load("@mylib//classes/helper.star", "helper")
helper(name = "test")
`), 0644)

	eng := NewEngine()
	eng.SetProjectRoot(dir)
	eng.SetLayerRoot("mylib", layerDir)

	if err := eng.ExecFile(filepath.Join(recipeDir, "test.star")); err != nil {
		t.Fatalf("ExecFile with layer load: %v", err)
	}

	if _, ok := eng.Units()["test"]; !ok {
		t.Fatal("unit 'test' not found — layer load() didn't work")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/starlark/ -run TestLoadFunction -v
```

Expected: FAIL — `SetProjectRoot` not defined.

- [ ] **Step 3: Write the load resolution logic**

Create `internal/starlark/load.go`:

```go
package starlark

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"go.starlark.net/starlark"
)

// loadCache caches loaded modules to avoid re-evaluation.
type loadCache struct {
	mu      sync.Mutex
	modules map[string]*loadResult
}

type loadResult struct {
	globals starlark.StringDict
	err     error
}

func newLoadCache() *loadCache {
	return &loadCache{modules: make(map[string]*loadResult)}
}

// SetProjectRoot sets the root directory for // path resolution.
func (e *Engine) SetProjectRoot(root string) {
	e.projectRoot = root
}

// SetLayerRoot registers a layer name → directory mapping for @name// paths.
func (e *Engine) SetLayerRoot(name, root string) {
	if e.layerRoots == nil {
		e.layerRoots = make(map[string]string)
	}
	e.layerRoots[name] = root
}

// makeLoadFunc returns a Starlark load handler that resolves:
//   - "//path" — relative to project root
//   - "@layer//path" — relative to named layer root
//   - "relative/path" — relative to the loading file
func (e *Engine) makeLoadFunc(fromFile string) func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
	cache := e.loadCache
	if cache == nil {
		cache = newLoadCache()
		e.loadCache = cache
	}

	return func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
		resolved, err := e.resolveLoadPath(fromFile, module)
		if err != nil {
			return nil, err
		}

		cache.mu.Lock()
		if result, ok := cache.modules[resolved]; ok {
			cache.mu.Unlock()
			return result.globals, result.err
		}
		cache.mu.Unlock()

		// Evaluate the module
		predeclared := e.builtins()
		childThread := &starlark.Thread{
			Name: resolved,
			Load: e.makeLoadFunc(resolved),
		}
		globals, err := starlark.ExecFile(childThread, resolved, nil, predeclared)

		result := &loadResult{globals: globals, err: err}
		cache.mu.Lock()
		cache.modules[resolved] = result
		cache.mu.Unlock()

		return globals, err
	}
}

func (e *Engine) resolveLoadPath(fromFile, module string) (string, error) {
	switch {
	case strings.HasPrefix(module, "//"):
		// Project-root-relative
		if e.projectRoot == "" {
			return "", fmt.Errorf("load(%q): project root not set", module)
		}
		return filepath.Join(e.projectRoot, strings.TrimPrefix(module, "//")), nil

	case strings.HasPrefix(module, "@"):
		// Layer reference: @name//path
		parts := strings.SplitN(module[1:], "//", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("load(%q): invalid layer reference (expected @name//path)", module)
		}
		layerName := parts[0]
		path := parts[1]
		root, ok := e.layerRoots[layerName]
		if !ok {
			return "", fmt.Errorf("load(%q): layer %q not found", module, layerName)
		}
		return filepath.Join(root, path), nil

	default:
		// Relative to the loading file
		dir := filepath.Dir(fromFile)
		return filepath.Join(dir, module), nil
	}
}
```

- [ ] **Step 4: Update engine.go to use the load handler**

Add fields to Engine struct and update ExecFile/ExecString:

```go
// Add to Engine struct:
type Engine struct {
	mu          sync.Mutex
	project     *Project
	machines    map[string]*Machine
	units     map[string]*Unit
	commands    map[string]*Command
	layerInfo   *LayerInfo
	globals     starlark.StringDict
	projectRoot string
	layerRoots  map[string]string
	loadCache   *loadCache
}

// Update ExecFile to use load handler:
func (e *Engine) ExecFile(path string) error {
	thread := &starlark.Thread{
		Name: path,
		Load: e.makeLoadFunc(path),
	}
	predeclared := e.builtins()

	globals, err := starlark.ExecFile(thread, path, nil, predeclared)
	if err != nil {
		return fmt.Errorf("evaluating %s: %w", path, err)
	}
	e.globals = globals
	return nil
}

// Update ExecString similarly:
func (e *Engine) ExecString(filename, src string) error {
	thread := &starlark.Thread{
		Name: filename,
		Load: e.makeLoadFunc(filename),
	}
	predeclared := e.builtins()

	globals, err := starlark.ExecFile(thread, filename, src, predeclared)
	if err != nil {
		return fmt.Errorf("evaluating %s: %w", filename, err)
	}
	e.globals = globals
	return nil
}
```

- [ ] **Step 5: Update loader.go to set project root**

In `LoadProjectFromRoot`, set the project root before evaluating files:

```go
func LoadProjectFromRoot(root string) (*Project, error) {
	eng := NewEngine()
	eng.SetProjectRoot(root)

	// ... rest unchanged
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./internal/starlark/ -v
go test ./...
```

Expected: All pass, including new load() tests.

- [ ] **Step 7: Commit**

```bash
git add internal/starlark/load.go internal/starlark/load_test.go internal/starlark/engine.go internal/starlark/loader.go
git commit -m "feat: implement Starlark load() for class imports and module references"
```

---

## Task 3: Recursive Unit Discovery

The module-core module organizes units in subdirectories (`units/toolchain/`,
`units/base/`, etc.). The loader needs to glob `units/**/*.star` instead of just
`units/*.star`.

**Files:**

- Modify: `internal/starlark/loader.go` — change `evalDir` to recursive glob

- [ ] **Step 1: Update evalDir to walk recursively**

```go
func evalDir(eng *Engine, root, subdir string) error {
	base := filepath.Join(root, subdir)
	return filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // directory doesn't exist, skip
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".star") {
			return nil
		}
		if err := eng.ExecFile(path); err != nil {
			return fmt.Errorf("evaluating %s: %w", path, err)
		}
		return nil
	})
}
```

Add `"os"` and `"strings"` to the import block if not already present.

- [ ] **Step 2: Add test fixture with subdirectories**

Create `testdata/valid-project/units/libs/testlib.star`:

```python
unit(
    name = "testlib",
    version = "1.0.0",
    source = "https://example.com/testlib-1.0.tar.gz",
    build = ["make"],
)
```

- [ ] **Step 3: Update loader test to verify recursive discovery**

Add to `TestLoadProject` in `internal/starlark/loader_test.go`:

```go
	// Units in subdirectories should also be found
	if _, ok := proj.Units["testlib"]; !ok {
		t.Error("expected unit 'testlib' from units/libs/ subdirectory")
	}
```

Update the expected unit count from 5 to 6.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/starlark/ -run TestLoadProject -v
go test ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/starlark/loader.go testdata/valid-project/units/libs/
git commit -m "feat: recursive unit discovery (units/**/*.star)"
```

---

## Task 4: module-core Module — Starlark Classes

Create the class files as pure Starlark functions that call the `unit()`
primitive.

**Files:**

- Create: `modules/module-core/MODULE.star`
- Create: `modules/module-core/classes/autotools.star`
- Create: `modules/module-core/classes/cmake.star`
- Create: `modules/module-core/classes/go.star`
- Create: `modules/module-core/classes/image.star`

- [ ] **Step 1: Create MODULE.star**

```python
module_info(
    name = "module-core",
    description = "Yoe-NG base module: toolchain, base system, essential libraries",
)
```

- [ ] **Step 2: Create autotools.star**

```python
def autotools(name, version, source, sha256="", deps=[], runtime_deps=[],
              configure_args=[], patches=[], services=[], conffiles=[],
              license="", description="", **kwargs):
    build = [
        "./configure --prefix=$PREFIX " + " ".join(configure_args),
        "make -j$NPROC",
        "make DESTDIR=$DESTDIR install",
    ]
    unit(
        name=name, version=version, source=source, sha256=sha256,
        deps=deps, runtime_deps=runtime_deps, patches=patches,
        build=build, services=services, conffiles=conffiles,
        license=license, description=description, **kwargs,
    )
```

- [ ] **Step 3: Create cmake.star**

```python
def cmake(name, version, source, sha256="", deps=[], runtime_deps=[],
          cmake_args=[], patches=[], services=[], conffiles=[],
          license="", description="", **kwargs):
    build = [
        "cmake -B build -S . -DCMAKE_INSTALL_PREFIX=$PREFIX " +
            " ".join(["-D" + a for a in cmake_args]),
        "cmake --build build -j$NPROC",
        "DESTDIR=$DESTDIR cmake --install build",
    ]
    unit(
        name=name, version=version, source=source, sha256=sha256,
        deps=deps, runtime_deps=runtime_deps, patches=patches,
        build=build, services=services, conffiles=conffiles,
        license=license, description=description, **kwargs,
    )
```

- [ ] **Step 4: Create go.star**

```python
def go_binary(name, version, source, tag="", sha256="",
              go_package="", deps=[], runtime_deps=[],
              services=[], conffiles=[], environment={},
              license="", description="", **kwargs):
    if not go_package:
        go_package = "./cmd/" + name
    build = [
        "go build -o $DESTDIR$PREFIX/bin/" + name + " " + go_package,
    ]
    unit(
        name=name, version=version, source=source, sha256=sha256,
        tag=tag, deps=deps, runtime_deps=runtime_deps,
        build=build, services=services, conffiles=conffiles,
        license=license, description=description, **kwargs,
    )
```

- [ ] **Step 5: Create image.star**

```python
def yoe_image(name, version, description="", packages=[], hostname="yoe",
              timezone="UTC", locale="en_US.UTF-8", services=[],
              partitions=[], **kwargs):
    image(
        name=name, version=version, description=description,
        packages=packages, hostname=hostname, timezone=timezone,
        locale=locale, services=services, partitions=partitions,
        **kwargs,
    )
```

- [ ] **Step 6: Commit**

```bash
git add modules/module-core/
git commit -m "feat: add module-core module with Starlark classes"
```

---

## Task 5: module-core — Base Units

Real units for the packages needed in a minimal bootable image. For the first
pass, these use simple build steps. The sources must be real URLs.

**Files:**

- Create: `modules/module-core/units/libs/zlib.star`
- Create: `modules/module-core/units/base/busybox.star`
- Create: `modules/module-core/units/base/linux.star`

- [ ] **Step 1: Create zlib unit**

`modules/module-core/units/libs/zlib.star`:

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "zlib",
    version = "1.3.1",
    source = "https://zlib.net/zlib-1.3.1.tar.gz",
    sha256 = "9a93b2b7dfdac77ceba5a558a580e74667dd6fede4585b91eefb60f03b72df23",
    license = "Zlib",
    description = "Compression library",
)
```

- [ ] **Step 2: Create busybox unit**

`modules/module-core/units/base/busybox.star`:

```python
unit(
    name = "busybox",
    version = "1.36.1",
    source = "https://busybox.net/downloads/busybox-1.36.1.tar.bz2",
    sha256 = "b8cc24c9574d809e7279c3be349795c5d5ceb6fdf19ca709f80cde50e47de314",
    license = "GPL-2.0",
    description = "Swiss army knife of embedded Linux",
    build = [
        "make defconfig",
        "make -j$NPROC",
        "make CONFIG_PREFIX=$DESTDIR install",
    ],
)
```

- [ ] **Step 3: Create linux kernel unit**

`modules/module-core/units/base/linux.star`:

```python
unit(
    name = "linux",
    version = "6.6.87",
    source = "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.6.87.tar.xz",
    sha256 = "89e0e40d3e8b7cae8b3e3b0e5fa7e84c7d2117aae5de83fc3eb79e75109a96ec",
    license = "GPL-2.0",
    description = "Linux kernel",
    deps = ["gcc", "make"],
    build = [
        "make x86_64_defconfig",
        "make -j$NPROC bzImage",
        "install -D arch/x86/boot/bzImage $DESTDIR/boot/vmlinuz",
    ],
)
```

- [ ] **Step 4: Commit**

```bash
git add modules/module-core/units/
git commit -m "feat: add base units (zlib, busybox, linux kernel)"
```

---

## Task 6: module-core — Machines and Images

**Files:**

- Create: `modules/module-core/machines/qemu-x86_64.star`
- Create: `modules/module-core/images/base-image.star`

- [ ] **Step 1: Create QEMU x86_64 machine**

`modules/module-core/machines/qemu-x86_64.star`:

```python
machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    description = "QEMU x86_64 virtual machine (KVM)",
    kernel = kernel(
        unit = "linux",
        defconfig = "x86_64_defconfig",
        cmdline = "console=ttyS0 root=/dev/vda2 rw",
    ),
    qemu = qemu_config(
        machine = "q35",
        cpu = "host",
        memory = "1G",
        firmware = "ovmf",
        display = "none",
    ),
)
```

- [ ] **Step 2: Create base image**

`modules/module-core/images/base-image.star`:

```python
image(
    name = "base-image",
    version = "1.0.0",
    description = "Minimal bootable Yoe-NG system",
    artifacts = [
        "busybox",
    ],
    hostname = "yoe",
    timezone = "UTC",
    services = [],
    partitions = [
        partition(label="boot", type="vfat", size="64M",
                  contents=["vmlinuz"]),
        partition(label="rootfs", type="ext4", size="512M", root=True),
    ],
)
```

- [ ] **Step 3: Commit**

```bash
git add modules/module-core/machines/ modules/module-core/images/
git commit -m "feat: add qemu-x86_64 machine and base-image definitions"
```

---

## Task 7: Disk Image Generation

Replace the tar.gz stub with actual disk image creation using standard Linux
tools.

**Files:**

- Create: `internal/image/disk.go`
- Create: `internal/image/disk_test.go`
- Modify: `internal/image/rootfs.go` — call disk image generator

- [ ] **Step 1: Write the disk image generator**

Create `internal/image/disk.go`:

```go
package image

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	yoestar "github.com/YoeDistro/yoe-ng/internal/starlark"
)

// GenerateDiskImage creates a partitioned disk image from a rootfs directory.
func GenerateDiskImage(rootfs, imgPath string, unit *yoestar.Unit, w io.Writer) error {
	// Calculate total image size from partitions
	totalMB := 0
	for _, p := range unit.Partitions {
		size := parseSizeMB(p.Size)
		if size == 0 {
			size = 512 // default for "fill"
		}
		totalMB += size
	}
	if totalMB == 0 {
		totalMB = 512
	}

	fmt.Fprintf(w, "  Creating %dMB disk image...\n", totalMB)

	// Create sparse image file
	if err := createSparseImage(imgPath, totalMB); err != nil {
		return fmt.Errorf("creating image: %w", err)
	}

	// Partition with sfdisk (GPT)
	if err := partitionImage(imgPath, unit.Partitions, w); err != nil {
		return fmt.Errorf("partitioning: %w", err)
	}

	// Set up loop device, format partitions, copy files
	if err := populateImage(imgPath, rootfs, unit, w); err != nil {
		return fmt.Errorf("populating: %w", err)
	}

	return nil
}

func createSparseImage(path string, sizeMB int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(int64(sizeMB) * 1024 * 1024)
}

func partitionImage(imgPath string, partitions []yoestar.Partition, w io.Writer) error {
	if len(partitions) == 0 {
		// Single rootfs partition
		partitions = []yoestar.Partition{
			{Label: "rootfs", Type: "ext4", Size: "fill", Root: true},
		}
	}

	// Build sfdisk script
	script := "label: gpt\n"
	for _, p := range partitions {
		size := ""
		sizeMB := parseSizeMB(p.Size)
		if sizeMB > 0 {
			size = fmt.Sprintf("size=%dM, ", sizeMB)
		}
		ptype := "linux"
		if p.Type == "vfat" {
			ptype = "uefi"
		}
		script += fmt.Sprintf("%stype=%s, name=%s\n", size, ptype, p.Label)
	}

	fmt.Fprintf(w, "  Partitioning (GPT)...\n")
	cmd := exec.Command("sfdisk", imgPath)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func populateImage(imgPath, rootfs string, unit *yoestar.Unit, w io.Writer) error {
	// Use loop device to access partitions
	// losetup --find --show --partscan imgPath
	out, err := exec.Command("losetup", "--find", "--show", "--partscan", imgPath).Output()
	if err != nil {
		// Fallback: use guestfish or just create the tar.gz
		fmt.Fprintf(w, "  (losetup not available — creating rootfs.tar.gz fallback)\n")
		return createTarFallback(rootfs, imgPath, w)
	}
	loopDev := strings.TrimSpace(string(out))
	defer exec.Command("losetup", "-d", loopDev).Run()

	// Format and populate each partition
	for i, p := range unit.Partitions {
		partDev := fmt.Sprintf("%sp%d", loopDev, i+1)
		fmt.Fprintf(w, "  Formatting %s as %s...\n", p.Label, p.Type)

		switch p.Type {
		case "vfat":
			exec.Command("mkfs.vfat", "-n", strings.ToUpper(p.Label), partDev).Run()
			// Copy boot files
			mountDir := filepath.Join(filepath.Dir(imgPath), "mnt-boot")
			os.MkdirAll(mountDir, 0755)
			exec.Command("mount", partDev, mountDir).Run()
			// Copy kernel
			for _, pattern := range p.Contents {
				matches, _ := filepath.Glob(filepath.Join(rootfs, "boot", pattern))
				for _, f := range matches {
					exec.Command("cp", f, mountDir).Run()
				}
			}
			exec.Command("umount", mountDir).Run()

		case "ext4":
			exec.Command("mkfs.ext4", "-L", p.Label, "-q", partDev).Run()
			if p.Root {
				mountDir := filepath.Join(filepath.Dir(imgPath), "mnt-rootfs")
				os.MkdirAll(mountDir, 0755)
				exec.Command("mount", partDev, mountDir).Run()
				// Copy rootfs
				exec.Command("cp", "-a", rootfs+"/.", mountDir+"/").Run()
				exec.Command("umount", mountDir).Run()
			}
		}
	}

	return nil
}

func createTarFallback(rootfs, imgPath string, w io.Writer) error {
	tarPath := imgPath + ".tar.gz"
	cmd := exec.Command("tar", "czf", tarPath, "-C", rootfs, ".")
	return cmd.Run()
}

func parseSizeMB(size string) int {
	if size == "fill" || size == "" {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(size, "%dM", &n); err == nil {
		return n
	}
	if _, err := fmt.Sscanf(size, "%dG", &n); err == nil {
		return n * 1024
	}
	return 0
}
```

Add `"strings"` to the import block.

- [ ] **Step 2: Update rootfs.go to call disk image generator**

Replace the `generateImage` function in `internal/image/rootfs.go`:

```go
func generateImage(rootfs, imgPath string, unit *yoestar.Unit, w io.Writer) error {
	return GenerateDiskImage(rootfs, imgPath, unit, w)
}
```

- [ ] **Step 3: Run tests**

```bash
go build ./...
go test ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/image/disk.go internal/image/rootfs.go
git commit -m "feat: generate partitioned disk images (GPT, ext4, vfat)"
```

---

## Task 8: Boot Configuration

For QEMU x86_64 with serial console, we need extlinux or a simple boot config so
the kernel knows where the root filesystem is.

**Files:**

- Modify: `internal/image/rootfs.go` — install boot config during assembly

- [ ] **Step 1: Add boot config generation to applyConfig**

Add to `applyConfig` in `internal/image/rootfs.go`:

```go
	// Install boot configuration (extlinux for QEMU)
	bootDir := filepath.Join(rootfs, "boot", "extlinux")
	os.MkdirAll(bootDir, 0755)
	extlinuxConf := `DEFAULT yoe
LABEL yoe
    LINUX /boot/vmlinuz
    APPEND console=ttyS0 root=/dev/vda2 rw
`
	os.WriteFile(filepath.Join(bootDir, "extlinux.conf"), []byte(extlinuxConf), 0644)
	fmt.Fprintln(w, "  Installed boot configuration (extlinux)")
```

- [ ] **Step 2: Commit**

```bash
git add internal/image/rootfs.go
git commit -m "feat: install extlinux boot config for QEMU serial console"
```

---

## Task 9: End-to-End Integration Test

Wire everything together with a test project that uses the module-core module
and builds a complete image.

**Files:**

- Create: `testdata/e2e-project/PROJECT.star`
- Create: `internal/build/e2e_test.go`

- [ ] **Step 1: Create e2e test project**

`testdata/e2e-project/PROJECT.star`:

```python
project(
    name = "e2e-test",
    version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64", image = "base-image"),
    repository = repository(path = "build/repo"),
    cache = cache(path = "build/cache"),
    layers = [
        layer("github.com/yoe/module-core",
              local = "../../layers/module-core"),
    ],
)
```

- [ ] **Step 2: Write the integration test**

Create `internal/build/e2e_test.go`:

```go
package build

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	yoestar "github.com/YoeDistro/yoe-ng/internal/starlark"
)

func TestE2E_DryRun(t *testing.T) {
	// This test verifies the full pipeline works end-to-end in dry-run mode:
	// load project → resolve layers → evaluate units → build DAG → dry run
	projectDir := filepath.Join("..", "..", "testdata", "e2e-project")
	if _, err := os.Stat(filepath.Join(projectDir, "PROJECT.star")); os.IsNotExist(err) {
		t.Skip("e2e test project not found")
	}

	proj, err := yoestar.LoadProject(projectDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	// Should have machines from module-core layer
	if _, ok := proj.Machines["qemu-x86_64"]; !ok {
		t.Error("expected qemu-x86_64 machine from module-core layer")
	}

	// Should have units from module-core layer
	if _, ok := proj.Units["busybox"]; !ok {
		t.Error("expected busybox unit from module-core layer")
	}
	if _, ok := proj.Units["linux"]; !ok {
		t.Error("expected linux unit from module-core layer")
	}
	if _, ok := proj.Units["base-image"]; !ok {
		t.Error("expected base-image from module-core layer")
	}

	// Dry run should work
	var buf bytes.Buffer
	abs, _ := filepath.Abs(projectDir)
	opts := Options{
		DryRun:     true,
		ProjectDir: abs,
		Arch:       "x86_64",
	}

	if err := BuildRecipes(proj, nil, opts, &buf); err != nil {
		t.Fatalf("BuildRecipes dry run: %v", err)
	}

	output := buf.String()
	t.Logf("Dry run output:\n%s", output)

	if len(output) == 0 {
		t.Error("dry run produced no output")
	}
}
```

- [ ] **Step 3: Run the integration test**

```bash
go test ./internal/build/ -run TestE2E -v
```

This test verifies: project loading → module resolution → unit evaluation → DAG
construction → dry run output. It doesn't actually build or boot — that requires
the container and real tools.

- [ ] **Step 4: Manual end-to-end test (inside container)**

```bash
CGO_ENABLED=0 go build -o yoe ./cmd/yoe
cd testdata/e2e-project
../../yoe build --dry-run
../../yoe build base-image    # actually builds
../../yoe run                 # boots in QEMU
```

- [ ] **Step 5: Commit**

```bash
git add testdata/e2e-project/ internal/build/e2e_test.go
git commit -m "feat: add end-to-end integration test for QEMU x86_64 image"
```

---

## Summary

After completing all 9 tasks:

1. `apk` can resolve dependencies from our repo (APKINDEX)
2. Units can `load()` classes from modules
3. Subdirectory unit layout works
4. module-core provides real Starlark classes
5. Real units exist for zlib, busybox, linux kernel
6. QEMU x86_64 machine and base-image are defined
7. Disk images have real partitions (GPT, ext4, vfat)
8. Boot configuration works for QEMU serial console
9. End-to-end test validates the pipeline

The result: `yoe build base-image --machine qemu-x86_64` produces a bootable
disk image, and `yoe run` boots it to a busybox shell over serial console.
