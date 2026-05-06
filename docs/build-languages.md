# Build & Configuration Languages

An analysis of embeddable languages for defining units, build rules, and project
configuration in `[yoe]`. This informs the choice of how users express _what to
build_ and _how to build it_.

## The Problem

`[yoe]` needs a way for users to define:

- **Units** — what to build, from what source, with what dependencies
- **Classes/rules** — how to build (autotools, cmake, go, image assembly)
- **Project config** — cache locations, remote repos, module references
- **Machine definitions** — architecture, kernel, bootloader, partitions
- **Image definitions** — package lists, services, hostname, partitions

The simplest approach is a data format (TOML/YAML) for all of these. But
experience shows that pure data formats accumulate escape hatches as complexity
grows: conditional dependencies, machine-specific overrides, image inheritance,
shell commands embedded in strings. These are signs the data format wants to be
a language.

## Requirements

1. **Simple for the common case** — defining a package unit should be as
   readable as a TOML file
2. **Composable** — modules, overlays, and unit extensions without modifying
   originals
3. **Expressive when needed** — conditionals, loops, helper functions for
   complex build logic
4. **Deterministic** — same inputs always produce the same output (critical for
   content-addressed caching)
5. **Sandboxed** — build definitions cannot perform arbitrary I/O or network
   access
6. **Go-native** — embeddable in a Go binary without external dependencies
7. **Familiar syntax** — low learning curve for developers

## Language Survey

### Starlark

**Used by:** Bazel (Google), Buck2 (Meta), Pants, Gazelle

