# Serial console for hardware in the loop — Implementation Plan

> **Status:** Implemented 2026-09-19 (all seven phases; unit tests green).
> Hardware verification on a Raspberry Pi 5 with a USB serial adapter is still
> to be done by hand, see Verification below. Target-state doc:
> [hardware-in-loop](../hardware-in-loop.md). Tracks GitHub issue #157 (HIL:
> serial terminal through the TUI). The SSH terminal half of that issue is out
> of scope here.

## Problem

Developing against a real board means watching its serial console: the
bootloader, the kernel, the login prompt, the panic. Today that console is
outside yoe. The developer runs `tio` or `screen` in another terminal, nothing
is logged unless they remember to, and Claude Code has no way to read what the
board printed or to type at it. The TUI already knows which machine is selected
and already hands the terminal to QEMU for `r`; it should own the hardware
console the same way.

## Design

The design is fixed by [docs/hardware-in-loop.md](../hardware-in-loop.md); this
section records the decisions behind it.

- **Machine declares speed, developer declares device.** `serial_config(baud)`
  on the machine is intrinsic to the board and checked in. The host adapter is
  per developer and lives in `local.star` as `serial_ports = {machine: path}`,
  where the path is the adapter's `/dev/serial/by-id/` link. The kernel and udev
  already give every USB adapter with a serial number a stable, readable name
  there, so yoe stores that path as is and never invents an identity scheme of
  its own. Adapters with no by-id entry are stored by `/dev/tty*` path. Machines
  without a `serial` block have no console; yoe never guesses a baud.
- **One owner, many attachments.** Serial adapters are exclusive, so the first
  yoe process to open one becomes the owner and serves the raw stream on
  `build/serial/<machine>.sock`. Every other yoe process (a second terminal,
  Claude Code) attaches through the socket. This is what lets the TUI keep
  capturing while `yoe serial send` types at the board.
- **One log representation.** `build/serial/<machine>.log` is line-oriented,
  timestamped, escape-stripped text. It is what a person reads after the fact
  and what Claude reads; the raw stream stays available live through the socket.
  Not keeping a second raw log keeps the feature to one file per machine.
- **Interactive mode is passthrough, not emulation.** The TUI releases the
  terminal (bubbletea `tea.Exec`) and yoe copies bytes in raw mode, `tio` style.
  The user's terminal emulator does the VT work, so no in-TUI terminal emulator
  is needed. The Serial tab is a read-only scrollback of the stripped text for
  monitoring.
- **Enumeration is a directory listing.** `serial.List()` reads
  `/dev/serial/by-id/`, resolves each link, and adds any `/dev/ttyUSB*` or
  `/dev/ttyACM*` node no link points at. The vendor, product, and serial shown
  in the picker are parsed from the link name
  (`usb-<Vendor>_<Product>_<Serial>-if00-port0`). No sysfs walk and no USB
  library. On a host without `/dev/serial/by-id/` (macOS), the list is the
  `/dev/cu.*` nodes.
- **Library.** Use `go.bug.st/serial` (BSD-3, Apache-compatible) only to open a
  port with the right termios settings on Linux and macOS, matching the `!linux`
  build tags already in `internal/device`. Raw terminal mode uses
  `github.com/charmbracelet/x/term`, already in the module graph.
- **Cache-neutral.** `Machine.Serial` is host-side. Nothing in
  `internal/resolve/hash.go` reads machine fields other than the name, so the
  new field cannot enter a unit hash. Verify with `yoe desc` before and after.

Out of scope, recorded in the doc as `(planned)`: routing the QEMU console
through the same session, and direct programming. Both build on the `Transport`
seam introduced in Phase 3, so the plan keeps that seam clean.

## Phase Overview

- **Phase 1** — Remaining target-state docs (`yoe-tool.md`, board pages).
- **Phase 2** — Starlark surface: `serial_config()`, `Machine.Serial`,
  `local.star` `serial_ports` / `serial_listen`; BSP machines declare consoles.
