# File Templates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move inline file content (heredocs) out of Starlark units into
external template files processed by Go's `text/template`, with `install_file`
and `install_template` as **step-value constructors** used directly in
`task(..., steps=[...])`.

**Architecture:** Install steps are pure data values produced at Starlark
evaluation time and executed by the build executor alongside shell strings and
Starlark callables. A new `InstallStepValue` Starlark type carries
`(Kind, Src, Dest, Mode)`. `ParseTaskList` recognises these values inside
`steps=[...]` and converts them to `Step{Install: &InstallStep{...}}`. The
executor dispatches on `step.Install != nil` and calls a Go helper
(`doInstallStep`) that resolves `<DefinedIn>/<unit-name>/<src>`, renders (for
templates) or copies (for static files), and writes to the env-expanded dest. No
thread-local context, no placeholder/delegate builtins.

**Tech Stack:** Go stdlib (`text/template`, `crypto/sha256`, `encoding/json`);
`go.starlark.net` for Starlark integration.

**Spec:** `docs/file-templates.md`

---

## Background: what is already committed (and must be reshaped)

An earlier revision of this plan used **side-effecting** install builtins that
read a per-thread `TemplateContext`. That revision's Tasks 1–5 are already
committed on this branch:

| Commit                | What landed                                                  | Under new design                                                             |
| --------------------- | ------------------------------------------------------------ | ---------------------------------------------------------------------------- |
| `bbda8c86`, `b37eb62` | `Unit.Extra map[string]any` + `registerUnit()` capture       | **Keep as-is**                                                               |
| `8ae9ac8`             | `TemplateContext` struct, `templateKey`, thread-local wiring | Remove `TemplateContext` struct / `templateKey`; keep `BuildTemplateContext` |
| `045ab0b`, `3bdb521`  | `fnInstallFile` side-effecting + path hardening              | Rewrite as `doInstallStep` helper (executor-called, not a builtin)           |
| `da8e2a8`             | `fnInstallTemplate` side-effecting                           | Same — fold into `doInstallStep`                                             |
| `6be8fe4`             | Executor attaches `TemplateContext` before task fn steps     | Replace with direct `step.Install` dispatch                                  |

So: `Extra` stays. `BuildTemplateContext` stays. Everything else in the
old-design install machinery gets rewritten in the first three tasks of this
revised plan.

---

## File Structure

**New:**

- `internal/starlark/install_step.go` — `InstallStepValue` Starlark type +
  `fnInstallFile` / `fnInstallTemplate` constructors
- `modules/module-core/units/base/base-files/inittab.tmpl`
- `modules/module-core/units/base/base-files/rcS`
- `modules/module-core/units/base/base-files/os-release.tmpl`
- `modules/module-core/units/base/base-files/extlinux.conf`
- `modules/module-core/units/net/network-config/udhcpc-default.script`
- `modules/module-core/units/net/network-config/S10network`
- `modules/module-core/units/net/simpleiot/simpleiot.init`

**Modified:**

- `internal/starlark/types.go` — add `InstallStep` Go struct + `Step.Install`
  field
- `internal/starlark/builtins.go` — drop placeholder `install_*` entries and
  `fnInstall*Placeholder` functions; extend `ParseTaskList` to recognise
  `*InstallStepValue`
- `internal/build/templates.go` — remove `TemplateContext` struct and
  `templateKey`; delete the side-effecting `fnInstallFile` / `fnInstallTemplate`
  builtins; add `doInstallStep` executor helper; keep `BuildTemplateContext`,
  `resolveTemplatePath`, `expandEnv`, `modeFromKwargs`
- `internal/build/starlark_exec.go` — remove `SetTemplateContext`; remove
  `install_*` entries from `BuildPredeclared`
- `internal/build/executor.go` — drop `SetTemplateContext` call; add step loop
  case for `step.Install`
- `internal/build/templates_test.go` — rewrite `TestFnInstallFile` /
  `TestFnInstallTemplate` to drive `doInstallStep` directly; keep
  `TestBuildTemplateContext` tests
- `internal/resolve/hash.go` — include `Extra` JSON and files-dir contents in
  unit hash
- `internal/resolve/hash_test.go` — tests for new hash inputs
- `modules/module-core/units/base/base-files.star` — step-value form
- `modules/module-core/units/net/network-config.star` — step-value form
- `modules/module-core/units/net/simpleiot.star` — step-value form
- `docs/file-templates.md` — remove `## Status: Spec`
- `CHANGELOG.md` — Unreleased entry

---

## Task 1: `InstallStepValue` Starlark type + constructors

Replace the placeholder/delegate `fnInstallFile` / `fnInstallTemplate` in
`internal/starlark/builtins.go` with real constructors that return an
`*InstallStepValue`. At the Starlark layer these become ordinary global builtins
— no side effects, no thread-local, no build-time-only gate.

**Files:**

- Create: `internal/starlark/install_step.go`
- Modify: `internal/starlark/types.go` (add `InstallStep` Go struct)
- Modify: `internal/starlark/builtins.go` (replace placeholders; register real
  builtins)
- Test: `internal/starlark/install_step_test.go` (new file)

### Design of `InstallStepValue`

A frozen, hashable `starlark.Value` carrying:

```go
type InstallStepValue struct {
    Kind string // "file" or "template"
    Src  string
    Dest string
    Mode int
}
```

Required `starlark.Value` methods:

