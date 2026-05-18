# Unit Service Management Design

Move service enablement from image definitions to unit definitions. Units
declare which init scripts they provide via `services = [...]`. The image
assembly reads this metadata from installed packages and auto-enables services.

## Current State

Services are declared in two places:

1. **Units** — `services` field exists but is unused metadata (not stored in
   APK)
2. **Images** — `services = ["sshd", "simpleiot"]` creates `S50<name>` symlinks

This creates redundancy. The image author must know which services each package
provides and manually list them. Adding a new service-providing package requires
updating both the unit and the image.

Init scripts reach the rootfs through different mechanisms:

- Upstream `make install` (e.g., openssh installs `/etc/init.d/sshd`)
- Custom build task (e.g., simpleiot creates `/etc/init.d/simpleiot`)
- Baked into filename (e.g., network-config creates `/etc/init.d/S10network`)

## Design

### Unit Declaration

Units declare which init services they provide:

```python
# Upstream provides the init script
autotools(
    name = "openssh",
    services = ["sshd"],
    ...
)

# Unit provides the init script in a build task
go_binary(
    name = "simpleiot",
    services = ["simpleiot"],
    ...
)

# Custom priority via S-prefix
unit(
    name = "network-config",
    services = ["S10network"],
    ...
)
```

The `services` field declares what init scripts the package provides. How the
init script gets into the package (upstream install vs custom task) is the unit
author's concern.

### APK Metadata

The `services` field is stored in `.PKGINFO` as `service` lines:

```
pkgname = openssh
pkgver = 9.9p1-r0
service = sshd
```

Multiple services per package are supported (one `service` line each).

### Image Assembly

The `image()` class no longer accepts a `services` parameter. During rootfs
assembly, the image class:

1. Reads `.PKGINFO` from each installed APK
2. Extracts `service = <name>` lines
3. For each service:
   - If name matches `S\d+.*` (e.g., `S10network`): create symlink as-is
   - Otherwise: create `S50<name>` → `../init.d/<name>` symlink
4. Only creates the symlink if `/etc/init.d/<name>` exists in the rootfs

### What Changes

**Go code:**

- `internal/artifact/apk.go` — `generatePKGINFO()` writes `service = <name>`
  lines from `unit.Services`
- `modules/module-core/classes/image.star` — remove `services` parameter from
  `image()`. Replace the current services loop with logic that reads service
  metadata from installed APKs.

**Reading services from APKs:** The image class runs in Starlark during build
time. To read `.PKGINFO` from installed APKs, the simplest approach is a shell
command in the Starlark `run()` call:

```sh
# Extract service lines from all installed APKs
for apk in $REPO/*.apk; do
    tar xzf "$apk" .PKGINFO -O 2>/dev/null | grep '^service = ' | cut -d' ' -f3
done
```

Or a Go builtin that returns the services list. The shell approach is simpler
and avoids adding another builtin.

**Starlark units:**

- `openssh.star` — add `services = ["sshd"]`
- `simpleiot.star` — already has init script task, add
  `services = ["simpleiot"]`
- `network-config.star` — add `services = ["S10network"]`, remove the `S10`
  prefix from the init script filename since the image assembly will handle it.
  Actually, keep the `S10network` filename as-is since the service name includes
  the priority prefix.
- `dev-image.star` and `base-image.star` — remove `services` parameter

## Non-Goals

- Service disable/override at the image level (future work)
- Systemd service files (current init system is busybox init + OpenRC-style
  scripts)
- Runtime service management on the device (`rc-update` equivalent)
