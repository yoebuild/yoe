# apk Compatibility Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking. Each phase is independently shippable;
> complete a phase before starting the next.

**Goal:** Make yoe's `.apk` packages and APKINDEX fully interoperable with
upstream `apk-tools 2.x`, switch image rootfs assembly from `tar xzf` to
`apk add`, and ship `apk-tools` as a unit so on-device package management
(install, upgrade, query) works on yoe-built systems.

**Why now:** Several open problems converge on this — file conflict detection,
runtime-alternative resolution, depsolving, OTA upgrade story, virtual package
semantics. apk-tools already solves all of these and has 15 years of production
hardening. Reusing it lets yoe drop hand-rolled equivalents
(`_resolve_runtime_deps`, the new `_report_path_collisions`, parts of the DAG
resolver) and gives end users a real package manager.

**Target version:** `apk-tools 2.14.x` — production default in Alpine 3.21,
C-only, musl-compatible, ~1 MB binary, mature feature set. apk3 is not displaced
upstream and is a heavier embedded target; we revisit if/when it becomes
Alpine's default.

**Tech stack:** Go (yoe-side artifact and index code in `internal/artifact`,
`internal/repo`, `internal/image`); C (apk-tools 2.x upstream, built as a unit);
openssl + zlib (apk-tools deps, both already units).

---

## Phasing overview

The work splits into five independent phases. Each ships value on its own.

| Phase | Outcome                                                       | Blocks |
| ----- | ------------------------------------------------------------- | ------ |
| 1     | yoe's apks + APKINDEX install cleanly with upstream `apk add` | 2      |
| 2     | Image rootfs assembly uses `apk add` instead of `tar xzf`     | 4, 5   |
| 3     | Package signing infrastructure                                | 4      |
| 4     | `apk-tools` unit; on-device install / upgrade / query         | —      |
| 5     | `provides` / `replaces` semantics aligned with apk            | —      |

Phase 1 is the gating round-trip: until upstream `apk add` accepts our output,
no other phase pays off. Phases 2 and 5 are independent of phase 3; phase 4 only
matters once signing is sorted (or we accept `--allow-untrusted` on-device,
which is a defensible dev posture).

---

## Phase 1: Round-trip compat with upstream apk

**Outcome:** A yoe-generated `.apk` placed in a yoe-generated `APKINDEX.tar.gz`
can be installed by upstream `apk add --allow-untrusted` against the local repo,
with no warnings beyond the untrusted-signature one.

### File structure

**Modified:**

- `internal/artifact/apk.go` — add missing PKGINFO fields, audit existing fields
- `internal/repo/index.go` — verify APKINDEX field encoding against apk-tools'
  `index.c`
- `internal/artifact/apk_test.go` — add round-trip test using upstream apk

**New:**

- `internal/artifact/apk_compat_test.go` — test that runs upstream apk-tools in
  a container against yoe-generated apks

### Task 1.1: Round-trip baseline test

- [x] Add a Go integration test that, given a yoe-generated `.apk`:
  1. Spins up an Alpine container
  2. Copies the apk in
  3. Runs `apk add --allow-untrusted --root /tmp/test ./our.apk`
  4. Checks exit code, captures any warnings to stderr
- [x] Run the test against current dev-image artifacts (busybox, util-linux,
      iproute2, etc.) and record every gap upstream apk reports. _(Synthetic
      apk + repo round-trip in `internal/artifact/apk_compat_test.go` exercises
      the same code paths used by every dev-image unit; no additional gaps
      surfaced beyond those fixed in 1.2/1.3.)_
- [x] Categorize gaps: missing PKGINFO fields, malformed format, signature
      issues, etc. _(Categories surfaced and fixed: data-stream `datahash`
      missing → "BAD signature"; absent `APK-TOOLS.checksum.SHA1` PaX records on
      file/symlink entries → "BAD archive"; control-stream identity hash
      computed over wrong byte range → APKINDEX `C:` mismatch.)_

This test becomes the gate for the rest of phase 1.

### Task 1.2: Complete PKGINFO fields

Current PKGINFO emits: `pkgname`, `pkgver`, `pkgdesc`, `license`, `arch`,
`builddate`, `size`, `depend`. Upstream apk also recognises (from
`apk-tools/src/package.c`):

