# module-core Module Design

The base metadata module for Yoe-NG. Contains the toolchain, base system,
essential libraries, build classes, and QEMU machine definitions that every
Yoe-NG project depends on.

## Location

`modules/module-core/` in the main yoe-ng repository. Will be extracted to its
own Git repo (`github.com/yoe/module-core`) once stable. During development,
projects reference it as a local module override:

```python
# PROJECT.star
modules = [
    module("github.com/yoe/module-core", local = "../modules/module-core"),
]
```

## Directory Structure

```
modules/module-core/
├── MODULE.star
├── classes/
│   ├── autotools.star
│   ├── cmake.star
│   ├── meson.star
│   ├── go.star
│   ├── rust.star
│   ├── python.star
│   ├── node.star
│   ├── image.star
│   ├── sdk.star
│   └── systemd.star
├── machines/
│   ├── qemu-x86_64.star
│   ├── qemu-arm64.star
│   └── qemu-riscv64.star
├── units/
│   ├── toolchain/
│   │   ├── gcc.star
│   │   ├── binutils.star
│   │   ├── glibc.star
│   │   ├── linux-headers.star
│   │   ├── make.star
│   │   ├── pkg-config.star
│   │   ├── autoconf.star
│   │   ├── automake.star
│   │   ├── libtool.star
│   │   ├── cmake.star
│   │   ├── meson.star
│   │   └── ninja.star
│   ├── base/
│   │   ├── busybox.star
│   │   ├── systemd.star
│   │   ├── util-linux.star
│   │   ├── kmod.star
│   │   ├── apk-tools.star
│   │   ├── bubblewrap.star
│   │   └── linux.star
│   ├── bootloaders/
│   │   ├── u-boot.star
│   │   ├── ovmf.star
│   │   └── opensbi.star
│   ├── libs/
│   │   ├── zlib.star
│   │   ├── openssl.star
│   │   ├── libffi.star
│   │   ├── ncurses.star
│   │   ├── readline.star
│   │   ├── expat.star
│   │   ├── gmp.star
│   │   ├── mpfr.star
│   │   ├── xz.star
│   │   ├── zstd.star
│   │   ├── bzip2.star
│   │   └── dbus.star
│   ├── net/
│   │   ├── openssh.star
│   │   ├── curl.star
│   │   ├── networkmanager.star
│   │   ├── iproute2.star
│   │   └── ca-certificates.star
│   └── debug/
│       ├── gdb.star
│       ├── strace.star
│       ├── tcpdump.star
│       └── vim.star
└── images/
    ├── base-image.star
    └── dev-image.star
```

Units are organized by category in subdirectories. This scales better than a
flat layout as the module grows toward 100+ units.

## MODULE.star

```python
module_info(
    name = "module-core",
    description = "Yoe-NG base module: toolchain, base system, essential libraries, and QEMU machines",
    # No deps — this is the root module. All other modules depend on this one.
)
```

## Architecture: Primitives vs. Classes

The `yoe` Go binary provides low-level **primitives** as Starlark builtins.
These are the atoms that everything else is built from:

**Go builtins (primitives):** `unit()`, `image()`, `machine()`, `project()`,
`module_info()`, `partition()`, `kernel()`, `uboot()`, `qemu_config()`,
`defaults()`, `repository()`, `cache()`, `s3_cache()`, `sources()`, `module()`,
`subunit()`, `auto()`

**Starlark classes (in this module):** `autotools()`, `cmake()`, `meson()`,
`go_binary()`, `rust_binary()`, `python_unit()`, `node_unit()`, `sdk()`,
`systemd_service()`

Classes are ordinary `.star` files that call `unit()` with the right build
steps. Users can read them, override them in their project's `classes/`
directory, or write new ones.

### Class Example: autotools.star

```python
def autotools(name, version, source, sha256 = "", deps = [], runtime_deps = [],
              configure_args = [], make_args = [], make_install_args = [],
              patches = [], services = [], conffiles = [], subpackages = None,
              license = "", description = "", **kwargs):
    """Autotools build pattern: configure / make / make install."""
    build = [
        "./configure --prefix=$PREFIX " + " ".join(configure_args),
        "make -j$NPROC " + " ".join(make_args),
        "make DESTDIR=$DESTDIR install " + " ".join(make_install_args),
    ]
    unit(
        name = name,
        version = version,
        source = source,
        sha256 = sha256,
        deps = deps,
        runtime_deps = runtime_deps,
        patches = patches,
        build = build,
        services = services,
        conffiles = conffiles,
        subpackages = subpackages,
        license = license,
        description = description,
        **kwargs,
    )
```

