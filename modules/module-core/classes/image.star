# Debian's Essential + Priority:required userland. mmdebstrap
# --variant=custom installs nothing implicitly, but every Debian
# maintainer script assumes this base is present: libc6's own preinst
# calls sed; countless postinsts call grep, awk (mawk), find
# (findutils), and gzip; deb-systemd-helper enables services
# (init-system-helpers). Seeding it into every Debian image's closure
# means the rootfs always has the toolset apt and dpkg expect, instead
# of failing deep in a maintainer script with "<tool>: not found" (exit
# 127). Closure resolution dedups any names an image also lists.
#
# Terminfo and clear/tput are deliberately NOT seeded (no ncurses-base /
# ncurses-bin): module-core's source-built ncurses already ships those
# files, and pulling Debian's split packages alongside it makes both
# own /usr/bin/clear, which dpkg refuses to unpack.
_DEBIAN_ESSENTIAL = [
    "base-files",
    "base-passwd",
    "bash",
    "bsdutils",
    "coreutils",
    "dash",
    "debianutils",
    "diffutils",
    "dpkg",
    "findutils",
    "grep",
    "gzip",
    "hostname",
    "init-system-helpers",
    "libc-bin",
    "login",
    "mawk",
    "perl-base",
    "sed",
    "sysvinit-utils",
    "tar",
    "util-linux",
]

# Distros that use the apt/dpkg/glibc backend (mmdebstrap rootfs
# assembly, .deb packaging). Ubuntu rides Debian's machinery, so both
# resolve the same Essential base and assembly path; only the feed,
# suite, and mirror differ. Mirrors yoestar.IsAptFamily on the Go side.
_APT_DISTROS = ["debian", "ubuntu"]

def _is_apt_distro(d):
    return d in _APT_DISTROS