| Method     | Behaviour                                                                    |
| ---------- | ---------------------------------------------------------------------------- |
| `String()` | `install_file("rcS", "/etc/rcS", mode=0o755)` or `install_template(...)`     |
| `Type()`   | `"InstallStep"`                                                              |
| `Freeze()` | no-op (value is already immutable)                                           |
| `Truth()`  | `starlark.True`                                                              |
| `Hash()`   | FNV-1a over `Kind\x00Src\x00Dest\x00<mode>` so equal steps have equal hashes |

### Go-side mirror

`internal/starlark/types.go` gains:

```go
// InstallStep is a declarative step that installs a file from the unit's files
// directory into DESTDIR during task execution. It is produced by the Starlark
// install_file() / install_template() builtins and executed by the build
// executor (see internal/build.doInstallStep).
type InstallStep struct {
    Kind string // "file" or "template"
    Src  string // path relative to <DefinedIn>/<unit-name>/
    Dest string // absolute path in build env; $VAR references expanded at exec time
    Mode int    // unix file mode for the installed file
}
```

And `Step` gains `Install *InstallStep` alongside `Command` and `Fn`.

- [ ] **Step 1: Write failing test for install_file constructor**

Create `internal/starlark/install_step_test.go`:

```go
package starlark

import (
    "testing"

    "go.starlark.net/starlark"
)

func TestInstallFile_ReturnsValue(t *testing.T) {
    thread := &starlark.Thread{Name: "test"}
    predeclared := starlark.StringDict{
        "install_file":     starlark.NewBuiltin("install_file", fnInstallFile),
        "install_template": starlark.NewBuiltin("install_template", fnInstallTemplate),
    }
    globals, err := starlark.ExecFile(thread, "t.star", `
f = install_file("rcS", "$DESTDIR/etc/init.d/rcS", mode = 0o755)
t = install_template("inittab.tmpl", "$DESTDIR/etc/inittab")
`, predeclared)
    if err != nil {
        t.Fatalf("ExecFile: %v", err)
    }
    f, ok := globals["f"].(*InstallStepValue)
    if !ok {
        t.Fatalf("f = %T, want *InstallStepValue", globals["f"])
    }
    if f.Kind != "file" || f.Src != "rcS" || f.Dest != "$DESTDIR/etc/init.d/rcS" || f.Mode != 0o755 {
        t.Errorf("f = %+v, unexpected fields", f)
    }
    tt, ok := globals["t"].(*InstallStepValue)
    if !ok {
        t.Fatalf("t = %T, want *InstallStepValue", globals["t"])
    }
    if tt.Kind != "template" || tt.Mode != 0o644 {
        t.Errorf("t = %+v, want default mode 0o644", tt)
    }
}

func TestInstallStepValue_HashStable(t *testing.T) {
    a := &InstallStepValue{Kind: "file", Src: "a", Dest: "/b", Mode: 0o644}
    b := &InstallStepValue{Kind: "file", Src: "a", Dest: "/b", Mode: 0o644}
    ha, _ := a.Hash()
    hb, _ := b.Hash()
    if ha != hb {
        t.Errorf("equal values hash differently: %d vs %d", ha, hb)
    }
    c := &InstallStepValue{Kind: "file", Src: "a", Dest: "/b", Mode: 0o755}
    hc, _ := c.Hash()
    if ha == hc {
        t.Error("different modes should produce different hashes")
    }
}
```

- [ ] **Step 2: Run test; confirm failure**

```bash
go test ./internal/starlark/ -run TestInstallFile_ReturnsValue -v
```

Expected: FAIL — `InstallStepValue` undefined; `fnInstallFile` currently a
placeholder that returns an error when called outside build context.

- [ ] **Step 3: Create install_step.go with type + constructors**

Create `internal/starlark/install_step.go`:

```go
package starlark

import (
    "encoding/binary"
    "fmt"
    "hash/fnv"

    "go.starlark.net/starlark"
)

// InstallStepValue is the Starlark value returned by install_file() and
// install_template(). It is an immutable, frozen, hashable description of a
// file-install action; execution is performed by the build executor when it
// reaches the step in a task's steps= list.
type InstallStepValue struct {
    Kind string // "file" or "template"
    Src  string // relative to <DefinedIn>/<unit-name>/
    Dest string // env-expanded at execution time
    Mode int
}

var _ starlark.Value = (*InstallStepValue)(nil)

func (s *InstallStepValue) String() string {
    fn := "install_file"
    if s.Kind == "template" {
        fn = "install_template"
    }
    return fmt.Sprintf("%s(%q, %q, mode=0o%o)", fn, s.Src, s.Dest, s.Mode)
}

func (*InstallStepValue) Type() string         { return "InstallStep" }
func (*InstallStepValue) Freeze()               {}
func (*InstallStepValue) Truth() starlark.Bool  { return starlark.True }

func (s *InstallStepValue) Hash() (uint32, error) {
    h := fnv.New32a()
    h.Write([]byte(s.Kind))
    h.Write([]byte{0})
    h.Write([]byte(s.Src))
    h.Write([]byte{0})
    h.Write([]byte(s.Dest))
    h.Write([]byte{0})
    var buf [8]byte
    binary.LittleEndian.PutUint64(buf[:], uint64(s.Mode))
    h.Write(buf[:])
    return h.Sum32(), nil
}

// fnInstallFile implements the Starlark builtin install_file(src, dest, mode=0o644).
// Returns an InstallStepValue; has no side effects.
func fnInstallFile(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
    return buildInstallStep("install_file", "file", args, kwargs, 0o644)
}

// fnInstallTemplate is identical to fnInstallFile but with Kind="template".
func fnInstallTemplate(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
    return buildInstallStep("install_template", "template", args, kwargs, 0o644)
}

func buildInstallStep(name, kind string, args starlark.Tuple, kwargs []starlark.Tuple, defMode int) (starlark.Value, error) {
    var src, dest starlark.String
    if err := starlark.UnpackPositionalArgs(name, args, nil, 2, &src, &dest); err != nil {
        return nil, err
    }
    mode := defMode
    for _, kv := range kwargs {
        k := string(kv[0].(starlark.String))
        if k != "mode" {
            return nil, fmt.Errorf("%s: unexpected kwarg %q", name, k)
        }
        n, ok := kv[1].(starlark.Int)
        if !ok {
            return nil, fmt.Errorf("%s: mode must be int, got %s", name, kv[1].Type())
        }
        v, ok := n.Int64()
        if !ok {
            return nil, fmt.Errorf("%s: mode out of range", name)
        }
        mode = int(v)
    }
    return &InstallStepValue{Kind: kind, Src: string(src), Dest: string(dest), Mode: mode}, nil
}
```

