# Roadmap

> **About this document:** the individual roadmap items now live as GitHub
> issues labeled
> [`roadmap`](https://github.com/yoebuild/yoe/issues?q=is%3Aissue+is%3Aopen+label%3Aroadmap).
> That is the list to browse, search, claim, and close. This page keeps only the
> framing that does not belong to any single item: why a theme matters, and what
> has already landed. Design discussion belongs in the relevant `docs/*.md` —
> link to it from the issue.

## Developer Experience

The biggest leverage area: making yoe pleasant for the developer writing apps
that run on yoe-built devices, not just for the author of a distro.

### Source can directly embed units

A `.star` file living in the application's own source tree, declaring its
dependencies and included directly from a `PROJECT.star`. This is what lets yoe
serve as an application build tool as well as a system build tool.

### Build & Deploy Loop

Goal: app developers work directly in their app's git repo, not against an
extracted SDK. The build container _is_ the SDK. See [dev-env.md](dev-env.md)
for the design.

## Needed Units

Existing units can be found via `yoe list` or by browsing
`modules/units-core/units/`. Units still wanted are filed individually.

## Testing

Today: Go unit tests under `internal/*` and a single dry-run e2e test. No
on-device tests, no image smoke tests, no build-time package QA, no CI workflow
that runs builds. Design and intended shape in [testing.md](testing.md), which
also compares to Yocto's `oeqa` / `INSANE.bbclass` / `ptest` / `buildhistory`.

## Self-Hosting

The ultimate dogfood test: develop yoe on a yoe-built device. Forces the distro
to be capable enough for real engineering work, not just demo targets, and
surfaces gaps in container hosting, editor experience, and the build cache all
at once.

The first cut shipped as `selfhost-image` for the Raspberry Pi 5 — see
[selfhost.md](selfhost.md). It bundles yoe, Go, Docker, git, bubblewrap, and the
dev-image tool set.

## Already landed

Kept here because the roadmap is where these were last tracked; the detail lives
in the linked docs.

- **glibc target.** yoe builds glibc/`.deb` images via the Debian and Ubuntu
  backends (`apt_feed`), in addition to the musl/`.apk` Alpine path. Both Debian
  and Ubuntu are CI boot- and SSH-verified on arm64 and x86_64. This enables
  workloads whose binaries require glibc (some cgo, prebuilt vendor SDKs, the
  upstream Helix release, etc.).
- **`yoe serve` / `yoe deploy <unit> <host>` /
  `yoe device repo {add,remove,list}`.** See [feed-server.md](feed-server.md).
- **Source download retries and mirrors.** A transient upstream failure is
  retried, and a unit that lists `mirrors` falls back to them once the primary
  URL is exhausted. See [caching.md](caching.md).

See [yoe-tool.md](yoe-tool.md) for design notes on `(planned)` CLI sections, and
[metadata-format.md](metadata-format.md) for the unit and configuration format.