def image(name, artifacts=[], distro_artifacts={}, hostname=None, timezone="", locale="",
          partitions=[], scope="machine",
          container="toolchain", container_arch="target", deps=[],
          version=None, distro=None, **kwargs):
    """Create a bootable disk image from packages.

    `version` defaults to ctx.project_version (from PROJECT.star) so the TUI's
    VERSION column shows what build the image represents and `/etc/os-release`
    on the device matches.

    `hostname` defaults to ctx.machine so a fleet of identically-imaged devices
    is distinguishable on the LAN by board (raspberrypi4.local,
    qemu-x86_64.local, etc.). Pass an explicit string to override (e.g. a kiosk
    image that wants its own brand).

    `distro` selects the distro this image targets. When unset, the project's
    `defaults.distro` (overridable per-developer via `local.star`'s
    `default_distro_override`) supplies the fallback. With nothing set in
    either, image evaluation errors — every image must resolve to a distro.

    `distro_artifacts` is a `{distro: [names]}` map letting one image definition
    target multiple distros whose package names differ (musl/openrc/apk vs
    systemd/glibc/dpkg). Only the branch matching the effective distro is
    consulted; the others are inert lists — never resolved, never forcing their
    feed module to load — so a shared image carrying a `"debian"` branch builds
    fine in an Alpine-only project. There is no closed-distro key check: the
    distro set is open, and a typo'd key for a distro you never build is simply
    never reached.
    """
    if version == None:
        version = ctx.project_version
    if hostname == None:
        hostname = ctx.machine

    # Effective-distro cascade: image's own distro -> local override ->
    # project default -> error. Matches Project.EffectiveDistroForImage
    # on the Go side. resolve_closure() requires the effective distro so
    # the R21a per-unit visibility filter can drop tagged units that
    # don't match this image's distro.
    effective_distro = distro
    if not effective_distro:
        effective_distro = ctx.default_distro_override
    if not effective_distro:
        effective_distro = ctx.default_distro
    if not effective_distro:
        fail("image %s: no distro set and project has no defaults.distro" % name)

    # Merge machine packages. The machine config's `packages` list
    # (e.g. ["syslinux"] on qemu-x86_64) names module-core source units
    # that build against musl/Alpine; pulling them into a Debian image's
    # closure contaminates the per-unit sysroot with musl-linked
    # binaries. Skip the merge on non-alpine distros — the Debian image
    # bootloader requirements come in via apt's transitive closure
    # instead, and machine-specific board firmware should be declared
    # explicitly per-image.
    # distro_artifacts: merge only the branch for this image's effective distro.
    # Non-selected branches are never read, so they cost nothing and don't force
    # their distro's feed module to be present.
    all_artifacts = list(artifacts) + list(distro_artifacts.get(effective_distro, []))
    if effective_distro == "alpine":
        all_artifacts = all_artifacts + list(ctx.machine_config.packages)
    elif _is_apt_distro(effective_distro):
        all_artifacts = all_artifacts + _DEBIAN_ESSENTIAL

    # Resolve the machine kernel for this image's distro. ctx.provides is built
    # once from the project default machine and is distro-blind, so a per-distro
    # kernel (machine_config.kernel.distro_unit) can only be picked here, where
    # the effective distro is known. Single-unit machines register
    # provides["linux"] globally and need no override; per-distro machines carry
    # no global entry, so image() substitutes the unit for effective_distro.
    kernel_provides = None
    kernel_unit = None
    mc = getattr(ctx, "machine_config", None)
    if mc != None:
        k = getattr(mc, "kernel", None)
        if k != None:
            kernel_provides = getattr(k, "provides", None)
            du = getattr(k, "distro_unit", None)
            if du:
                if effective_distro not in du:
                    fail("image %s: machine kernel has no entry for distro %r" % (name, effective_distro))
                kernel_unit = du[effective_distro]

    # Resolve provides (e.g., "linux" → "linux-rpi4")
    explicit = []
    for a in all_artifacts:
        if kernel_unit != None and a == kernel_provides:
            explicit.append(kernel_unit)
        else:
            r = ctx.provides.get(a, None)
            explicit.append(r if r != None else a)

    # Resolve transitive runtime dependencies for the rootfs / build path.
    # `explicit` (above) is preserved separately for UX surfaces like the
    # TUI tree, where seeing the user's pre-closure list rather than the
    # flattened set is much less misleading.
    #
    # resolve_closure() is a Go-side builtin that walks the runtime-dep
    # graph and materializes synthetic units (alpine_feed entries) on
    # demand, so the working set stays bounded by closure size rather
    # than catalog size.
    resolved = resolve_closure(explicit, distro = effective_distro)

    # Use machine partitions if image doesn't specify its own
    all_partitions = partitions if partitions else list(ctx.machine_config.partitions)

    # Merge class deps with user deps
    all_deps = list(deps)
    if container and container not in all_deps:
        all_deps.append(container)

    # Distro-specific rootfs assembly. Alpine images run apk add to
    # populate the rootfs; Debian images extract each .deb's data.tar
    # then run dpkg --configure -a in the glibc toolchain container.
    if _is_apt_distro(effective_distro):
        rootfs_fn = lambda: _assemble_debian_rootfs(resolved, hostname, timezone, locale)
        disk_fn = lambda: _create_disk_image_debian(name, all_partitions)
    else:
        rootfs_fn = lambda: _assemble_rootfs(resolved, hostname, timezone, locale)
        disk_fn = lambda: _create_disk_image(name, all_partitions)

    unit(
        name = name,
        version = version,
        scope = scope,
        unit_class = "image",
        distro = effective_distro,
        artifacts = resolved,
        artifacts_explicit = explicit,
        partitions = all_partitions,
        container = container,
        container_arch = container_arch,
        sandbox = True,
        shell = "bash",
        deps = all_deps,
        tasks = [
            task("rootfs", fn=rootfs_fn),
            task("disk", fn=disk_fn),
        ],
        **kwargs,
    )

