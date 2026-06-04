<!--
Spec: Bound the TUI detail-view log panes (fix OOM from unbounded log reads)
Date: 2026-06-04
-->

# Bound the TUI detail-view log panes: fix the OOM from unbounded log reads

## Summary

The interactive TUI (`yoe` with no args) can drive the whole machine into the
kernel OOM killer while a build is running. The detail view loads the **entire**
`executor.log` and `build.log` for the selected unit into `[]string` slices on
every 500 ms tick, and re-materializes every wrapped display line on every
render. A chatty or runaway build log grows those slices without bound: observed
twice on 2026-06-04 reaching ~52–53 GB resident on a 62 GB host before the
process was killed.

The fix is to stop holding whole logs in memory. Read only the last
`maxDetailLogLines` lines via a ring buffer (`readFileTail`), and skip the
re-read entirely when the file has not grown since the last tick. Memory for the
two panes is bounded to tens of MB regardless of on-disk log size. The complete
logs remain on disk at `executor.log` / `build.log`.

This is a reliability fix, not a feature. No user-facing behavior changes for
normal builds; the only observable difference is that in-TUI scrollback is
capped at `maxDetailLogLines` per pane (the full log stays on disk).

## The bug, precisely

`internal/tui/app.go`:

```go
func readFileAll(path string) []string {        // reads the ENTIRE file, uncapped
    ...
    for scanner.Scan() { lines = append(lines, scanner.Text()) }
}

func (m *model) refreshDetail() {
    m.outputLines = readFileAll(.../executor.log)
    m.logLines    = readFileAll(.../build.log)
    if m.autoFollow { m.scrollToBottom() }
}
```

`refreshDetail()` is invoked from the 500 ms `tickMsg` handler (`tea.Tick` →
`case tickMsg` → `refreshDetail`) whenever the detail view is open. So while a
unit builds and its log grows, every half-second the TUI:

1. Re-reads both whole files into fresh `[]string` slices (two resident copies,
   the old ones awaiting GC).
2. Calls `detailAllLines()`, which wraps **every** output and log line into a
   third, fully materialized `[]string` for windowing, search, and
   `detailMaxScroll` — `detailAllLines()` is called multiple times per frame
   (render at the view body, again in `applyDetailSearch`, again in
   `detailTotalLines`/`detailMaxScroll`).

For a multi-GB or runaway log this is tens of GB of `string` headers plus
backing bytes, re-allocated at 2 Hz. That is the OOM.

### Evidence

Both kills name `yoe`, not the terminal, inside the per-surface cgroup that
makes the journal *look* like the terminal died:

```
oom-kill: ...cpuset=user.slice...app-ghostty-surface-transient-3677.scope,task=yoe,pid=168992
Out of memory: Killed process 168992 (yoe) total-vm:122307104kB, anon-rss:52567296kB
Out of memory: Killed process 492775 (yoe) total-vm:117305356kB, anon-rss:53500732kB
```

An idle detail view (no growing log) sits at ~525 MB; the blowup only appears
under an active, verbose build — matching the loss of `signal-desktop` and
`obsidian` to the same global OOM at 17:01 and 17:17.

## Approach

### `readFileTail(path, maxLines)` — ring buffer

Replace `readFileAll` with a tail reader that keeps only the last `maxLines`
lines in a fixed-capacity ring, then unrolls to chronological order:

```go
// readFileTail returns the last maxLines lines of the file (all of them if
// fewer). Bounds resident memory so a runaway build log can't OOM the TUI.
// The complete log always remains on disk. Returns nil if unreadable.
func readFileTail(path string, maxLines int) []string {
    f, err := os.Open(path)
    if err != nil { return nil }
    defer f.Close()

    scanner := bufio.NewScanner(f)
    scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // long lines up to 1MB

    ring := make([]string, 0, maxLines)
    next, full := 0, false
    for scanner.Scan() {
        if !full {
            ring = append(ring, scanner.Text())
            if len(ring) == maxLines { full = true; next = 0 }
            continue
        }
        ring[next] = scanner.Text()
        next = (next + 1) % maxLines
    }
    if !full { return ring }
    out := make([]string, 0, maxLines)
    out = append(out, ring[next:]...) // oldest first
    out = append(out, ring[:next]...)
    return out
}

const maxDetailLogLines = 50000 // ~tens of MB per pane; far more than any sane build
```