Starlark is a dialect of Python designed specifically for build system
configuration. It is deterministic (no `import os`, no network, no randomness),
hermetic, and embeddable. The Go implementation
([go.starlark.net](https://github.com/google/starlark-go)) is maintained by
Google.

**Example unit:**

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "openssh",
    version = "9.6p1",
    source = "https://cdn.openbsd.org/.../openssh-9.6p1.tar.gz",
    configure_args = ["--sysconfdir=/etc/ssh"],
    deps = ["zlib", "openssl"],
)
```

**Strengths:**

- Battle-tested at enormous scale (Google's entire build, Meta's mobile builds)
- Python-like syntax — most developers can read it immediately
- Deterministic by design — no side effects, no mutable global state
- Mature Go library with good documentation
- Functions and `load()` provide natural composition
- Built for exactly this use case

**Weaknesses:**

- No native data merging/overlay system (unlike Nix or CUE) — composition is
  through explicit function arguments
- Subtle differences from Python can trip up experienced Python developers (no
  `class`, no exceptions, no `import`, dict insertion order matters)
- The `load()` system adds a dependency resolution layer for build files
  themselves

**Composability model:** Function calls and macros. A base unit exports a
configurable function; modules call it with overrides. Explicit but verbose:

```python
# module-core/openssh.star — base unit as a function
def openssh(extra_deps=[], extra_configure_args=[], **overrides):
    autotools(
        name = "openssh",
        version = "9.6p1",
        deps = ["zlib", "openssl"] + extra_deps,
        configure_args = ["--sysconfdir=/etc/ssh"] + extra_configure_args,
        **overrides,
    )

# vendor-bsp/openssh.star — vendor module extends it
load("//module-core/openssh.star", "openssh")
openssh(extra_deps=["vendor-crypto"])
```

---

### CUE

**Used by:** Dagger, various Kubernetes tooling

CUE is a configuration language created by Marcel van Lohuizen (who also created
`gofmt`). Its defining feature is **unification** — you define partial
configurations in separate files and CUE merges them, checking constraints
automatically. Types and values exist on a single lattice; a type is just a
constraint on a value.

**Example unit:**

```cue
openssh: {
    version: "9.6p1"
    deps: ["zlib", "openssl"]
    build: ["./configure --prefix=$PREFIX", "make -j$NPROC"]
}
```

**Example overlay (separate file, merges automatically):**

```cue
openssh: {
    deps: ["zlib", "openssl", "vendor-crypto"]
    configure_args: ["--with-vendor-crypto"]
}
```

**Strengths:**

- **Closest to Nix-style composability** — partial definitions in different
  files merge automatically without explicit imports
- Types-as-constraints provide built-in validation (`version: =~"^[0-9]"`)
- Go-native implementation
- No Turing-completeness — guaranteed termination
- Excellent for data-heavy configuration

**Weaknesses:**

- **Cannot express imperative build logic** — no loops for generating targets,
  no calling external commands, no procedural steps
- Unusual paradigm (lattice-based unification) — steeper learning curve than
  expected for what looks like JSON
- Smaller ecosystem and community than Starlark
- Would need pairing with another language for class/rule logic

**Composability model:** Implicit merging via unification. Define parts in
different files within the same package; CUE merges them and reports conflicts.
This is the most Nix-like model available outside of Nix itself.

---

### Nickel

**Used by:** Tweag projects, NixOS-adjacent tooling

Nickel is explicitly designed to be "Nix, but simpler." It has contracts
(gradual typing), merge semantics (like Nix's `//` operator), and a Python-like
syntax. It aims to be the configuration language Nix should have been.

**Example unit:**

```nickel
{
  openssh = {
    version = "9.6p1",
    deps = ["zlib", "openssl"],
    build = fun arch =>
      if arch == "arm64" then
        ["./configure --host=aarch64", "make"]
      else
        ["./configure", "make"],
  }
}
```

**Strengths:**

- **Designed for Nix-style composition** — record merging, overrides, and
  priority annotations
- Contracts provide validation without a separate type system
- More approachable syntax than Nix
- Deterministic evaluation

**Weaknesses:**

- **Not Go-native** — implemented in Rust; embedding in a Go binary requires FFI
  or running as a subprocess
- Young project — smaller ecosystem, less battle-testing
- Smaller community than Starlark or CUE

**Composability model:** Record merging with priority, very similar to Nix
overlays. Define a base, merge overrides, and Nickel resolves conflicts using
priority annotations.

---

### Jsonnet

**Used by:** Grafana (dashboards), Tanka (Kubernetes), various config generation

A templating language that extends JSON with variables, conditionals, imports,
functions, and object composition via the `+` operator.

**Example unit:**

```jsonnet
local base = import 'module-core/openssh.jsonnet';

base {
  deps+: ['vendor-crypto'],
  configure_args+: ['--with-vendor-crypto'],
}
```

**Strengths:**

- Simple mental model — "JSON with functions and imports"
- Object merging with `+:` (append) and `+` (override) is intuitive
- Go-native implementation ([go-jsonnet](https://github.com/google/go-jsonnet))
- Deterministic
- Good for layered configuration

**Weaknesses:**

- **Designed for data generation, not build systems** — no concept of targets,
  dependencies, or build phases
- Verbose for complex logic
- Weaker validation than CUE (no constraint system)
- Less expressive than Starlark for imperative build logic

**Composability model:** Object inheritance with `+` operator. Import a base
object, override or append fields. Straightforward and explicit.

---

### Lua / Luau

**Used by:** Neovim, Redis, game engines, Premake (build system)

Lightweight embeddable scripting language. Luau (Roblox) adds gradual typing.

**Example unit:**

```lua
autotools {
    name = "openssh",
    version = "9.6p1",
    deps = {"zlib", "openssl"},
    configure_args = {"--sysconfdir=/etc/ssh"},
}
```

**Strengths:**

- Extremely lightweight runtime (~200KB)
- Very fast (LuaJIT, Luau)
- Simple, well-understood language
- Good Go bindings (gopher-lua, go-luau)
- Tables provide natural composition via metatables

**Weaknesses:**

- **Not deterministic by default** — has `os.execute`, `io.open`, etc. that must
  be sandboxed by removing from the environment
- Not designed for build systems — no built-in `load()` or module system
  suitable for build file composition
- 1-indexed arrays (trivial but annoys developers)
- No built-in constraint/validation system

**Composability model:** Table merging via metatables or explicit merge
functions. Powerful but requires convention — the language doesn't enforce a
composition pattern.

---

### Nix Language

**Used by:** NixOS, Nixpkgs (100,000+ packages)

A pure, lazy, functional language designed for package management and system
configuration.

**Example unit:**

```nix
{ stdenv, zlib, openssl }:
stdenv.mkDerivation {
  pname = "openssh";
  version = "9.6p1";
  buildInputs = [ zlib openssl ];
  configureFlags = [ "--sysconfdir=/etc/ssh" ];
}
```

**Strengths:**

- **The gold standard for composability** — overlays, overrides, and the
  fixpoint pattern enable arbitrary layered modification of any package
- Lazy evaluation means unused definitions have zero cost
- Proven at massive scale (100,000+ packages in Nixpkgs)
- Perfectly deterministic

**Weaknesses:**

- **Not embeddable** — the evaluator is a C++ application, not a library
- Steep learning curve — the language is deceptively complex (laziness,
  fixpoints, `callPackage` patterns)
- Error messages are notoriously poor
- Debugging "which overlay changed this attribute?" is difficult
- The very power of overlays is also a debuggability problem — implicit
  modification from anywhere makes tracing changes hard

**Composability model:** Overlays and the fixed-point pattern. A base package
set is a function; overlays are functions that modify it. The system computes
the fixed point, producing the final package set. Extremely powerful, but the
indirection makes debugging non-trivial.

---

## Comparison Matrix

| Feature               | Starlark          | CUE         | Nickel    | Jsonnet         | Lua          | Nix             |
| --------------------- | ----------------- | ----------- | --------- | --------------- | ------------ | --------------- |
| Go-native             | Yes               | Yes         | No (Rust) | Yes             | Yes          | No (C++)        |
| Deterministic         | By design         | By design   | By design | By design       | Must sandbox | By design       |
| Sandboxed             | By design         | By design   | By design | By design       | Must sandbox | By design       |
| Build system proven   | Bazel/Buck2       | Dagger      | Young     | No              | Premake      | NixOS           |
| Composability         | Functions         | Unification | Merging   | Object `+`      | Tables       | Overlays        |
| Implicit merging      | No                | Yes         | Yes       | Partial         | No           | Yes             |
| Imperative logic      | Yes               | No          | Limited   | Limited         | Yes          | No (functional) |
| Learning curve        | Low (Python-like) | Medium      | Medium    | Low (JSON-like) | Low          | High            |
| Community size        | Large             | Medium      | Small     | Medium          | Large        | Large           |
| Constraint validation | No                | Built-in    | Contracts | No              | No           | No              |

## Recommendation

**Starlark** is the recommended choice for `[yoe]`.

**Why:**

1. **Proven for exactly this use case.** Bazel and Buck2 demonstrate that
   Starlark works for build system configuration at the largest scales. No other
   language on this list has been tested as thoroughly in the build system
   domain.

2. **One language for everything.** Units, classes, project config, machine
   definitions — all Starlark. No TOML + shell + something-else stack. Simple
   units read like declarative config; complex classes use real control flow.

3. **Go-native.** The `go.starlark.net` library embeds directly in the `yoe`
   binary. No FFI, no subprocess, no external runtime.

4. **Deterministic and sandboxed by design.** Critical for content-addressed
   caching — if the build definition is deterministic, the cache key is
   reliable. Starlark guarantees this without any configuration.

5. **Familiar syntax.** Python-like syntax means most developers can read
   Starlark immediately. The restrictions (no classes, no exceptions, no I/O)
   are subtractive — you learn what you _can't_ do, not a new paradigm.

**What we give up compared to Nix/CUE:**

- No implicit merging — composition is through explicit function calls and
  `**kwargs`. This means module overrides are more verbose but also more
  traceable. When debugging "why does openssh have vendor-crypto in its deps?",
  you can grep for the function call. In Nix, you'd have to trace overlay
  evaluation order.

- No built-in constraint validation — unit validation happens in Go code (the
  `yoe` engine) rather than in the language itself. CUE's constraint system is
  elegant, but adding a second language isn't worth it.

**Composability pattern for modules:**

`[yoe]`'s module system (vendor BSP modules, product modules) works through
Starlark's function composition:

```python
# Module 1: module-core/openssh.star
def openssh(extra_deps=[], **overrides):
    unit(
        name = "openssh",
        version = "9.6p1",
        deps = ["zlib", "openssl"] + extra_deps,
        **overrides,
    )

# Module 2: vendor-bsp/openssh.star
load("//module-core/openssh.star", "openssh")
openssh(extra_deps=["vendor-crypto"])

# Module 3: product/openssh.star (further customization)
load("//vendor-bsp/openssh.star", "openssh")
openssh(extra_configure_args=["--with-pam"])
```

Each module is explicit about what it modifies and where the base comes from.
This is less magical than Nix overlays but easier to debug.

## What This Means for `[yoe]`

With Starlark as the single language, the project structure becomes:

```
my-project/
├── PROJECT.star              # project config: name, caches, modules
├── machines/
│   ├── beaglebone-black.star
│   ├── raspberrypi4.star
│   └── qemu-arm64.star
├── units/
│   ├── openssh.star          # package unit
│   ├── myapp.star            # app unit (Go)
│   ├── base-image.star       # image unit
│   └── dev-image.star        # image unit (extends base)
├── classes/                  # reusable build rule functions
│   ├── autotools.star
│   ├── cmake.star
│   ├── go.star
│   └── image.star
└── overlays/
```

TOML is eliminated entirely. Units, classes, machines, and project config are
all `.star` files. The Go `yoe` binary provides the built-in functions
(`unit()`, `image()`, `machine()`, `project()`, etc.) that Starlark code calls.

## Starlark Ecosystem & Adoption

Understanding the breadth of Starlark adoption helps validate the choice and
provides reference implementations to learn from.

### Projects Using Starlark (the language)

These projects implement their own Starlark interpreter (typically in Java or
C++):

- **Bazel** (Google) — the build system Starlark was originally designed for.
  Java-based Starlark interpreter. The largest and most mature Starlark
  deployment.
- **Buck2** (Meta) — Meta's next-generation build system, uses Starlark for
  `BUCK` files. Rust-based interpreter.
- **Pants** — a Python-ecosystem build system that uses Starlark for `BUILD`
  files. Rust-based interpreter.
- **Copybara** (Google) — a tool for transforming and moving code between
  repositories. Java-based.

### Projects Using starlark-go (the Go library)

These projects embed the
[go.starlark.net](https://github.com/google/starlark-go) Go library — the same
library `[yoe]` would use:

- **Tilt** — microservice dev environment; uses Starlark for `Tiltfile`
  configuration
- **Delve** — the standard Go debugger; uses Starlark as a scripting language
  for automation
- **Drone** — CI/CD platform; supports Starlark as an alternative to YAML
  pipelines
- **Isopod** (Cruise Automation) — DSL framework for Kubernetes configuration
- **Kurtosis** — developer tool for packaging and running containerized service
  environments
- **envd** — CLI for building Docker images for ML development and production
- **Bramble** — a purely functional build system and package manager
- **Gazelle** (Bazel) — BUILD file generator for Go/Protobuf projects; uses
  starlark-go for evaluating directives
- **AsCode** — infrastructure-as-code using Starlark on top of Terraform
- **AutoKitteh** — developer platform for workflow automation and orchestration
- **FizzBee** — system design language for verifying distributed systems

### Why This Matters for `[yoe]`

The starlark-go library is actively maintained by Google and used in production
by a diverse set of Go projects. The pattern of embedding starlark-go to provide
a sandboxed, deterministic configuration language in a Go CLI is
well-established — `[yoe]` would be following a proven approach, not blazing a
new trail.

## Open Questions

- **Class composition:** Should multiple classes be applied via multiple
  function calls (`autotools()` + `systemd_service()`) or via a single wrapper
  macro (`systemd_autotools()`)? Both work; the question is which to encourage
  as convention.
- **Machine-specific conditionals:** Should machine properties be available as
  Starlark globals during unit evaluation, or passed explicitly? Globals are
  convenient but reduce hermeticity.
- **REPL / interactive evaluation:** Should `yoe` provide a Starlark REPL for
  debugging unit evaluation? Bazel has `bazel query`; a similar introspection
  tool would help users understand how their units resolve.
