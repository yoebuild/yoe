# Go Workflows

This page covers Go-related workflows in `[yoe]`: building Go binaries as units,
where the Go module/build cache lives, and how to recover from common pitfalls.

## Building a Go binary as a unit

The `go_binary` class in `module-core/classes/go.star` wraps the standard build
pattern: clone the source, run `go build`, install the resulting binary into
`$DESTDIR$PREFIX/bin`. A minimal unit looks like:

```python
load("//classes/go.star", "go_binary")

go_binary(
    name = "siot",
    version = "0.18.5",
    source = "https://github.com/simpleiot/simpleiot.git",
    tag = "v0.18.5",
    go_package = "./cmd/siot",   # optional, defaults to ./cmd/<name>
    binary = "siot",             # optional, defaults to <name>
    license = "Apache-2.0",
)
```

Cross-compilation is automatic: yoe sets `GOARCH` from the target machine's arch
(`x86_64` → `amd64`, `arm64` → `arm64`, `riscv64` → `riscv64`) and forces
`CGO_ENABLED=0 GOOS=linux` so the result is a statically-linked Linux binary.

The build runs inside the upstream `golang:1.24` container by default. Override
with `container = "golang:1.23"` (or any pullable Go image) when a unit needs a
specific toolchain version.

## Cache layout

Go-class units share a project-scoped cache mounted into the build container at
`/go/cache`:

| Inside container  | On host                    | Purpose                                                           |
| ----------------- | -------------------------- | ----------------------------------------------------------------- |
| `/go/cache/mod`   | `<project>/cache/go/mod`   | `GOMODCACHE` — downloaded modules (`go.bug.st/serial@v1.6.4/...`) |
| `/go/cache/build` | `<project>/cache/go/build` | `GOCACHE` — compiled package artifacts                            |

The cache survives across builds, so the second build of any Go unit is much
faster than the first. It also survives across different Go units in the same
project — `simpleiot` and a hypothetical second Go unit will share downloaded
modules and built artifacts.

## Cleaning the cache

`go mod download` writes module files with mode `0444` and their parent
directories with mode `0555` — read-only by design, so you can't accidentally
edit a cached module. As a side effect, `rm -rf` against the cache fails with
"Permission denied" even though every file is owned by your user: `unlink` needs
the write bit on the _parent directory_, and Go has stripped it.

Recover with one of these:

```bash
# Option 1: chmod first, then rm — fastest
chmod -R u+w <project>/cache/go && rm -rf <project>/cache/go

# Option 2: let Go do it (this is what `go clean` is for)
GOMODCACHE=<project>/cache/go/mod go clean -modcache
GOCACHE=<project>/cache/go/build go clean -cache
```

Symptom you'd see if you skip the `chmod`:

```
rm: cannot remove 'yoe-test/cache/go/mod/go.bug.st/serial@v1.6.4/serial.go': Permission denied
```

This is a permission-bit issue, not an ownership issue. `stat -c '%U:%G'` on the
file will show your username, not `root`.

## Build sandbox notes

Go builds run inside the build container as `--user $(id -u):$(id -g)`, so all
cache writes land on the host as your user. The `golang:1.24` upstream image has
`/go` owned by root, but the bind mount overlays that with the project cache
directory before any build step runs.

Cross-arch Go builds (e.g. building a `riscv64` binary on `x86_64`) use QEMU
user-mode emulation via `binfmt_misc`. Run `yoe container binfmt` once to
register the QEMU handlers if you haven't yet — the TUI surfaces a warning
banner when this is missing.

## Customising the build

Common knobs on `go_binary`:

| Field          | Default        | Notes                                                                                  |
| -------------- | -------------- | -------------------------------------------------------------------------------------- |
| `go_package`   | `./cmd/<name>` | Path passed as the final arg to `go build`.                                            |
| `binary`       | `<name>`       | Installed filename when it differs from the unit name (e.g. `simpleiot` → `siot`).     |
| `container`    | `golang:1.24`  | Override to pin a different toolchain.                                                 |
| `tasks`        | empty          | Extra tasks (e.g. installing init scripts) merged after the default `build` task.      |
| `runtime_deps` | empty          | Packages installed alongside the binary on the device (e.g. `openrc` for the service). |

For anything more involved than a `go build` — multi-binary repos, code
generation, embedded assets — drop down to the plain `unit()` class and write
the `task("build", steps=[...])` directly. `go_binary` is just a thin wrapper
around that pattern.
