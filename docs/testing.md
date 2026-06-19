# Testing (planned)

> **Status:** This document describes the intended shape of yoe's test story.
> Today, yoe ships Go unit tests under `internal/*` and a single end-to-end Go
> test at `internal/build/e2e_test.go` that loads `testdata/e2e-project/` and
> exercises a dry-run build. CI (`.github/workflows/`) runs `go test ./...`, a
> `yoe` build, markdown formatting, and a full from-source build of `base-image`
> via `e2e-build.yaml`. There is no `yoe test` subcommand, no on-device test
> runner, and no image smoke-test framework. The sections below describe what's
> planned; each one calls out what exists today vs. what's future work.

## Goals

Testing in yoe needs to cover six distinct levels, because regressions can hide
at any of them:

1. **Compiler-level (Go):** yoe's own logic — DAG resolution, hash computation,
   Starlark evaluation, repo indexing.
2. **Build-time package QA:** every built package passes a fixed set of sanity
   checks (ownership, stripping, RPATH, host-path leaks, missing SONAMEs, etc.).
   Failures fail the build. Yocto's equivalent is `INSANE.bbclass`.
3. **Per-unit functional tests:** a unit's build produces the expected files,
   services, metadata, runtime deps. Destdir assertions, run inside the build
   sandbox.
4. **On-device upstream tests:** a unit ships its own `make check` (or
   `cargo test`, etc.) output as an installable test subpackage; the booted
   device runs them. Catches ABI / linkage regressions that destdir-level tests
   miss. Yocto's equivalent is `ptest`.
5. **Image-level smoke tests:** boot the image (QEMU or real hardware), run
   assertions over SSH — network up, services running, basic flows work.
6. **Hardware-in-loop (HIL):** image-level tests against a flashed physical
   device, not just QEMU.

The `yoe test` command unifies levels 3–6 behind one driver so the same test
spec runs against a destdir, a QEMU image, or a physical device. Build-time QA
(level 2) is always-on and runs as part of every package build, not opt-in.

## Today

### Go unit tests

Standard `go test` coverage across `internal/`:

```sh
source envsetup.sh
yoe_test          # go test ./...
```

Notable suites:

- `internal/build/*_test.go` — sandbox, executor, templates, starlark exec.
- `internal/starlark/*_test.go` — loader, builtins, install steps.
- `internal/source/source_test.go` — git/tarball fetchers.
- `internal/repo/*_test.go` — APKINDEX generation, signing.
- `internal/image/rootfs_test.go` — rootfs assembly logic.

### End-to-end Go test

`internal/build/e2e_test.go` loads `testdata/e2e-project/` and runs a dry-run
build of `dev-image`. It validates:

- Project + module load.
- Unit registration (busybox, linux, zlib, base-image, etc.).
- DAG resolution and topological sort.

It does not actually build anything — it stops at the dry-run boundary. A real
build inside CI would need a Docker daemon, the toolchain container, and several
minutes of compute.

### CI

Two workflows run under `.github/workflows/`:

- `ci.yaml` — on every push to `main` and every pull request: `go test ./...`, a
  `yoe` binary build, and `prettier --check` on `**/*.md`.
- `e2e-build.yaml` — a full from-source build of `base-image` (bootstrap
  toolchain, musl, busybox, the kernel, image assembly), verifying the resulting
  `base-image.img`. Because it is expensive (Docker, tens of minutes), it runs
  on pushes to `main`, on a nightly schedule, and via manual dispatch — not on
  every pull request. Successive runs reuse the content-addressed cache via
  `actions/cache`, so an unchanged graph rebuilds incrementally.

## Build-time Package QA (planned)

> **Status:** Not implemented. Today the only built-in check is package-level
> path-conflict detection (a file installed by two packages without an explicit
> `replaces=` annotation fails image assembly). No checks run against an
> individual unit's destdir before packaging.

Every unit's destdir is sanity-checked before it is packaged (into an `.apk` or
`.deb`). Failures fail the build. This is the cheapest tier of testing — runs on
every build with no opt-in — and catches the most common shipping bugs:

- **File ownership and mode:** all installed files must be owned by `0:0` (root)
  with mode that matches the unit's policy. Setuid binaries must be declared
  explicitly (no accidental setuid via upstream `make install`).