def _assemble_rootfs(packages, hostname, timezone, locale):
    """Install packages into the rootfs using apk-tools.

    apk handles dependency resolution from APKINDEX, enforces file-conflict
    detection, and populates /lib/apk/db/installed automatically. The
    `packages` list still includes transitive runtime deps from
    `resolve_closure()` so the build-time DAG schedules everything,
    but apk will re-resolve install order itself.

    Flags:
      --root            — destination rootfs
      --initdb          — create /lib/apk/db on a fresh rootfs
      --no-network      — never reach the public Alpine mirrors
      --no-cache        — keep /etc/apk/cache out of the rootfs
      -X $REPO          — yoe's local Alpine-layout repo

    Install scripts run at assembly time. apk's chroot-then-exec model
    needs /bin/sh to exist inside the rootfs by the time a script wants
    it; busybox's .post-install (`#!/bin/busybox sh`) creates the applet
    symlinks (/bin/sh, /sbin/init, …) before any later package's
    `#!/bin/sh` script runs, so dependency ordering bootstraps the
    chicken-and-egg the same way `apk add --initdb` does on a fresh
    Alpine install. Image assembly already runs in a `--platform linux/<arch>`
    container matching the target, so chrooted execs are native.

    The project's signing public key is pre-staged into the rootfs at
    /etc/apk/keys/<keyname>.rsa.pub before `apk add` runs — apk reads
    `<root>/etc/apk/keys/` to validate signatures, and `--keys-dir`
    interacts oddly with `--root` in apk 2.x. base-files installs the
    same file via its data tar, so the in-rootfs key after install is
    identical to the pre-staged one.

    Intentional file shadows (busybox stubs vs the real util-linux/iproute2/
    procps-ng/etc.) are declared per-unit via `replaces = [...]`, which apk
    honors at install time. Without those annotations, a file conflict here
    is a real bug — let apk fail the build instead of papering over it with
    --force-overwrite.
    """
    run("mkdir -p $DESTDIR/rootfs/etc/apk/keys")
    run("cp $YOE_KEYS_DIR/$YOE_KEY_NAME $DESTDIR/rootfs/etc/apk/keys/")

    # privileged = True runs directly in the container (no bwrap) as root,
    # so apk can `chroot $DESTDIR/rootfs` to execute install scripts.
    # Under bwrap, chroot fails with "Operation not permitted" because the
    # default bwrap profile drops CAP_SYS_CHROOT.
    pkg_args = " ".join(packages)
    run("apk add " +
        "--root $DESTDIR/rootfs " +
        "--initdb " +
        "--no-network " +
        "--no-cache " +
        "-X $REPO " +
        pkg_args,
        privileged = True)

    # The kernel's `make modules_install DEPMOD=true` skipped depmod (the
    # toolchain container has no depmod), so the rootfs ships .ko files
    # without a `modules.dep` index for modprobe to read. kmod inside the
    # rootfs supplies depmod; chroot in to generate the index for every
    # installed kernel version.
    run("""
for kvdir in $DESTDIR/rootfs/lib/modules/*/; do
    [ -d "$kvdir" ] || continue
    chroot $DESTDIR/rootfs depmod -a $(basename $kvdir)
done
""", privileged = True)

    # apk add applied per-file ownership directly from each apk's tar
    # headers — e.g. /var/lib/navidrome:navidrome:navidrome, /etc/shadow
    # root:root with mode 600, setuid bits intact — and we deliberately do
    # not touch it again. `dir_size_mb` and any other host-side walks must
    # tolerate dirs they cannot enter (see fnDirSizeMB, which fail-softs
    # on EACCES); mkfs.ext4 -d runs root in the container and remains the
    # authoritative reader of the assembled tree. See docs/security.md
    # and docs/comparisons.md for the design discussion.

    if hostname:
        run("mkdir -p $DESTDIR/rootfs/etc")
        run("echo %s > $DESTDIR/rootfs/etc/hostname" % hostname)

    if timezone:
        run("mkdir -p $DESTDIR/rootfs/etc")
        run("echo %s > $DESTDIR/rootfs/etc/timezone" % timezone)
    # Note: init.d service symlinks are baked into each apk's data tar at
    # package-time (see internal/artifact/apk.go's materializeServiceSymlinks),
    # so apk add — image-time or on-target — produces the same rootfs. yoe
    # does not patch the rootfs after install.

