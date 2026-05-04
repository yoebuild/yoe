# Build Dependencies and Caching

Traditional embedded build systems maintain a sharp boundary between "building
the OS" and "developing applications." The OS team produces an SDK — a frozen
snapshot of the sysroot, toolchain, and headers — and hands it to application
developers. From that point on, the two worlds drift: the SDK ages, libraries
diverge, and "it works on my machine" becomes "it works with my SDK version."

`[yoe]` eliminates this boundary by recognizing that there are distinct kinds of
build dependencies, and they should be managed differently:

- **Host tools** (compilers, build utilities, code generators) — these come from
  Docker containers. Every unit can specify its own container, so one team's
  toolchain requirements don't constrain another. A kernel unit can use a
  minimal C toolchain container. A Go application can use the official
  `golang:1.23` image. A Rust service can pin a specific Rust nightly.
- **Library dependencies** (headers, shared libraries your code links against) —
  these come from a shared sysroot populated by apk packages. Each unit produces
  an apk package when it builds; that package is either built locally or pulled
  from a cache (team-level or global). Before a unit builds, its declared
  dependencies are installed from these packages into the sysroot — the same way
  `apt install libssl-dev` populates `/usr/include` and `/usr/lib` on a Debian
  system. Most developers never build OpenSSL themselves; they pull the cached
  package and get the headers and libraries they need in seconds.
- **Language-native dependencies** (Go modules, npm packages, Cargo crates, pip
  packages) — these are managed by the language's own package manager, not the
  sysroot. A Go unit runs `go build` and Go fetches its own modules. A Node unit
  runs `npm install`. Cargo handles Rust crates. These ecosystems already solve
  dependency resolution, caching, and reproducibility — `[yoe]` doesn't
  reimplement any of that. The container provides the language runtime (Go
  compiler, Node, rustc), and the language's package manager handles the rest.
  When a language unit _also_ needs a C library (e.g., a Rust crate linking
  against libssl via cgo or FFI), that C library comes from the sysroot as
  usual.

**Caching is symmetric at the unit level.** Every unit — regardless of language
— produces an apk package that is cached and shared across developers, CI, and
build machines. Most people never rebuild a unit; they pull the cached apk.

The difference shows up when you _do_ rebuild: a C unit finds its dependencies
already in the sysroot (from other units' cached apks), while a Rust unit has
Cargo recompile its crate dependencies using its local cache. This is fine — the
person rebuilding a Rust unit is the developer actively working on it, and their
local Cargo cache handles repeat builds. Go builds so fast it does not matter.
Some ecosystems go further: PyPI distributes pre-compiled wheels globally, so
`pip install` pulls binaries for most packages without compiling anything.
`[yoe]` doesn't need to replicate what these ecosystems already provide.

**Native builds unlock existing package ecosystems.** This is especially clear
with Python. In traditional cross-compilation systems like Yocto or Buildroot,
PyPI wheels are useless — pip runs on the x86_64 host but the target is ARM, so
pre-compiled `aarch64` wheels can't be installed. Instead, every Python package
needs a custom recipe that cross-compiles C extensions against the target
sysroot, effectively reimplementing pip. In `[yoe]`, pip runs inside a
native-arch container (real ARM64 or QEMU-emulated), so `pip install numpy` just
downloads the `aarch64` wheel from PyPI and unpacks it — no compilation, no
custom recipe. The same advantage applies to any language ecosystem that
distributes pre-built binaries by architecture.

Note, there are risks with safety or mission-critical systems of using packages
from a compromised global package system. We could force building of Python
packages in some cases or verify the binaries via a hash mechanism. This point
is for developers, we should be able to leverage all the conveniences modern
language ecosystems provide.

Containers provide the _tools_ to build. The sysroot provides C/C++ _libraries_
to link against. Language-native package managers handle everything else. For
any given unit, the developer, the system team, and CI all use the _same_
container — that's how you stay in sync. A new developer clones the repo, runs
`yoe build`, and gets working build environments pulled automatically.

Docker containers are already the standard way teams manage development
environments. `[yoe]` leans into this rather than inventing a parallel universe
of SDKs.
