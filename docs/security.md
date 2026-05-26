# Security and Threat Model

`[yoe]` is a build system. It runs arbitrary code that you and the modules you
import write — Starlark logic plus shell scripts inside a build container. This
page describes what that container actually protects you against, what it does
not, and how to think about trusting the code `yoe` will execute on your behalf.

The short version: **treat every unit and every module the same way you treat a
`curl | sh` URL.** If you import it, you are running it. The build container is
a convenience for hermetic toolchains, not a security boundary.

## Threat model

`[yoe]` is designed for a developer or build operator running it on a machine
they control, against source code and modules they have chosen to import. It is
not designed to safely execute untrusted units.

In particular:

- **Trusted.** You. Your project's `PROJECT.star`, your project's unit `.star`
  files, every module you list in `modules = [...]`, every upstream source URL
  and git remote those units pull from.
- **Untrusted.** The booted target device, network traffic to/from on-device
  package installs (`apk add`, `yoe deploy`). These are protected by the apk
  signing chain — see [apk Signing](signing.md).
- **Out of scope.** Running other people's `PROJECT.star` files, hosting `yoe`
  builds on a multi-tenant machine, sandboxing one project from another on the
  same host. `yoe` does not attempt any of this today.

If the question is "can a rogue unit damage the host?", the honest answer is
**yes, easily**. The rest of this page explains why, and what limits exist.

## How a build actually runs

Every `yoe` build step that needs a toolchain runs in a Docker (or Podman)
container launched by `internal/container.go`. The relevant flags are:

```
docker run --rm --privileged \
  --user <host-uid>:<host-gid> \
  -v <projectDir>:/project \
  -v <srcDir>:/build/src \
  -v <destDir>:/build/destdir \
  -v <sysroot>:/build/sysroot:ro \
  -v <cacheDir>:<containerPath> ...   # per-unit cache_dirs
  -w /project \
  <image> sh -c '<build command>'
```

Two things in that command line dominate the security picture:

- **`--privileged` is unconditional.** Every container yoe launches is
  privileged. That means all Linux capabilities are granted, the host's `/dev`
  is exposed (or near-equivalent on Docker), AppArmor/SELinux profiles are not
  enforced, seccomp is off, and `/sys` is read-write. The container is not a
  sandbox in any meaningful sense — it is a chroot with a toolchain.
- **`--user uid:gid` runs as you, except when it doesn't.** Most steps drop to
  the host user, so files written to mounted paths are owned by you and the
  container cannot directly write host devices that require root. But several
  paths run **as root in the privileged container** (see below), and at that
  point a hostile build step can write `/dev/sda`, load kernel modules, or pivot
  the mount namespace and escape.

### Code paths that run as root in the privileged container

| Path                                           | Why                                                                                                                   |
| ---------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `image`-class units                            | apk extraction (preserves per-file uid/gid from package tar metadata), `mkfs.ext4 -d`, `losetup`, `mount`, `extlinux` |
| Any `run(..., privileged = True)` in a unit    | Same, exposed as a Starlark builtin                                                                                   |
| QEMU device runner (`internal/device/qemu.go`) | Needs `/dev/kvm`                                                                                                      |
| Bootstrap stage 1 (`createBuildRoot`)          | `apk add --root` builds the build root                                                                                |
| `yoe cache clean` and `yoe build --clean`      | Removes the resulting root-owned files from `build/` since the host user can't `rm` them                              |

For these paths, container UID is root, all caps are present, and the host's
`/dev` is reachable. There is no defense-in-depth layer beneath that.