### Class Example: cmake.star

```python
def cmake(name, version, source, sha256 = "", deps = [], runtime_deps = [],
          cmake_args = [], patches = [], services = [], conffiles = [],
          subpackages = None, license = "", description = "", **kwargs):
    """CMake build pattern."""
    build = [
        "cmake -B build -S . -DCMAKE_INSTALL_PREFIX=$PREFIX "
            + " ".join(["-D" + a for a in cmake_args]),
        "cmake --build build -j$NPROC",
        "DESTDIR=$DESTDIR cmake --install build",
    ]
    unit(
        name = name,
        version = version,
        source = source,
        sha256 = sha256,
        deps = deps,
        runtime_deps = runtime_deps,
        patches = patches,
        build = build,
        services = services,
        conffiles = conffiles,
        subpackages = subpackages,
        license = license,
        description = description,
        **kwargs,
    )
```

### Class Example: go.star

```python
def go_binary(name, version, source, tag = "", sha256 = "",
              go_package = "", deps = [], runtime_deps = [],
              services = [], conffiles = [], environment = {},
              license = "", description = "", **kwargs):
    """Go application build pattern."""
    if not go_package:
        go_package = "./cmd/" + name
    build = [
        "go build -o $DESTDIR$PREFIX/bin/" + name + " " + go_package,
    ]
    unit(
        name = name,
        version = version,
        source = source,
        sha256 = sha256,
        tag = tag,
        deps = deps,
        runtime_deps = runtime_deps,
        build = build,
        services = services,
        conffiles = conffiles,
        environment = environment,
        license = license,
        description = description,
        **kwargs,
    )
```

### Class Example: systemd.star

```python
def systemd_service(name, unit, conffiles = [], wants = [], after = []):
    """Add a systemd service unit to an existing package."""
    # This is a modifier — it doesn't create a package, it adds metadata
    # to an already-registered package. The yoe engine merges this.
    package_extend(
        name = name,
        services = [unit],
        conffiles = conffiles,
    )
```

### Note on image.star and sdk.star

`image()` and `sdk()` are Go primitives because they have fundamentally
different build paths (rootfs assembly vs. source compilation). The
`classes/image.star` and `classes/sdk.star` files in the module are thin
wrappers that re-export the primitive with module-specific defaults (e.g.,
default partition layouts, default base packages). Units can use the primitive
directly or use the class wrapper.

## Unit Conventions

1. **One unit per `.star` file**, named after the package. `zlib.star` produces
   the `zlib` package (plus automatic `-dev`, `-doc`, `-dbg` sub-packages).

2. **Units use module classes via `load()`:**

   ```python
   load("//classes/autotools.star", "autotools")

   autotools(
       name = "zlib",
       version = "1.3.1",
       source = "https://zlib.net/zlib-1.3.1.tar.gz",
       sha256 = "...",
   )
   ```

   Within the module, `//` is relative to the module root. Downstream projects
   loading from this module use `@module-core//classes/autotools.star`.

3. **Default sub-packages** apply automatically (`-dev`, `-doc`, `-dbg`). Units
   only declare `subpackages` for custom splits (e.g., openssh-server vs.
   openssh-client).

4. **Version pinning** — each unit pins an exact upstream version and sha256.
   Version bumps are explicit commits.

5. **Toolchain units** may set `bootstrap = True` to indicate they can be built
   with a foreign toolchain during stage 0/1 bootstrap.

## Unit Examples