- **Phase 3** — `internal/serial`: by-id enumeration, open, `Transport`.
- **Phase 4** — Session: fan-out, log writer, reconnect, socket owner/client,
  TCP listener.
- **Phase 5** — CLI: `yoe serial` (attach, log, send, list), raw-mode attach
  loop with `ctrl-t` chords, `yoe config set serial-port`.
- **Phase 6** — TUI: Setup → Serial port picker, Serial tab, Enter → attach.
- **Phase 7** — Skill, changelog, docs-vs-code verification.

Each phase ends with `go build ./... && go test ./...` green and `gofmt`.

## Phase 1: Target-state docs

`docs/hardware-in-loop.md` is written in final form already. Finish the rest so
a reviewer sees the whole surface before code lands.

### Task 1.1: `docs/SUMMARY.md`

Done alongside this plan: the page is listed in the User Guide section after
Monitoring and Telemetry.

### Task 1.2: `docs/yoe-tool.md`

- Command Overview table: add `yoe serial`.
- New `### yoe serial` section after `### yoe run`, with the command block from
  the HIL doc and a one-paragraph pointer to the HIL doc for the session model.
- `### yoe config`: document `serial-port`.
- TUI section: the intro sentence names four tabs; add **Serial**. Key bindings
  table gets `tab` wording updated. Setup page mention of **Serial port**. New
  `#### Serial tab` subsection with the key table from the HIL doc.
- `machine()` example under `yoe run` is QEMU; leave it. Add `serial` to the
  machine fields reference in `docs/metadata-format.md` where `qemu` is
  described, and the `local.star` fields (`serial_ports`, `serial_listen`)
  wherever `qemu_ports` is documented.

### Task 1.3: Board pages

`docs/machine-rpi.md` and `docs/machine-beagleplay.md`: one sentence each noting
the machine declares a 115200 console and linking to the HIL doc for mapping an
adapter.

## Phase 2: Starlark surface

### Task 2.1: `serial_config()` builtin and `Machine.Serial`

`internal/starlark/types.go`:

```go
type SerialConfig struct {
    Baud int
}

type Machine struct {
    ...
    Serial *SerialConfig // nil when the machine declares no serial console
}
```

`internal/starlark/builtins.go`: register `serial_config` next to `qemu_config`
(a `makeStruct`). In `fnMachine`'s kwargs loop add `case "serial"`: require the
struct, require `baud` to be a positive int, error otherwise with the machine
name (`machine %q: serial requires baud`). No default baud.

Test in `internal/starlark`: machine with `serial` parses `Baud`; machine
without leaves `Serial` nil; `serial_config()` with no baud errors.

### Task 2.2: `local.star` fields

`internal/starlark/local.go`:

- `LocalOverrides.SerialPorts map[string]string` and
  `LocalOverrides.SerialListen string`.
- Loader: `serial_ports` must be a dict of string → string; `serial_listen` is a
  string. Reuse the existing error style.
- Writer: emit `serial_ports = {...}` with keys sorted, only when non-empty;
  emit `serial_listen` when set.

Extend the existing load/write round-trip test.

### Task 2.3: BSP machines declare their consoles

Add `serial = serial_config(baud = 115200)` to
`modules/module-bsp/machines/raspberrypi4.star`, `raspberrypi5.star`, and
`beagleplay.star`. The speed matches each `cmdline` `console=` argument.

`cache/modules/module-jetson/machines/jetson-orin-nano-devkit.star` is an
external module: make the same edit in the cached copy, surface the path, and
hand the commit and push to the user before any command that syncs modules.

Verify hash neutrality: `yoe desc dev-image --machine raspberrypi5` in
`testdata/e2e-project` shows the same input hash before and after this task.

## Phase 3: `internal/serial` — ports and transport

New package. No dependency on `internal/tui` or `internal/device`.

