# AI-First Tooling for `[yoe]`

`[yoe]` is designed as an **AI-first build system**. While every operation has a
CLI equivalent, the primary interface for many workflows is a conversation with
an AI assistant that understands the build system deeply. This document defines
the skills (AI-driven workflows) that ship with `[yoe]`.

## Why AI-First

Embedded Linux development has a steep learning curve — not because the concepts
are hard, but because there are many concepts and they interact in non-obvious
ways. An AI assistant that understands units, dependencies, machine definitions,
build isolation, and packaging can:

- **Lower the barrier to entry.** A developer can describe what they want in
  natural language and get working units, machine definitions, and image
  configurations.
- **Reduce debugging time.** Build failures in embedded systems often involve
  subtle interactions between toolchain flags, dependency ordering, and
  cross-module overrides. An AI that can read the full dependency graph and
  build logs can diagnose issues faster than manual investigation.
- **Automate routine maintenance.** Version bumps, security patches, license
  audits, and dependency updates are tedious but critical. AI skills can
  automate these with human review.
- **Make the build system self-documenting.** Instead of reading docs, ask the
  assistant "how does openssh get into my image?" and get a traced answer
  through the actual dependency graph.

## Skill Categories

### Unit Development

#### `/new-unit`

Create a new unit from a description or upstream URL. The AI determines the
build system (autotools, cmake, meson, etc.), fetches the source to inspect it,
identifies dependencies, and generates a complete `.star` file.

```
/new-unit https://github.com/example/myapp
/new-unit "I need an MQTT broker for IoT devices"
/new-unit "add libcurl with HTTP/2 support"
```

#### `/update-unit <name>`

Bump a unit to the latest upstream version. Checks for new releases, updates the
version and sha256, runs a test build, and reports any patch conflicts or
dependency changes.

```
/update-unit openssl
/update-unit --all --dry-run
```

#### `/audit-unit <name>`

Review a unit for common issues: missing runtime dependencies, incorrect
license, unnecessary build dependencies, suboptimal configure flags, missing
sub-package splits.

```
/audit-unit openssh
```

### Image & Machine Configuration

#### `/new-machine`

Generate a machine definition from a board name or SoC. Looks up kernel
defconfig, device trees, bootloader configuration, and QEMU settings (if
applicable).

```
/new-machine beagleplay
/new-machine "Raspberry Pi 5"
/new-machine "custom board with i.MX8M Plus"
```

#### `/new-image`

Design an image unit interactively. Asks about the use case (gateway, HMI,
headless sensor, development), suggests appropriate packages, configures
services, and generates the `.star` file.

```
/new-image "industrial gateway with MQTT and OPC-UA"
/new-image "minimal headless sensor node"
```

#### `/image-size`

Analyze an image unit and estimate the installed size. Break down by package,
identify the largest contributors, and suggest ways to reduce size (remove debug
packages, switch to smaller alternatives, strip unnecessary features).

```
/image-size base-image
/image-size dev-image --compare base-image
```

### Dependency Analysis

#### `/why <package>`

Trace why a package is included in an image. Shows the full dependency chain
from image unit to the specific package, including which packages pull it in as
a runtime dependency.

```
/why libssl
/why dbus --image dev-image
```

#### `/what-if`

Simulate the impact of a change without building. "What if I remove
networkmanager from the image?" "What if I update glibc to 2.40?"

```
/what-if remove networkmanager from base-image
/what-if update glibc to 2.40
/what-if add python3 to dev-image
```

### Build Debugging

#### `/diagnose`

Analyze a build failure. Reads the build log, identifies the root cause (missing
dependency, configure flag issue, patch conflict, toolchain mismatch), and
suggests a fix.

```
/diagnose openssh
/diagnose  # diagnose the most recent failure
```

#### `/build-log <unit>`

Summarize a build log — highlight warnings, errors, and anything unusual. Filter
out noise (compiler progress, make output) and surface what matters.

```
/build-log linux
/build-log openssl --warnings-only
```

### Security & Maintenance

#### `/cve-check`

Scan units against known CVEs. Reports which packages have outstanding
vulnerabilities, their severity, and whether newer upstream versions fix them.

```
/cve-check
/cve-check openssl
/cve-check --image base-image
```

#### `/license-audit`

Audit all packages in an image for license compliance. Flag incompatible license
combinations, missing license declarations, and packages that need special
handling (GPL with linking exceptions, etc.).

```
/license-audit base-image
/license-audit --format spdx
```

#### `/security-review`

Review an image configuration for security issues: services running as root,
unnecessary packages, missing hardening flags (ASLR, stack protector, fortify),
world-readable sensitive files, default passwords.

```
/security-review base-image
```

### Module Management

#### `/new-module`

Scaffold a new module with MODULE.star, directory structure, and example units.

```
/new-module vendor-bsp "BSP module for our custom board"
/new-module product "Product-specific units and images"
```

#### `/module-diff`

Compare two versions of a module. Show what units changed, what versions bumped,
what new units were added, and what was removed.

```
/module-diff @module-core v1.0.0 v1.1.0
```

### Development Environment

`[yoe]` does not ship a separate SDK — `yoe` itself is the dev environment. See
[Development Environments](dev-env.md) for the full model.

#### `/dev-setup`

Guide a developer through getting `yoe` + Docker installed and their editor
configured for Starlark (syntax highlighting, language server, formatters).
Verify the toolchain works by building a small unit end to end.

```
/dev-setup
/dev-setup --for rust  # also install Rust-native tooling on the workstation
```

#### `/devshell <unit>`

Wrapper over `yoe shell` — drops into the unit's build sandbox with the same env
vars, container, and mounted sysroot that `yoe build` uses. Useful for debugging
configure issues, probing deps, or testing build commands manually.

```
/devshell openssh
/devshell linux --machine beaglebone-black
```

### Documentation & Learning

#### `/explain <concept>`

Explain a `[yoe]` concept in context. Not just documentation — the AI reads the
project's actual configuration and explains how the concept applies to this
specific project.

```
/explain "how does caching work for my project"
/explain "what happens when I run yoe build base-image"
/explain "how do modules compose in my project"
```

#### `/diff-from-yocto`

For developers coming from Yocto, explain how a Yocto concept maps to `[yoe]`.
References the actual Yocto documentation and provides side-by-side comparisons.

```
/diff-from-yocto bbappend
/diff-from-yocto "MACHINE_FEATURES"
/diff-from-yocto sstate-cache
```

## Implementation Notes

Skills are implemented as Claude Code plugins that ship with the `yoe` tool.
Each skill:

- Has access to the full project state via `yoe desc`, `yoe refs`, `yoe graph`,
  and direct Starlark file reading
- Can invoke `yoe` CLI commands to gather information (build logs, dependency
  graphs, cache status)
- Can create and modify `.star` files with the user's approval
- Runs in the context of the current project directory

Skills that modify files (like `/new-unit` or `/update-unit`) always show the
proposed changes and ask for confirmation before writing. Skills that only read
and analyze (like `/why` or `/diagnose`) run without confirmation.