### Toolchain: glibc.star

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "glibc",
    version = "2.39",
    source = "https://ftp.gnu.org/gnu/glibc/glibc-2.39.tar.xz",
    sha256 = "...",
    license = "LGPL-2.1",
    description = "GNU C Library",
    deps = ["linux-headers", "binutils"],
    configure_args = [
        "--enable-kernel=5.15",
        "--with-headers=$PREFIX/include",
        "--enable-shared",
        "--disable-profile",
    ],
    subpackages = {
        "dev": auto(),
        "doc": auto(),
        "dbg": auto(),
        "utils": subunit(
            description = "glibc utility programs",
            files = ["/usr/bin/ldd", "/usr/bin/getconf", "/sbin/ldconfig"],
        ),
    },
    bootstrap = True,
)
```

### Base: busybox.star

```python
unit(
    name = "busybox",
    version = "1.36.1",
    source = "https://busybox.net/downloads/busybox-1.36.1.tar.bz2",
    sha256 = "...",
    license = "GPL-2.0",
    description = "Swiss army knife of embedded Linux",
    deps = ["glibc"],
    runtime_deps = ["glibc"],
    build = [
        "make defconfig",
        "make -j$NPROC",
        "make CONFIG_PREFIX=$DESTDIR install",
    ],
    bootstrap = True,
)
```

### Base: linux.star (kernel)

```python
unit(
    name = "linux",
    version = "6.6.30",
    source = "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.6.30.tar.xz",
    sha256 = "...",
    license = "GPL-2.0",
    description = "Linux kernel",
    deps = ["gcc", "binutils", "make"],
    build = [
        # $KERNEL_DEFCONFIG and $KERNEL_DEVICE_TREES come from machine config
        "make $KERNEL_DEFCONFIG",
        "make -j$NPROC",
        "make INSTALL_MOD_PATH=$DESTDIR modules_install",
        "install -D arch/$KARCH/boot/*Image $DESTDIR/boot/",
    ],
    subpackages = {
        "dev": subunit(
            description = "Linux kernel headers for building modules",
            files = ["/usr/src/linux/include/**", "/usr/src/linux/Module.symvers"],
        ),
    },
)
```

### Libs: zlib.star

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "zlib",
    version = "1.3.1",
    source = "https://zlib.net/zlib-1.3.1.tar.gz",
    sha256 = "...",
    license = "Zlib",
    description = "Compression library",
    runtime_deps = ["glibc"],
)
```

### Net: openssh.star

```python
load("//classes/autotools.star", "autotools")

autotools(
    name = "openssh",
    version = "9.6p1",
    source = "https://cdn.openbsd.org/pub/OpenBSD/OpenSSH/portable/openssh-9.6p1.tar.gz",
    sha256 = "...",
    license = "BSD",
    description = "OpenSSH client and server",
    deps = ["zlib", "openssl"],
    runtime_deps = ["zlib", "openssl"],
    configure_args = [
        "--sysconfdir=/etc/ssh",
        "--with-privsep-path=/var/empty/sshd",
    ],
    subpackages = {
        "dev": auto(),
        "doc": auto(),
        "dbg": auto(),
        "server": subunit(
            description = "OpenSSH server",
            files = ["/usr/sbin/sshd", "/etc/ssh/sshd_config"],
            services = ["sshd"],
        ),
        "client": subunit(
            description = "OpenSSH client utilities",
            files = ["/usr/bin/ssh", "/usr/bin/scp", "/usr/bin/sftp",
                     "/usr/bin/ssh-keygen", "/usr/bin/ssh-agent"],
        ),
    },
    conffiles = ["/etc/ssh/sshd_config"],
)
```

## Machine Definitions

### machines/qemu-x86_64.star

```python
machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    description = "QEMU x86_64 virtual machine (KVM)",
    kernel = kernel(
        unit = "linux",
        defconfig = "x86_64_defconfig",
        cmdline = "console=ttyS0 root=/dev/vda2 rw",
    ),
    bootloader = uboot(
        unit = "ovmf",
    ),
    qemu = qemu_config(
        machine = "q35",
        cpu = "host",
        memory = "1G",
        firmware = "ovmf",
        display = "none",
    ),
)
```

### machines/qemu-arm64.star

```python
machine(
    name = "qemu-arm64",
    arch = "arm64",
    description = "QEMU AArch64 virtual machine (KVM)",
    kernel = kernel(
        unit = "linux",
        defconfig = "defconfig",
        cmdline = "console=ttyAMA0 root=/dev/vda2 rw",
    ),
    qemu = qemu_config(
        machine = "virt",
        cpu = "host",
        memory = "1G",
        firmware = "aavmf",
        display = "none",
    ),
)
```

### machines/qemu-riscv64.star

```python
machine(
    name = "qemu-riscv64",
    arch = "riscv64",
    description = "QEMU RISC-V 64-bit virtual machine (KVM)",
    kernel = kernel(
        unit = "linux",
        defconfig = "defconfig",
        cmdline = "console=ttyS0 root=/dev/vda2 rw",
    ),
    bootloader = uboot(
        unit = "opensbi",
    ),
    qemu = qemu_config(
        machine = "virt",
        cpu = "host",
        memory = "1G",
        firmware = "opensbi",
        display = "none",
    ),
)
```

## Image Definitions

### images/base-image.star