### Task 3.1: Enumeration

```go
type Port struct {
    ByID   string // /dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0, or ""
    Path   string // /dev/ttyUSB0 (what ByID resolves to this boot)
    Vendor string // "FTDI FT232R USB UART", parsed from the by-id name
    Serial string // "A50285BI", parsed from the by-id name
}

func List() ([]Port, error)
func Match(ports []Port, mapped string) (Port, bool)
func (p Port) Key() string // ByID when set, else Path: the value stored in local.star
```

`List` reads `/dev/serial/by-id/` (missing directory is not an error), resolves
each link with `filepath.EvalSymlinks`, then appends any `/dev/ttyUSB*` and
`/dev/ttyACM*` node not already covered. `Match` compares `mapped` against
`Key()` first, then against `Path`, so a hand-written `/dev/ttyUSB0` still
matches the adapter that currently sits there. Parsing the by-id name into
vendor and serial is a pure function; table-test it on FTDI, CP210x, CH340
(`usb-1a86_USB_Serial-if00-port0`, no serial), and an `-if01` multi-port name.
Test `List` against a temp dir standing in for `/dev` via an injected root.

### Task 3.2: Transport

```go
type Transport interface {
    io.ReadWriteCloser
    Break() error
}

func Open(path string, baud int) (Transport, error) // 8N1, no flow control
```

Implement `Break` with the library's `SetBreak` around a 250 ms pause. Add
`go.bug.st/serial` to `go.mod`; note the license in the commit message.

## Phase 4: Session — capture, log, socket

### Task 4.1: Log writer

```go
type LogWriter struct{ ... }
func NewLogWriter(w io.Writer, now func() time.Time) *LogWriter
func (l *LogWriter) Write(p []byte) (int, error)
func (l *LogWriter) Marker(text string)
```

Behavior: assemble bytes into lines; treat `\r\n`, `\n`, and lone `\r` as line
ends (a bare `\r` after printable text is a line end so progress bars do not
concatenate); strip CSI/OSC/single-ESC sequences; drop other control bytes
except `\t`; prefix each line with `[2006-01-02 15:04:05.000] `. `Marker` writes
`[ts] --- serial capture started: <machine> <by-id or path> <path> <baud> ---`.
Flush a partial line after 250 ms of silence so a prompt with no newline still
lands in the log. Table-driven tests: CR-only input, escape-laden `ls --color`
output, split escape sequence across two `Write`s, idle flush.

### Task 4.2: Session core

```go
type Session struct{ ... }

type Config struct {
    Machine  string
    Mapped   string // the local.star value: by-id path or /dev/tty* path
    Baud     int
    Dir      string // build/serial
    Listen   string // optional TCP address
}

func Open(ctx context.Context, cfg Config) (*Session, error)
func (s *Session) Subscribe() (<-chan []byte, func())  // raw output fan-out
func (s *Session) Write(p []byte) (int, error)          // to the adapter
func (s *Session) Break() error
func (s *Session) State() State                          // connected / waiting / error, current Path
func (s *Session) Close() error
```

- Reader goroutine copies adapter → log writer and all subscribers. Slow
  subscribers drop with a counter rather than stall the reader.
- Reconnect: when the adapter is missing at open or the read returns an error,
  enter `waiting`, re-run `List` + `Match` every 2 s, reopen on success, write a
  marker line. Surface the state so the TUI can show it.
- The session owns `<Dir>/<machine>.log` (append, `0o644`) and creates `Dir`.

Test with a fake `Transport` built on `net.Pipe` and an injected `List` func:
data flows to the log and to two subscribers; a transport error triggers
`waiting` then reconnect; `Close` releases everything.

### Task 4.3: Socket owner and client

- Owner: listen on `<Dir>/<machine>.sock` (remove a stale socket only after a
  failed dial proves nothing is behind it). Each accepted connection is a
  subscriber for output and a writer for input. Same for the optional TCP
  listener in `Config.Listen`.
