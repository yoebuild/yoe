# Alpine apk passthrough

How yoe consumes prebuilt Alpine packages through `module-alpine`, and the sharp
edges around metadata, noarch routing, and `-dev` subpackages. Read this before
adding new `alpine_pkg` units, debugging a "no such package" install error, or
expanding the Alpine surface beyond `module-core`'s source-built userland.

## Why this exists

Yoe started by treating every package as something it builds from source — each
unit produced a `$DESTDIR` of files and `internal/artifact/apk.go` packaged that
destdir into a fresh `.apk` with a yoe-generated `PKGINFO` and a project-key
signature. For `module-alpine` units (which fetch a prebuilt Alpine apk), the
same path applied: extract Alpine's apk into `$DESTDIR`, then rebuild a
yoe-flavoured apk on top.

That works as long as Alpine's `PKGINFO` is just a list of names — but Alpine's
apks carry a lot more:

- `replaces = busybox` so two packages can both ship a path without apk failing
  the install.
- `provides = so:libcrypto.so.3=3.5.4-r0` so packages that link against a shared
  library find their dep cleanly.
- `provides = cmd:sh=1.37.0-r14` so file-deps like `/bin/sh` resolve.
- `triggers = /usr/lib/firmware*` so kernel module updates re-fire hot-plug
  helpers.
- `.pre-install` / `.post-install` / `.trigger` shell scripts that run at
  install-time inside a chroot of the rootfs.

Regenerating `PKGINFO` from a hand-written `.star` drops every field the
generator didn't enumerate. Most visibly: busybox's `.post-install` is where
applet symlinks (`/bin/sh`, `/sbin/init`, …) get created via
`/bin/busybox --install -s`. Without it the kernel boots and finds no working
`init`.

The current architecture — _passthrough_ — sidesteps all of this by publishing
Alpine's apk verbatim, only swapping the signature.

## Passthrough, in two pieces

### 1. `alpine_pkg` declares `passthrough_apk`

```python
# testdata/.../module-alpine/classes/alpine_pkg.star (excerpt)
common = dict(
    name = name,
    version = base_version,
    release = release,
    source = url,
    passthrough_apk = asset,        # <— the new field
    runtime_deps = runtime_deps,
    provides = provides,
    replaces = replaces,
    ...
    tasks = [task("install", steps = _install_steps(asset))],
)
```