```python
load("//classes/image.star", "image")

image(
    name = "base-image",
    version = "1.0.0",
    description = "Minimal bootable Yoe-NG system",
    artifacts = [
        "busybox",
        "systemd",
        "util-linux",
        "apk-tools",
    ],
    hostname = "yoe",
    timezone = "UTC",
    locale = "en_US.UTF-8",
    services = ["systemd-networkd", "systemd-resolved"],
    partitions = [
        partition(label = "boot", type = "vfat", size = "64M",
                  contents = ["*Image", "*.dtb"]),
        partition(label = "rootfs", type = "ext4", size = "fill", root = True),
    ],
)
```

### images/dev-image.star

```python
load("//classes/image.star", "image")

image(
    name = "dev-image",
    version = "1.0.0",
    description = "Development image with networking, SSH, and debug tools",
    artifacts = [
        # Base
        "busybox",
        "systemd",
        "util-linux",
        "apk-tools",
        # Networking
        "openssh-server",
        "networkmanager",
        "curl",
        "ca-certificates",
        "iproute2",
        # Debug
        "gdb",
        "strace",
        "tcpdump",
        "vim",
    ],
    hostname = "yoe-dev",
    timezone = "UTC",
    locale = "en_US.UTF-8",
    services = ["sshd", "NetworkManager"],
    partitions = [
        partition(label = "boot", type = "vfat", size = "64M",
                  contents = ["*Image", "*.dtb"]),
        partition(label = "rootfs", type = "ext4", size = "fill", root = True),
    ],
)
```

## Phasing

### Phase 1: Module skeleton + classes + toolchain

- `MODULE.star`
- All 10 class files (`autotools`, `cmake`, `meson`, `go`, `rust`, `python`,
  `node`, `image`, `sdk`, `systemd`)
- Toolchain units: `gcc`, `binutils`, `glibc`, `linux-headers`
- Build tool units: `make`, `pkg-config`, `autoconf`, `automake`, `libtool`,
  `cmake` (the package), `meson`, `ninja`

**Deliverable:** Can build C/C++ packages from source inside a Yoe-NG build
root.

### Phase 2: Base system + QEMU machines

- Base units: `busybox`, `systemd`, `util-linux`, `kmod`, `apk-tools`,
  `bubblewrap`
- Kernel unit: `linux`
- Bootloader units: `u-boot`, `ovmf`, `opensbi`
- Machine definitions: `qemu-x86_64`, `qemu-arm64`, `qemu-riscv64`
- Image units: `base-image`, `dev-image`

**Deliverable:** `yoe build base-image --machine qemu-x86_64` produces a
bootable image.

### Phase 3: Essential libs + networking

- Compression: `zlib`, `xz`, `zstd`, `bzip2`
- Crypto/TLS: `openssl`, `ca-certificates`
- Core libs: `libffi`, `ncurses`, `readline`, `expat`, `gmp`, `mpfr`
- Networking: `openssh`, `curl`, `networkmanager`, `dbus`, `iproute2`
- Debug tools: `gdb`, `strace`, `tcpdump`, `vim`

**Deliverable:** A practical embedded image with SSH, network management, and
TLS.

### Phase 4+: Expanding coverage

Real hardware machines (BeagleBone, Raspberry Pi, VisionFive 2), language
runtimes (Python, Node.js), multimedia, databases, firmware tools, etc. Driven
by actual project needs.

## Engine Changes Required

Moving classes from Go builtins to Starlark files requires these changes to the
`yoe` engine:

1. **Remove Go builtins for classes.** `autotools()`, `cmake()`, `go_binary()`
   are no longer registered as predeclared names. They exist only as Starlark
   functions in module `.star` files.

2. **Implement `load()` resolution.** The Starlark thread's load handler must
   resolve:
   - `//path` — relative to the current file's module root (or project root if
     not in a module)
   - `@module-name//path` — relative to the named module's root

3. **Evaluate modules before project units.** The loader must fetch/cache
   modules (per `yoe module sync`), then make their files available for `load()`
   resolution before evaluating project units.

4. **Add `package_extend()` primitive.** Needed for `systemd_service()` and
   similar modifier classes that add metadata to an existing package without
   creating a new one.

5. **Add `bootstrap` flag to `unit()`.** Marks units that participate in stage
   0/1 bootstrap and can be built with a foreign toolchain.

6. **Recursive unit discovery.** The current loader globs `units/*.star`. With
   categorized subdirectories, it needs to glob `units/**/*.star`.