- Client:

```go
func Attach(ctx context.Context, sockPath string) (io.ReadWriteCloser, error)
func OpenOrAttach(ctx context.Context, cfg Config) (Console, bool /*owner*/, error)
```

`Console` is the small interface both the `Session` and the attached socket
satisfy (`Read`, `Write`, `Close`, `Break` returning `ErrBreakUnavailable` on a
client). `OpenOrAttach` dials first; on connection-refused it opens the adapter
and becomes owner. Test owner/client with a temp dir.

### Task 4.4: `yoe clean`

`build/serial/` sits under `build/`, so `yoe clean` already removes it. Add a
test asserting a stray `.sock` and `.log` are gone after clean, so the path
stays covered if the layout moves.

## Phase 5: CLI

### Task 5.1: Machine and mapping resolution

`internal/device/serialcfg.go` (or a helper in `internal/serial` that takes the
project): resolve machine with the same cascade `yoe run` uses (`--machine`,
`local.star` machine, `Defaults.Machine`), then require `Machine.Serial` and a
`SerialPorts[machine]` entry. Errors name the fix:

- no `serial` block →
  `machine "x" declares no serial console; add serial = serial_config(baud = ...) to its machine file`
- no mapping →
  `no serial adapter mapped for "x"; run yoe serial list, then yoe config set serial-port <path>`

### Task 5.2: Raw attach loop

`internal/serial/attach.go`:

```go
func Interactive(ctx context.Context, c Console, in io.Reader, out io.Writer, opts InteractiveOptions) error
```

Puts `in` into raw mode when it is a terminal (`term.MakeRaw`, restore on
return), copies `out ← c` and `c ← in` with the `ctrl-t` prefix parser: `q`
return, `b` `c.Break()` (print `break unavailable through the socket` on
`ErrBreakUnavailable`), `t` toggle a local timestamp prefix on output lines, `?`
print the chord list, `ctrl-t` send a literal `0x14`. The parser is a pure state
machine; test it on byte sequences.

### Task 5.3: `yoe serial` subcommands

`cmd/yoe/serial.go`, dispatched from `main.go`:

- `yoe serial [--machine m] [--listen addr]` → `OpenOrAttach` then
  `Interactive`. On owner exit it closes the session; on client EOF it prints
  `session ended; taking over the adapter` and reopens as owner.
- `yoe serial log [-f] [-n N]` → read `build/serial/<machine>.log`; `-f` polls
  for growth (no inotify dependency). Works with no session running.
- `yoe serial send [--raw] text...` → attach or open, write text (`+ "\n"`
  unless `--raw`), exit. Open-as-owner here must not linger: close after the
  write.
- `yoe serial list` → table of `IDENTITY  PATH  VENDOR  PRODUCT`, marking the
  one mapped to the current machine.

### Task 5.4: `yoe config set serial-port`

Extend the `config set` switch in `cmd/yoe/main.go`: `serial-port <id>` sets
`SerialPorts[currentMachine]`; empty value deletes the key. Update the
supported-keys error string. Extend `yoe config show` to print the mapping for
the current machine.

## Phase 6: TUI

All in `internal/tui`; keep new code in `serial.go` rather than growing `app.go`
further.

### Task 6.1: Session lifecycle

- Model gains `serial *serial.Session`, `serialLines []string` (bounded ring, 10
  000 lines), `serialFollow bool`, `serialCursor int`, and the `local.star`
  fields.
- On start, and whenever the selected machine changes on Setup, call
  `serialStart()`: if `Machine.Serial` and a mapping exist, open the session
  (owner) and start a `tea.Cmd` that reads a subscriber channel through a
  `LogWriter`-style line splitter and delivers `serialLinesMsg`. If a session
  already owns the socket (a `yoe serial` in another terminal started first),
  attach as a client instead and show `attached to existing session` in the
  header.
- Close the session on quit and on machine change.

