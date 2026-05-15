# Design: `binary` class for prebuilt-binary units

## Purpose

Many GitHub (and other-host) projects ship official prebuilt arm64 / x86_64
binaries on every release. For **leaf tools** that aren't linked into anything
else `[yoe]` builds — `kubectl`, `helm`, `gh`, `task`, `fly`, `helix`, the Go
toolchain, etc. — rebuilding from source is wasted work. `[yoe]` should be able
to consume the upstream binary directly.

The `binary` class wraps that pattern: declare base URL, per-arch asset (literal
or templated), and per-arch SHA256 hashes. The class fetches, integrity-checks,
extracts (or copies, for bare binaries), places one or more binaries on `$PATH`,
optionally installs additional files / directories, and creates any requested
symlinks.

This is a **leaf-tool class**. Anything that other units link against, or that
other units need headers from, should still build from source.

## Non-goals

- **Not for libraries.** Output is binaries on `$PATH`, not `.so`/`.h` for
  consumers.
- **Not for closed-source vendor SDKs.** v1 doesn't model EULAs, restricted
  redistribution, or licence-acceptance gates.
- **Not a "build from source if no binary available" fallback.** If the project
  doesn't ship arm64, the unit errors. That's correct.
- **No SHA256SUMS auto-fetch, no Sigstore / cosign verification.** Literal
  per-arch SHA256 is the v1 integrity check.

## API surface

```python
load("//classes/binary.star", "binary")

# 1. Bare single binary (no archive)
binary(
    name = "kubectl",
    version = "1.29.0",
    base_url = "https://dl.k8s.io/release/v{version}/bin/linux",
    asset = "{arch}/kubectl",
    sha256 = {"x86_64": "...", "arm64": "..."},
    license = "Apache-2.0",
)
# Implicit: binaries = ["kubectl"]
# Result: $SRCDIR/kubectl → $PREFIX/bin/kubectl

# 2. Single binary inside an archive (template)
binary(
    name = "helm",
    version = "3.14.0",
    base_url = "https://get.helm.sh",
    asset = "helm-v{version}-linux-{arch}.tar.gz",
    sha256 = {"x86_64": "...", "arm64": "..."},
    license = "Apache-2.0",
)
# Implicit: binaries = ["helm"] → $SRCDIR/helm → $PREFIX/bin/helm
# (top-level dir already stripped by source.Prepare)

# 3. Asymmetric per-arch asset names (Rust target triples)
binary(
    name = "fly",
    version = "0.2.0",
    base_url = "https://github.com/superfly/flyctl/releases/download/v{version}",
    assets = {
        "x86_64": "fly-x86_64-unknown-linux-musl.tar.gz",
        "arm64":  "fly-aarch64-unknown-linux-musl.tar.gz",
    },
    sha256 = {"x86_64": "...", "arm64": "..."},
    license = "Apache-2.0",
)

# 4. Multiple stand-alone binaries from one archive
binary(
    name = "tarsnap",
    version = "1.0.40",
    base_url = "...",
    asset = "tarsnap-{version}-linux-{arch}.tar.gz",
    sha256 = {...},
    binaries = ["bin/tarsnap", "bin/tarsnap-keygen", "bin/tarsnap-keymgmt"],
)
# Each path's basename becomes the install name.

# 5. Bundle (Go toolchain): install whole tree, expose specific binaries
binary(
    name = "go",
    version = "1.22.0",
    base_url = "https://go.dev/dl",
    asset = "go{version}.linux-{arch}.tar.gz",
    sha256 = {"x86_64": "...", "arm64": "..."},
    install_tree = "$PREFIX/lib/go",
    binaries = ["bin/go", "bin/gofmt"],
    license = "BSD-3-Clause",
)
# install_tree changes the meaning of `binaries`:
# - Whole $SRCDIR copied to $PREFIX/lib/go
# - Each binaries entry becomes a SYMLINK $PREFIX/bin/<basename> → ../lib/go/<path>
# - bin/go and bin/gofmt keep their pkg/, src/, lib/ siblings intact.

# 6. Bundle with rename + alias (helix)
binary(
    name = "helix",
    version = "24.07",
    base_url = "https://github.com/helix-editor/helix/releases/download/{version}",
    assets = {
        "x86_64": "helix-{version}-x86_64-linux.tar.xz",
        "arm64":  "helix-{version}-aarch64-linux.tar.xz",
    },
    sha256 = {"x86_64": "...", "arm64": "..."},
    install_tree = "$PREFIX/lib/helix",
    binaries = ["hx"],                                 # → $PREFIX/bin/hx
    symlinks = {"$PREFIX/bin/h": "../lib/helix/hx"},   # short alias
    license = "MPL-2.0",
)

# 7. Custom name (asset filename ≠ install name)
binary(
    name = "flyctl",
    version = "0.2.0",
    base_url = "...",
    asset = "fly-v{version}-linux-{arch}.tar.gz",
    sha256 = {...},
    binaries = {"flyctl": "fly"},                      # install $SRCDIR/fly as $PREFIX/bin/flyctl
)
```