def _assemble_debian_rootfs(packages, hostname, timezone, locale):
    """Install packages into the rootfs with a single mmdebstrap run.

    mmdebstrap drives apt + dpkg in one pass: it resolves the package
    list against the project's local Debian repo ($REPO, with the
    Packages/Release index the repo emitter writes), unpacks every .deb,
    and runs maintainer scripts so the rootfs boots into a fully
    configured dpkg state — populated /var/lib/dpkg/status, postinst-
    created users and groups, update-alternatives, and systemd/OpenRC
    service-preset enablement. This mirrors how the Alpine path hands its
    resolved closure to a single `apk add`, and replaces the previous
    per-package extract loop that started a fresh container for every
    .deb (the slow part) and never populated the dpkg database, so
    maintainer scripts never actually ran.

    --variant=custom installs exactly the resolved closure plus the hard
    dependencies apt pulls from the same index — no implicit Essential /
    Priority base — keeping the image to the explicit closure, and
    Recommends are disabled for the same reason. mmdebstrap installs its
    own policy-rc.d (and diverts start-stop-daemon) for the duration, so
    no daemon starts while configuring. The local repo is consumed with
    [trusted=yes]: it is a build-time file: mirror, so signature
    verification is skipped the same way image assembly trusts the local
    Alpine repo. The repo is referenced with the copy: method (not file:)
    so apt can reach the .debs from inside mmdebstrap's mount namespace —
    file: paths aren't visible there, and copy: stages each .deb into the
    target's apt cache.

    Runs privileged (root in a --privileged container) so mmdebstrap's
    root mode can chroot and mount /proc, /sys, /dev/pts in the target
    while configuring. The suite comes from $SUITE, which the build sets
    from the project's debian_feed — the same source the repo emitter
    stamps into the index, so the mmdebstrap target and the index it
    reads can never drift.
    """
    pkg_list = ",".join(packages)

    # Set hostname/timezone in the same container so the whole assembly
    # is one container start, not N.
    extra = ""
    if hostname:
        extra += "\necho %s > $DESTDIR/rootfs/etc/hostname" % hostname
    if timezone:
        extra += "\necho %s > $DESTDIR/rootfs/etc/timezone" % timezone

    run("""
set -eu
case "$ARCH" in
    x86_64) debarch=amd64 ;;
    arm64)  debarch=arm64 ;;
    *) echo "debian rootfs: unsupported arch $ARCH" >&2; exit 1 ;;
esac

mkdir -p $DESTDIR/rootfs

# Establish merged-usr before any package is extracted. mmdebstrap
# --variant=custom skips the usr-merge that the normal variants set up,
# leaving /bin, /sbin, /lib as real directories. The usrmerge package
# (pulled by init-system-helpers' "usrmerge | usr-is-merged" dep) then
# tries a fragile post-hoc conversion that ldd-walks every binary and
# dies in the chroot. The --setup-hook runs against the empty target
# ($1) before extraction, so turning the alias dirs into symlinks here
# means packages unpack straight into /usr and usrmerge's postinst sees
# an already-merged system and no-ops. lib64 is created on every arch:
# amd64 needs it for the ELF loader, and an unused symlink is inert on
# arm64.
#
# The --extract-hook pre-stages the awk alternative. mmdebstrap's single
# `dpkg --install --force-depends` batch does not configure mawk before
# the packages whose maintainer scripts call awk (ucf, run from
# openssh-server's postinst), so awk is missing on the first pass and
# the postinst exits 127. The hook runs after extraction (mawk's binary
# present) but before configure, pointing /usr/bin/awk at mawk through
# the standard /etc/alternatives link; mawk's own update-alternatives
# call then adopts the identical link idempotently.
mmdebstrap --mode=root --variant=custom --setup-hook='for d in bin sbin lib lib64; do mkdir -p "$1/usr/$d"; ln -sf "usr/$d" "$1/$d"; done' --extract-hook='mkdir -p "$1/etc/alternatives"; ln -sf /usr/bin/mawk "$1/etc/alternatives/awk"; ln -sf /etc/alternatives/awk "$1/usr/bin/awk"' --architectures="$debarch" --include="%s" --aptopt='APT::Get::Install-Recommends "false"' --aptopt='Acquire::Check-Valid-Until "false"' "$SUITE" "$DESTDIR/rootfs" "deb [trusted=yes] copy:$REPO $SUITE main"

# Fail loudly on a half-configured rootfs. mmdebstrap runs dpkg with
# --force-depends during the essential bootstrap, so a broken dependency
# graph (e.g. a Packages index missing Pre-Depends edges) degrades to
# warnings and a subtly broken image rather than a hard error. Gate on
# it: every package must reach "ii" (installed/installed). chroot is
# native here — foreign-arch builds run in an arch-matched container.
broken=$(chroot $DESTDIR/rootfs dpkg-query -W -f='${db:Status-Abbrev} ${binary:Package}\n' | grep -v '^ii ' || true)
if [ -n "$broken" ]; then
    echo "debian rootfs: packages not fully installed/configured:" >&2
    echo "$broken" >&2
    exit 1
fi

# Regenerate the initramfs after the whole userland is configured.
# mmdebstrap runs dpkg as a single --configure pass, so linux-image's
# postinst fires update-initramfs while coreutils/libattr are still mid-
# configure. dracut-install copies kernel modules with `cp --preserve=
# xattr`, which needs libattr in place; run too early it copies nothing
# and the image ships an initramfs with no virtio/ext4 drivers, so the
# kernel can't find its root device and hangs in the local-block loop.
# drivers actually land in it. Only rebuild a kernel that already has a
# real initramfs (Debian's linux-image postinst leaves /boot/initrd.img-$kv
# behind). Ubuntu only *Recommends* an initramfs generator, so an Ubuntu
# image carries no update-initramfs and no real initrd — it boots through
# the kernel's built-in virtio/ext4 drivers instead (the launcher omits
# -initrd when no real file is present), so there is nothing to regenerate.
for kvdir in $DESTDIR/rootfs/lib/modules/*/; do
    [ -d "$kvdir" ] || continue
    kv=$(basename "$kvdir")
    [ -f "$DESTDIR/rootfs/boot/initrd.img-$kv" ] || continue
    chroot $DESTDIR/rootfs update-initramfs -u -k "$kv"
done

# Make the kernel and initramfs world-readable. Ubuntu ships /boot/vmlinuz-*
# and /boot/initrd.img-* mode 0600 (root-only) as a hardening measure; Debian
# and Alpine ship them 0644. On qemu-arm64 (and any firmware-less direct-kernel
# machine) the boot test runs host QEMU as the invoking user and hands the
# kernel/initrd straight to -kernel/-initrd off the host rootfs, so a 0600
# kernel makes QEMU fail with "could not load kernel". The bootloader on real
# hardware and x86 disk boot read /boot as root and don't care, so relaxing to
# 0644 (matching Debian/Alpine) only enables the direct-kernel path.
for f in $DESTDIR/rootfs/boot/vmlinuz-* $DESTDIR/rootfs/boot/initrd.img-*; do
    [ -e "$f" ] && chmod a+r "$f"
done

mkdir -p $DESTDIR/rootfs/etc%s
""" % (pkg_list, extra), privileged = True)