`passthrough_apk` names the upstream apk file in the unit's `srcDir`. The unit's
`install` task still runs (it extracts Alpine's data segment into `$DESTDIR` so
downstream units' build sysroots see the headers and libraries), but the
published apk is _not_ repackaged from that destdir.

### 2. The executor calls `RepackAPK` instead of `CreateAPK`

`internal/build/executor.go` branches on `unit.PassthroughAPK`:

- Empty → `artifact.CreateAPK(destDir, ...)` — the original "build a fresh apk"
  path. Source-built `module-core` units take this path.
- Set → `artifact.RepackAPK(srcAPK, ...)` — splits Alpine's apk into its three
  concatenated gzip streams, drops the Alpine signature, re-signs the control
  stream (PKGINFO + install scripts) with the project key, and concatenates
  `[new_sig, control, data]` into the published apk.

`RepackAPK` does not rewrite anything inside the control or data segments.
Alpine's `PKGINFO`, `replaces`, `provides`, `triggers`, install scripts, file
checksums — all unchanged.

The destdir extraction is still useful: yoe's per-unit sysroots are built by
hardlinking each dep's destdir into `<unit>/sysroot/`, so a unit that
`gcc -lfoo`s against a `module-alpine` library finds headers and shared objects
there. Image-time `apk add` reads the published apk (passthrough) and never
looks at the destdir.

## Two metadata systems, two purposes

After passthrough, every unit has metadata in two places:

- **`.star` fields** (`runtime_deps`, `provides`, `replaces`, …) drive yoe's
  _resolver_: build-order DAG, runtime-closure walk for image artifacts,
  virtual-package routing (`linux` → `linux-rpi4`), TUI USED-BY/PULLS-IN trees.
- **Upstream `PKGINFO`** (inside the apk's control segment) drives apk-tools at
  install time on the target: real shared-library deps, file-dep resolution,
  install-script execution, conflict checking.

These overlap conceptually but serve different stages. yoe's resolver doesn't
see `so:libcrypto.so.3` because it's not in the `.star`; apk-tools doesn't see
yoe's virtual `linux` because that's a yoe concept, not an apk one.

The `.star` therefore _should_ mirror enough of upstream's metadata that yoe's
resolver makes the same decisions apk-tools would — without duplicating every
field. `gen-unit.py` (in `module-alpine/scripts/`) populates `.star` fields from
Alpine's APKINDEX so the resolver-side view is accurate by construction.
Hand-edits are still needed for yoe-specific overrides (e.g.
`services = ["docker"]` to wire the runlevel symlink at packaging time).

## noarch routing — the four-part fix

apk-tools is unforgiving here: it constructs fetch URLs from PKGINFO's `arch`
field, _not_ from where it found the index entry. So a noarch apk _has_ to
physically live at `<repo>/noarch/<file>` — putting it under `<repo>/<arch>/`
and listing it in `<arch>/APKINDEX` doesn't make apk look there. Conversely,
apk's solver only reads one arch's APKINDEX per repository invocation, so noarch
entries also have to appear in the per-arch index.

The full design now in tree:

1. **`executor.go` routes noarch passthrough apks to `<repo>/noarch/`.** The
   arch comes from upstream PKGINFO via `artifact.ReadAPKArch`, not from the
   build arch.
2. **`GenerateIndex` scans the sibling `<repo>/noarch/` tree** when building a
   per-arch index, so each arch's APKINDEX advertises every noarch package as
   `A:noarch`. apk's solver finds the entry from any per-arch index.
3. **`Publish` regenerates every per-arch APKINDEX after a noarch publish.**
   Without this, the per-arch indexes go stale on every noarch unit rebuild.
4. **`cacheValid` looks under `<repo>/noarch/`** when the apk isn't in the
   per-arch dir, so noarch passthrough units don't rebuild on every `yoe build`
   invocation.

Symptoms when one of these halves is missing:

- `package mentioned in index not found` — usually file in arch dir but PKGINFO
  says noarch (apk fetches from `<base>/noarch/`, 404s).
- `<name> (no such package): required by world[<name>]` — file in noarch dir but
  per-arch APKINDEX doesn't reference it.
- noarch unit shows `[building]` every run despite the published apk being
  unchanged — `cacheValid` was checking the wrong directory.

## Auto-emitted `so:` provides

For yoe-source-built units, `internal/artifact/apk.go` walks `$DESTDIR`, opens
every regular file with Go's `debug/elf`, reads `DT_SONAME`, and emits one
`provides = so:<soname>=<ver>-r<rel>` line per shared library. This matches
Alpine's abuild convention and lets Alpine prebuilts that declare
`depend = so:libcrypto.so.3` resolve cleanly against a yoe-built `openssl`.

The mirror — auto-emit `depend = so:<soname>` from `DT_NEEDED` — is on the
roadmap (see `docs/roadmap.md`'s "Auto-depend from ELF DT_NEEDED").

## Worked example: why we couldn't use Alpine's docker-openrc

Tested end-to-end during the OpenRC switch. Documenting because the same shape
of problem will recur with any Alpine package whose dep tree pokes deep enough
into `module-core`'s source-built userland.

**The goal**: wire dockerd into the OpenRC default runlevel using Alpine's
`docker-openrc` package (which ships `/etc/init.d/docker` and a
`/etc/conf.d/docker` config template Alpine maintains).

**The dep tree (drawn out from upstream PKGINFOs):**

```
docker-openrc
└── log_proxy
    ├── musl
    └── glib
        ├── pcre2
        ├── libffi
        ├── libintl
        └── libmount
            └── libblkid
```

`log_proxy` is a tiny Alpine utility for capturing daemon stdout/stderr to
syslog. Glib is needed because log_proxy uses GIO. libmount/libblkid are in
glib's transitive deps because GIO has mount-table integration.

**The conflict.** Yoe's source-built `util-linux` (in `module-core`) ships
`libmount.so.1` and `libblkid.so.1` directly — it's a monolithic build. Alpine
splits util-linux: `libmount` and `libblkid` are separate apks. When apk's
solver tries to install both yoe's util-linux and Alpine's libmount/libblkid, it
fails because both packages own the same library paths.

**First attempt: `prefer_modules = {"util-linux": "alpine"}`.** Forces the
Alpine prebuilt instead of yoe's source-built version. Resolves the library
conflict. But Alpine's `util-linux` apk is a _meta_ package — it ships nothing
on disk; the actual binaries live in `util-linux-misc`, `util-linux-login`, the
libraries in `libuuid`/`libmount`/`libblkid`, and the headers + unversioned
`.so` symlinks needed at compile time live in `util-linux-dev`.

After pulling subpackages in via `runtime_deps`, the next layer: `e2fsprogs`
(yoe-source-built) needs `libuuid` headers + the unversioned `libuuid.so`
symlink to compile. Those live in `util-linux-dev`. Adding that pulls
`libfdisk`, `liblastlog2`, `libsmartcols`, `sqlite-dev` — none of which have yoe
units. Each one in turn pulls more.

**The yak shave.** To fully consume Alpine's `util-linux-dev`, we'd need units
for at least a dozen Alpine subpackages, plus their `-dev` counterparts, plus
careful conflict bookkeeping where yoe-source-built packages still ship
competing files. That's days of work and a much larger Alpine surface to
maintain.

**The trade.** A 30-line yoe-side OpenRC service script (in
`modules/module-core/units/net/docker-init/`) gives us the same boot behaviour
with no transitive deps. We give up Alpine's `/etc/conf.d/docker` config
template and the log_proxy stdout-capture story; for a yoe image those costs are
minor.

The lesson generalizes: Alpine's `-dev` subpackage convention is fundamentally
at odds with yoe's monolithic source-built libraries. Picking off Alpine
packages one at a time is fine; widening the surface to consume Alpine's whole
library-development ecosystem is a significant architecture decision, not a
one-off fix.

## What's still rough

Items where the architecture is "works for now" but obviously incomplete.

- **Hand-edited fields lost on regeneration.** `gen-unit.py` rewrites the whole
  `.star` body. Yoe-specific annotations like `services = [...]` and overrides
  like `runtime_deps` filtering have to be re-applied manually each time. Either
  the generator should learn these patterns (see `docs/roadmap.md`
  "deltas-over-PKGINFO field naming") or the cached `.star` files should be
  split into a generated chunk + a hand-edited chunk that survives.

- **Unresolved-package handling in the generator.** Today `_translate_one` drops
  only deps that don't exist in _any_ Alpine index. Deps that exist in Alpine
  but not as a yoe unit yet (the common case) are still emitted, which breaks
  yoe's resolver until someone generates the missing unit. Could be fixed by
  passing the current set of cached units to the generator and emitting a
  `# UNRESOLVED:` note for any that are missing.

- **No support for `-dev` packages.** All the architectural reasons in the
  docker-openrc example. Until yoe has a story for splitting headers out of
  source-built libraries (or for systematically wrapping Alpine's `-dev`
  ecosystem), pulling new Alpine packages in is a manual review for "does this
  transitively need any -dev subpackage."

- **No `triggers` execution.** Alpine apks ship `.trigger` scripts that fire on
  path changes (e.g. `udev` re-runs hot-plug rules when a module is added). The
  passthrough copy includes them, but yoe's image assembly doesn't currently
  invoke them in any consistent way. apk's on-target trigger machinery handles
  them after first boot, but image-build-time triggers (`-t` in apk) don't
  happen.

- **Auto-depend from `DT_NEEDED`.** Counterpart to the auto-`so:`-provides scan
  that already runs. Would catch the class of bug where a `.star` declares
  `runtime_deps` that silently misses a transitive shared-lib dependency.
  Roadmap item; design in `docs/roadmap.md`.

- **`prefer_modules` with subpackage expansion.** When you push a monolithic
  source-built unit (`util-linux`) to Alpine's split form, yoe's resolver
  follows `runtime_deps` from the meta package — but _build-time_ deps
  (`unit.Deps`) on `util-linux` don't pull subpackage destdirs into the build
  sysroot. Workaround: hand-edit downstream units to depend on the subpackages
  directly. Long-term the resolver should walk runtime_deps for build-deps too,
  or `unit.Deps` should accept the same expansion.

## Reference

- `internal/artifact/apk.go` — `CreateAPK`, `RepackAPK`, `ReadAPKArch`,
  `scanSONAMEs`.
- `internal/build/executor.go` — passthrough branch in the build loop;
  `cacheValid` for the noarch lookup.
- `internal/repo/local.go` — `Publish` (with cross-arch reindex on noarch) and
  `index.go`'s `GenerateIndex` (sibling-noarch scan).
- `testdata/.../module-alpine/classes/alpine_pkg.star` — the wrapper class.
  `scripts/gen-unit.py` — the unit generator.
- `docs/module-alpine.md` — when to reach for `module-alpine` vs `module-core`
  (rubric, not architecture).
