# Language Choices: Go and Starlark

`[yoe]` makes two deliberate language decisions on two different axes, and the
split between them is itself part of the design:

- **The engine is written in Go.** The `yoe` binary that resolves the unit
  graph, hashes inputs, drives containers, signs packages, and renders the TUI
  is a Go program.
- **Units, machines, images, and project config are written in Starlark.** What
  to build and how to build it is expressed in `.star` files, never in Go.

This page explains why each language was chosen over its alternatives, and why
keeping the two separate is a feature rather than an accident. For the full
survey of configuration-language candidates (CUE, Nickel, Jsonnet, Lua, Nix),
see [Build & Configuration Languages](build-languages.md); this page gives the
holistic rationale and adds the engine-language comparison that the survey does
not cover.

## Simplicity

The overwhelming reason for using Go and Starlark is simplicity. While Rust and
Nix are more powerful and capable, they come with a cost: complexity. They are
harder to debug. We might argue that AI can do the debugging, but even AI is
slower debugging a complex system than a simple one. Iteration speed is critical
in a build system.

`[yoe]` targets small teams that may not have a Rust or Nix expert on staff.
Many of these developers are coming from application or microcontroller
development. Go and Starlark are comprehensible by anyone with any programming
experience.

## Two languages, two jobs

A build system has two very different kinds of code in it. One kind _decides_
things: which units are in the closure, whether a cache entry is still valid,
which container to spawn, whether a signature verifies. The other kind
_declares_ things: this package is named openssh, it builds with autotools, it
depends on zlib and openssl.

`[yoe]` keeps these in separate languages on purpose. Go holds the policy;
Starlark holds the data. Starlark has no I/O primitives of its own; it cannot
open a file, spawn a process, or reach the network except through a builtin that
the Go side chose to register. That boundary is what makes the system auditable:
the complete set of things any unit can cause to happen is the enumerable set of
Go builtins, and it does not grow when a user imports another module. The
[Security and Threat Model](security.md) page treats this "Starlark is data, Go
is policy" split as the single biggest leverage point in yoe's security story.

A single-language design would give up that boundary. Embedding a general
scripting language for config (Python, JavaScript, Lua-with-`os`) hands every
unit author the host's standard library as an attack surface; writing config as
Go plugins makes every unit a program that has to be compiled and trusted as
code. The two-language split is what lets simple units read like declarative
config while the engine stays a small, reviewable Go core.

## The engine: why Go

### What the engine actually does

The `yoe` binary is an orchestrator. Its work is to:

- resolve and validate the whole unit DAG before anything builds,
- compute content-addressed input hashes and check the cache,
- shell out to the real workers: `git` to fetch sources, `docker`/`podman` to
  run per-unit builds (including foreign-arch builds under QEMU user-mode), and
  `apk`/`apt`/compilers inside those containers,
- assemble root filesystems and bootable disk images,
- sign packages and repository indexes, serve them, and drive on-device updates,
- render an interactive TUI.

The wall-clock time of almost any real build is dominated by the _child
processes_ the engine launches (gcc, the kernel build, `mmdebstrap`, rootfs
assembly), not by the engine's own CPU work. The engine's job is to coordinate
those processes correctly and quickly, not to crunch numbers.

### Why Go fits that job

- **It is an orchestrator, not a hot loop.** Because runtime is spent inside
  spawned compilers and package tools, a garbage-collected language with a fast
  startup and excellent process and I/O handling is exactly right. The things a
  systems language buys you (no GC pauses, manual memory layout) buy almost
  nothing here.
- **Concurrency matches the workload.** Building a DAG with bounded parallelism
  is the canonical goroutines-and-channels problem. Fan out the ready units, cap
  the workers, cancel cleanly through `context`. This is idiomatic, readable Go.
- **One static binary.** `yoe` ships as a single static binary users drop onto a
  laptop, a build farm, or a CI runner, with no runtime to install. Build times
  for the engine itself are seconds, which keeps iteration on `yoe` fast.