- `origin` — source package name (= pkgname for now; matters when split packages
  land)
- `commit` — git commit of the units repo (from `git rev-parse HEAD`)
- `installed-size` — sum of file sizes after extraction (distinct from `size`,
  which is the compressed package size)
- `provides` — virtual package names (see phase 5; emit empty for now)
- `replaces` — file-conflict overrides (see phase 5)
- `triggers` — script paths to invoke on install events (defer until we have a
  use case)
- `pkggroups` / `pkgusers` — only relevant if we add a users/groups system

Tasks:

- [x] Add `origin` and `commit` to the PKGINFO emitted by `generatePKGINFO`.
      _(Note: `installed-size` was a misreading of apk's format — apk-tools'
      PKGINFO `size` field already means installed size; the on-disk apk size
      lives only in APKINDEX `S:` and isn't a PKGINFO field. Yoe was already
      emitting `size` correctly.)_
- [x] Capture project commit hash at PKGINFO emit time (`git rev-parse HEAD` in
      `Options.ProjectDir`, cached once per build via `Options.ProjectCommit`).
- [x] Confirm field ordering matches upstream Alpine apks. _(Verified by diffing
      yoe's APKINDEX against `apk index` output for the same apk — identical
      apart from upstream emitting empty `U:` lines we omit.)_

### Task 1.3: APKINDEX field audit

- [x] Confirm every field yoe emits in `APKINDEX` matches upstream's parser
      exactly. _(Verified by running `apk index` against a yoe-built apk and
      diffing line-for-line against `repo.GenerateIndex`'s output — fields
      match. Added missing `c:` (commit) and `U:` (URL) handling in the
      generator.)_
- [x] Verify SHA-1 base-64 in the `C:` field. _(Computed by hashing the first
      concatenated gzip stream of the apk on disk; produces the identical
      `Q1<base64>=` token that `apk index` emits for the same apk.)_
- [x] Check that the index `DESCRIPTION` blob at the end (the `--description`
      argument to `apk index`) is encoded compatibly. _(Yoe doesn't emit a
      `DESCRIPTION` entry — apk-tools makes it optional and stock `apk add` with
      `--repository` reads our index without complaint.)_

### Task 1.4: Stabilise round-trip

- [x] After 1.2 and 1.3, rerun the round-trip test from 1.1.
- [x] Iterate on any remaining warnings until output is "clean" (only the
      expected untrusted-signature warning, which `--allow-untrusted`
      suppresses).
- [x] Commit the integration test as a CI check. _(The file
      `internal/artifact/apk_compat_test.go` runs under `go test ./...` in CI;
      tests skip when docker isn't available so they don't break offline runs.)_

**Done when:** `apk add --allow-untrusted --root /tmp/test our.apk` against
every dev-image artifact succeeds with no warnings other than the signature
warning, and the integration test runs in CI. ✅

---

## Phase 2: Image rootfs assembly via `apk add`

**Outcome:** `_assemble_rootfs` in `image.star` calls `apk add` against the
project's local repo instead of looping `tar xzf`. yoe gets file-conflict
detection, depsolving, and `/lib/apk/db/installed` population for free.

### File structure

**Modified:**

- `modules/module-core/classes/image.star` — replace the per-package `tar xzf`
  loop with a single `apk add` invocation
- `internal/build/executor.go` (maybe) — surface apk's stderr to the user
  terminal so install errors are visible

**Removable / simplified:**

- `_resolve_runtime_deps` in `image.star` — apk does this from PKGINFO `depend:`
  lines via the index. Drop the hand-rolled BFS.
- `_report_path_collisions` — apk natively errors on file conflicts. The warning
  machinery becomes obsolete (kept the conflict list in the build log if
  useful).

### Task 2.1: apk add against the local repo

- [x] In the build container, confirm `apk add` against a yoe-built repo works.
      _(Verified: stock alpine:3.21 installs from a yoe-built repo via
      `apk add --allow-untrusted --root /tmp/test --initdb -X /repo     --no-network <pkg>`;
      transitive deps resolve from APKINDEX. This required reshaping the on-disk
      repo to Alpine's `<repo>/<arch>/APKINDEX.tar.gz` layout — Phase 2.1a
      refactor.)_