- [ ] **Step 4: Add `InstallStep` Go struct + `Step.Install` field**

Edit `internal/starlark/types.go`. After the existing `Step` struct (around line
166–169), add:

```go
// InstallStep describes a file installation action produced by the Starlark
// install_file() / install_template() builtins. Executed by the build executor.
type InstallStep struct {
    Kind string // "file" or "template"
    Src  string // path relative to <DefinedIn>/<unit-name>/
    Dest string // env-expanded at execution time
    Mode int
}
```

Change `Step` to:

```go
type Step struct {
    Command string            // shell command
    Fn      starlark.Callable // Starlark function
    Install *InstallStep      // install_file / install_template step
}
```

- [ ] **Step 5: Replace placeholders in builtins.go with real registrations**

Edit `internal/starlark/builtins.go`. In the `builtins()` method (the table
around line 12–34), replace:

```go
            "install_file":     starlark.NewBuiltin("install_file", fnInstallFilePlaceholder),
            "install_template": starlark.NewBuiltin("install_template", fnInstallTemplatePlaceholder),
```

with:

```go
            "install_file":     starlark.NewBuiltin("install_file", fnInstallFile),
            "install_template": starlark.NewBuiltin("install_template", fnInstallTemplate),
```

Then delete `fnInstallFilePlaceholder` and `fnInstallTemplatePlaceholder` (both
in builtins.go, ~lines 65–85). Remove any imports that become unused.

- [ ] **Step 6: Extend ParseTaskList to recognise InstallStepValue**

In `internal/starlark/builtins.go`, locate `ParseTaskList` (line ~101). In the
`steps=` loop (currently the `switch val := sv.(type)` around line 129), add a
third case **before** the `starlark.Callable` case (because `*InstallStepValue`
does not implement `Callable` — but ordering matters for readability):

```go
                    case *InstallStepValue:
                        t.Steps = append(t.Steps, Step{Install: &InstallStep{
                            Kind: val.Kind,
                            Src:  val.Src,
                            Dest: val.Dest,
                            Mode: val.Mode,
                        }})
```

- [ ] **Step 7: Run starlark tests; confirm PASS**

```bash
source envsetup.sh && go test ./internal/starlark/ -v
```

Expected: `TestInstallFile_ReturnsValue` PASS, `TestInstallStepValue_HashStable`
PASS, no regressions in pre-existing tests.

- [ ] **Step 8: Commit**

```bash
git add internal/starlark/install_step.go internal/starlark/install_step_test.go internal/starlark/types.go internal/starlark/builtins.go
git commit -m "starlark: make install_file/install_template return InstallStep values"
```

---

## Task 2: Executor dispatch on `step.Install`; remove thread-local wiring

Delete the old side-effecting install plumbing now that steps carry the install
intent as data. Add the dispatch case that calls a new `doInstallStep` helper.

**Files:**

- Modify: `internal/build/templates.go` — remove `TemplateContext` struct,
  `templateKey`, `fnInstallFile`, `fnInstallTemplate`; add `doInstallStep`; keep
  `BuildTemplateContext`, `resolveTemplatePath`, `expandEnv`, `modeFromKwargs`
  (delete `modeFromKwargs` if no longer referenced after the builtin removals)
- Modify: `internal/build/starlark_exec.go` — delete `SetTemplateContext`;
  remove `install_*` from `BuildPredeclared`
- Modify: `internal/build/executor.go` — delete `SetTemplateContext` call at
  step.Fn branch; add `step.Install != nil` branch
- Modify: `internal/build/templates_test.go` — rewrite install tests to exercise
  `doInstallStep` directly

### `doInstallStep` signature

```go
// doInstallStep executes a single install-step against the filesystem. It is
// called from the executor's task step loop when step.Install != nil. The
// template data map and env are the same ones used for shell and fn steps in
// the enclosing task, so variable semantics stay consistent across step kinds.
func doInstallStep(unit *yoestar.Unit, step *yoestar.InstallStep, data map[string]any, env map[string]string) error
```

- [ ] **Step 1: Rewrite templates_test.go for doInstallStep**

Replace existing `TestFnInstallFile_*` and `TestFnInstallTemplate_*` functions
in `internal/build/templates_test.go` (keep `TestBuildTemplateContext_*` — those
still apply). New tests exercise `doInstallStep` directly, no Starlark threads:

```go
func TestDoInstallStep_CopiesFileVerbatim(t *testing.T) {
    tmp := t.TempDir()
    unitDir := filepath.Join(tmp, "unit-src", "my-unit")
    if err := os.MkdirAll(unitDir, 0o755); err != nil {
        t.Fatal(err)
    }
    content := []byte("#!/bin/sh\necho hello\n")
    if err := os.WriteFile(filepath.Join(unitDir, "script.sh"), content, 0o644); err != nil {
        t.Fatal(err)
    }
    destDir := filepath.Join(tmp, "destdir")
    u := &yoestar.Unit{Name: "my-unit", DefinedIn: filepath.Join(tmp, "unit-src")}
    step := &yoestar.InstallStep{
        Kind: "file",
        Src:  "script.sh",
        Dest: "$DESTDIR/usr/bin/script.sh",
        Mode: 0o755,
    }
    env := map[string]string{"DESTDIR": destDir}
    if err := doInstallStep(u, step, nil, env); err != nil {
        t.Fatalf("doInstallStep: %v", err)
    }
    got, err := os.ReadFile(filepath.Join(destDir, "usr/bin/script.sh"))
    if err != nil {
        t.Fatal(err)
    }
    if string(got) != string(content) {
        t.Errorf("content mismatch")
    }
    info, _ := os.Stat(filepath.Join(destDir, "usr/bin/script.sh"))
    if info.Mode().Perm() != 0o755 {
        t.Errorf("mode = %o, want 0o755", info.Mode().Perm())
    }
}

func TestDoInstallStep_RendersTemplateWithData(t *testing.T) {
    tmp := t.TempDir()
    unitDir := filepath.Join(tmp, "unit-src", "base-files")
    _ = os.MkdirAll(unitDir, 0o755)
    _ = os.WriteFile(filepath.Join(unitDir, "info.tmpl"),
        []byte("machine={{.machine}}\nconsole={{.console}}\n"), 0o644)

    destDir := filepath.Join(tmp, "destdir")
    u := &yoestar.Unit{Name: "base-files", DefinedIn: filepath.Join(tmp, "unit-src")}
    step := &yoestar.InstallStep{
        Kind: "template",
        Src:  "info.tmpl",
        Dest: "$DESTDIR/etc/info",
        Mode: 0o644,
    }
    env := map[string]string{"DESTDIR": destDir}
    data := map[string]any{"machine": "qemu-x86_64", "console": "ttyS0"}
    if err := doInstallStep(u, step, data, env); err != nil {
        t.Fatalf("doInstallStep: %v", err)
    }
    got, _ := os.ReadFile(filepath.Join(destDir, "etc/info"))
    want := "machine=qemu-x86_64\nconsole=ttyS0\n"
    if string(got) != want {
        t.Errorf("got %q, want %q", got, want)
    }
}

func TestDoInstallStep_MissingKeyIsError(t *testing.T) {
    tmp := t.TempDir()
    unitDir := filepath.Join(tmp, "unit-src", "u")
    _ = os.MkdirAll(unitDir, 0o755)
    _ = os.WriteFile(filepath.Join(unitDir, "x.tmpl"), []byte(`{{.missing}}`), 0o644)

    u := &yoestar.Unit{Name: "u", DefinedIn: filepath.Join(tmp, "unit-src")}
    step := &yoestar.InstallStep{Kind: "template", Src: "x.tmpl", Dest: "$DESTDIR/out", Mode: 0o644}
    env := map[string]string{"DESTDIR": filepath.Join(tmp, "dd")}
    if err := doInstallStep(u, step, map[string]any{}, env); err == nil {
        t.Fatal("expected error on missing key, got nil")
    }
}

func TestDoInstallStep_PathEscapeRejected(t *testing.T) {
    tmp := t.TempDir()
    u := &yoestar.Unit{Name: "u", DefinedIn: filepath.Join(tmp, "unit-src")}
    step := &yoestar.InstallStep{Kind: "file", Src: "../../etc/passwd", Dest: "$DESTDIR/p", Mode: 0o644}
    if err := doInstallStep(u, step, nil, map[string]string{"DESTDIR": tmp}); err == nil {
        t.Fatal("expected escape error, got nil")
    }
}
```

Drop the `go.starlark.net/starlark` import from the test file since tests no
longer touch threads — but leave it if other tests in the file still need it
(check at compile time).

- [ ] **Step 2: Run the test; confirm they fail**

```bash
go test ./internal/build/ -run TestDoInstallStep -v
```

Expected: FAIL — `doInstallStep` undefined.

- [ ] **Step 3: Rewrite templates.go**

Replace the contents of `internal/build/templates.go` with:

```go
package build

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "text/template"

    yoestar "github.com/YoeDistro/yoe-ng/internal/starlark"
)

// BuildTemplateContext builds the context map passed to Go templates, merging
// auto-populated fields (arch, machine, console, project) and unit identity
// fields (name, version, release) with the unit's Extra kwargs. Extra wins on
// key collision so explicit unit fields always override defaults.
func BuildTemplateContext(u *yoestar.Unit, arch, machine, console, project string) map[string]any {
    m := map[string]any{
        "name":    u.Name,
        "version": u.Version,
        "release": int64(u.Release),
        "arch":    arch,
        "machine": machine,
        "console": console,
        "project": project,
    }
    for k, v := range u.Extra {
        m[k] = v
    }
    return m
}

// doInstallStep executes a single install-step against the filesystem. Called
// from the executor's task step loop when step.Install != nil.
func doInstallStep(u *yoestar.Unit, step *yoestar.InstallStep, data map[string]any, env map[string]string) error {
    srcPath, err := resolveTemplatePath(u, step.Src)
    if err != nil {
        return fmt.Errorf("install %s: %w", step.Src, err)
    }
    destPath := expandEnv(step.Dest, env)

    raw, err := os.ReadFile(srcPath)
    if err != nil {
        return fmt.Errorf("install %s: reading %s: %w", step.Src, srcPath, err)
    }

    var out []byte
    switch step.Kind {
    case "file":
        out = raw
    case "template":
        tmpl, err := template.New(filepath.Base(srcPath)).
            Option("missingkey=error").
            Parse(string(raw))
        if err != nil {
            return fmt.Errorf("install_template %s: parsing: %w", srcPath, err)
        }
        var buf strings.Builder
        if err := tmpl.Execute(&buf, data); err != nil {
            return fmt.Errorf("install_template %s: rendering: %w", srcPath, err)
        }
        out = []byte(buf.String())
    default:
        return fmt.Errorf("install %s: unknown kind %q", step.Src, step.Kind)
    }

    if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
        return fmt.Errorf("install %s: creating dest dir: %w", step.Src, err)
    }
    if err := os.WriteFile(destPath, out, os.FileMode(step.Mode)); err != nil {
        return fmt.Errorf("install %s: writing %s: %w", step.Src, destPath, err)
    }
    return nil
}

// resolveTemplatePath resolves a relative path against the unit's files
// directory: <DefinedIn>/<unit-name>/<relPath>. Rejects paths that escape
// the unit files directory (e.g. "../../etc/passwd").
func resolveTemplatePath(u *yoestar.Unit, relPath string) (string, error) {
    filesDir := filepath.Join(u.DefinedIn, u.Name)
    resolved := filepath.Join(filesDir, relPath)
    rel, err := filepath.Rel(filesDir, resolved)
    if err != nil {
        return "", fmt.Errorf("resolving %q: %w", relPath, err)
    }
    if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
        return "", fmt.Errorf("path %q escapes unit files directory", relPath)
    }
    return resolved, nil
}

// expandEnv expands $VAR and ${VAR} references using the provided build env.
// Unknown variables expand to the empty string — we deliberately do NOT fall
// back to the host process environment, because that would break
// reproducibility and content-addressed caching.
func expandEnv(s string, env map[string]string) string {
    return os.Expand(s, func(key string) string {
        return env[key]
    })
}
```

This removes `TemplateContext`, `templateKey`, `fnInstallFile`,
`fnInstallTemplate`, and `modeFromKwargs` (the last one was only used by the
builtins).

- [ ] **Step 4: Clean up starlark_exec.go**

Edit `internal/build/starlark_exec.go`:

1. Delete `SetTemplateContext` (around lines 104–111).
2. In `BuildPredeclared` (~line 185), drop the `install_file` and
   `install_template` entries — leaving only `"run"`.
3. If `NewBuildThread` had any `install_*` thread locals from the earlier plan,
   remove those too (none are expected).

After this change, `BuildPredeclared` looks like:

```go
func BuildPredeclared() starlark.StringDict {
    return starlark.StringDict{
        "run": starlark.NewBuiltin("run", fnRun),
    }
}
```

- [ ] **Step 5: Update executor.go**

Edit `internal/build/executor.go`:

1. In the step loop (around line 346), add a new branch before the
   `step.Command != ""` branch (so install and shell order in the code matches
   reading order in the unit — but either order is fine):

```go
            if step.Install != nil {
                fmt.Fprintf(logW, "    [%d/%d] %s\n", i+1,
                    len(t.Steps), installStepLabel(step.Install))
                if err := doInstallStep(unit, step.Install, tctxData, env); err != nil {
                    if !opts.Verbose {
                        fmt.Fprintf(w, "  build log: %s\n", logPath)
                    }
                    return fmt.Errorf("task %s: %w", t.Name, err)
                }
                continue
            }
```

Add the helper near `doInstallStep` in templates.go:

```go
func installStepLabel(s *yoestar.InstallStep) string {
    fn := "install_file"
    if s.Kind == "template" {
        fn = "install_template"
    }
    return fmt.Sprintf("%s: %s -> %s", fn, s.Src, s.Dest)
}
```

2. In the `step.Fn != nil` branch (~line 375–405), delete the
   `SetTemplateContext(thread, &TemplateContext{...})` call. The `thread` now
   just carries sandbox, execer, and ctx.

