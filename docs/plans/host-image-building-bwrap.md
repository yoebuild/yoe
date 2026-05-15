# Plan: Move Image Building to Host with bwrap User Namespaces

## Context

Files created inside the Docker container are owned by root because the
container runs as root with `--privileged`. The existing design docs
(`docs/build-environment.md` lines 269-336) already plan for bwrap user
namespaces as the pseudo-root mechanism for image assembly. This plan implements
that design — a stepping stone toward the long-term goal of running all builds
on the host via bwrap + Tier 1 build root, with Docker only for
bootstrapping/onboarding.

**Goal**: Unit compilation stays in the container (for now). Image assembly
moves to the host, running inside a bwrap user namespace
(`--unshare-user --uid 0 --gid 0`) so files are owned by the real user on the
host filesystem.

## Approach: Two-Phase Build

```
Phase 1 (container):  yoe build openssh base-image
  → builds unit deps (openssh, busybox, etc.) inside Docker
  → skips image-class units
  → .apk artifacts land in build/repo/ (bind-mounted)

Phase 2 (host):  yoe assembles image units on host
  → bwrap --unshare-user --uid 0 --gid 0 provides pseudo-root
  → container rootfs (exported once) provides tools (tar, mkfs.ext4, sfdisk)
  → output files owned by real user
```

## Host Requirements Change

| Before           | After                      |
| ---------------- | -------------------------- |
| `yoe` + `docker` | `yoe` + `docker` + `bwrap` |

bwrap is in every major distro's package manager. Same class of requirement as
Docker. Falls back to container-based image building with a warning if bwrap is
unavailable.

## Implementation Steps

### Step 1: Container rootfs export/cache

**File: `internal/container.go`**

Add `ExportContainerRootfs(cacheDir string) (string, error)`:

- Checks if `<cacheDir>/rootfs-<version>/` exists
- If not: `docker create yoe-ng:<version>` + `docker export` + extract to cache
  dir
- Returns path to extracted rootfs
- Cache is invalidated when container version bumps

This gives bwrap access to all container tools (sfdisk, mkfs.ext4, etc.) without
requiring them on the host.

### Step 2: Add bwrap user namespace wrapper

**File: `internal/build/sandbox.go`**

Add `RunInUserNamespace(cfg *ImageSandboxConfig, command string) error`:

```go
type ImageSandboxConfig struct {
    ContainerRootfs string // ro-bind as /
    Binds           []Bind // additional rw/ro bind mounts
}

type Bind struct {
    Src, Dst string
    ReadOnly bool
}
```

Builds a bwrap command:

```
bwrap --unshare-user --uid 0 --gid 0 \
    --ro-bind <container-rootfs> / \
    --bind <rootfs-dir> /rootfs \
    --bind <output-dir> /output \
    --ro-bind <repo-dir> /repo \
    --dev /dev --proc /proc --tmpfs /tmp \
    -- sh -c '<command>'
```

The `--unshare-user --uid 0 --gid 0` maps the real user to uid 0 inside the
namespace. Files created appear owned by the invoking user on the host.

### Step 3: Split build dispatch for image units

**File: `internal/build/executor.go`**

Modify `BuildRecipes` to separate image and non-image units:

```go
func BuildRecipes(proj, names, opts, w) error {
    // ... existing DAG/order/hash logic ...

    var imageRecipes, nonImageRecipes []string
    for _, name := range order {
        if proj.Units[name].Class == "image" {
            imageRecipes = append(imageRecipes, name)
        } else {
            nonImageRecipes = append(nonImageRecipes, name)
        }
    }

    // Build non-image units (may be in container)
    for _, name := range nonImageRecipes { ... }

    // Build image units (on host, via bwrap)
    if !InContainer() && len(imageRecipes) > 0 {
        for _, name := range imageRecipes {
            buildImageOnHost(proj, proj.Units[name], opts, w)
        }
    }
}
```

Add `buildImageOnHost()` that:

1. Exports container rootfs (Step 1)
2. Calls `image.Assemble` via `RunInUserNamespace` (Step 2)
3. Uses a `yoe image-assemble` internal sub-command (Step 4) inside the
   namespace

### Step 4: Internal `image-assemble` sub-command

**File: `cmd/yoe/main.go`**

Add `image-assemble` as an internal command that runs **before** the container
gate (lines 33-49):

```go
case "image-assemble":
    // Internal: called by host yoe inside bwrap namespace
    cmdImageAssemble(args)
    return
```

This command:

- Accepts unit name, project dir, output dir as args
- Loads project, finds unit, calls `image.Assemble` directly
- Runs inside bwrap namespace with pseudo-root

### Step 5: Update container dispatch

**File: `cmd/yoe/main.go`**

Modify the container gate (lines 51-65) so that when `yoe build` is invoked on
the host:

1. Load project config on host (pure Go, no tools needed - `loadProject()`
   already works)
2. Determine if any targets are image units
3. Enter container for non-image unit deps only:
   `ExecInContainer(["build", "--skip-images", ...])`
4. After container returns, run image assembly on host via bwrap

Add `--skip-images` flag to `cmdBuild` to support this.

### Step 6: Remove `--privileged` from container

**File: `internal/container.go`**

Replace `--privileged` (line 81) with `--security-opt seccomp=unconfined`
(needed for bwrap inside Docker). The container no longer needs losetup/mount
since image building moved to the host.

### Step 7: Drop losetup/mount bootloader path

**File: `internal/image/disk.go`**

Remove the `installBootloader` losetup/mount/extlinux path (lines 238-291). Keep
only the MBR boot code write (lines 210-236) which works without root. The
VBR/ldlinux.sys files are already in the rootfs and get included via
`mkfs.ext4 -d`.

### Step 8: Update docs

**File: `docs/build-environment.md`**

- Add `bwrap` to host requirements table
- Update the architecture diagram to show image assembly on host
- Mark the pseudo-root design as implemented

## Critical Files

| File                         | Change                                                     |
| ---------------------------- | ---------------------------------------------------------- |
| `internal/container.go`      | Add `ExportContainerRootfs()`, remove `--privileged`       |
| `internal/build/sandbox.go`  | Add `RunInUserNamespace()`, `ImageSandboxConfig`           |
| `internal/build/executor.go` | Split image/non-image build paths                          |
| `cmd/yoe/main.go`            | Add `image-assemble` sub-command, two-phase build dispatch |
| `internal/image/disk.go`     | Remove losetup/mount bootloader path                       |
| `docs/build-environment.md`  | Update host requirements and architecture                  |

## Verification

1. `go build ./...` - compiles
2. `go test ./...` - passes
3. `yoe build base-image` on host:
   - Deps build in container (non-image units)
   - Image assembly runs on host via bwrap
   - Output files in `build/base-image/output/` owned by real user (not root)
4. `ls -la build/base-image/output/` - verify file ownership is current user
5. `yoe run base-image` - QEMU boots the image (functional test)

## Decisions

1. **Unit build file ownership**: Deferred. Image assembly is the immediate pain
   point. Unit builds stay in the container as-is. Long-term, all builds move to
   host via bwrap + Tier 1 build root.

2. **bwrap availability**: Fall back to container-based image building with a
   warning if bwrap is not on the host. Keeps backward compat.
