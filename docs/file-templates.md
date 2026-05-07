# File Templates

Move inline file content out of Starlark units into external template files
processed by Go's `text/template`. A unified `map[string]any` context serves as
both the template data and the hash input — one source of truth.

## Problem

Units currently embed multi-line file content as heredocs inside shell step
strings. This is hard to read, hard to edit, and prevents tools (syntax
highlighters, linters) from understanding the embedded content.

Examples of inline content today:

- `base-files.star` — inittab, os-release, extlinux.conf
- `network-config.star` — udhcpc default.script, OpenRC `network` init script
- `image.star` — sfdisk partition tables, extlinux install scripts

## Design

### Template Files

Templates live in a directory named after the unit, alongside the `.star` file:

```
modules/module-core/
  units/
    base/
      base-files.star
      base-files/                # same name as the unit
        inittab.tmpl
        os-release.tmpl
        extlinux.conf.tmpl
    net/
      network-config.star
      network-config/
        udhcpc-default.script
        network                  # OpenRC service script
      simpleiot.star
      simpleiot/
        simpleiot.init
```

Files without `.tmpl` extension are copied verbatim via `install_file()`. Files
with `.tmpl` are processed through Go's `text/template` via
`install_template()`.

### Unit Context (`map[string]any`)

A single `map[string]any` is used for both template rendering and hash
computation. The executor auto-populates standard fields, and any extra kwargs
passed to `unit()` are captured into the same map. No separate `vars` field —
just add fields directly to the unit:

```python
unit(
    name = "my-app",
    version = "1.0.0",
    port = 8080,
    log_level = "info",
    debug = True,
    ...
)
```

Templates access all fields: `{{.port}}`, `{{.log_level}}`, `{{.name}}`.

**Auto-populated fields** (injected by the executor, not declared in the unit):

| Key       | Source                             | Example         |
| --------- | ---------------------------------- | --------------- |
| `name`    | unit name                          | `"base-files"`  |
| `version` | unit version                       | `"1.0.0"`       |
| `release` | unit release                       | `0`             |
| `arch`    | target architecture                | `"x86_64"`      |
| `machine` | active machine name                | `"qemu-x86_64"` |
| `console` | serial console from kernel cmdline | `"ttyS0"`       |
| `project` | project name                       | `"my-project"`  |

Unit kwargs override auto-populated fields if there's a name collision (explicit
wins).

**Go implementation:** `registerUnit()` captures all unrecognized kwargs into a
`map[string]any` on the Unit struct. The executor merges auto-populated fields
(lower priority) with unit fields (higher priority) to build the context map.
Classes pass `**kwargs` through to `unit()`, so custom fields flow naturally:

```python
autotools(
    name = "my-lib",
    version = "1.0",
    source = "...",
    custom_flag = "enabled",  # flows through **kwargs to unit()
)
```

### Template Syntax

Go `text/template` with the unit context map:

```
# inittab.tmpl
::sysinit:/sbin/openrc sysinit
::sysinit:/sbin/openrc boot
::wait:/sbin/openrc default
{{.console}}::respawn:/sbin/getty -L {{.console}} 115200 vt100
::ctrlaltdel:/sbin/reboot
::shutdown:/sbin/openrc shutdown
```

```
# os-release.tmpl
NAME=Yoe
ID=yoe
PRETTY_NAME="Yoe Linux ({{.machine}})"
HOME_URL=https://github.com/yoebuild/yoe
```

```
# config.toml.tmpl (custom vars)
[server]
port = {{.port}}
log_level = "{{.log_level}}"
debug = {{.debug}}
```

### Starlark API

Two new builtins are **step-value constructors**, not side-effecting calls. They
return a value that the build executor recognises and dispatches when the task
runs, in the same step list as shell strings and Starlark callables:

```python
# install_file(src, dest, mode=0o644) -> InstallStep
# Copies src verbatim from the unit's files directory to dest.

# install_template(src, dest, mode=0o644) -> InstallStep
# Renders src through Go text/template with the unit's context map, then
# writes the result to dest.
```

They are used directly in `task(..., steps=[...])`, no `fn=lambda:` wrapper
required:

```python
task("build", steps = [
    "mkdir -p $DESTDIR/etc $DESTDIR/boot/extlinux",
    install_template("inittab.tmpl", "$DESTDIR/etc/inittab"),
    install_template("os-release.tmpl", "$DESTDIR/etc/os-release"),
])
```

`src` paths are relative to the **calling .star file's** template directory:
`<dir(file)>/<basename(file) without .star>/`. For a call written in
`units/base/base-files.star`, `"inittab.tmpl"` resolves to
`units/base/base-files/inittab.tmpl`. Paths that escape that directory
(`"../../etc/passwd"`) are rejected.

