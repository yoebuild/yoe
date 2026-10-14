---
name: serial-console
description: >
  This skill should be used when a board is attached to the host and the user
  asks what it printed, whether it booted, why it stopped at U-Boot or a kernel
  panic, to "check the serial console", "watch the board boot", "send a command
  to the board", "/serial-console", or when a build or deploy needs to be
  verified on real hardware. Reads and drives the board's serial console through
  `yoe serial`.
---

# Serial Console

Read and type at the selected machine's serial console through the log and
socket yoe keeps under `build/serial/`. The TUI (or a `yoe serial` in another
terminal) owns the adapter; these commands attach to it, so nothing has to be
closed first.

## Read what the board printed

```sh
yoe serial log -n 60        # the last 60 lines
yoe serial log -f           # follow live, ctrl-c to stop
```

The log is timestamped, escape-stripped text, one console line per line. Marker
lines (`--- serial capture started … ---`,
`--- serial adapter disconnected … ---`) show where a session or a replug
happened. Look for:

- `U-Boot` and `Starting kernel` for the boot chain.
- `Kernel panic`, `Oops`, `BUG:` for a crash.
- `login:` for a complete boot; `Welcome to` on Debian and Ubuntu images.
- A long gap between timestamps for a hang.

Read the log rather than attaching interactively: an interactive attach holds
the terminal and never returns on its own.

## Type at the board

```sh
yoe serial send "uname -a"
yoe serial log -n 5
```

`send` appends a newline; `--raw` sends the bytes as given, for example
`yoe serial send --raw $'\x03'` for ctrl-c. Give the board a moment before
reading the reply. There is no login automation: if the log shows a `login:`
prompt, send the user name, then the password, then the command.

## When nothing is captured

`yoe serial log` says there is no log yet when no yoe process has owned the
console. Check in this order:

1. `yoe serial list` shows the adapter. If not, it is not plugged in or the user
   lacks permission on the device node.
2. `yoe config show` reports a `Serial:` line for the machine. `unmapped` means
   `yoe config set serial-port <by-id path>` (or the TUI Setup page) is needed;
   no line at all means the machine declares no
   `serial = serial_config(baud = ...)`.
3. Start the TUI, or run `yoe serial` in a terminal the user watches, so a
   process owns the adapter and capture begins.

## Rules

- Never leave `yoe serial` (interactive) running from a script; use `log` and
  `send`.
- Do not send destructive commands (`reboot`, `dd`, `mkfs`, package removal)
  without the user asking for them.
- Quote log lines back with their timestamps when explaining a failure, so the
  user can find them in `build/serial/<machine>.log`.