### Task 6.2: Setup → Serial port

Add `"Serial port"` to `setupOptions`. Sub-screen mirrors the flash page: rows
from `serial.List()` refreshed every 2 s while open. Each row shows `Vendor`,
`Serial`, and `Path`, with `(mapped)` on the row whose `Key()` equals the
current mapping and `(no by-id entry)` on rows with an empty `ByID`. Enter
writes `Key()` to `SerialPorts[machine]`, `d` deletes it, Esc returns. Both
persist through the existing `WriteLocalOverrides` call and restart the session.
When the machine has no `serial` block, the sub-screen shows the fix-it message
from Task 5.1 instead of the list.

### Task 6.3: Serial tab

Add `tabSerial` after `tabDiagnostics`. `viewSerialTab()` renders a header line
(machine, by-id name, path, baud, state) and a viewport of `serialLines`. Keys
per the HIL doc: `j/k`, `g/G`, `c`, `s`, `Enter`, `?`. Scrolling up sets
`serialFollow = false`; `G` sets it back. Help overlay entries for the tab.

### Task 6.4: Enter → interactive

Implement `tea.ExecCommand` in-process:

```go
type serialExec struct{ c serial.Console; in io.Reader; out io.Writer }
func (e *serialExec) Run() error { return serial.Interactive(ctx, e.c, e.in, e.out, opts) }
```

Return
`tea.Exec(&serialExec{...}, func(err error) tea.Msg { return execDoneMsg{err: err} })`.
bubbletea releases the terminal for the duration; `ctrl-t q` returns to the tab.
Capture continues because the session is unaffected.

### Task 6.5: Tests

Follow the QEMU-ports TUI tests: Setup row present; picker writes and clears the
mapping; tab renders header states (`waiting`, `connected`, no mapping);
`serialLinesMsg` appends and follow/scroll behave; ring buffer caps at the
bound.

## Phase 7: Skill, changelog, verification

### Task 7.1: `serial-console` skill

Add `.claude/skills/serial-console/SKILL.md` (embedded via `SkillsFS`): when to
use (board attached, boot or runtime question), the three commands
(`yoe serial log -n`, `yoe serial log -f`, `yoe serial send`), how to find the
login prompt or a panic in the log, and the rule to never hold `yoe serial`
interactive open from a script. Mention it in `docs/ai-skills.md` under Build
Debugging.

### Task 7.2: Changelog

One `[Unreleased]` entry, user-benefit first: the TUI captures a board's serial
console, shows it in a Serial tab, logs it, and `yoe serial` lets a shell or
Claude read and type at it.

### Task 7.3: Docs agree with code

Walk `docs/hardware-in-loop.md` and the `yoe-tool.md` additions against the
implementation: every key, flag, field, and error string named in the docs
exists and behaves as written. Fix the code or the doc, not the plan. Flip this
plan's Status and the `SPEC_PLAN_INDEX.md` row to Done.

## Verification

- `go build ./... && go test ./...` green; `gofmt -l` empty;
  `source envsetup.sh && yoe_format`.
- Hash neutrality: `yoe desc dev-image --machine raspberrypi5` identical before
  and after Phase 2 in `testdata/e2e-project`.
- Hardware: Raspberry Pi 5 on an FTDI adapter. Map it on Setup, power the board,
  watch the boot in the Serial tab, press Enter and log in, `ctrl-t q` back,
  confirm `build/serial/raspberrypi5.log` has the boot with timestamps and no
  escape sequences. From a second terminal with the TUI still running:
  `yoe serial send "uname -a"` then `yoe serial log -n 5` shows the reply.
  Unplug and replug the adapter: tab shows `waiting`, then `connected`, and a
  marker line lands in the log.
- macOS build: `GOOS=darwin go build ./...` compiles. `List` is plain filesystem
  code, so it needs no build tag; on a host without `/dev/serial/by-id/` it
  falls through to the `/dev/cu.*` glob.
