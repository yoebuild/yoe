# Editable QEMU port forwards in the TUI — Implementation Plan

> **Status:** Done. Shipped in PR #135 on branch `kraj/main`. This plan was
> written alongside the change to satisfy the spec/plan requirement for
> AI-assisted code; it describes the implemented design in its final form.

## Problem

The **Setup → QEMU settings** Ports section listed a machine's declared forwards
as read-only and only let the user _append_ extra local forwards. When a machine
forward like `8080:8080` collided with a host port already in use, `yoe run`
failed its port-availability preflight and there was no way to move the forward
off the busy port from the UI — the user had to hand-edit `local.star` or accept
the failure.

The `--port` CLI flag already solved this at run time: an override whose guest
port matches a machine forward _replaces_ that forward (rather than appending a
second, still-colliding entry). The fix brings that same replace-by-guest-port
capability to the TUI so any forward — including a machine-declared one — can be
retargeted in place.

## Design

The Ports section is reframed from "machine rows (read-only) + local rows
(editable)" to a single **effective forward list**: the machine's declarations
with the `local.star` `qemu_ports` overrides already merged in
(replace-by-guest-port), which is exactly what `yoe run` binds. Every row is
editable; the merge layer (`device.MergeQEMUPorts`) is shared between the run
path and the TUI so the displayed list and the bound list can never diverge.

Editing a machine-declared forward writes a `local.star` override keyed to that
guest port — which supersedes the machine entry at merge time — rather than
mutating any checked-in `.star` file. Run-time precedence is unchanged: machine
← `local.star` ← CLI `--port`.

## Phase Overview

- **Phase 1** — Export the merge primitive so the TUI and run path share it.
- **Phase 2** — TUI model + effective-list helpers and edit/delete handlers.
- **Phase 3** — TUI rendering of the merged, tagged, editable list.
- **Phase 4** — Wire the run preflight to honor overrides.
- **Phase 5** — Tests.
- **Phase 6** — Docs and changelog.

## Phase 1: Share the merge primitive (`internal/device/`)

### Task 1.1: Export `MergeQEMUPorts`

Rename `mergeQEMUPorts` → `MergeQEMUPorts` in `internal/device/qemu.go` so the
`internal/tui` package can compute the same effective forward list `yoe run`
binds. Update all in-package callers (`qemu.go`, `boottest.go`). No behavior
change — replace-by-guest-port semantics are unchanged; only the visibility and
the doc comment (now noting CLI `--port` _and_ `local.star qemu_ports` as
override sources) move.

## Phase 2: Effective-list model and handlers (`internal/tui/app.go`)

### Task 2.1: Model state

Add `qemuEditIndex int` to the model — the effective-port row currently being
edited, or `-1` when adding a brand-new forward (initialized to `-1` in `Run`
and reset to `-1` everywhere an edit ends). Update the comments on `qemuPorts`
(now "overrides layered on the machine's declared forwards, replace-by-guest")
and `qemuEditing`.

### Task 2.2: Effective-list helpers

- `machinePorts()` — the default machine's declared forwards, or nil.
- `effectivePorts()` — `MergeQEMUPorts(machinePorts(), qemuPorts)`; the single
  source of truth the Ports section edits.
- Re-point `qemuRowCount`, `qemuPortRowIndex`, `qemuIsAddRow` at
  `effectivePorts()` instead of `qemuPorts`.

### Task 2.3: Override-mutation helpers

- `portGuest(s)` — guest side of `host:guest`.
- `dropOverrideByGuest(guest)` — remove any override for that guest, allocating
  a fresh slice so bubbletea value-copies sharing the old backing array are
  untouched.
- `setEffectivePort(mapping)` — drop any prior override for the guest, then
  record a new override _unless_ the mapping already equals the machine default
  (in which case the machine produces it and no override is stored). This is
  what lets an edit of a machine-declared forward "stick" by writing an override
  with the same guest port.
- `deleteEffectivePort(idx)` — a forward backed by an override is removed
  (reverting to the machine default when the machine declares that guest, else
  dropping it); a machine-declared forward with no override cannot be deleted
  and surfaces a message telling the user to edit its host port instead.

### Task 2.4: Key handlers

- `updateSetupQEMU`: **Enter** on a port row opens the inline field pre-filled
  with the current mapping (`qemuEditIndex = idx`); **Enter** on the add row
  opens an empty field (`qemuEditIndex = -1`). `a` / `+` jump to the add field.
  `d` / `-` call `deleteEffectivePort`.
- `updateSetupQEMUPortInput`: **Enter** validates, and when editing an existing
  forward whose guest port changed, drops the stale old-guest override before
  calling `setEffectivePort` so no orphaned forward lingers; then lands the
  cursor on the row now carrying that guest and persists. Empty entry / **Esc**
  close the field and reset `qemuEditIndex`.

## Phase 3: Rendering (`internal/tui/app.go`)

`viewSetupQEMU` renders the merged `effectivePorts()` list, each row tagged
`machine` or `local` (a guest port present in `qemuPorts` is `local`). A shared
`editField` closure renders the inline `host:guest` field with a blinking caret,
used both for the row under edit and the add prompt. `qemuSettingsSummary`
reports `len(effectivePorts())`, fixing a prior double-count that summed the
machine and local forwards even when an override replaced a machine forward.
Update the in-screen help bar: Enter = edit highlighted forward, `a`/`+` = add,
`d`/`-` = revert/remove.

## Phase 4: Run preflight honors overrides (`internal/tui/app.go`)

In `updateUnits`, the pre-launch "is a guest already running / is the port free"
check calls `device.CheckQEMUPortsAvailable(mc, m.qemuPorts)` (previously
`nil`), so it tests the same effective forwards `yoe run` binds. Without this, a
forward the user just moved off a busy host port would still be flagged as
colliding on its original machine port.

## Phase 5: Tests

- `internal/device/qemu_test.go`: rename existing cases to `MergeQEMUPorts`; add
  `TestCheckQEMUPortsAvailable_OverrideRetargetsBusyPort` — bind a real port to
  simulate a busy host port, confirm the machine forward collides with no
  override and that an override onto a free port clears it.
- `internal/tui/app_test.go`: a `qemuTestModel` / `qemuKey` harness plus cases
  covering retarget-machine-forward, edit-back-to-default-drops-override,
  change-guest-drops-stale-override, invalid/empty/Esc input handling,
  delete-reverts-override, delete-machine-forward-refused,
  delete-purely-local-removes, add via `a` and via Enter-on-add-row, and the
  summary count regression.

## Phase 6: Docs and changelog

- `docs/machine-qemu.md` and `docs/yoe-tool.md`: rewrite the Ports section to
  describe the editable effective list, the machine/local tags, and the
  revert-vs-remove behavior of `d`.
- `CHANGELOG.md`: one user-facing entry under `[Unreleased]`.

## Verification

- `go test ./internal/device/ ./internal/tui/` green; new TUI tests raise
  coverage on the port-edit handlers and `qemuSettingsSummary`.
- Full `go test ./internal/... ./cmd/...` green on a branch rebased on latest
  `main`.
- Changed Markdown passes `prettier --check`.