def _create_disk_image_debian(name, partitions):
    """Disk image creator for Debian rootfs.

    Mirrors _create_disk_image's structure (sparse image, sfdisk MBR,
    per-partition mkfs, dd into the disk image) but uses the syslinux
    files shipped in the glibc toolchain container at
    /usr/lib/SYSLINUX/mbr.bin instead of the Alpine-style
    /usr/share/syslinux/mbr.bin path. The Debian extlinux binary
    (also in the glibc toolchain container) writes /boot/extlinux/ldlinux.sys
    onto the root partition.

    Requires a kernel and /boot/extlinux/extlinux.conf in the rootfs.
    The kernel needs to be in the artifact list (e.g. linux-image-amd64
    on x86_64 bookworm); _write_debian_extlinux_conf generates the
    .conf below.
    """
    if not partitions:
        return

    rootfs_mb = dir_size_mb("rootfs")
    total_mb = 1
    for p in partitions:
        total_mb += _parse_size_mb(p.size)

    img = "$DESTDIR/%s.img" % name
    run("dd if=/dev/zero of=%s bs=1M count=0 seek=%d" % (img, total_mb))

    sfdisk_lines = "label: dos\\n"
    for i, p in enumerate(partitions):
        size_mb = _parse_size_mb(p.size)
        ptype = "c" if p.type == "vfat" else "83"
        size_spec = "size=%dMiB, " % size_mb if i < len(partitions) - 1 else ""
        bootable = ", bootable" if i == 0 else ""
        sfdisk_lines += "%stype=%s%s\\n" % (size_spec, ptype, bootable)
    run("printf '%s' | sfdisk %s" % (sfdisk_lines, img))

    # Generate extlinux.conf in the rootfs before mkfs.ext4 -d snapshots it.
    _write_debian_extlinux_conf()

    offset = 1
    for p in partitions:
        size_mb = _parse_size_mb(p.size)
        part_img = img + "." + p.label + ".part"
        run("dd if=/dev/zero of=%s bs=1M count=0 seek=%d" % (part_img, size_mb))

        if p.type == "vfat":
            run("mkfs.vfat -n %s %s" % (p.label.upper(), part_img))
            run("mcopy -sQi %s $DESTDIR/rootfs/boot/* ::/ 2>/dev/null || true" % part_img, privileged = True)
        elif p.type == "ext4":
            headroom_mb = 25
            if rootfs_mb + headroom_mb > size_mb:
                fail("\nrootfs (%d MB) won't fit in partition '%s' (%d MB) with %d MB headroom;\nincrease the partition size in your image definition" % (rootfs_mb, p.label, size_mb, headroom_mb))
            # syslinux 6.04 (Debian bookworm) reads ext4 with extents
            # enabled — no ^64bit/^extent stripping required.
            run("mkfs.ext4 -d $DESTDIR/rootfs -L %s %s %dM" % (p.label, part_img, size_mb), privileged = True)

        run("dd if=%s of=%s bs=1M seek=%d conv=notrunc" % (part_img, img, offset))
        run("rm -f %s" % part_img)
        offset += size_mb

    if ctx.arch == "x86_64":
        _install_syslinux_debian(img, partitions)