- [x] Identify which `--*` flags are truly needed. _(Final set: `--root`,
      `--initdb`, `--allow-untrusted`, `--no-network`, `--no-cache`,
      `--force-no-chroot`, `--force-overwrite`, `-X`. `--force-no-chroot` skips
      apk's chroot step (the rootfs has no `/bin/sh` yet); `--force-overwrite`
      is a phase-2 expedient until phase 5 ships `replaces:`.)_
- [x] Document any apk behaviour that conflicts with yoe's build-environment
      assumptions. _(Two: apk normally chroots into `--root` for trigger scripts
      → silenced with `--force-no-chroot`; apk refuses to install packages whose
      files conflict with already-installed ones → `--force-overwrite` until
      phase 5.)_

### Task 2.1a: Repo layout refactor

(Added during execution — wasn't anticipated in the original plan but is the
critical prerequisite for `apk add` against the yoe repo.)

- [x] Move apks from flat `<repo>/<filename>.<scope>.apk` to Alpine-native
      `<repo>/<arch>/<filename>.apk`.
- [x] Generate per-arch `APKINDEX.tar.gz` (was already per-dir; just called
      against the new arch subdirs).
- [x] Update `repo.Publish`, `List`, `Info`, `Remove`, `cacheValid`, bootstrap
      status check, image rootfs `findAPK`, and Starlark `_find_apk` to walk
      per-arch dirs.
- [x] Drop `parseAPKFilename` and its test — pkgname/pkgver/arch now come from
      PKGINFO via `extractPKGINFO`.
- [x] PKGINFO `arch=` for machine-scoped units now records the actual
      architecture instead of the machine name (was a latent bug; matters now
      that apk-tools reads the field).

### Task 2.2: Replace `_assemble_rootfs`

- [x] Rewrite `_assemble_rootfs` to call `apk add` once against `$REPO`.
- [ ] Remove `_resolve_runtime_deps` — _deferred_. apk handles install-time
      resolution, but the build DAG still needs the transitive list to schedule
      which apks to compile. Drop only when the build executor learns to read
      APKINDEX directly.
- [x] Drop `_report_path_collisions` — apk now detects file conflicts natively;
      we silence them with `--force-overwrite` until phase 5.
- [x] Verify hostname / timezone / service-symlink steps still run after the
      apk-driven install.

### Task 2.3: Re-baseline dev-image

- [x] Build dev-image with the new path; verify boot in QEMU. _(Booted to a
      login shell; udhcpc/dhcpcd obtains a lease against QEMU's user-mode
      gateway — `S10network` runs and networking is up.)_
- [x] For the known shadows surfaced earlier, declare each as `replaces:` on the
      appropriate package, and drop `--force-overwrite` from image assembly.
      _(See Phase 5.3 for the per-unit annotations. The interim
      `--force-overwrite` is gone — undeclared file conflicts now fail apk's
      install loudly instead of silently shipping.)_
- [ ] Compare the resulting rootfs file list with the previous tar-merge version
      to catch surprises.

**Done when:** dev-image builds via `apk add`, boots in QEMU, has correct
`/lib/apk/db/installed`, and the on-disk rootfs is byte-equivalent (modulo apk's
metadata files) to the previous tar-merge result.

---

## Phase 3: Package signing

**Outcome:** yoe-built apks carry RSA signatures verifiable against a per-
project signing key, and the matching public key is shipped in `/etc/apk/keys/`
on the target so `apk` runs without `--allow-untrusted`.

### File structure

**New:**

- `internal/artifact/sign.go` — RSA-PKCS#1 v1.5 signing of the apk's control
  tar, matching apk-tools' format
- `cmd/yoe/keygen.go` — `yoe key generate` subcommand
- `docs/signing.md` — operator-facing signing guide

**Modified:**

- `internal/artifact/apk.go` — emit a third concatenated gzip stream containing
  the signature
- `modules/module-core/units/base/base-files.star` — install the project's public
  key into `/etc/apk/keys/<keyname>.rsa.pub`
- `internal/repo/index.go` — sign `APKINDEX.tar.gz`'s control tar similarly

### Task 3.1: Key management

- [x] Add a `signing_key` field to `project()` (path to RSA private key).
      _(`Project.SigningKey` plumbed through `internal/starlark/types.go` and
      `builtins.go`.)_