### Skip re-reads when the file has not grown

`refreshDetail` re-reads on every tick even when nothing changed (the common
case: viewing a finished build). Track the last-seen size per pane, keyed on the
unit the cache belongs to, and read only when the size differs:

```go
// model fields
outputLines   []string // executor.log tail (capped at maxDetailLogLines)
logLines      []string // build.log tail (capped at maxDetailLogLines)
outputSize    int64    // last-seen size of executor.log
logSize       int64    // last-seen size of build.log
detailLogUnit string   // unit the cached panes/sizes correspond to

func (m *model) refreshDetail() {
    unitDir := build.UnitBuildDir(m.projectDir, m.unitScopeDir(m.detailUnit), m.detailUnit, m.distro)
    outputPath := filepath.Join(unitDir, "executor.log")
    logPath := filepath.Join(unitDir, "build.log")

    if m.detailLogUnit != m.detailUnit { // new unit: invalidate cache
        m.detailLogUnit = m.detailUnit
        m.outputSize, m.logSize = -1, -1
    }
    if sz, changed := fileSizeChanged(outputPath, m.outputSize); changed {
        m.outputLines, m.outputSize = readFileTail(outputPath, maxDetailLogLines), sz
    }
    if sz, changed := fileSizeChanged(logPath, m.logSize); changed {
        m.logLines, m.logSize = readFileTail(logPath, maxDetailLogLines), sz
    }
    if m.autoFollow { m.scrollToBottom() }
}

// fileSizeChanged reports the file's current size (0 if missing) and whether
// it differs from prev.
func fileSizeChanged(path string, prev int64) (int64, bool) {
    var sz int64
    if fi, err := os.Stat(path); err == nil { sz = fi.Size() }
    return sz, sz != prev
}
```

Keying the cache on `detailLogUnit` makes it self-correcting: the two `esc`
reset sites that already `nil` the panes also clear `detailLogUnit` (`= ""`) so
re-entering the same unit reloads. No other call site needs to know about the
size tracking.

The downstream consumers (`detailAllLines`, the view windowing,
`applyDetailSearch`, `detailMaxScroll`, `scrollToBottom`) keep operating on
`outputLines` / `logLines` unchanged — they are simply now bounded slices, so
the per-render transient is bounded too.

## Considered alternatives

- **Offset-index lazy windowing (full, unbounded scrollback).** Store one
  `int64` byte-offset per line and materialize only the on-screen window by
  seeking. Preserves complete scrollback at ~8 MB per million lines. Rejected
  for now: the detail view's unified scroll+search model assumes a single fully
  materialized `[]string` with absolute line indices (search stores match
  indices into it; `detailMaxScroll`, match-centering, and the dependency
  header all index the same slice). Reworking that is a large change to the core
  TUI rendering/search path and is not interactively verifiable in this
  environment. A bounded tail removes the OOM with a localized, testable change;
  the offset index can be a follow-up if longer in-TUI scrollback is wanted.

- **Generous flat cap with full re-read each tick.** Simpler, but still rescans
  the whole on-disk file at 2 Hz; the size-skip is cheap and removes the idle
  cost, so it is included.

## Scope

Touches only `internal/tui/app.go`:

- Replace `readFileAll` with `readFileTail` + add `fileSizeChanged` and the
  `maxDetailLogLines` const.
- Add `outputSize`, `logSize`, `detailLogUnit` model fields.
- Update `refreshDetail` to size-gate reads and tail-cap.
- Clear `detailLogUnit` at the two detail-exit (`esc`) reset sites alongside the
  existing `outputLines = nil` / `logLines = nil`.

Add a unit test (`internal/tui/app_test.go`) for `readFileTail`: fewer than
`maxLines`, more than `maxLines` (verifies it keeps the *last* N in order), and
a missing file (returns `nil`).

## Non-goals / limitations

- In-TUI scrollback is capped at `maxDetailLogLines` per pane. The full,
  untruncated logs remain on disk at `executor.log` / `build.log` and are
  reachable via the detail view's editor/shell actions.
- No change to how logs are written, only how the TUI reads them.

## Validation

- `go build ./...` and `go test ./internal/tui/...`.
- Manual: open the detail view on a unit, run a verbose build, confirm resident
  memory stays flat (tens of MB for the panes) instead of climbing with log
  size; confirm auto-follow still tracks the tail and search still works within
  the retained window.
