# Yoe-NG build container (Tier 0)
#
# Tools-only container for build execution. The yoe CLI runs on the host
# and invokes this container only for build commands that need the
# toolchain (gcc, bwrap, mkfs, etc.). No yoe binary inside.
#
# ALPINE VERSION COUPLING — read before bumping the FROM line below.
#
# This container's Alpine release MUST match _ALPINE_RELEASE in
# @units-alpine//classes/alpine_pkg.star. Target packages from that module
# (musl, openssl, sqlite, …) are ABI- and signing-key-coupled to the
# Alpine release they were built against. Bumping one without the other
# yields a toolchain whose libc/headers don't match the target image's
# libc — a subtle ABI mismatch that compiles fine, links fine, then
# segfaults on first run.
#
# When bumping: update the FROM tag here, _ALPINE_RELEASE in
# alpine_pkg.star, and every alpine_pkg unit's version + sha256 in the
# same commit, then rebuild the container so its baked apk-tools keyring
# matches.

FROM alpine:3.21

# Common packages (all architectures)
RUN apk add --no-cache \
    bubblewrap \
    apk-tools \
    git \
    tar \
    gzip \
    bzip2 \
    xz \
    patch \
    coreutils \
    bash \
    make \
    gcc \
    g++ \
    musl-dev \
    linux-headers \
    sfdisk \
    e2fsprogs \
    dosfstools \
    mtools \
    losetup \
    mount \
    autoconf \
    automake \
    libtool \
    perl \
    bc \
    flex \
    bison \
    elfutils-dev \
    openssl-dev \
    diffutils \
    findutils \
    sed \
    cmake

# Per-language runtimes (python3, nodejs/npm, bun) are NOT installed here.
# The python_venv and nodejs_app / bun_app classes add their runtimes to
# unit.deps so the apks land in the per-unit sysroot at build time —
# matching CLAUDE.md's "no installing packages in the container" rule and
# letting each project pin its own runtime version.

# x86-only packages (bootloader, firmware) — skip on other arches
RUN if [ "$(uname -m)" = "x86_64" ]; then \
        apk add --no-cache syslinux ovmf; \
    fi

# Create /build for bwrap bind mounts
RUN mkdir -p /build/src /build/destdir

# Create loop device nodes (needed for losetup in --privileged mode)
RUN for i in $(seq 0 7); do mknod -m 0660 /dev/loop$i b 7 $i 2>/dev/null || true; done

WORKDIR /project
