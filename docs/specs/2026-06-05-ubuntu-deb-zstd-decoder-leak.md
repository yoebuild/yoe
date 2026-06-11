<!--
Post-mortem: OOM during Ubuntu image builds traced to an unclosed zstd
decoder in the .deb parser, amplified by O(N²) index regeneration.
Date: 2026-06-05
-->

# Post-mortem: Ubuntu image builds OOM the machine

## Summary

Building an Ubuntu image with `yoe build -distro ubuntu dev-image` grew the
`yoe` process to ~54 GB resident and got it killed by the kernel OOM killer,
taking other desktop apps down with it. Debian image builds never showed the
problem.

The root cause was a one-line bug in the upstream `.deb` parser
(`pault.ag/go/debian`): it wrapped each zstd decompressor in `io.NopCloser`, so
the decoder was never closed. A streaming zstd decoder runs a background
goroutine holding large window/history buffers that are freed only by `Close()`;
with the no-op closer, every parsed `.deb` leaked a goroutine and its buffers.
Ubuntu compresses its `.deb` members with zstd; Debian uses xz, which has no
such goroutine — that is the entire reason one distro tripped it and the other
did not.

A second, independent issue amplified the leak: the index emitter regenerated
the whole `Packages`/`Release` set after **every** published `.deb`, re-reading
the entire pool each time — O(packages²) parses, hence O(packages²) leaked
decoders.

Two fixes:

1. **Forked the parser** and made the zstd reader close its decoder. This is the
   real fix; it bounds memory regardless of how many `.deb`s are parsed.
2. **Decoupled publishing from index generation** so the index is built once
   when it is consumed, not once per package — turning the emit cost from
   O(packages²) into O(packages).

After both, the same build holds a flat ~285 MB and completes.

## Symptom

`yoe build -distro ubuntu dev-image` ran the machine out of memory and was
killed. The interactive TUI was _not_ involved — this was a plain command-line
build. A separate, already-known TUI memory bug
([2026-06-04-tui-log-tail-memory.md](2026-06-04-tui-log-tail-memory.md)) reads
whole logs into memory, but that code path does not run during a CLI build, so
it was ruled out early.

## Evidence from the kernel log

The OOM report named `yoe` as the victim:

```
oom-kill: ...task=yoe,pid=2068454,uid=1000
Out of memory: Killed process 2068454 (yoe)
  total-vm:112355592kB, anon-rss:56276264kB, file-rss:84kB, pgtables:154632kB
```

Reading that off:

- **~53.7 GB resident** (`anon-rss` 56,276,264 kB), ~107 GB virtual.
- **Almost entirely anonymous** heap (`file-rss` only 84 kB) — so this was Go
  heap growth, not memory-mapped log files. (This is the detail that
  distinguishes it from the TUI log bug.)
- **154 MB of page tables** — the fingerprint of one enormous flat heap.

The per-process table confirmed `yoe` dwarfed everything else (next-largest
process: 496 MB), and it had drained all 16 GB of swap (`Free swap = 80kB`) on
top of 64 GB RAM.

The seconds before the kill were wall-to-wall Docker container churn — a veth
pair created and unregistered roughly once per second. `yoe` was actively
driving a container-per-package build loop while its heap climbed.

## Ruling out the obvious suspects

**Feed catalog size.** A natural first guess is that the Ubuntu package index is
huge and we parse it all into memory. The opposite is true: the Ubuntu feed is
6,487 entries / 7 MB; the Debian feed is 68,755 entries / 54 MB. Debian is ~8×
larger and never OOMs. Catalog parsing is not the driver.

**Per-build output buffering.** The build executor streams each unit's output to
`build.log` / `executor.log` via `io.MultiWriter` — it does not buffer whole
logs in memory. Not the driver either.

## Watching the leak with `gctrace`

Running the build under a memory-capped cgroup (so it could only kill itself,
not the desktop) with `GODEBUG=gctrace=1`:

```
systemd-run --user --scope -p MemoryMax=14G -p MemorySwapMax=0 \
  env GODEBUG=gctrace=1 yoe build -distro ubuntu dev-image
```

The **live heap after each GC** (the third number in `start->end->live`) climbed
monotonically:

```
gc25  live 1478 MB
gc27  live 2313 MB
gc29  live 4035 MB
gc31  live 6443 MB
gc33  live 10189 MB   ... and still climbing
```

~600 MB/s of _retained_ memory that GC could not reclaim — the signature of a
leak, not just allocation churn. The deltas grew as the build progressed, a hint
of superlinear (O(N²)) behavior.

## Pinpointing the allocation with a heap profile

A `SIGUSR1`-triggered heap profile (a temporary hook added to `main.go` for the
hunt, since removed) taken mid-climb showed one site dominating:

```
flat  flat%   cum%
6719MB 89.62% 89.62%  klauspost/compress/zstd.(*Decoder).startStreamDecoder.func2
 412MB  5.50% 95.12%  zstd.(*blockDec).reset
```

`startStreamDecoder.func2` is the zstd decoder's background decode goroutine,
holding window and history buffers. Diffing two snapshots (2 GB → 7.5 GB)
attributed essentially all the growth to that goroutine, reached via the index
emitter:

```
build.packageDeb → repo.PublishDeb → repo.GenerateDebianIndex
  → repo.buildPackagesFile → repo.packageStanza  (→ deb.ReadDeb → zstd)
```

The goroutine is detached, so it can never be garbage collected while it is
alive: it blocks waiting for work and exits only when the decoder is `Close()`d.
No `Close()` means the goroutine — and its buffers — live forever.

## Root cause