3. `tctxData` is already computed earlier (line ~324 in the comment "Build the
   template context data map for install_file / install_template.") — keep that;
   it's the data map doInstallStep needs.

- [ ] **Step 6: Run the full test suite**

```bash
source envsetup.sh && yoe_build && go test ./... -v 2>&1 | tail -40
```

Expected: PASS. The `TestDoInstallStep_*` tests should pass; older
`TestFnInstall*` tests no longer exist. Starlark tests still pass (install
constructors return values).

- [ ] **Step 7: Commit**

```bash
git add internal/build/templates.go internal/build/starlark_exec.go internal/build/executor.go internal/build/templates_test.go
git commit -m "build: dispatch InstallStep in executor, remove thread-local template wiring"
```

---

## Task 3: Hash `Unit.Extra` + files directory contents

Unchanged in intent from the previous revision of this plan. Contents of
`<DefinedIn>/<unit-name>/` and the `Extra` map both feed the unit hash so
template edits and extra-kwarg changes invalidate the cache.

**Files:**

- Modify: `internal/resolve/hash.go`
- Modify: `internal/resolve/hash_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/resolve/hash_test.go`:

```go
func TestUnitHash_ExtraAffectsHash(t *testing.T) {
    base := func() *yoestar.Unit {
        return &yoestar.Unit{
            Name: "my-app", Version: "1.0.0", Class: "unit",
            Extra: map[string]any{"port": int64(8080)},
        }
    }
    h1 := UnitHash(base(), "x86_64", nil)
    u2 := base()
    u2.Extra["port"] = int64(9000)
    h2 := UnitHash(u2, "x86_64", nil)
    if h1 == h2 {
        t.Error("hash did not change when Extra changed")
    }
}

func TestUnitHash_ExtraKeyOrderStable(t *testing.T) {
    u1 := &yoestar.Unit{
        Name: "u", Version: "1", Class: "unit",
        Extra: map[string]any{"a": int64(1), "b": int64(2), "c": int64(3)},
    }
    u2 := &yoestar.Unit{
        Name: "u", Version: "1", Class: "unit",
        Extra: map[string]any{"c": int64(3), "b": int64(2), "a": int64(1)},
    }
    if UnitHash(u1, "x86_64", nil) != UnitHash(u2, "x86_64", nil) {
        t.Error("hash depends on Extra map iteration order")
    }
}

func TestUnitHash_FilesDirectoryAffectsHash(t *testing.T) {
    tmp := t.TempDir()
    unitDir := filepath.Join(tmp, "unit-src", "u")
    if err := os.MkdirAll(unitDir, 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(unitDir, "a.tmpl"), []byte("one"), 0o644); err != nil {
        t.Fatal(err)
    }
    u := &yoestar.Unit{
        Name: "u", Version: "1", Class: "unit",
        DefinedIn: filepath.Join(tmp, "unit-src"),
    }
    h1 := UnitHash(u, "x86_64", nil)
    if err := os.WriteFile(filepath.Join(unitDir, "a.tmpl"), []byte("two"), 0o644); err != nil {
        t.Fatal(err)
    }
    h2 := UnitHash(u, "x86_64", nil)
    if h1 == h2 {
        t.Error("hash did not change when file in unit files dir changed")
    }
}
```

- [ ] **Step 2: Run tests; confirm failure**

```bash
go test ./internal/resolve/ -run 'TestUnitHash_Extra|TestUnitHash_FilesDirectory' -v
```

Expected: FAIL (current hash ignores Extra and files dir).

- [ ] **Step 3: Extend `UnitHash`**

Edit `internal/resolve/hash.go`. Ensure imports include `crypto/sha256`,
`encoding/json`, `io`, `os`, `path/filepath`, `sort`, `strings`.

In `UnitHash`, just before the `Dependencies` section (existing line ~57), add:

```go
    // Extra kwargs — JSON-encoded with sorted keys for stability.
    if len(unit.Extra) > 0 {
        if b, err := json.Marshal(unit.Extra); err == nil {
            fmt.Fprintf(h, "extra:%s\n", b)
        }
    }

    // Unit files directory: <DefinedIn>/<unit-name>/ — hash file contents so
    // template/static file edits invalidate the cache.
    if unit.DefinedIn != "" {
        hashFilesDir(h, filepath.Join(unit.DefinedIn, unit.Name))
    }
```

Go's `encoding/json` sorts `map[string]any` keys on Marshal, so nested-map
determinism is free. (If nested arrays require recursion, add a `sortedMap`
helper — only needed if tests reveal drift.)

Append helpers:

```go
// hashFilesDir writes a deterministic digest of the files under dir into h.
// Paths are sorted so iteration order doesn't change the hash. Missing
// directories are silently skipped — not every unit has a files directory.
func hashFilesDir(h io.Writer, dir string) {
    info, err := os.Stat(dir)
    if err != nil || !info.IsDir() {
        return
    }
    var paths []string
    _ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
        if err != nil || d.IsDir() {
            return err
        }
        paths = append(paths, p)
        return nil
    })
    sort.Strings(paths)
    for _, p := range paths {
        rel, _ := filepath.Rel(dir, p)
        content, err := os.ReadFile(p)
        if err != nil {
            continue
        }
        sum := sha256.Sum256(content)
        fmt.Fprintf(h, "file:%s:%x\n", rel, sum[:])
    }
}
```

- [ ] **Step 4: Run hash tests; confirm PASS**

```bash
go test ./internal/resolve/ -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/resolve/hash.go internal/resolve/hash_test.go
git commit -m "resolve: hash Unit.Extra and files directory contents"
```

---

## Task 4: Migrate base-files to install steps

**Files:**

- Create: `modules/module-core/units/base/base-files/inittab.tmpl`
- Create: `modules/module-core/units/base/base-files/rcS`
- Create: `modules/module-core/units/base/base-files/os-release.tmpl`
- Create: `modules/module-core/units/base/base-files/extlinux.conf`
- Modify: `modules/module-core/units/base/base-files.star`

- [ ] **Step 1: Create inittab.tmpl**

```
::sysinit:/bin/mount -t proc proc /proc
::sysinit:/bin/mount -t sysfs sys /sys
::sysinit:/bin/hostname -F /etc/hostname
::sysinit:/etc/init.d/rcS
{{.console}}::respawn:/sbin/getty -L {{.console}} 115200 vt100
::ctrlaltdel:/sbin/reboot
::shutdown:/bin/umount -a -r
```

- [ ] **Step 2: Create rcS (static)**

```sh
#!/bin/sh
for s in /etc/init.d/S*; do
    [ -x "$s" ] && "$s" start
done
```

- [ ] **Step 3: Create os-release.tmpl**

```
NAME=Yoe
ID=yoe
PRETTY_NAME="Yoe Linux ({{.machine}})"
HOME_URL=https://github.com/YoeDistro/yoe
```

- [ ] **Step 4: Create extlinux.conf (static, machine-specific; templating
      deferred)**

```
DEFAULT yoe
LABEL yoe
    LINUX /boot/vmlinuz
    APPEND console=ttyS0 root=/dev/vda1 rw devtmpfs.mount=1
```

- [ ] **Step 5: Rewrite base-files.star**

Replace the contents of `modules/module-core/units/base/base-files.star` with:

```python
load("//classes/users.star", "user", "users_commands")

def base_files(name = "base-files", users = None):
    """Creates a base filesystem skeleton unit with the given users.

    Override this in your image to add users:
        load("//units/base/base-files.star", "base_files")
        load("//classes/users.star", "user")
        base_files(name = "base-files-dev", users = [
            user(name = "root", uid = 0, gid = 0, home = "/root"),
            user(name = "myuser", uid = 1000, gid = 1000, password = "secret"),
        ])
    """
    if not users:
        users = [user(name = "root", uid = 0, gid = 0, home = "/root")]

    deps = []
    for u in users:
        if u["password"]:
            deps.append("openssl")
            break
    if "toolchain-musl" not in deps:
        deps.append("toolchain-musl")

    unit(
        name = name,
        version = "1.0.0",
        release = 3,
        scope = "machine",
        license = "MIT",
        description = "Base filesystem skeleton: users, groups, dirs, inittab, boot config",
        deps = deps,
        container = "toolchain-musl",
        container_arch = "target",
        tasks = [
            task("build", steps = (
                [
                    "mkdir -p $DESTDIR/etc $DESTDIR/root $DESTDIR/proc $DESTDIR/sys"
                    + " $DESTDIR/dev $DESTDIR/tmp $DESTDIR/run"
                    + " $DESTDIR/etc/init.d $DESTDIR/boot/extlinux",
                ]
                + users_commands(users)
                + [
                    install_template("inittab.tmpl", "$DESTDIR/etc/inittab"),
                    install_file("rcS", "$DESTDIR/etc/init.d/rcS", mode = 0o755),
                    install_template("os-release.tmpl", "$DESTDIR/etc/os-release"),
                    install_file("extlinux.conf", "$DESTDIR/boot/extlinux/extlinux.conf"),
                ]
            )),
        ],
    )

base_files()
```

`release` bumped from 2 to 3 (package content changes).

- [ ] **Step 6: Build base-files end-to-end**

```bash
source envsetup.sh && yoe_build && cd testdata/e2e-project && ../../yoe build base-files 2>&1 | tail -40
```

- [ ] **Step 7: Verify rendered files**

```bash
cat build/base-files.machine/destdir/etc/inittab
cat build/base-files.machine/destdir/etc/os-release
ls -l build/base-files.machine/destdir/etc/init.d/rcS
```

Expected: `inittab` has `ttyS0::respawn:...`; `os-release` has
`PRETTY_NAME="Yoe Linux (qemu-x86_64)"`; `rcS` is mode 0o755.

- [ ] **Step 8: Commit**

```bash
cd /scratch4/yoe/yoe-ng
git add modules/module-core/units/base/base-files/ modules/module-core/units/base/base-files.star
git commit -m "units: migrate base-files to install_template and install_file"
```

---

## Task 5: Migrate network-config to install steps

**Files:**

- Create: `modules/module-core/units/net/network-config/udhcpc-default.script`
- Create: `modules/module-core/units/net/network-config/S10network`
- Modify: `modules/module-core/units/net/network-config.star`

- [ ] **Step 1: Create udhcpc-default.script**

```sh
#!/bin/sh
case "$1" in
    bound|renew)
        ip addr flush dev "$interface"
        ip addr add "$ip/${mask:-24}" dev "$interface"
        [ -n "$router" ] && ip route add default via "$router"
        [ -n "$dns" ] && {
            : > /etc/resolv.conf
            for d in $dns; do
                echo "nameserver $d" >> /etc/resolv.conf
            done
        }
        ;;
    deconfig)
        ip addr flush dev "$interface"
        ;;
esac
```

- [ ] **Step 2: Create S10network**

```sh
#!/bin/sh
ip link set lo up
ip link set eth0 up
udhcpc -i eth0 -s /usr/share/udhcpc/default.script -q
```

- [ ] **Step 3: Rewrite network-config.star**

```python
unit(
    name = "network-config",
    version = "1.0.0",
    release = 1,
    license = "MIT",
    description = "DHCP networking via busybox udhcpc on eth0",
    services = ["S10network"],
    runtime_deps = ["busybox"],
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps = [
            "mkdir -p $DESTDIR/usr/share/udhcpc $DESTDIR/etc/init.d",
            install_file("udhcpc-default.script",
                         "$DESTDIR/usr/share/udhcpc/default.script", mode = 0o755),
            install_file("S10network", "$DESTDIR/etc/init.d/S10network", mode = 0o755),
        ]),
    ],
)
```

- [ ] **Step 4: Build and verify**

```bash
cd testdata/e2e-project && ../../yoe build network-config 2>&1 | tail -20
ls -l build/network-config.*/destdir/etc/init.d/S10network
ls -l build/network-config.*/destdir/usr/share/udhcpc/default.script
```

Expected: both files present and mode 0o755.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe-ng
git add modules/module-core/units/net/network-config/ modules/module-core/units/net/network-config.star
git commit -m "units: migrate network-config to install_file"
```

---

## Task 6: Migrate simpleiot to install steps

**Files:**

- Create: `modules/module-core/units/net/simpleiot/simpleiot.init`
- Modify: `modules/module-core/units/net/simpleiot.star`

- [ ] **Step 1: Create simpleiot.init**

```sh
#!/bin/sh
case "$1" in
    start) /usr/bin/siot &;;
    stop) killall siot;;
