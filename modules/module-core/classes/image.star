def image(name, artifacts=[], hostname="", timezone="", locale="",
          partitions=[], scope="machine",
          container="toolchain-musl", container_arch="target", deps=[],
          version=None, **kwargs):
    """Create a bootable disk image from packages.

    `version` defaults to PROJECT_VERSION (from PROJECT.star) so the TUI's
    VERSION column shows what build the image represents and `/etc/os-release`
    on the device matches.
    """
    if version == None:
        version = PROJECT_VERSION
    # Merge machine packages
    all_artifacts = list(artifacts) + list(MACHINE_CONFIG.packages)

    # Resolve provides (e.g., "linux" → "linux-rpi4")
    explicit = []
    for a in all_artifacts:
        r = PROVIDES.get(a, None)
        explicit.append(r if r != None else a)

    # Resolve transitive runtime dependencies for the rootfs / build path.
    # `explicit` (above) is preserved separately for UX surfaces like the
    # TUI tree, where seeing the user's pre-closure list rather than the
    # flattened set is much less misleading.
    resolved = _resolve_runtime_deps(explicit)

    # Use machine partitions if image doesn't specify its own
    all_partitions = partitions if partitions else list(MACHINE_CONFIG.partitions)

    # Merge class deps with user deps
    all_deps = list(deps)
    if container and container not in all_deps:
        all_deps.append(container)

    unit(
        name = name,
        version = version,
        scope = scope,
        unit_class = "image",
        artifacts = resolved,
        artifacts_explicit = explicit,
        partitions = all_partitions,
        container = container,
        container_arch = container_arch,
        sandbox = True,
        shell = "bash",
        deps = all_deps,
        tasks = [
            task("rootfs", fn=lambda: _assemble_rootfs(resolved, hostname, timezone, locale)),
            task("disk", fn=lambda: _create_disk_image(name, all_partitions)),
        ],
        **kwargs,
    )

def _assemble_rootfs(packages, hostname, timezone, locale):
    """Install packages into the rootfs using apk-tools.

    apk handles dependency resolution from APKINDEX, enforces file-conflict
    detection, and populates /lib/apk/db/installed automatically. The
    `packages` list still includes transitive runtime deps from
    `_resolve_runtime_deps` so the build-time DAG schedules everything,
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

    # apk ran as root and left root-owned files (e.g. /root with mode 700)
    # throughout the rootfs. Subsequent host-side steps (`dir_size_mb`
    # below, the destdir walks in the build executor) run as the host
    # build user and can't enter those dirs. Hand ownership back to the
    # build user; _create_disk_image will chown to root again right
    # before mkfs.ext4 -d so the on-target rootfs is owned by root.
    run("chown -R $(stat -c %u:%g /project) $DESTDIR/rootfs",
        privileged = True)

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

def _create_disk_image(name, partitions):
    if not partitions:
        return

    # Capture the rootfs size before the chown to root below — dir_size_mb
    # walks on the host as the build user, and a post-chown walk can't enter
    # mode-700 root-owned dirs (e.g., /root). Used by the ext4 preflight
    # below to fail with a clear message when contents won't fit.
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
        bootable = ", bootable" if p.root else ""
        sfdisk_lines += "%stype=%s%s\\n" % (size_spec, ptype, bootable)

    run("printf '%s' | sfdisk %s" % (sfdisk_lines, img))

    # Rootfs was assembled as the host build user (docker --user uid:gid), so
    # every file under $DESTDIR/rootfs is owned by that uid. mkfs.ext4 -d copies
    # ownership into the filesystem verbatim, so the booted system would see
    # files owned by whatever host user ran the build. Chown to root before
    # packing, and chown the destdir back at the end so the next build's
    # os.RemoveAll() on the host can clean it up.
    run("chown -R 0:0 $DESTDIR/rootfs", privileged = True)

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
            ext4_opts = "-O ^64bit,^metadata_csum,^extent " if ARCH == "x86_64" else ""
            run("mkfs.ext4 %s-d $DESTDIR/rootfs -L %s %s %dM" % (ext4_opts, p.label, part_img, size_mb),
                privileged = True)

        run("dd if=%s of=%s bs=1M seek=%d conv=notrunc" % (part_img, img, offset))
        run("rm -f %s" % part_img)
        offset += size_mb

    # Install bootloader (x86 syslinux)
    if ARCH == "x86_64":
        _install_syslinux(img, partitions)

    # Restore destdir ownership to the host build user. The chown -R above,
    # plus any root-owned files the privileged mkfs/mcopy/syslinux steps left
    # behind, would otherwise block the next build's os.RemoveAll on the host.
    run("chown -R $(stat -c %u:%g /project) $DESTDIR", privileged = True)

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

def _resolve_runtime_deps(packages):
    """Expand a package list to include all transitive runtime dependencies.
    Starlark has no recursion or while loops, so we use iterative BFS
    with a for loop over a generous upper bound.
    """
    # BFS: discover all transitive runtime deps
    seen = {}
    queue = list(packages)
    for _i in range(1000):  # upper bound on iterations
        if not queue:
            break
        name = queue[0]
        queue = queue[1:]
        if name in seen:
            continue
        seen[name] = True
        deps = RUNTIME_DEPS.get(name, None)
        if deps != None:
            for dep in deps:
                resolved = PROVIDES.get(dep, None)
                d = resolved if resolved != None else dep
                if d not in seen:
                    queue = queue + [d]

    # Topological sort: emit packages whose deps are all emitted
    remaining = list(seen.keys())
    ordered = []
    emitted = {}
    for _round in range(len(remaining) + 1):
        next_remaining = []
        for name in remaining:
            deps = RUNTIME_DEPS.get(name, None)
            ready = True
            if deps != None:
                for dep in deps:
                    resolved = PROVIDES.get(dep, None)
                    d = resolved if resolved != None else dep
                    if d in seen and d not in emitted:
                        ready = False
                        break
            if ready:
                ordered.append(name)
                emitted[name] = True
            else:
                next_remaining.append(name)
        remaining = next_remaining
        if not remaining:
            break
    # Append any remaining (circular deps)
    for name in remaining:
        ordered.append(name)
    return ordered

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