- **A lean, Go-native dependency set.** The in-process work uses focused Go
  libraries: [`go.starlark.net`](https://github.com/google/starlark-go) for
  evaluation, the [Charm](https://github.com/charmbracelet) stack
  (`bubbletea`/`bubbles`/`lipgloss`) for the TUI, `go-crypto` and
  `golang.org/x/crypto` for apt GPG and apk RSA signing, and `zeroconf` for mDNS
  device discovery. The heavy lifting (git, docker, package managers, compilers)
  is shelled out to the real tools rather than reimplemented against a library,
  so the dependency surface stays small in a mature ecosystem.
- **Good company.** The closest cultural ancestors of a build orchestrator are
  all Go: Bazel's Go tooling, BuildKit, ko, and Chainguard's melange/apko (an
  apk-based image builder directly adjacent to module-alpine). Choosing Go keeps
  `[yoe]` in the current of build and cloud-native tooling rather than swimming
  against it.

### The alternatives

- **Rust.** Rust's strengths (memory safety without a GC, zero-cost
  abstractions) are real, but they target problems `[yoe]` does not have. The
  engine spends its life waiting on child processes, so there is no hot path for
  zero-cost abstractions to accelerate, and a GC pause in an orchestrator is
  invisible. yoe's actual correctness risks live in build and caching
  _semantics_ (a dropped DAG edge, a hash that invalidates too much or too
  little, a dangling runtime-closure reference), and the borrow checker does not
  catch those. Rust's async model (lifetimes across `.await`, `Send`/`Sync`
  bounds) also makes the same subprocess-plus-TUI fan-out harder to write and
  maintain. Rust would earn its place for a different component (a
  high-throughput content-addressed store daemon, or the compilers themselves),
  but not for the orchestration layer.