esac
```

- [ ] **Step 2: Replace the `init-script` task**

In `modules/module-core/units/net/simpleiot.star`, change the `init-script` task
from the current heredoc form to:

```python
        task("init-script", steps = [
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("simpleiot.init",
                         "$DESTDIR/etc/init.d/simpleiot", mode = 0o755),
        ]),
```

The `build` task stays as-is.

- [ ] **Step 3: Build and verify**

```bash
cd testdata/e2e-project && ../../yoe build simpleiot 2>&1 | tail -20
ls -l build/simpleiot.*/destdir/etc/init.d/simpleiot
```

Expected: file present and mode 0o755. (simpleiot pulls Go modules over the
network on first build; may be slow.)

- [ ] **Step 4: Commit**

```bash
cd /scratch4/yoe/yoe-ng
git add modules/module-core/units/net/simpleiot/ modules/module-core/units/net/simpleiot.star
git commit -m "units: migrate simpleiot init-script to install_file"
```

---

## Task 7: Full e2e + docs + changelog

**Files:**

- Modify: `docs/file-templates.md` — remove `## Status: Spec`
- Modify: `CHANGELOG.md` — Unreleased entry

- [ ] **Step 1: Run full e2e**

```bash
source envsetup.sh && yoe_build && yoe_e2e_x86_64 2>&1 | tail -30
```

