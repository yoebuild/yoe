# TUI Redesign: Unit List with Inline Builds

## Context

The current TUI is a basic menu-driven interface that doesn't do much — it shows
a main menu with options like "Build all" and "Browse units" but doesn't
actually run builds or interact with the build system. The goal is to make the
TUI the primary interactive interface: a unit list with inline build status,
background builds, and quick-action keys for editing, diagnosing, and adding
units.

Running `yoe` with no arguments launches the TUI (the `yoe tui` subcommand has
been removed).

## Default View: Unit List

The TUI opens directly to a scrollable list of all units. Each row shows the
unit name, class, and build status:

```
  Yoe-NG  Machine: qemu-x86_64  Image: base-image

  NAME                CLASS       STATUS
→ base-files          unit        ● cached
  busybox             unit        ● cached
  linux               unit        ▌building...
  musl                unit        ● cached
  ncurses             autotools   ● cached
  openssh             unit        ● failed
  openssl             autotools   ● cached
  util-linux          autotools
  zlib                autotools   ● cached

  b build  e edit  d diagnose  l log  a add  enter details  q quit
```

### Status Indicators

| Indicator      | Color          | Meaning                  |
| -------------- | -------------- | ------------------------ |
| (none)         | —              | Never built              |
| `● cached`     | dim/gray       | Built and cached         |
| `▌building...` | flashing green | Build subprocess running |
| `● failed`     | red            | Last build failed        |

"Flashing green" means the indicator toggles visibility on a 500ms tick,
creating a blinking effect while the build is in progress.

### Key Bindings

| Key     | Action                                         |
| ------- | ---------------------------------------------- |
| `b`     | Build selected unit in background              |
| `e`     | Open unit's `.star` file in `$EDITOR`          |
| `d`     | Launch `claude /diagnose <unit>` (suspend TUI) |
| `l`     | Open unit's build log in `$EDITOR`             |
| `a`     | Launch `claude /new-unit` (suspend TUI)        |
| `Enter` | Show live log tail for the selected unit       |
| `B`     | Build all units in background                  |
| `j/k`   | Navigate up/down                               |
| `↑/↓`   | Navigate up/down                               |
| `q`     | Quit                                           |

### Initial Status Detection

On startup, the TUI determines each unit's status by checking the filesystem:

- If `build/<unit>/.cache-<hash>` exists → `cached`
- If `build/<unit>/build.log` exists but no cache marker → `failed`
- Otherwise → never built (no indicator)

The hash is computed using the existing `resolve.UnitHash()` function.

## Detail View: Live Log Tail

Pressing Enter on a unit switches to a full-screen live tail of its build log.
The view shows the last ~20 lines of `build/<unit>/build.log`, updating on a
500ms tick if a build is running.

```
  ← linux (building...)

  CC      arch/x86/boot/header.o
  AS      arch/x86/boot/bioscall.o
  CC      arch/x86/boot/cmdline.o
  CC      arch/x86/boot/copy.o
  ...

  esc back  b build  d diagnose
```

Key bindings in detail view:

| Key   | Action                           |
| ----- | -------------------------------- |
| `Esc` | Return to unit list              |
| `b`   | Build this unit in background    |
| `d`   | Launch Claude diagnose (suspend) |

## Background Builds

Building a unit (`b` key) spawns `yoe build --force <unit>` as a child process.
Multiple builds can run concurrently. The TUI does not capture stdout/stderr
directly — the build writes to `build/<unit>/build.log` as usual. The TUI reads
the log file on each tick for the live tail view.

When the subprocess exits:

- Exit code 0 → status becomes `cached`
- Non-zero → status becomes `failed`

A Bubble Tea `tea.Msg` is sent when each subprocess completes, carrying the unit
name and exit code.

## Suspend/Resume for External Tools

The `e`, `d`, `l`, and `a` keys all suspend the TUI using Bubble Tea's
`tea.ExecProcess` (or equivalent `tea.Exec` with an `exec.Command`). This
releases the terminal to the child process. When the child exits, the TUI
resumes with its full state intact.

| Key | Command                          |
| --- | -------------------------------- |
| `e` | `$EDITOR <path-to-unit.star>`    |
| `d` | `claude /diagnose <unit>`        |
| `l` | `$EDITOR build/<unit>/build.log` |
| `a` | `claude /new-unit`               |

For `e`, the TUI locates the unit's `.star` file by searching
`modules/**/units/**/<name>.star`.

## Architecture

All code lives in `internal/tui/app.go` (single file, extending the existing
Bubble Tea app). The model struct gains:

```go
type unitStatus int
const (
    statusNone unitStatus = iota
    statusCached
    statusBuilding
    statusFailed
)

type model struct {
    proj       *yoestar.Project
    projectDir string
    units      []string
    statuses   map[string]unitStatus  // per-unit build status
    cursor     int
    view       view                    // viewUnits or viewDetail
    detailUnit string                  // unit shown in detail view
    logLines   []string               // last N lines of build log
    tick       bool                    // toggles for flashing effect
    width      int
    height     int
    message    string
    builds     map[string]*exec.Cmd   // running build subprocesses
}
```

Views reduce to two: `viewUnits` (default) and `viewDetail` (log tail). The old
`viewMain` and `viewMachines` are removed.

### Messages

```go
type buildDoneMsg struct {
    unit string
    err  error
}

type tickMsg time.Time
```

A 500ms ticker drives the flashing indicator and log tail refresh.

## Files to Modify

| File                  | Change                                           |
| --------------------- | ------------------------------------------------ |
| `internal/tui/app.go` | Rewrite: unit list view, background builds, etc. |
| `cmd/yoe/main.go`     | Already done: no-args launches TUI               |

## Verification

1. `yoe` with no args in the e2e-project shows the unit list with correct
   statuses
2. `b` on a unit starts a background build, indicator flashes green
3. Build completion flips status to cached (green) or failed (red)
4. Multiple concurrent builds work without interference
5. `e` opens the unit file in `$EDITOR` and returns to TUI
6. `l` opens the build log in `$EDITOR` and returns to TUI
7. `d` launches Claude Code and returns to TUI
8. `Enter` shows live log tail that updates during a build
9. `Esc` from detail view returns to unit list
10. `q` exits cleanly, killing any running build subprocesses