- **C / C++.** Manual memory management and a weaker concurrency story for this
  shape of work, with slower iteration and heavier dependency handling. The
  systems `[yoe]` aims to improve on (Yocto's BitBake is Python over C) sit
  lower on this axis already; moving the engine down a level would trade away
  productivity for control yoe does not need.
- **Zig.** Promising as a language, but the ecosystem an engine leans on (a
  mature embeddable config language, TUI toolkit, crypto and archive libraries)
  is not there yet. Zig's best role here is as a target language for the things
  yoe builds, not as the engine.
- **Python.** The incumbent for build systems (BitBake, SCons) and the source of
  the very pain `[yoe]` exists to reduce: slow startup and execution, no single
  static binary, runtime-dependency packaging headaches, and a concurrency story
  constrained by the GIL. Starlark already gives users Python-shaped _syntax_
  without paying Python's runtime costs in the engine.

**Verdict: Go.** It is the natural fit for an orchestrator that coordinates many
processes, ships as one binary, and lives in a Go-native tooling ecosystem.

## The configuration language: why Starlark

### What it has to do

The configuration language expresses units, classes, machines, images, and
project config. The hard requirements fall out of the rest of the system:

- **Deterministic:** the same inputs must always evaluate the same way, because
  the evaluated unit feeds a content-addressed cache key.
- **Sandboxed:** build definitions must not perform arbitrary I/O or network
  access, both for caching integrity and for the security boundary above.
- **Go-embeddable:** it has to run inside the `yoe` binary with no subprocess or
  FFI.
- **Readable by humans and AI:** units are meant to be written and reviewed by
  small teams and generated by AI assistants, so a familiar syntax matters.
- **Expressive enough:** conditionals, loops, and helper functions for the
  classes that encode real build logic.

### Why Starlark fits

Starlark is a dialect of Python built for exactly this purpose, used by Bazel
and Buck2 at enormous scale. It is deterministic and hermetic by construction
(no `import`, no clocks, no randomness, bounded execution), it embeds directly
through the Google-maintained `go.starlark.net` library, and its Python-shaped
syntax is one most developers (and most AI models) read fluently on first sight.
Its restrictions are subtractive: you learn what you _cannot_ do, not a new
paradigm. And because it carries no I/O of its own, it is the "data" half of the
data-versus-policy split that keeps the engine's audit surface small.

### The alternatives, in brief

The [Build & Configuration Languages](build-languages.md) survey works through
CUE, Nickel, Jsonnet, and Lua in detail; the short version is that each either
cannot express imperative build logic (CUE, Jsonnet), is not Go-native (Nickel
is Rust), or is not deterministic without manual sandboxing (Lua). Starlark is
the only candidate that is build-proven, Go-native, deterministic by design, and
familiar all at once.

Nix deserves a direct word because it is the most credible rival and the subject
of its own page, [yoe and Nix](nix.md). The Nix _language_ has one genuine
advantage Starlark lacks: best-in-class build-time composition through overlays
and `overrideAttrs`, the cleanest way in existence to say "the same package, but
with this flag flipped." That power cuts both ways, though. The same override
model is a leading reason Nix is hard to learn and harder to debug: overlays
modify any package's attributes from a distance, so tracing which overlay set a
value means replaying evaluation order; `.override`, `.overrideAttrs`, and the
`callPackage` fixpoint operate at different layers you have to understand before
you can pick between them; and lazy evaluation tends to surface an override
mistake far from where it was made. The one capability Starlark gives up is also
the one that costs Nix the most in comprehensibility. Beyond that, the Nix
evaluator is a C++ application rather than a library, so embedding it would mean
shelling out to a full Nix installation, and the wider Nix model is rough for
small, hardware-bootable images. `[yoe]` handles build-time variation
differently: it reuses the binary across everything that shares a
`(distro, arch, and sometimes machine)` key and pushes the rest to runtime
resolution.

**Verdict: Starlark.** It is the only option that satisfies every hard
requirement, and it is proven for precisely this use case.

## Trade-offs

These choices are not free, and it is worth naming the costs.

- **Go's garbage collector** is present, but irrelevant for an orchestrator
  whose latency is set by the compilers it launches.
- **Starlark has no implicit merging or override system.** Composition is
  through explicit function calls and keyword arguments, which is more verbose
  than a Nix overlay but far easier to trace: to learn why a unit gained a
  dependency, you grep for the call rather than replay an overlay evaluation
  order. This is the spot most worth watching as the unit set grows. `[yoe]`'s
  answer today is to keep a unit the single definition of a build and resolve
  variation at runtime wherever possible (see
  [Yoe and distributions](distro.md)); if build-time variation pressure ever
  outgrows that, the response would be a disciplined, Starlark-level override
  mechanism, not adopting Nix wholesale.

## When we would revisit

These decisions are durable, but two concrete situations would reopen them:

- **A throughput-bound component.** If `[yoe]` grew, say, a content-addressed
  store daemon serving thousands of requests per second, that specific component
  would be worth weighing in Rust, without moving the orchestrator off Go.
- **Override pressure.** If runtime resolution and the single-definition rule
  stop covering real build-time variation, the config layer would gain an
  explicit override mechanism layered on Starlark, keeping the language while
  borrowing the one idea Nix does best.

Until then, Go for the engine and Starlark for configuration give `[yoe]` a
small auditable core, a single distributable binary, and units that humans and
AI can both read, which is exactly what a build system for small teams needs.
The constraints are what keeps it simple.

## See also

- [Build & Configuration Languages](build-languages.md): the full
  configuration-language survey and comparison matrix.
- [yoe and Nix](nix.md): the deeper Nix comparison.
- [Security and Threat Model](security.md): the data-versus-policy split as a
  security boundary.
- [Comparisons](comparisons.md): how `[yoe]` relates to other build systems,
  including the "one language, end to end" axis.