### Field reference

| field                                                                                       | type             | required                  | meaning                                                                                                                                                                                                                                                                                            |
| ------------------------------------------------------------------------------------------- | ---------------- | ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `name`, `version`                                                                           | str              | yes                       | standard                                                                                                                                                                                                                                                                                           |
| `base_url`                                                                                  | str              | yes                       | URL prefix; `{version}` substitution available                                                                                                                                                                                                                                                     |
| `asset`                                                                                     | str              | one of `asset` / `assets` | single template, with `{arch}` and `{version}` substitution                                                                                                                                                                                                                                        |
| `assets`                                                                                    | dict             | one of `asset` / `assets` | per-arch asset paths; `{version}` allowed (no `{arch}` — the dict key already selects arch)                                                                                                                                                                                                        |
| `arch_map`                                                                                  | dict             | no                        | yoe arch → token substituted into `{arch}` in `asset`; default `{x86_64: amd64, arm64: arm64}`; ignored when `assets` (literal) is used                                                                                                                                                            |
| `sha256`                                                                                    | dict             | yes                       | per-arch literal SHA256; never templated                                                                                                                                                                                                                                                           |
| `binaries`                                                                                  | list **or** dict | no                        | primary binaries to expose on `$PATH`. **List form**: each entry is a path inside `$SRCDIR`; basename becomes install name. **Dict form**: `{install_name: src_path_in_srcdir}`. Default: `[name]`. Use `[]` to disable default install. `{arch}` allowed in src paths.                            |
| `install_tree`                                                                              | str              | no                        | If set, the entire contents of `$SRCDIR` are copied to this path, and each `binaries` entry becomes a **symlink** `$PREFIX/bin/<install_name>` → `<install_tree>/<src_path>` instead of a copy. Use this for toolchains/bundles where the binary needs sibling directories (Go, Node, JDK, helix). |
| `extras`                                                                                    | list             | no                        | tuples `(src_in_srcdir, dst_in_destdir)` or `(src, dst, mode)`; supports files **and** directories. For irregular files outside the bundle pattern.                                                                                                                                                |
| `symlinks`                                                                                  | dict             | no                        | `dst_path → target` (relative or absolute); created after install. For aliases beyond the binaries list.                                                                                                                                                                                           |
| `license`, `description`, `services`, `conffiles`, `runtime_deps`, `deps`, `tasks`, `scope` |                  | no                        | passed through to `unit()`                                                                                                                                                                                                                                                                         |

### Templating

Two placeholders are supported, both substituted at Starlark eval time:

- `{version}` — replaced with the unit's `version` field. The class never
  injects a `v` prefix or any other decoration; if the upstream URL needs
  `v3.14.0`, write `v{version}`.
- `{arch}` — replaced with `arch_map[ARCH]` (in `asset`) or with the yoe arch
  token directly (in src paths inside `binaries` / `extras` / `install_tree`).

Both placeholders apply to: `base_url`, `asset`, values in the `assets` dict,
src paths in `binaries`, src paths in `extras`, `install_tree`, and `symlinks`
values (target paths).

Neither placeholder applies to: `sha256` values, `binaries` install names (dict
keys), `symlinks` keys (destination paths), or `arch_map` itself. Hashes and
install destinations are always literal.

### Validation (Starlark eval time)

The class errors immediately, before `unit()` is called, if:

- `ARCH` (predeclared) is not a key in `assets`, `sha256`, or — when `asset` is
  templated — in `arch_map`.
- Neither `asset` nor `assets` is set; or both are set.
- `binaries == []` **and** `extras == []` (nothing would be installed).
- `binaries` dict has an entry whose key contains `/` (install names live in
  `$PREFIX/bin/`, not subdirectories).

