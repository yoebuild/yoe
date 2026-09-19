# Hardware in the Loop

`[yoe]` is built for development with real hardware in the loop, whether the
loop is closed by a person or by an AI agent. The board sits on the desk, its
serial console is always captured, and everything yoe learns from it is
available to the TUI, to the shell, and to Claude Code.

## Serial console

Every board with a UART header exposes its bootloader and kernel console over a
USB serial adapter. yoe treats that console as a first-class input:

- A machine declares its console speed once, in its machine definition.
- Each developer maps that machine to a specific USB serial device on their
  host, and the mapping survives replugs and port renumbering.
- While the TUI runs, yoe captures the console continuously, shows it in a
  **Serial** tab, and appends it to a timestamped log file.
- Other processes attach to the live session through a local socket, so a shell
  or Claude Code can read and write the console while the TUI still owns the
  port. A TCP listener is available for tools on other hosts.
- An interactive mode hands the whole terminal to the board, the way
  [tio](https://github.com/tio/tio) does, and a key chord returns to the TUI.

### Declaring the console on a machine

A machine that has a serial console declares its speed with `serial`:

```python
machine(
    name = "raspberrypi5",
    arch = "arm64",
    kernel = kernel(
        unit = "linux-rpi5",
        cmdline = "console=ttyAMA0,115200 root=/dev/mmcblk0p2 rootwait rw",
    ),
    serial = serial_config(baud = 115200),
    ...
)
```

`baud` is required. The block carries only what is intrinsic to the board; which
host device the console is plugged into is a per-developer choice and lives in
`local.star` (below). A machine without a `serial` block has no serial console
as far as yoe is concerned. The QEMU machines are in this group: their console
is the terminal `yoe run` starts in.

The `serial` block is a host-side setting. It does not enter any unit's input
hash, so adding it to a machine does not invalidate the cache.

### Mapping a machine to a host device

The console for a machine is chosen on the TUI Setup page (`s`): move to
**Serial port** and press Enter. The sub-screen lists every serial adapter on
the host, one row per entry in `/dev/serial/by-id/`, with the vendor, product,
and serial number read out of the entry name and the `/dev/tty*` path it points
at this boot:

```
  Serial port for raspberrypi5

→ FTDI FT232R USB UART        A50285BI   /dev/ttyUSB0   (mapped)
  Silicon Labs CP2102N        0001       /dev/ttyUSB1
  /dev/ttyACM0                           (no by-id entry)

  Enter select   d clear   Esc back
```

Enter selects the row for the current machine, `d` clears the selection, and Esc
goes back. The choice is written to `local.star` as the by-id path:

```python
local(
    serial_ports = {
        "raspberrypi5": "/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0",
        "beagleplay": "/dev/ttyACM0",
    },
)
```

A by-id path names the physical adapter, not the port number it happened to get
this boot, so the mapping holds when `/dev/ttyUSB0` and `/dev/ttyUSB1` trade
places or the adapter moves to another hub. The kernel and udev maintain these
links; yoe only lists them. An adapter with no by-id entry (some bridges report
no serial number) is listed under its `/dev/tty*` path and stored that way; that
path can move between boots, so such a mapping is worth a glance on the picker
after a replug.

The same setting is available from the shell:

```sh
# List the serial adapters yoe can see, with by-id and current paths
yoe serial list

# Map the current machine to an adapter (written to local.star)
yoe config set serial-port /dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0

# Clear the mapping
yoe config set serial-port ""
```

### Capture

When the TUI starts and the selected machine has both a `serial` block and a
`local.star` mapping, yoe opens the adapter and starts capturing. If the adapter
is not present yet, the Serial tab reports that it is waiting for the device and
yoe keeps looking; when the adapter disappears mid-session, capture resumes as
soon as it returns. Switching machines on the Setup page closes the old console
and opens the new one.

Capture writes two things under the project's `build/serial/` directory:

- `<machine>.log` — the console as line-oriented text. Every line carries a
  timestamp, and terminal escape sequences are removed so the file reads cleanly
  in an editor, in `grep`, or in Claude Code. A marker line records each time
  capture starts. The file is appended across sessions; `yoe clean` removes it.
- `<machine>.sock` — a Unix socket that carries the raw console in both
  directions. Whichever yoe process opened the adapter owns the socket; every
  other yoe process attaches through it instead of opening the adapter a second
  time. When the owner exits, the socket goes away and an attached interactive
  `yoe serial` takes over the adapter itself.

To expose the same raw stream on the network, set a listen address:

```python
local(
    serial_listen = "127.0.0.1:7001",
)
```

or pass `--listen` to `yoe serial`. Anything that speaks a plain TCP stream can
attach, for example `nc localhost 7001`. There is no authentication on this
port; bind it to loopback or a trusted network only.

### The Serial tab

The home screen's fourth tab (`tab` / `shift+tab` to cycle) shows the captured
console. It follows the newest output by default; scrolling up pauses the follow
and `G` resumes it. The header shows the machine, the adapter's by-id name and
current path, the baud rate, and whether the adapter is connected.

| Key         | Action                                                         |
| ----------- | -------------------------------------------------------------- |
| `Enter`     | Attach interactively (full-screen, see below)                  |
| `j/k` `↑/↓` | Scroll                                                         |
| `g/G`       | Jump to the oldest captured line / resume following the newest |
| `c`         | Clear the on-screen scrollback (the log file is untouched)     |
| `s`         | Open Setup to change the adapter mapping                       |
| `?`         | Show the keyboard cheat sheet for this tab                     |

The scrollback held in memory is bounded; the log file is the complete record.

### Interactive mode

Pressing Enter on the Serial tab, or running `yoe serial` in a shell, gives the
terminal to the board. Keystrokes go to the console byte for byte and the
console's output is drawn by your terminal emulator, so full-screen programs on
the target (`vi`, `top`, a bootloader menu) work exactly as they do over `tio`
or `screen`. Capture continues in the background, so the session still lands in
the log.

A `ctrl-t` prefix reaches the local commands, following `tio`:

| Chord           | Action                                       |
| --------------- | -------------------------------------------- |
| `ctrl-t q`      | Detach and return to the TUI or the shell    |
| `ctrl-t b`      | Send a break                                 |
| `ctrl-t t`      | Toggle a local timestamp prefix on each line |
| `ctrl-t ?`      | List these chords                            |
| `ctrl-t ctrl-t` | Send a literal `ctrl-t` to the board         |

Sending a break requires owning the adapter. A `yoe serial` attached through the
socket forwards bytes only, so `ctrl-t b` reports that it is unavailable there.

### `yoe serial`

```sh
# Attach interactively to the current machine's console
yoe serial

# Attach to another machine's console
yoe serial --machine beagleplay

# Show the captured log; -f follows it like tail -f
yoe serial log
yoe serial log -f
yoe serial log -n 200

# Send a line to the console (a newline is appended)
yoe serial send "cat /proc/cpuinfo"

# Send text with no newline appended
yoe serial send --raw $'\x03'

# List detected adapters
yoe serial list

# Also serve the raw stream on a TCP port for this session
yoe serial --listen 127.0.0.1:7001
```

`yoe serial` and `yoe serial send` attach to a running session's socket when one
exists and open the adapter themselves when one does not. `yoe serial log` reads
the log file, so it works whether or not anything is attached. All three resolve
the machine the same way `yoe run` does: `--machine`, then the `local.star`
machine, then the project default.

### Using the console from Claude Code

The log file and `yoe serial send` are all an agent needs to close the loop:

```sh
yoe serial send "dmesg | tail -20"
yoe serial log -n 40
```

The log is plain timestamped text, so the same file that a person reads after a
failed boot is the file Claude reads to find where the boot stopped. The
`/diagnose` skill reads it alongside the build log when a unit's image is the
one on the board. A typical loop is: flash or deploy, watch `yoe serial log -f`
until the login prompt or a panic, act on what it says, repeat.

## Direct programming (planned)

> **Status:** Today `yoe flash` writes an image to removable media, and the
> Arduino UNO Q work adds a partition-level `qdl` path for boards with no
> removable medium. Programming a board in place over its USB or debug port,
> driven from the same Setup page that maps its console, is future work and has
> no implementation plan yet.

## QEMU console capture (planned)

> **Status:** `yoe run` attaches the QEMU serial console to the terminal it
> starts in, and `--boot-test` watches that console for the login marker. A
> future change routes the QEMU console through the same capture session as a
> hardware console, so the Serial tab, the log file, and `yoe serial send` work
> identically for a virtual machine. Not yet planned in detail.