Resolving relative to the call site — not to the resulting unit's `unit()` call
site — is what lets a helper function package its templates next to itself and
reuse them across many units. For example, `base_files()` in
`units/base/base-files.star` can be called from `images/dev-image.star` with
`name = "base-files-dev"`; the install steps it returns still find their
templates in `units/base/base-files/`, not in `images/base-files-dev/`.

`dest` has environment variables (`$DESTDIR`, `$PREFIX`, etc.) expanded from the
task's build environment. Unknown variables expand to the empty string — there
is no fallback to the host process environment, to preserve reproducibility.

Install steps are pure data — `install_template(...)` can be bound to a name,
stored in a list, or generated from a helper function before being placed in
`steps=[...]`. They evaluate at unit-load time; execution happens later, in the
executor, when the step is reached.

### Example: base-files with templates

**Before (inline heredocs):**

```python
task("build", steps=[
    "mkdir -p $DESTDIR/etc",
    """cat > $DESTDIR/etc/inittab << INITTAB
::sysinit:/bin/mount -t proc proc /proc
::sysinit:/bin/hostname -F /etc/hostname
${CONSOLE}::respawn:/sbin/getty -L ${CONSOLE} 115200 vt100
::ctrlaltdel:/sbin/reboot
::shutdown:/bin/umount -a -r
INITTAB""",
    """cat > $DESTDIR/etc/os-release << OSRELEASE
NAME=Yoe
ID=yoe
PRETTY_NAME="Yoe Linux ($MACHINE)"
HOME_URL=https://github.com/yoebuild/yoe
OSRELEASE""",
])
```

**After (external templates):**

`base-files/inittab.tmpl`:

```
::sysinit:/sbin/openrc sysinit
::sysinit:/sbin/openrc boot
::wait:/sbin/openrc default
{{.console}}::respawn:/sbin/getty -L {{.console}} 115200 vt100
::ctrlaltdel:/sbin/reboot
::shutdown:/sbin/openrc shutdown
```

`base-files/os-release.tmpl`:

```
NAME=Yoe
ID=yoe
PRETTY_NAME="Yoe Linux ({{.machine}})"
HOME_URL=https://github.com/yoebuild/yoe
```

```python
unit(
    name = "base-files",
    version = "1.0.0",
    tasks = [
        task("build", steps = [
            "mkdir -p $DESTDIR/etc $DESTDIR/root $DESTDIR/proc $DESTDIR/sys"
                + " $DESTDIR/dev $DESTDIR/tmp $DESTDIR/run"
                + " $DESTDIR/boot/extlinux",
            install_template("inittab.tmpl", "$DESTDIR/etc/inittab"),
            install_template("os-release.tmpl", "$DESTDIR/etc/os-release"),
            install_template("extlinux.conf.tmpl",
                             "$DESTDIR/boot/extlinux/extlinux.conf"),
        ]),
    ],
)
```

### Example: simpleiot init script

`simpleiot/simpleiot.init`:

```sh
#!/sbin/openrc-run
command="/usr/bin/siot"
command_background="yes"
pidfile="/run/simpleiot.pid"

depend() {
    need net
}
```

```python
go_binary(
    name = "simpleiot",
    version = "0.18.5",
    services = ["simpleiot"],
    tasks = [
        task("build", steps = [...]),
        task("init-script", steps = [
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("simpleiot.init",
                         "$DESTDIR/etc/init.d/simpleiot", mode = 0o755),
        ]),
    ],
)
```

### Example: custom app with extra fields

```python
unit(
    name = "my-app",
    version = "2.0.0",
    port = 8080,
    workers = 4,
    enable_tls = True,
    tasks = [
        task("config", steps = [
            "mkdir -p $DESTDIR/etc/my-app",
            install_template("app.conf.tmpl", "$DESTDIR/etc/my-app/app.conf"),
        ]),
    ],
)
```

`my-app/app.conf.tmpl`:

```
# Generated by Yoe for {{.machine}}
listen_port = {{.port}}
workers = {{.workers}}
{{if .enable_tls}}tls_cert = /etc/ssl/certs/ca-certificates.crt{{end}}
```

### Hashing

The unit context map (`map[string]any`) is JSON-serialized with sorted keys and
included in the unit hash. This means:

- Changing any unit field changes the hash and triggers a rebuild
- Auto-populated fields (arch, machine) already affect the hash through existing
  mechanisms, but including them in the context map makes it explicit
- No separate hash logic needed for template fields vs build fields

Additionally, all files in the unit's files directory
(`<DefinedIn>/<unit-name>/`) are hashed by content. Changing a template file
changes the hash.

### Path Resolution

Template paths resolve to `<DefinedIn>/<unit-name>/<relPath>`:

```go
func resolveTemplatePath(unit *Unit, relPath string) string {
    return filepath.Join(unit.DefinedIn, unit.Name, relPath)
}
```

This matches the existing container convention:

| Unit file                        | Associated directory         |
| -------------------------------- | ---------------------------- |
| `containers/toolchain-musl.star` | `containers/toolchain-musl/` |
| `units/base/base-files.star`     | `units/base/base-files/`     |
| `units/net/network-config.star`  | `units/net/network-config/`  |

### Go Implementation

Install steps are **pure data values** produced at Starlark evaluation time and
executed by the build executor. There is no thread-local wiring, no placeholder
builtins, and no "must be called inside a task fn" error path — they're
third-class steps alongside shell strings and Starlark callables.

**New file: `internal/build/templates.go`**

- `BuildTemplateContext` — build the per-unit `map[string]any` from unit
  identity fields, `Extra`, and auto-populated
  `arch`/`machine`/`console`/`project`
- `doInstallStep` — execute a resolved `InstallStep` against a unit: read from
  `<DefinedIn>/<unit-name>/<src>`, render (if template) or copy, write to
  expanded dest
- `resolveTemplatePath` — resolve `<DefinedIn>/<unit-name>/<relPath>` with
  escape protection
- `expandEnv` — expand `$DESTDIR` etc. in destination paths using the task's
  build env (no host fallback, for reproducibility)

Custom Go template functions (e.g. `sizeMB`, `sfdiskType`) are out of scope for
this spec and belong to the `starlark-packaging-images` work that migrates
`image.star` partition templates.

**Modified: `internal/starlark/builtins.go`**

- Register `install_file` and `install_template` as ordinary global builtins
  that return `*InstallStepValue`. No placeholder-delegate pattern needed — they
  have no side effects.
- Capture unrecognized `unit()` kwargs into `Extra map[string]any` on the Unit
  struct.

**Modified: `internal/starlark/types.go`**

- New `InstallStepValue` — a `starlark.Value` implementation carrying
  `(Kind, Src, Dest, Mode)`. Frozen on construction; implements `Hash` so tasks
  containing install steps are deterministic.
- New `InstallStep` — Go-native mirror of the above, referenced by `Step`.
- `Step` gains an `Install *InstallStep` field.
- `Unit` gains an `Extra map[string]any` field.
- `ParseTaskList` recognises `*InstallStepValue` entries in `steps=[...]` and
  converts each to `Step{Install: &InstallStep{...}}`.

**Modified: `internal/build/executor.go`**

- Build a per-unit `map[string]any` template context via `BuildTemplateContext`.
- Task step loop gains a third case: `step.Install != nil` →
  `doInstallStep(unit, step.Install, ctxData, env)`. Command and Fn cases are
  unchanged.

**Modified: `internal/resolve/hash.go`**

- JSON-serialize the context map (sorted keys) and include in the unit hash.
- Hash contents of all files in the unit's files directory.

### What is NOT needed (vs. an earlier side-effecting design)

- No thread-local `TemplateContext` key on the build thread
- No `SetTemplateContext` helper
- No placeholder/delegate builtins in `internal/starlark/builtins.go`
- No `BuildPredeclared` entries for `install_file` / `install_template`
- No `fn=lambda: _install()` boilerplate in unit files

### What Stays in Go

Template rendering runs on the host (Go executor), not in the container. This
keeps template data (machine config, unit metadata) accessible without passing
it through environment variables. The rendered files are placed in the build
directory, then the container mounts them.

## Implementation Order

1. **`Extra` field on Unit** — capture unrecognized kwargs in `registerUnit()`.
2. **`InstallStepValue` + constructors** — Starlark value type and the
   `install_file` / `install_template` global builtins. Pure, side-effect-free.
3. **`Step.Install` + `ParseTaskList` dispatch** — extend the Go `Step` type and
   recognise install-step values inside `steps=[...]`.
4. **Executor dispatch + `doInstallStep`** — `BuildTemplateContext`, executor
   case for `step.Install`, and `doInstallStep` I/O. This step also removes the
   earlier thread-local wiring (`TemplateContext` thread key,
   `SetTemplateContext`) now that it is dead.
5. **Hashing** — include context map JSON (sorted keys) and files-directory
   contents in the unit hash.
6. **Migrate base-files** — inittab, os-release, extlinux.conf as install steps.
7. **Migrate network-config** — udhcpc script and `network` init script as
   install steps.
8. **Migrate simpleiot** — init-script task becomes a one-line install step.

## Non-Goals

- **Jinja2 or other template engines.** Go `text/template` is in stdlib and
  sufficient.
- **Template inheritance or includes.** Keep templates flat and simple.
- **Build-time template rendering in the container.** Templates are rendered by
  the Go executor on the host.