- **ELF binary checks:**
  - Stripped (or has separate debug info).
  - No `RPATH` / `RUNPATH` pointing at the build-time sysroot
    (`/build/sysroot/...` baked into a target binary is the classic bug).
  - All `NEEDED` libraries are satisfied by the unit's `runtime_deps` (catches a
    unit linking libfoo without depending on it).
  - Architecture matches the target arch (no x86_64 binary in an arm64 package
    because the build slipped to host gcc).
- **Path leaks:** no absolute paths under `/build/`, `$DESTDIR`, `/tmp/build-*`,
  or the host build user's home directory in installed files (binaries, scripts,
  pkg-config files, libtool `.la` files).
- **Conffile sanity:** any path declared in `conffiles=` actually exists in the
  destdir; conffiles outside `/etc/` are flagged.
- **License:** `license=` is set, and a copy of the upstream license file lands
  at a known location.

Every check has a known-acceptable escape hatch on the unit (e.g.,
`qa_skip = ["rpath"]`) so a unit can opt out per-rule with a comment explaining
why, instead of being forced to vendor in workarounds.

## `yoe test <unit>` (planned)

> **Status:** Not implemented. `cmd/yoe/main.go` has no `test` case in its
> command dispatch.

Run a unit's tests against the appropriate target. The driver picks the right
mode based on the unit's class and the `--target` flag:

```sh
# Unit-level: assert destdir contents after build
yoe test zlib

# Image-level: boot the image in QEMU and run smoke tests
yoe test dev-image

# Hardware-in-loop: SSH into a real device and run tests there
yoe test dev-image --target dev-pi.local
```

### Unit-level tests

A unit declares tests inline:

```python
unit(
    name = "zlib",
    version = "1.3.1",
    ...
    tests = [
        test("install-layout", steps = [
            "[ -f $DESTDIR/usr/lib/libz.so.1.3.1 ]",
            "[ -L $DESTDIR/usr/lib/libz.so ]",
            "$DESTDIR/usr/bin/minigzip --version | grep -q 1.3.1",
        ]),
    ],
)
```

Tests run inside the same per-unit container the build used, against the
already-built destdir. Failures are unit-build failures — no separate phase to
forget.

### On-device upstream tests

Most upstream projects (openssl, zlib, busybox, etc.) ship a real test suite —
`make check`, `cargo test`, `pytest`. Running it against the binary you just
built is the highest-confidence test you can run, because it exercises the
actual ABI / linkage / runtime behavior of the package on the target arch and
libc. Yocto calls this `ptest`.

A unit can declare an upstream test suite as an installable subpackage:

```python
unit(
    name = "openssl",
    ...
    upstream_tests = task("ptest", steps = [
        "make TESTS='*' check-only DESTDIR=$DESTDIR/usr/lib/yoe-tests/openssl",
    ]),
)
```

`yoe build` produces a separate `openssl-tests` package (`.apk` or `.deb`)
alongside the main package. On the booted device:

```sh
yoe test openssl --on-device dev-pi.local
# Alpine: ssh dev-pi.local 'apk add openssl-tests && /usr/lib/yoe-tests/openssl/run.sh'
# Debian: ssh dev-pi.local 'apt-get install openssl-tests && /usr/lib/yoe-tests/openssl/run.sh'
```

This catches regressions that destdir assertions cannot:

- A library that built but links against the wrong libc symbol.
- A binary that runs in QEMU user-mode but crashes on real hardware.
- An optimization flag that breaks a corner case the upstream covers.

Test packages stay out of the default image (`dev-image` does not list them) but
ship in the project's package repo so they can be installed on-demand.

### Image-level tests

An image declares smoke tests that run against a booted instance:

```python
image(
    name = "dev-image",
    artifacts = [...],
    tests = [
        test("boots-and-network", steps = [
            "ssh-with-retry root@$TARGET 'true'",
            "ssh root@$TARGET 'ip -4 -o addr | grep -v 127.0.0.1'",
            "ssh root@$TARGET 'getent hosts github.com'",
        ]),
        test("services-up", steps = [
            "ssh root@$TARGET 'pgrep sshd'",
            "ssh root@$TARGET 'pgrep dhcpcd'",
        ]),
    ],
)
```