Error messages name the unit, the arch, and the missing field. No silent
defaults.

## Source preparation: prerequisite Go changes

The existing source pipeline (`internal/source/workspace.go::Prepare` →
`extractTarball`) handles tarballs (`.tar.gz`/`.tar.xz`/`.tar.bz2`/`.tgz`) with
automatic top-level-directory stripping and SHA256 verification. The `binary`
class relies on that machinery, but two gaps need to be filled before it can
work:

1. **`.zip` extraction.** Add zip support to the extraction path so
   `.zip`-packaged releases extract into `$SRCDIR` like tarballs do. Apply the
   same auto-strip-top-level-dir behaviour.
2. **Bare-binary "extraction".** Add a path that handles non-archive downloads:
   when the cached file is not a recognised archive (`.tar.*`, `.tgz`, `.tbz2`,
   `.zip`), copy it into `$SRCDIR` as `$SRCDIR/<asset basename>` (e.g.
   `$SRCDIR/kubectl` for asset `amd64/kubectl`), preserving the upstream
   filename, and make it executable. The class then references it via `binaries`
   like any other archived layout — for the typical `name == asset_basename`
   case the default `binaries = [name]` already resolves correctly.

   Detection precedence: filename extension first; fall back to magic-byte
   sniffing for files with no/unknown extension. ELF (`7f 45 4c 46`) and any
   non-archive content are treated as bare. Unknown content with an archive
   extension still goes through the archive path and surfaces the existing
   error.

These changes live in `internal/source/workspace.go` (extraction) and possibly
`internal/source/fetch.go` (extension probe). They don't change behaviour for
any existing unit — every current source is either a git repo or a tarball with
a recognised extension.

## Class internals

1. **Resolve URL and SHA at Starlark eval time.** Read predeclared `ARCH`.
   Validate as above. Define a substitution helper that replaces both
   `{version}` (with the unit's `version` field) and `{arch}` (with
   `arch_map[ARCH]` for the templated `asset` form, or with the yoe arch token
   directly elsewhere). Then compose:
   - `asset_path = subst(assets[ARCH])` if `assets` is set, else `subst(asset)`.
   - `source = subst(base_url) + "/" + asset_path`.
   - `sha = sha256[ARCH]` (literal — no substitution).
2. **Normalise `binaries`** into a canonical list of `(install_name, src_path)`
   tuples:
   - `[]` → no entries.
   - List of strings → `[(basename(p), p) for p in list]`.
   - Dict → `list(dict.items())`.
   - Default (field omitted) → `[(name, name)]`.
   - Apply `{version}` and `{arch}` substitution to each `src_path`. Install
     names (dict keys) are literal.
3. **Call
   `unit(source=source, sha256=sha, container="toolchain-musl", sandbox=False, ...)`.**
   Cache keying is per-URL, so each arch gets its own cache slot for free.
4. **Generate one `task("install", steps=[...])`.** The shell does, in order:
   - **If `install_tree` is set**: `mkdir -p $DESTDIR<install_tree>` then
     `cp -aT $SRCDIR/. $DESTDIR<install_tree>/.` to copy the whole extracted
     tree.
   - **Binaries**:
     - Without `install_tree`: for each `(install_name, src_path)`,
       `install -m0755 $SRCDIR/<src_path> $DESTDIR$PREFIX/bin/<install_name>`.
     - With `install_tree`: for each `(install_name, src_path)`, compute a
       relative target from `$PREFIX/bin/` to `<install_tree>/<src_path>`
       (Starlark-side, deterministic) and emit
       `ln -sfn <relative_target> $DESTDIR$PREFIX/bin/<install_name>`.
   - **Extras** (each tuple): `mkdir -p $(dirname dst)` then
     `cp -aT $SRCDIR/<src> $DESTDIR<dst>`; `chmod` if mode supplied. `-aT`
     preserves directory structure and treats `<dst>` as the new name of
     `<src>`.
   - **Symlinks**: `mkdir -p $(dirname dst)` then
     `ln -sfn <target> $DESTDIR<dst>`.

The unit author never sees fetch, extract, hash-verify, or strip logic — all of
that is the platform's job. The class generates whatever shell is needed from
the declarative fields.

## Container