def _write_debian_extlinux_conf():
    """Generate /boot/extlinux/extlinux.conf inside the rootfs.

    Walks /boot for vmlinuz-* and initrd.img-* files (named by Debian's
    linux-image-amd64 maintainer scripts) and picks the highest version.
    If no kernel is present, writes a placeholder that boots to a
    rescue-style message; the build still completes so the disk image
    is at least a valid bootable container that the user can populate.
    """
    cmdline = ctx.machine_config.kernel.cmdline if hasattr(ctx.machine_config, "kernel") else "console=ttyS0 root=/dev/sda1 rw"
    run("""
set -e
mkdir -p $DESTDIR/rootfs/boot/extlinux
vmlinuz=$(ls $DESTDIR/rootfs/boot/vmlinuz-* 2>/dev/null | sort -V | tail -1 | xargs -n1 basename || true)
initrd=$(ls $DESTDIR/rootfs/boot/initrd.img-* 2>/dev/null | sort -V | tail -1 | xargs -n1 basename || true)
if [ -z "$vmlinuz" ]; then
    echo "WARN: no kernel in /boot — image won't boot"
    cat > $DESTDIR/rootfs/boot/extlinux/extlinux.conf <<EOF
DEFAULT linux
TIMEOUT 50
PROMPT 1
LABEL linux
    MENU LABEL Debian (no kernel installed)
    KERNEL no-kernel-installed
EOF
else
    cat > $DESTDIR/rootfs/boot/extlinux/extlinux.conf <<EOF
DEFAULT linux
TIMEOUT 50
PROMPT 1
LABEL linux
    MENU LABEL Debian
    KERNEL /boot/$vmlinuz
EOF
    if [ -n "$initrd" ]; then
        echo "    INITRD /boot/$initrd" >> $DESTDIR/rootfs/boot/extlinux/extlinux.conf
    fi
    echo "    APPEND %s" >> $DESTDIR/rootfs/boot/extlinux/extlinux.conf
fi
""" % cmdline, privileged = True)