The driver:

1. Builds the image (or reuses cache).
2. Boots it in QEMU (or attaches over SSH for `--target=<host>`).
3. Runs each test step. On failure, captures the serial console + journal.
4. Shuts down the image.

### HIL mode

`--target=<host>` skips the build/boot phase and runs tests directly against an
already-running device. Useful for testing real hardware without a separate test
harness.

## CI Integration

Three CI tiers, in order of cost:

1. **Go tests** — `go test ./...` on every push and PR (`ci.yaml`). Cheap,
   catches the bulk of regressions. The dry-run image build lives here too:
   `internal/build/e2e_test.go` loads `testdata/e2e-project/` and resolves the
   unit graph without building, catching Starlark-level and graph breakage.
   _Implemented._
2. **Full image build** — `yoe build base-image` from source on pushes to
   `main`, nightly, and on demand (`e2e-build.yaml`). Expensive (Docker, tens of
   minutes) but catches actual build regressions. _Implemented._
3. **Image smoke tests** — boot the built image and assert over SSH (the
   `yoe test <image>` driver below). _Planned;_ once it lands, `e2e-build.yaml`
   gains a `yoe test base-image` step after the build.

## Build History / Regression Tracking (planned)

> **Status:** Not implemented. Yocto's equivalent is `buildhistory`.

Track per-build artifact metadata so a PR can show what changed in
machine-readable form: package sizes, file lists, RDEPENDS, image contents,
kernel config diff. Run as a CI job on `main` and on PRs; surface notable diffs
as a PR comment ("dev-image grew 4.2 MB", "openssh.apk's RDEPENDS gained
`libfido2`").

This isn't testing per se, but it occupies the same regression-detection slot —
many regressions show up as "size of X grew unexpectedly" or "Y suddenly depends
on Z" before they manifest as a functional failure.

## Kernel QA (planned)

> **Status:** Not implemented; mentioned as a TODO in
> [containers.md](containers.md).

For container-host images, run upstream `moby/moby`'s `check-config.sh` against
the kernel's resulting `.config` to verify the required `CONFIG_*` options are
set. Failures should fail the build, not warn.

## Comparison to Yocto

Yocto's test infrastructure (`oeqa`) is the closest reference. The mapping:

| Yocto                              | yoe equivalent                                     |
| ---------------------------------- | -------------------------------------------------- |
| `oe-selftest` / `bitbake-selftest` | `go test ./...` (Go unit tests under `internal/`)  |
| `INSANE.bbclass` / `QA_LOG`        | Build-time package QA (planned)                    |
| `ptest` / `ptest-runner`           | `yoe test <unit> --on-device` (planned)            |
| `oeqa.runtime` / `testimage`       | `yoe test <image>` (planned)                       |
| `oeqa.sdk` / `testsdk`             | _(no SDK product; `yoe shell` is the dev surface)_ |
| `testexport` (run on hardware)     | `yoe test <image> --target <host>` (planned)       |
| `runqemu`                          | `yoe run` (already shipped)                        |
| `buildhistory`                     | Build history / regression tracking (planned)      |
| `INHERIT += "create-spdx"`         | _(license tracking lives in unit fields today)_    |

Where yoe diverges by design:

- **No SDK product to test.** Yocto's `testsdk` validates the cross-compiler
  tarball it produces; yoe ships no such artifact, so the tier doesn't exist.
  The `yoe shell` container takes its place; treat shell entry as the SDK
  validation point.
- **One driver, several targets.** `yoe test` picks unit / image / HIL mode from
  flags; Yocto splits into `testimage`, `testexport`, `ptest-runner`, etc., each
  with its own configuration. Yoe collapses them so the same test spec runs in
  all three places.
- **QA fails the build, not warns.** Yocto's QA is configurable per-rule
  (warning vs. error vs. skip) and many sites silence rules to keep builds
  green. Yoe defaults all rules to error and exposes per-unit `qa_skip = [...]`
  so the opt-out is explicit and grep-able.

## See Also

- [Build Environment](build-environment.md) — the container/bwrap sandbox that
  unit tests run inside.
- [Containers](containers.md) — kernel QA discussion.
- [Yoe Tool](yoe-tool.md) — `yoe test` flags once implemented.
