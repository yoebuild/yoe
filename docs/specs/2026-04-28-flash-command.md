# `yoe flash` command — design

## Goal

Write a built `yoe` image to a removable block device (SD card / USB stick) on
the developer's Linux workstation. The command never runs as root on its own;
when privilege is required, it asks for explicit consent and shells out to
`sudo` once. Long-term the same command will flash devices over USB recovery
protocols (e.g. Jetson `tegrarcm`/`tegraflash`); v1 covers SD/USB only and is
written without speculative abstractions.

## Non-goals (v1)

- macOS / Windows support
- Image formats other than raw `.img`
- Read-back verification
- A backend interface for non-SD targets (added when Jetson lands and the real
  shape is known)
- External Go module dependencies for device enumeration
- Auto-elevation; the only `sudo` invocation is the chown prompt the user
  explicitly approves

## CLI surface

```
yoe flash <image-unit> <device>            # write image to device
yoe flash <image-unit> <device> --yes      # skip confirmation
yoe flash <image-unit> <device> --machine raspberrypi4
yoe flash <image-unit> <device> --dry-run  # report what would happen, no write
yoe flash list                             # enumerate candidate devices
```

- `<image-unit>` must be a unit with `class == "image"`. Error otherwise.
- `<device>` is a block device path (e.g. `/dev/sdb`, or `/dev/disk/by-id/...`
  which is symlink-resolved before validation). Required — there is no
  auto-pick.
- `--machine` overrides the project's default machine.
- `--yes` skips the final confirmation prompt; the chown prompt still fires if
  needed.
- `--dry-run` resolves the image and target, prints what would be written, and
  exits without opening the device.

## TUI integration

The unit detail view for an image unit gains a flash action. Pressing it:

1. Renders the device list (same code as `flash list`) inside the existing
   bubbletea model.
2. User selects a device with arrow keys + enter.
3. Confirmation prompt; on accept, the write runs with progress shown via a
   `bubbles/progress` bar in the detail view, and stdout/stderr from the write
   streamed to the existing log pane.

Adds `github.com/charmbracelet/bubbles` as a dep — same ecosystem as the
bubbletea / lipgloss already in use.

## Device discovery

Native walk of `/sys/class/block/`. No `lsblk`, no external Go modules.

For each entry `/sys/class/block/<name>`:

1. Skip by name pattern: `loop*`, `sr*`, `ram*`, `dm-*`, `md*`, `zram*`, and any
   name with a partition suffix (`sda1`, `mmcblk0p1`, `nvme0n1p1`).
2. Read `removable` (1/0), `size` (in 512-byte sectors), `ro` (1/0).
3. Read `device/vendor`, `device/model` if present.
4. Resolve the bus by walking the `device` symlink up to the bus `subsystem`
   link. Yields one of `usb`, `mmc`, `nvme`, `scsi`, `ata`.