`yoe`'s `internal/deb.ReadDeb` parses a `.deb` via `pault.ag/go/debian/deb`.
That library decompresses each tar member through a per-extension decompressor.
The zstd one (`deb/tarfile.go`):

```go
func zstdNewReader(r io.Reader) (io.ReadCloser, error) {
	reader, err := zstd.NewReader(r)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(reader), err   // ← decoder is never closed
}
```

The loader already threads a real `io.Closer` through to callers and closes it
(both for `control.tar.*` after reading control, and for `data.tar.*` via the
`Deb.Closer` chain). But for zstd that closer is a no-op, so all the existing
close plumbing has nothing to act on. Every `ReadDeb` on a zstd `.deb` leaks
**two** decoders — one for `control.tar.zst`, one for `data.tar.zst` (the latter
opened eagerly even when the caller only reads control).

### Why Ubuntu and not Debian

Ubuntu compresses `.deb` members with zstd (`control.tar.zst` / `data.tar.zst`);
Debian uses xz (`data.tar.xz`). In our pool: 282 `data.tar.xz` vs 105
`data.tar.zst`. The gzip/xz/lzma/bzip2 paths return readers with no persistent
goroutine, so they don't leak. Only the zstd path does — so the bug was
invisible on Debian and fatal on Ubuntu.

### The O(N²) amplifier

`PublishDeb` copied a `.deb` into the pool and then called
`GenerateDebianIndex`, which re-scanned the **entire** pool — and read each
`.deb` twice (once for its architecture, once for its `Packages` stanza). Called
once per published package, that is O(packages²) parses across a build, hence
O(packages²) leaked decoders. For a ~200-package image that is tens of thousands
of `ReadDeb` calls — which is how a per-`.deb` leak of tens of MB reached 54 GB.

## The fix

### 1. Close the zstd decoder (the real fix)

The bug is upstream and one line. We forked `paultag/go-debian` to
`yoebuild/go-debian` (tag `v0.19.0-yoe1`, commit `e21832a`) and made the zstd
reader close its decoder:

```go
type zstdReadCloser struct{ d *zstd.Decoder }

func (z zstdReadCloser) Read(p []byte) (int, error) { return z.d.Read(p) }
func (z zstdReadCloser) Close() error               { z.d.Close(); return nil }

func zstdNewReader(r io.Reader) (io.ReadCloser, error) {
	reader, err := zstd.NewReader(r)
	if err != nil {
		return nil, err
	}
	return zstdReadCloser{reader}, nil
}
```

Wired into `yoe` via a module replacement in `go.mod`:

```
replace pault.ag/go/debian => github.com/yoebuild/go-debian v0.19.0-yoe1
```

The same change is offered upstream as a draft pull request
(`paultag/go-debian#140`); once it lands and is released, the `replace` can be
dropped for the upstream version.

We chose a fork over reimplementing `.deb` parsing in `yoe`: the fork is a
4-line change at the true root that fixes every call path, current and future,
whereas a `yoe`-side workaround would have had to stop using the library's
loader entirely and reimplement member iteration, control parsing, and
compressor dispatch — far more code at a worse layer than the bug.

### 2. Generate the index once, not once per package

Independent of the leak, the per-publish full-pool re-scan was wasteful. We
decoupled the two concerns (`internal/repo/deb_emitter.go`,
`internal/build/executor.go`):

- `PublishDeb` now only copies the `.deb` into the pool. It no longer
  regenerates the index.
- `GenerateDebianIndex` reads each `.deb` **once** per run (architecture and
  `Packages` stanza come from a single parse) instead of twice.
- The index is regenerated from the pool only when it is consumed:
  - immediately before image assembly (an image already did this, so its
    `mmdebstrap` copy source can never lag the pool), and
  - once at the end of a build that published `.deb`s without building an image
    (so a direct `yoe build <deb-unit>` still leaves a current index).

A flag pair in the build orchestrator (`publishedDeb`, `imageRefreshed`) ensures
the end-of-build refresh runs exactly when needed and never redundantly when an
image already refreshed the index. Net: index emit drops from O(packages²) to
O(packages).

## Verification

Re-running the build confined to 14 GB with `gctrace`:

- **Memory:** peak live heap **285 MB**, a flat sawtooth, where the unpatched
  binary was past 10 GB and climbing at the same point. Build completed (exit
  0).
- **Index correctness, deb-only path:** `yoe build -distro ubuntu --force curl`
  published its `.deb` and the end-of-build refresh produced a well-formed index
  (all packages present, the rebuilt `curl` stanza updated).
- **Index correctness, image path:**
  `yoe build -distro ubuntu --force dev-image` ran the image-time regen and
  `mmdebstrap` assembled the rootfs and disk from it (exit 0) — proving the
  deferred index is valid at assembly time.
- `go test ./internal/repo/ ./internal/deb/ ./internal/build/` passes.

## Takeaways

- **An all-anonymous OOM with growing page tables is a Go heap leak**, not
  mmap'd files. That single line in the kernel report (`file-rss: 84kB`)
  redirected the whole investigation away from the TUI log bug.
- **`gctrace` first, profile second.** The live-heap-after-GC column proved a
  leak (vs. churn) in seconds and for free; the heap profile then named the
  exact site. Cap memory with a cgroup before reproducing a known OOM so the
  repro can't take the machine down.
- **A distro-specific failure is a data-shaped clue.** "Ubuntu but not Debian"
  pointed straight at what differs in the data — zstd vs xz compression — long
  before the profile confirmed it.
- **Leaks and O(N²) compound.** A bounded per-iteration leak is survivable; an
  O(N²) call count turns it into an OOM. Fixing the leak was necessary; fixing
  the quadratic emit removed the multiplier and sped up builds as a bonus.