Expected: e2e build of base-image succeeds (exercises base-files, kernel, image
assembly).

- [ ] **Step 2: Drop `## Status: Spec`**

Edit `docs/file-templates.md`: delete the `## Status: Spec` line (currently
line 7) and the surrounding blank line. Before committing, skim the spec for any
sub-section that is still future work and mark those individually `(planned)`
per the project doc convention — notably the `extlinux.conf` templating for
machine-specific console/root args, if not done here.

- [ ] **Step 3: Changelog entry**

Add under `## [Unreleased]` in `CHANGELOG.md`:

```
- **File templates** — units can declare external template files (`.tmpl`) and
  static files alongside the `.star` file, installed via `install_template()`
  and `install_file()` — step-value builtins usable directly in
  `task(..., steps=[...])` alongside shell strings. Templates are rendered
  with Go `text/template` using a unified `map[string]any` context merged
  from unit identity fields, extra kwargs on `unit()`, and auto-populated
  `arch`/`machine`/`console`/`project`. The context map and the contents of
  the unit's files directory are hashed, so template edits and extra-kwarg
  changes invalidate the cache. `base-files`, `network-config`, and
  `simpleiot` migrated off inline heredocs.
```

- [ ] **Step 4: Full tests + format check**

```bash
go test ./... && yoe_format_check
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/file-templates.md CHANGELOG.md
git commit -m "docs: mark file-templates spec as implemented and log changelog"
```

---

## Self-Review Summary

**Spec coverage under step-value design:**

| Spec item                                                         | Task                           |
| ----------------------------------------------------------------- | ------------------------------ |
| `install_file` / `install_template` return `InstallStep` values   | 1                              |
| Values used directly in `steps=[...]`, no `fn=lambda:` wrapper    | 1                              |
| `ParseTaskList` converts values to `Step{Install: ...}`           | 1                              |
| Executor dispatches on `step.Install`                             | 2                              |
| `doInstallStep` resolves path, renders / copies, writes           | 2                              |
| Thread-local `TemplateContext` / placeholder delegates removed    | 2                              |
| Path escape (`../../etc/passwd`) rejected                         | 2                              |
| `$DESTDIR` etc. expanded from task build env; no host fallback    | 2                              |
| `missingkey=error` for template rendering                         | 2                              |
| `BuildTemplateContext` merges unit identity + Extra + auto-fields | (kept from 8ae9ac8)            |
| `Unit.Extra` captured from unrecognized `unit()` kwargs           | (committed: bbda8c86, b37eb62) |
| Extra JSON in unit hash                                           | 3                              |
| Files directory contents in unit hash                             | 3                              |
| Migrate base-files / network-config / simpleiot                   | 4, 5, 6                        |
| E2E passes; spec status updated; changelog                        | 7                              |

**Deferred (not in this plan):**

- Custom Go template functions (`sizeMB`, `sfdiskType`) — belong with
  `starlark-packaging-images` implementation.
- Templating of `extlinux.conf` console/root args — machine-specific work
  tracked by the machine-portable images roadmap.

---

## Execution Handoff

Plan saved to `docs/superpowers/plans/2026-04-23-file-templates.md`. Two
execution options:

**1. Subagent-Driven (recommended)** — dispatch a fresh subagent per task,
review between tasks, fast iteration.

**2. Inline Execution** — execute tasks in this session using executing-plans,
batch execution with checkpoints.

**Which approach?**