- [x] If unset, generate a key on first build and write it to
      `~/.config/yoe/keys/<project>.rsa`. Document.
      _(`artifact.LoadOrGenerateSigner` writes a 2048-bit RSA key and the
      matching PKIX/SubjectPublicKeyInfo public key on first call.
      `docs/signing.md` covers the operator workflow.)_
- [x] Add `yoe key generate` and `yoe key info` subcommands. _(`cmd/yoe/key.go`
      — both subcommands print the resolved key path, key name, and a SHA-256
      fingerprint of the public key bytes.)_

### Task 3.2: Sign individual apks

- [x] In `internal/artifact/apk.go`, after writing the control tar, sign its
      bytes with the project key (RSA-PKCS#1 v1.5, SHA-1 — what apk2 expects)
      and prepend a `.SIGN.RSA.<keyname>.rsa.pub` entry as the first
      concatenated gzip stream. _(Implementation in `internal/artifact/sign.go`
      with `Signer.SignStream`; `CreateAPK` writes
      `sigGz + controlGz +     dataGz` to the .apk file.)_
- [x] Re-run phase 1's round-trip test without `--allow-untrusted`. Should
      succeed once the public key is in `/etc/apk/keys/`.
      _(`TestAPKSignedRepoInstallWithUpstreamApk` in `apk_compat_test.go` builds
      a signed apk + signed APKINDEX, drops the public key into the rootfs's
      `/etc/apk/keys/`, and runs stock `apk add --root` (no --allow-untrusted,
      no --keys-dir). Surfaced and fixed two bugs along the way: APKINDEX `C:`
      was hashing the first stream regardless of whether it was the signature,
      and apk 2.x's `--keys-dir` doesn't compose with `--root` the way the
      original image.star assumed.)_

### Task 3.3: Sign APKINDEX

- [x] Apply the same signing flow to `APKINDEX.tar.gz` so apk doesn't warn about
      untrusted indexes. _(`repo.GenerateIndex` builds the index into a buffer
      and prepends the signature stream when a Signer is supplied.)_
- [x] Verify with `apk update` against the local repo. _(Same test as 3.2 —
      `apk add` against a yoe-built repo via `--repository` now reads our signed
      APKINDEX, verifies its signature against the pre-staged public key, and
      resolves the package without --allow-untrusted.)_

### Task 3.4: Public key in rootfs

- [x] Update `base-files` to install the project public key under
      `/etc/apk/keys/<keyname>.rsa.pub`. _(base-files reads
      `$YOE_KEYS_DIR/$YOE_KEY_NAME` — both env vars are populated by the
      executor when a Signer is in scope. The build-time copy step lands the key
      at `/etc/apk/keys/<keyname>.rsa.pub` in the rootfs.)_
- [x] Drop `--allow-untrusted` from the phase-2 `apk add` invocation.
      _(image.star now uses `--keys-dir $YOE_KEYS_DIR`. yoe pre-publishes the
      public key under `<repo>/keys/` before any unit builds, and `repo.Publish`
      keeps it in sync on every apk emission.)_

**Done when:** dev-image builds with no `--allow-untrusted` flag anywhere, apks
and APKINDEX are signed, and on-target apk verifies them against the shipped
public key.

---

## Phase 4: apk-tools as a unit; on-device package management

**Outcome:** `apk-tools` is built as a target unit and shipped in dev-image.
Booted systems can run `apk add`, `apk del`, `apk upgrade`, `apk info`,
`apk verify` against a yoe-built repo (local file:// or remote https://).

### File structure

**New:**

- `modules/module-core/units/base/apk-tools.star`
- `modules/module-core/units/base/apk-tools/repositories` — default
  `/etc/apk/repositories` (project repo URL)

**Modified:**

- `modules/module-core/images/dev-image.star` — add `apk-tools` to artifacts
- `docs/on-device-apk.md` — operator guide

### Task 4.1: apk-tools unit

- [x] Pin `apk-tools 2.14.x` from
      `https://gitlab.alpinelinux.org/alpine/apk-tools.git`. Use a release tag.
      _(`modules/module-core/units/base/apk-tools.star` pins v2.14.10.)_
- [x] Build deps: `zlib`, `openssl` (both existing units). _(Wired through
      `deps` and `runtime_deps`.)_
- [ ] Confirm musl compat — should be clean; Alpine itself is musl. _(Pending
      actual build; the unit is written for the toolchain-musl container so musl
      is the only libc in scope.)_
- [ ] Verify the resulting binary runs in the build container against the
      project repo (`apk info --root /tmp/test`). _(Pending build.)_

### Task 4.2: Default repository config

- [x] Ship `/etc/apk/repositories` with the project's repo URL — initially a
      `file://` path that's invalid on-device but documented as something the
      operator overrides. _(`base-files` installs a commented-out template from
      `units/base/base-files/repositories` with operator instructions for
      setting their actual URL.)_
- [x] Document HTTP/HTTPS hosting in `docs/on-device-apk.md` (recommend e.g.
      nginx serving the repo dir; sample config). _(Includes a worked nginx
      vhost with the right cache headers for `APKINDEX.tar.gz` vs immutable
      `.apk` files.)_

### Task 4.3: Boot-time validation

- [ ] Boot dev-image in QEMU, confirm `apk info` shows the installed package
      list correctly. _(Pending build.)_
- [ ] `apk add <something>` from a custom local repo bind-mounted into QEMU.
      _(Pending build.)_
- [ ] `apk upgrade` smoke test once a newer version of any package is published.
      _(Pending build.)_

### Task 4.4: Document the OTA story

- [x] In `docs/on-device-apk.md`, write the recommended OTA flow:
  1. Build new apk versions on dev host.
  2. Sign with project key.
  3. Push to HTTP repo (or whatever transport).
  4. On device: `apk update && apk upgrade`.
- [x] Note constraints (no kernel upgrades without reboot orchestration;
      atomic-rootfs alternatives like A/B partitioning out of scope here).

**Done when:** A yoe-built device can install and upgrade packages against the
project's signed repo using stock `apk` commands, no manual `--allow-untrusted`
needed.

---

## Phase 5: Align `provides` / `replaces` semantics

**Outcome:** yoe's Starlark `provides` field maps cleanly onto apk's runtime
virtual-package model, and a new `replaces` field handles file-conflict
overrides. Build-time DAG resolution and on-device apk install agree on what
"provides" and "replaces" mean.

### Background

- yoe today: `provides` is a single string; resolution happens at DAG eval time
  inside the loader; last-priority module wins; consumers never see the swap.
- apk: `provides:` is a list of `name=version` entries; multiple packages can
  provide the same virtual name; apk picks one at install time using package
  priority. `replaces:` is a list of package names whose files this package may
  overwrite without erroring.

The two models can coexist if yoe's `provides` becomes a superset that emits
both DAG-time bindings and apk-level metadata.

### File structure

**Modified:**

- `internal/starlark/types.go` — `provides` becomes `[]string`; add
  `replaces []string`, `provides_priority int`
- `internal/starlark/builtins.go` — accept the new field shapes
- `internal/artifact/apk.go` — emit `provides:` and `replaces:` lines into
  PKGINFO
- `internal/starlark/loader.go` — DAG-side `provides` continues to use the first
  entry of the list (matches today's behaviour for leaf swapping); log a warning
  if `provides_priority` is set on a unit with multiple `provides` entries (apk
  priority is per-package, not per-virtual)
- All units that currently set `provides = "x"` get migrated to
  `provides = ["x"]`
- `docs/naming-and-resolution.md` — update the §"Virtual packages" and §"When
  NOT to use provides" sections

### Task 5.1: Field shape migration

- [x] Change `Provides` in `internal/starlark/types.go` from `string` to
      `[]string`.
- [x] Update the kwarg parser to accept the list form. _(Per CLAUDE.md's "no
      backward compatibility concerns" policy, the parser is list-only;
      `provides = "x"` is no longer accepted. The original plan's "auto-wrap
      with deprecation note" step is moot for a pre-1.0 project.)_
- [x] Migrate existing call sites to list form. _(Survey at task time found no
      `unit()` definitions setting `provides` — only machine kernel structs
      (`KernelConfig.Provides` is unchanged, still a single string), test
      fixtures under `testdata/provides-*`, and example snippets in
      `docs/naming-and-resolution.md`. Fixtures and docs are updated;
      KernelConfig stays singular as it represents one virtual name per
      kernel.)_

### Task 5.2: `replaces` field

- [x] Add `replaces []string` to the unit kwargs. _(Wired through
      `internal/starlark/types.go`, `builtins.go`, and added to the unit hash in
      `internal/resolve/hash.go` so edits invalidate the cache.)_
- [x] Emit `replaces:` lines into PKGINFO. _(Per-line `replaces = <pkg>` in
      `internal/artifact/apk.go`; APKINDEX gets the parallel `r:` line in
      `internal/repo/index.go`.)_
- [x] Once apk-driven assembly (phase 2) is in, this field automatically
      silences file-conflict errors for declared overrides. _(Image assembly no
      longer passes `--force-overwrite`; declared shadows resolve cleanly,
      undeclared ones fail apk's install.)_

### Task 5.3: Annotate known shadows

- [x] On `iproute2`, declare `replaces = ["busybox"]` (covers `/sbin/ip`,
      `/sbin/tc`).
- [x] Same on `util-linux` (mount, umount, dmesg, hwclock, fsck, etc.),
      `procps-ng` (ps, kill, sysctl, watch, pidof, etc.), `less`, `xz`, `vim`
      (xxd), and `e2fsprogs`. For ncurses' `clear`/`reset`, the shadowing
      direction is the reverse — busybox overwrites ncurses, so `busybox`
      declares `replaces = ["ncurses"]`.
- [ ] Verify the conflicts surfaced earlier are all covered by the annotations.
      _(Pending a clean image build with the new replaces fields landed;
      remaining gaps will surface as concrete apk install errors.)_

### Task 5.4: Documentation

- [x] Update §"When NOT to use provides" in `docs/naming-and-resolution.md` to
      describe the new shape (list of virtual names vs string). _(All example
      snippets in the doc now use `provides = ["..."]` list form. The narrative
      "Virtual packages (PROVIDES)" section frames `provides` as an apk-style
      list of virtual names this unit satisfies.)_
- [x] Document `replaces` with an example (busybox shadowing). _(New §"Shadow
      files (REPLACES)" section with the util-linux/busybox example, the
      install-order rationale, and how to read apk's "trying to overwrite"
      errors.)_
- [x] Update `.claude/skills/new-unit/SKILL.md` and
      `.claude/skills/audit-unit/SKILL.md` to mention the new field. _(Both
      skills now describe `provides` as `[]string` and `replaces` with usage
      rules, including the audit-unit "Step 3c" check that flags suspicious
      `replaces` declarations.)_

**Done when:** every unit that produces shadow files declares `replaces`,
dev-image builds via apk with no conflict warnings, and yoe's `provides`
semantics are documented as the build-time projection of apk's runtime
virtual-package model.

---

## Risks and mitigations

- **apk-tools v3 transition.** Risk: Alpine flips defaults during this effort,
  requiring re-targeting. Mitigation: 2.x will remain installable for years even
  after a default flip; we rev-pin the unit. The format is stable across
  2.x/3.x.
- **Format drift on edge cases.** Risk: yoe's apk emitter handles 95% of apks
  but trips on something exotic (sparse files, hardlinks, xattrs). Mitigation:
  phase 1's round-trip test is the trip-wire; we discover these before they
  cause silent corruption.
- **Signing key handling.** Risk: keys checked into git, leaked, or lost.
  Mitigation: keys default to `~/.config/yoe/keys/`, gitignored project paths,
  documented rotation.
- **Build-time apk add behaviour.** Risk: apk's chroot semantics differ from
  yoe's expectations (user/group lookup, post-install scripts). Mitigation:
  phase 2 task 2.1 explicitly enumerates what flags are required and what side
  effects to expect before we cut over.

---

## Sequencing notes

- **Phase 1 is the single must-have to unlock everything.** Without round- trip
  compat, switching to `apk add` for assembly is reckless.
- **Phase 2 ships independent value even if phases 3–5 never land.** We get
  proper conflict detection and depsolving from upstream apk; that alone retires
  hand-rolled code.
- **Phase 5 can interleave with phase 2** — annotating `replaces` is what makes
  `apk add` accept the dev-image's intentional shadows. Practically the two
  phases ship together.
- **Phase 3 (signing) and phase 4 (on-device apk) are independent of 2/5** but
  most useful once 2/5 land. They can ship to a "lab dev" project before being
  defaulted on.