def _install_syslinux_debian(img, partitions):
    """Install syslinux MBR + extlinux for a Debian disk image.

    Reads mbr.bin from /usr/lib/SYSLINUX/ (Debian path) inside the
    glibc toolchain container, then loop-mounts the root partition
    and runs extlinux --install. Same loop-device pre-creation pattern
    as _install_syslinux.
    """
    run("dd if=/usr/lib/SYSLINUX/mbr.bin of=%s bs=440 count=1 conv=notrunc" % img)

    offset_mb = 1
    root_size_mb = 0
    for p in partitions:
        size = _parse_size_mb(p.size)
        if p.root:
            root_size_mb = size
            break
        offset_mb += size
    if root_size_mb == 0:
        return

    offset_bytes = offset_mb * 1024 * 1024
    size_bytes = root_size_mb * 1024 * 1024
    run("""
set -e
for i in $(seq 0 31); do
    [ -b /dev/loop$i ] || mknod /dev/loop$i b 7 $i
done
LOOP=$(losetup --find --show --offset %d --sizelimit %d %s)
trap 'umount /mnt/extlinux 2>/dev/null; losetup -d $LOOP 2>/dev/null' EXIT
mkdir -p /mnt/extlinux
mount -t ext4 $LOOP /mnt/extlinux
extlinux --install /mnt/extlinux/boot/extlinux
""" % (offset_bytes, size_bytes, img), privileged = True)

def _create_disk_image(name, partitions):
    if not partitions:
        return

    # Walk the rootfs as the host build user to estimate partition fit
    # for the preflight below. dir_size_mb fail-softs on EACCES — dirs the
    # build user can't enter (mode-700 /root, service-user data dirs) are
    # skipped, so this is a slight underestimate. The 25 MB headroom in
    # the preflight absorbs that; mkfs.ext4 -d, which runs as root inside
    # the container, is the authoritative fit check.
    rootfs_mb = dir_size_mb("rootfs")

    total_mb = 1
    for p in partitions:
        total_mb += _parse_size_mb(p.size)

    img = "$DESTDIR/%s.img" % name
    run("dd if=/dev/zero of=%s bs=1M count=0 seek=%d" % (img, total_mb))

    sfdisk_lines = "label: dos\\n"
    for i, p in enumerate(partitions):
        size_mb = _parse_size_mb(p.size)
        ptype = "c" if p.type == "vfat" else "83"
        # Only specify size for non-last partitions; last gets remaining space
        size_spec = "size=%dMiB, " % size_mb if i < len(partitions) - 1 else ""
        # MBR bootable flag goes on the partition the firmware reads at
        # boot — that's partition 1 across every machine yoe currently
        # supports (the FAT boot partition on K3/RPi, the only partition
        # on QEMU). Flagging the rootfs instead made the AM62x ROM
        # silently reject SD cards as non-bootable.
        bootable = ", bootable" if i == 0 else ""
        sfdisk_lines += "%stype=%s%s\\n" % (size_spec, ptype, bootable)

    run("printf '%s' | sfdisk %s" % (sfdisk_lines, img))

    # No pre-mkfs chown: per-file ownership has been preserved end-to-end
    # from each apk's tar headers (apk add ran with privileged = True, and
    # nothing has touched the tree since). mkfs.ext4 -d reads stat()
    # ownership verbatim into the ext4 inodes, which is what we want.

    offset = 1
    for p in partitions:
        size_mb = _parse_size_mb(p.size)
        part_img = img + "." + p.label + ".part"
        run("dd if=/dev/zero of=%s bs=1M count=0 seek=%d" % (part_img, size_mb))

        if p.type == "vfat":
            run("mkfs.vfat -n %s %s" % (p.label.upper(), part_img))
            # Copy boot files from rootfs (root-owned; mcopy needs read access).
            run("mcopy -sQi %s $DESTDIR/rootfs/boot/* ::/ 2>/dev/null || true" % part_img, privileged = True)
        elif p.type == "ext4":
            # Preflight: fail with a clear message when the rootfs won't
            # fit in the partition with enough headroom for ext4 metadata.
            # The 25 MB margin covers block bitmaps, inode tables, journal,
            # and reserved blocks; without it, mkfs.ext4 -d fails mid-
            # populate with "Could not allocate block in ext2 filesystem"
            # — accurate but gives no hint that the partition size is the
            # knob to turn.
            headroom_mb = 25
            if rootfs_mb + headroom_mb > size_mb:
                fail("\nrootfs (%d MB) won't fit in partition '%s' (%d MB) with %d MB headroom;\nincrease the partition size in your image definition" % (rootfs_mb, p.label, size_mb, headroom_mb))

            # Disable ext4 features that syslinux 6.03 can't read (x86 only)
            ext4_opts = "-O ^64bit,^metadata_csum,^extent " if ctx.arch == "x86_64" else ""
            run("mkfs.ext4 %s-d $DESTDIR/rootfs -L %s %s %dM" % (ext4_opts, p.label, part_img, size_mb),
                privileged = True)

        run("dd if=%s of=%s bs=1M seek=%d conv=notrunc" % (part_img, img, offset))
        run("rm -f %s" % part_img)
        offset += size_mb

    # Install bootloader (x86 syslinux)
    if ctx.arch == "x86_64":
        _install_syslinux(img, partitions)

    # No post-build chown back to the host user. The point of preserving
    # per-file ownership end-to-end is that on-disk state in
    # $DESTDIR/rootfs reflects what the image actually contains — flipping
    # everything back to the build user here would destroy the debug
    # visibility we just spent the build preserving. Cleanup goes through
    # the container via `yoe build --clean` / `yoe cache clean`, both of
    # which rm as root in the same privileged context. See
    # docs/security.md for the threat-model implications.