The apk-extraction step on the `image`-class row deserves a short note: it runs
as root in the container so that `chown(path, hdr.uid, hdr.gid)` calls during
tar extraction actually succeed, which is what makes the assembled rootfs
contain (and the resulting ext4 image preserve) per-file ownership like
`navidrome:navidrome` for `/var/lib/navidrome` or `postgres:postgres` for
`/var/lib/postgresql`. The earlier workaround — `chown -R 0:0` on the rootfs
followed by a chown-back-to-host at end-of-build — collapsed all ownership to
root and obscured what the booted system would see; the current path preserves
real ownership and accepts the cost that `build/<image>.<arch>/destdir/rootfs/`
is owned by root after a build. `yoe cache clean` and `yoe build --clean` route
cleanup through the same container so the host user doesn't need `sudo` for
routine work. See
[Comparisons § Rootfs Ownership](comparisons.md#rootfs-ownership-how-each-project-handles-it)
for why this is preferred over LD_PRELOAD (fakeroot/pseudo) or user-namespace
(bwrap) alternatives.

### `run(host = True)` — there is no container at all

Units can ask Starlark to execute commands directly on the host:

```python
run("docker build -t %s -f %s/%s %s" % (tag, name, dockerfile, name), host = True)
```

This is how `modules/module-core/classes/container.star` builds container images
on the host's Docker daemon. The command runs through `bash -c` as your host
user, in `cfg.HostDir` (usually the unit's `.star` directory). There is no
namespace, no mount restriction, no `/project`-only view. A unit that uses
`host = True` has a shell as you. It can read `~/.ssh`, write
`~/.config/yoe/keys/`, or `rm -rf ~`.

### The bwrap layer

Build steps that opt into `sandbox = True` are wrapped with `bwrap` inside the
container (`internal/build/sandbox.go`). The bwrap call binds `/` to `/`,
read-only-binds `/proc` and the sysroot, and tmpfs's `/tmp`. **This is sysroot
hygiene, not isolation** — it prevents a unit from accidentally linking against
host libraries during a hermetic build. The whole bwrap invocation is inside the
same privileged container, so a unit that wants to escape can simply call `exit`
from bwrap and run anything it likes outside it, or call
`run(privileged = True)` to skip bwrap altogether.

## What a rogue unit can do

Given the above, a unit author can — without warning, prompts, or visible side
effects in `yoe build` output:

- **Read and modify the entire project tree.** `/project` is mounted read-write.
  That includes `PROJECT.star`, every other unit, the build cache, the apk repo,
  signing public keys, build logs.
- **Read every source the build has pulled.** `cache/sources/` and
  `cache/modules/` typically map under the project, but a unit's `cache_dirs`
  can bind any directory the user has access to.
- **Read environment variables passed to the build.** The Go process exports
  them into the container via `-e` flags.
- **Execute arbitrary host commands as you** via `run(host = True)`. This
  bypasses the container entirely. There is no allowlist, no path restriction,
  no "are you sure?" prompt. The unit author has the full power of your shell.
- **Run as root in a privileged container** via `run(privileged = True)` or by
  declaring `unit_class = "image"`. From there:
  - Overwrite `/dev/sda`, `/dev/nvme0n1`, USB sticks, any block device the
    kernel exposes.
  - Load kernel modules into the host kernel (`insmod`, `modprobe`).
  - Modify host firewall rules (`iptables`, `nft`) — the privileged container
    shares the network namespace by default, so changes are host-wide.
  - Read `/proc/<host-pid>` for every process on the host.
  - Mount any host filesystem and exfiltrate or modify it.
  - Trigger any of the well-known privileged-container escapes
    (`/sys/kernel/uevent_helper`, `core_pattern`, cgroup `release_agent`, etc.)
    to spawn a process on the host as root.
- **Tamper with the apk signing pipeline.** The project signing key lives at
  `~/.config/yoe/keys/<project>.rsa`. A `run(host = True)` step trivially reads
  it. A privileged in-container step can read it if it lives under `/project` or
  any mounted cache dir; the default location is in your home, which the
  container does not see — but `run(host = True)` does.
- **Poison the cache.** A unit can plant files in `cache/sources/`,
  `cache/modules/`, or per-unit `cache_dirs` mounts so the next build of another
  unit picks up tampered content.

## What `yoe` does limit

The container does provide some friction. It is worth being precise about what:

- **Unit builds that don't go privileged see only `/project` and their mounts.**
  A run-of-the-mill `make && make install` step running as your host UID cannot
  reach `$HOME`, system files outside `/project`, or the apk private key in
  `~/.config/yoe/keys/`. It can still corrupt anything inside `/project` and the
  configured cache dirs.
- **Most builds run as your host UID, not root.** Even with `--privileged`, a
  non-root container process cannot write block devices owned by `root:disk` or
  call `mount(2)` directly. A unit has to deliberately escalate via
  `privileged = True`, `unit_class = "image"`, or `host = True` to escape this.
- **Apks are signed and verified.** Output `.apk` files are signed with the
  project key, the public key is published to the repo and embedded in the
  rootfs, and on-device `apk add` / `yoe deploy` reject unsigned or
  wrongly-signed packages. See [apk Signing](signing.md). This protects the
  device → repo channel; it does not protect the host that produces the apks.
- **Source archives can declare integrity hashes.** Units that set
  `sha256 = "…"` or `apk_checksum = "…"` get post-download verification in
  `internal/source/fetch.go`. Units that omit both run whatever the upstream
  returned.

## Trust model for code `yoe` executes

`yoe` does not validate the units it loads. The trust chain for a build is:

1. **`PROJECT.star`** — you wrote it (or you imported it from a project you
   trust). It declares modules with `module(url = ..., ref = ...)`.
2. **Modules** are fetched with `git clone --depth 1 --branch <ref>` into
   `cache/modules/<name>/` (`internal/module/fetch.go`). `ref` is a branch or
   tag name — **not a commit hash**. A module upstream that retags a release
   ships you the new content on the next `yoe module sync`. The cache also
   trusts whatever bytes are already on disk; once cloned, integrity isn't
   re-checked.
3. **Unit sources** come from the URL in each unit's `source = ...` field,
   fetched via HTTPS, HTTP, or git. Integrity is verified if and only if the
   unit declares `sha256` or `apk_checksum`. Git sources rely on the tag
   pointing at the right commit at clone time.
4. **The build container image** is built from a Dockerfile in `module-core`,
   `module-alpine`, or another module — i.e., from the same supply chain as the
   units. It is not a vendor-supplied vetted image.

There is no signature on modules, no commit-hash pinning, and no notion of
"yoe-approved upstreams." If you import a module, you are running it.

## Practical guidance

- **Only run `yoe` on projects you control or that came from sources you
  trust.** Read the modules list. Audit unit `.star` files the same way you'd
  audit a shell script. The `audit-unit` skill (`docs/ai-skills.md`) is a useful
  first pass.
- **Don't run `yoe build` on a shared or production machine.** A build step with
  `host = True` or `privileged = True` is one careless module pin away from
  `rm -rf ~` or worse.
- **Don't put secrets in the project tree.** `/project` is fully readable and
  writable by every build step. Keep API keys, deployment credentials, and
  signing material outside the project directory, where the container's default
  mount cannot see them. (`host = True` can still see them — see above.)
- **Pin modules to release tags you've reviewed, and re-review on upgrade.**
  Until commit-hash pinning lands, the tag name is the trust anchor, and tags
  are mutable.
- **Be careful with `yoe module dev`.** Putting a module into dev mode means yoe
  uses your local checkout. If you also have an unrelated branch checked out
  there, those unit definitions are what the build will run.
- **Declare `sha256` or `apk_checksum` on every source archive you can.** Even
  for sources you trust, an integrity check catches MITM, mirror compromise, and
  accidental retags.
- **Keep `~/.config/yoe/keys/` permissions tight** (mode 0600 on the private
  key) and use distinct project names to avoid signing-key reuse across
  unrelated projects.

## Known weaknesses we'd accept patches for

These are explicit gaps, not unintentional bugs. PRs welcome.

- **Drop `--privileged` for the common case.** Most build steps (gcc, make, Go,
  Python wheels) don't need `CAP_SYS_ADMIN`. The flag is currently unconditional
  because image-class units need it; splitting the container invocation into
  privileged and non-privileged variants — and only escalating for the steps
  that genuinely need it — would dramatically reduce the host blast radius.
- **Remove `run(host = True)` and `run(privileged = True)` from Starlark.** The
  two kwargs that make "rogue unit = host compromise" trivial today. Spec'd in
  [Starlark unprivileged-only](https://github.com/yoebuild/yoe/blob/main/docs/specs/2026-05-20-starlark-unprivileged-only.md):
  delete both kwargs and move image-class and container-class privileged
  operations into Go drivers in `internal/`. Only two `.star` files in the whole
  tree use the kwargs today, both yoe-shipped classes, so the migration is
  bounded.
- **Pin modules by commit hash, not by ref.** A
  `module(ref = "v1.4.0", commit = "<sha>")` form, verified at clone and fetch
  time, would close the "upstream retagged the release" hole.
- **Verify cached modules on reuse.** `SyncIfNeeded` trusts whatever bytes are
  in `cache/modules/<name>/`. A simple manifest of expected commit hashes per
  module would detect tampering by other processes on the host.
- **Replace privileged loop/mount with a Go image assembler.** Tracked in
  [Build Environment §"Reducing Dependence on Docker's /dev"](build-environment.md).
  The same change removes `--privileged` from the image-assembly path.
- **Add a `--paranoid` mode that refuses `host = True` and
  `privileged = True`.** Useful for CI builds and for projects that want to fail
  loudly when a module tries to escape the container.
- **On-disk parsed-index cache poisoning.** Once feeds-as-modules ships (see
  `docs/specs/2026-05-13-feeds-as-modules.md` R21), each module's parsed
  APKINDEX is serialized to `feeds/<section>/<arch>/APKINDEX.cache` alongside
  the source index. The cache is gitignored and trusted on load (after header
  - source-hash check). An attacker with filesystem write access to the module
    checkout can plant a cache file with a matching header and arbitrary body,
    bypassing the source-index parse. Mitigations to consider when this becomes
    load-bearing: (a) move the cache to a process-owned location
    (`$XDG_CACHE_HOME/yoe/`) separate from the module checkout; (b) HMAC the
    cache body keyed to a per-install secret; (c) accept the equivalence ("cache
    trust = module-checkout filesystem-write trust") and document. Today's
    threat model already assumes module-checkout filesystem access implies
    trust, so (c) is the de-facto state — but the cache makes the attack
    mechanic cheap (no source-file edit needed), so worth revisiting.
- **In-tree signing keys + APKINDEX share the same access-control gate.**
  `module-alpine`'s `keys/` directory and `feeds/*/APKINDEX` live in the same
  git repo under the same maintainer write access. A compromised maintainer
  account can add a new trusted key + an APKINDEX signed by it in one commit,
  with no second factor. The maintainer playbook (`docs/module-alpine.md`, when
  feeds-as-modules lands) should flag key additions as higher-trust than routine
  APKINDEX refresh; longer-term mitigations include consuming-project-level key
  declarations or a signed-off-by CI gate on key-file changes.

## Where to look in the source

If you want to verify any of the claims above:

- `internal/container.go` — `containerRunArgs` builds the `docker run` line.
  `--privileged` is at line 162.
- `internal/build/sandbox.go` — bwrap invocation and what it binds.
- `internal/build/starlark_exec.go` — the `run()` builtin, including the
  `host=True` and `privileged=True` branches.
- `internal/build/executor.go` — `chownDirToHost`, the root-recovery path.
- `internal/image/disk.go` — image-class unit's `losetup`/`mount` flow.
- `internal/module/fetch.go` — module clone/fetch logic and lack of commit
  pinning.
- `internal/source/fetch.go` — source fetch and the SHA256 / apk_checksum
  verification.