Filter (mirrors balena's `etcher-sdk` `isSystem` heuristic):

- Keep iff `removable == 1` **or** `bus ∈ {usb, mmc}`.
- Drop iff `size == 0` (no media inserted).
- Drop iff `ro == 1` (write-protected).

Internal NVMe/SATA disks fall out of this filter via `removable == 0` and
`bus ∉ {usb, mmc}`. A USB-attached NVMe enclosure that reports `removable == 1`
is kept, which is the desired behavior (it's a removable drive).

Output of `yoe flash list`:

```
DEVICE        SIZE     BUS  VENDOR    MODEL
/dev/sdb      31.9 GB  usb  Generic   USB Flash Disk
/dev/mmcblk0  62.5 GB  mmc            SD64G
```

Empty list prints `No removable devices detected.` and exits 0.

## Image discovery

Given `<unit>` and resolved machine, look at:

```
build/<unit>.<machine>/destdir/<unit>.img
```

Drop the dead `.img.tar.gz` and `output/` paths from the existing `findImage` —
current builds emit only raw `.img`.

If absent: `no built image found — run yoe build <unit>` and exit non-zero.

## Write path

1. **Validate the device** — keep the existing `validateDevice` / `parentDisk` /
   `systemDisks` / `underlyingDevices` logic from
   `internal/device/flash.go:135-234` verbatim:
   - Path non-empty, exists, is a block device.
   - Symlink-resolve (handles `/dev/disk/by-id/...`).
   - Reduce to whole-disk via `parentDisk` (so `/dev/sdb1` becomes `/dev/sdb`
     for the system-disk comparison).
   - Refuse if the disk hosts `/`, `/boot`, `/boot/efi`, or `/usr`, including
     via LVM / dm-crypt / mdraid (`underlyingDevices` recurses through
     `/sys/class/block/<name>/slaves`).
2. **Mounted partitions hint** — read `/proc/self/mountinfo` for any source
   under `/dev/<basename>*`. If any are mounted, print:

   ```
   /dev/sdb has mounted partitions:
     /dev/sdb1 → /media/cbrake/BOOT
   Unmount with:
     udisksctl unmount -b /dev/sdb1
   ```

   and exit non-zero. Never call `udisksctl` from `yoe`.

3. **Confirmation prompt** unless `--yes`:

   ```
   Flash base-image.img (612 MB) → /dev/sdb (31.9 GB, Generic USB Flash Disk)?
   This will erase all data on /dev/sdb. Continue? [y/N]
   ```

4. **Open the device** with `O_WRONLY | O_EXCL`. `O_EXCL` on a block device is
   the kernel's belt-and-suspenders for "no partitions are currently mounted" —
   it fails fast even if step 2 missed something.
   - On `EACCES`: print

     ```
     Permission denied writing /dev/sdb.
     Run sudo chown <user> /dev/sdb? [y/N]
     ```

     (where `<user>` is resolved via `os/user.Current()` in `yoe`'s process and
     passed as a literal argv element to
     `exec.Command("sudo", "chown", user, devicePath)` — never written as a
     shell string, so sudo's environment can't reinterpret `$USER` to `root`).
     If user accepts, run sudo (which prompts for the password directly), then
     retry the open. If user declines, exit non-zero with
     `no write permission on /dev/sdb — run: sudo chown <user> /dev/sdb`.

   - On `EBUSY`: print the same mounted-partitions hint as step 2.

5. **Stream the image** — `io.CopyBuffer` with a 4 MiB buffer. After the copy:
   `f.Sync()`, then `f.Close()`. Pure stdlib.

   The write function takes a progress callback so CLI and TUI can render it
   differently from the same code path:

   ```go
   func Write(imagePath, devicePath string, progress func(written, total int64)) error
   ```

   The callback is invoked at most every 250ms (or every 16 MiB, whichever comes
   first) to avoid flooding either consumer.
   - **CLI** binds the callback to a stderr writer that overprints a single line
     with `\r`: `written 256 MiB / 612 MiB (42%) — 18.4 MiB/s`
   - **TUI** binds the callback to a tea.Cmd that emits
     `progressMsg{written, total}`. The detail view renders a `bubbles/progress`
     bar with the same `MiB / total — rate` line below it.

## Three layers of safety

Each catches different classes of mistake. None alone is sufficient.

| Layer                            | Catches                                                     |
| -------------------------------- | ----------------------------------------------------------- |
| Enumeration filter               | Non-removable internal disks never appear in list / TUI     |
| `validateDevice` + `systemDisks` | Disks hosting `/`, `/boot`, `/boot/efi`, `/usr` (incl. LVM) |
| `O_EXCL` open                    | Disks with currently-mounted partitions                     |

## File layout

```
internal/device/flash.go         — Flash() orchestration + existing safety logic
internal/device/flash_list.go    — sysfs-based enumeration (new)
internal/device/flash_mount.go   — /proc/self/mountinfo parsing (new)
internal/device/flash_write.go   — open / copy / fsync write path (new)
internal/device/flash_test.go    — unit tests with /sys and /proc fixtures
cmd/yoe/main.go                  — cmdFlash() updated; cmdFlashList() added
internal/tui/app.go              — flash action wired into image unit detail
```

The existing `Flash()` exported signature is preserved at the package boundary
so `cmdFlash` only needs minor changes; the body is rewritten to the new write
path.

## Testing

- `flash_list_test.go` — fixture trees rooted at `t.TempDir()` with
  `sys/class/block/...` populated to cover removable USB, MMC, NVMe, loop, sr,
  ram, partition-suffix names, missing-media (`size == 0`), and read-only
  (`ro == 1`). The list code's internal helper takes a `sysroot string`; the
  exported function calls it with `/sys`.
- `flash_mount_test.go` — feed a `/proc/self/mountinfo` byte slice and assert
  which partitions are reported as mounted.
- `flash_write_test.go` — pass a regular file in place of the block device and
  verify the copy / progress output. The `O_EXCL` and chown paths are not
  exercised in unit tests; they're covered manually on a real card.

## Out of scope (deferred)

- Backend interface for Jetson recovery / fastboot. Defer until at least one
  such backend exists; the current SD/USB write has nothing in common with the
  tegra protocol beyond "the user said `yoe flash <unit>`", so a speculative
  interface would be lying about a shared shape that isn't there. When Jetson
  lands, dispatch happens at the top of `Flash()` based on the resolved machine.
- macOS / Windows. `runtime.GOOS != "linux"` returns
  `flash currently supports Linux only`.
- Verification. Add later as `--verify` if the demand appears.
- `flash list --json` for tooling. Add when something needs it.