def _install_syslinux(img, partitions):
    """Install syslinux MBR boot code and extlinux on an x86 disk image."""
    # Write MBR boot code (first 440 bytes of mbr.bin)
    run("dd if=$DESTDIR/rootfs/usr/share/syslinux/mbr.bin of=%s bs=440 count=1 conv=notrunc" % img)

    # Find the root partition offset and size
    offset_mb = 1  # MBR overhead
    root_size_mb = 0
    for p in partitions:
        size = _parse_size_mb(p.size)
        if p.root:
            root_size_mb = size
            break
        offset_mb += size

    if root_size_mb == 0:
        return

    offset_bytes = offset_mb * 1024 * 1024
    size_bytes = root_size_mb * 1024 * 1024

    # Run extlinux --install via losetup with explicit offset (not -P which
    # requires partition device nodes). Needs privileged=True for losetup/mount.
    # Docker's --privileged does not populate /dev/loop*, so losetup --find
    # allocates a loop number via /dev/loop-control but then fails to open the
    # missing device node. Pre-create /dev/loop0..31 via mknod before losetup.
    run("""
set -e
for i in $(seq 0 31); do
    [ -b /dev/loop$i ] || mknod /dev/loop$i b 7 $i
done
LOOP=$(losetup --find --show --offset %d --sizelimit %d %s)
trap 'umount /mnt/extlinux 2>/dev/null; losetup -d $LOOP 2>/dev/null' EXIT
mkdir -p /mnt/extlinux
mount -t ext4 $LOOP /mnt/extlinux
extlinux --install /mnt/extlinux/boot/extlinux
""" % (offset_bytes, size_bytes, img), privileged=True)

def _parse_size_mb(size_str, default=256):
    """Parse a size string like '64M', '1G', or 'fill' into megabytes."""
    s = str(size_str)
    if s == "fill" or s == "":
        return default
    if s.endswith("M"):
        return int(s[:-1])
    if s.endswith("G"):
        return int(s[:-1]) * 1024
    return int(s)