`toolchain-musl` — already has `install`, `cp`, `mkdir`, `ln`. The fetched
binary is never executed at build time, so cross-arch installs (e.g., building
an arm64 image on an x86_64 host) don't incur QEMU emulation cost on the binary
itself. `sandbox=False` because there's no compile step that needs bwrap
isolation.

## Interaction with existing systems

- **Source fetch** — no changes to `fetch.go`'s HTTP path itself. Existing
  cache-by-URL-hash + SHA verification applies as-is.
- **Source extract** — `internal/source/workspace.go` gains zip + bare-file
  handling (see prerequisite section).
- **Cache** — content-addressed by URL hash + verified SHA, same as other HTTP
  sources. Per-arch URLs naturally produce per-arch cache entries.
- **APK ownership** — output goes through the existing `archive/tar`
  normalisation in `internal/artifact/apk.go` that forces `root:root`. The class
  doesn't think about ownership.
- **DAG / deps** — class adds `toolchain-musl` to `deps` so the container unit
  is in the graph. No other implicit deps.

## Failure modes (intentional, loud)

- **Unsupported arch on this unit**: `ARCH not in sha256` → Starlark error with
  the unit name and the missing arch. Caught at eval, before any fetch.
- **SHA mismatch**: existing fetcher behaviour — fetch fails, file removed from
  cache.
- **Both `asset` and `assets` set, or neither set**: Starlark error.
- **Empty install (no `binaries`, no `extras`)**: Starlark error ("nothing to
  install").
- **`binaries` install name contains `/`**: Starlark error.
- **Missing file in `$SRCDIR` referenced by a `binaries` src or `extras` src**:
  install task exits non-zero with the path it was looking for.

No silent fall-through anywhere.

## Out of scope for v1 (deferred)

- **Sigstore / cosign verification.** Add when a unit needs it; SHA256 is enough
  for now.
- **SHA256SUMS-file convenience.** A separate helper (or the `new-unit` skill)
  can scrape SHA256SUMS and emit literal hashes into the unit; the class itself
  stays declarative.
- **Auto-discovery of binary path.** v1 requires explicit `binaries` (or its
  default of `[name]`). A future "auto" mode could find executable files under
  `$SRCDIR/bin/`, but that's a hidden default the project policy explicitly
  avoids.
- **Custom strip-components.** The existing top-level-strip behaviour is what
  every release tarball needs in practice. If a real unit shows up that wants
  strip=0 or strip>1, add the field then.
- **Per-binary mode override.** All binaries are installed `0755`. If a case
  needs different modes per binary, add a third tuple element to the dict form.

## Testing

- **Unit eval tests** — Starlark tests that exercise each form (bare, template,
  asymmetric, multi-binary, bundle with `install_tree`, bundle with rename) and
  confirm the class produces a `unit()` call with the right
  `source`/`sha256`/tasks for ARCH=x86_64 and ARCH=arm64. Verify validation
  errors fire for: missing arch in `sha256`, both `asset` and `assets` set,
  empty install, `/` in a `binaries` install name.
- **Source-prep tests (Go)** — table-driven tests for the new zip path and the
  new bare-binary path: zip with top-level dir, zip flat at root, bare ELF, bare
  statically-linked binary. Existing tarball tests stay green.
- **End-to-end build test** — at least one real unit per form using `binary`.
  Candidates: `kubectl` (bare), `helm` (templated archive, single binary), `go`
  (`install_tree` + multi-binary). Run `yoe build` for both arches in CI; the
  produced apk should contain each declared binary at `/usr/bin/<install_name>`
  (or as a symlink, for the `install_tree` case) plus any declared `symlinks`
  and `extras`.

## Open questions for follow-up

- **Default `arch_map`.** `{x86_64: amd64, arm64: arm64}` covers the Go
  ecosystem. Rust projects often need different tokens. The class default is
  fine; per-unit `arch_map` overrides cover the rest. Worth revisiting if a
  pattern emerges (e.g., a `RUST_ARCH_MAP` constant exposed by the class).
- **Symlink target style.** Examples use relative paths (`../lib/helix/hx`).
  Both relative and absolute work; the class doesn't enforce one for `symlinks`.
  The `install_tree`-driven `binaries` symlinks always use relative targets so
  the install is relocatable.
- **Where bundle installs go.** Examples use `$PREFIX/lib/<name>/`. Some
  projects might prefer `$PREFIX/share/<name>/` or `/opt/<name>/`. The class
  doesn't pick — `install_tree` and `extras` destinations are explicit.
