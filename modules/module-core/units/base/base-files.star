load("//classes/users.star", "user", "users_commands")

# Alpine-style OpenRC runlevel membership. OpenRC's apk ships the
# /etc/init.d/<svc> scripts but not the runlevel symlinks — distros wire
# those up. base-files owns this configuration because it's the boot-time
# baseline every yoe image inherits. Per-unit `services = [...]` adds
# additional default-runlevel entries on top of these.
_RUNLEVELS = {
    # `sysfs` mounts /sys (and must run before `cgroups`, which mounts
    # /sys/fs/cgroup but only if /sys/fs/cgroup already exists as a
    # directory — created by the kernel when /sys is mounted). cgroups'
    # depend() uses `after sysfs`, which only orders execution when both
    # are in the same runlevel, so sysfs has to live here too.
    #
    # `cgroups` is required for container runtimes (dockerd, containerd)
    # which refuse to start with "Devices cgroup isn't mounted"
    # otherwise. Harmless on non-container images.
    "sysinit": ["sysfs", "cgroups", "devfs", "dmesg"],
    "boot":    ["bootmisc", "hostname", "modules", "sysctl"],
    "shutdown": ["mount-ro", "killprocs"],
}

def _runlevel_commands():
    cmds = []
    for runlevel, services in _RUNLEVELS.items():
        cmds.append("mkdir -p $DESTDIR/etc/runlevels/" + runlevel)
        for svc in services:
            cmds.append("ln -sf /etc/init.d/%s $DESTDIR/etc/runlevels/%s/%s"
                        % (svc, runlevel, svc))
    return cmds

def base_files(name = "base-files", users = None):
    """Creates a base filesystem skeleton unit with the given users.

    Override this in your image to add users:
        load("//units/base/base-files.star", "base_files")
        load("//classes/users.star", "user")
        base_files(name = "base-files-dev", users = [
            user(name = "root", uid = 0, gid = 0, home = "/root"),
            user(name = "myuser", uid = 1000, gid = 1000, password = "secret"),
        ])
    """
    if not users:
        users = [user(name = "root", uid = 0, gid = 0, home = "/root")]

    # openssl is needed at build time if any user has a password to hash
    deps = []
    for u in users:
        if u["password"]:
            deps.append("openssl")
            break
    if "toolchain" not in deps:
        deps.append("toolchain")

    # When root is intentionally passwordless (dev images), follow that
    # policy through to SSH on the apt distros (Debian and Ubuntu) so
    # passwordless root login works like the serial console — mirroring
    # what module-core's openssh init script does on Alpine. Their
    # openssh-server is a feed passthrough carrying sshd's strict upstream
    # defaults (PermitRootLogin prohibit-password, PermitEmptyPasswords
    # no), and both ship `Include /etc/ssh/sshd_config.d/*.conf`, so a
    # permissive drop-in there is the equivalent knob. Gated on the
    # apt-family $DISTRO values at build time; the Alpine path handles its
    # own case through the openssh unit. Omitted entirely when root has a
    # real password, so production images keep sshd's strict defaults.
    ssh_dev_steps = []
    root_passwordless = False
    for u in users:
        if u["uid"] == 0 and not u["password"]:
            root_passwordless = True
    if root_passwordless:
        ssh_dev_steps = [
            "if [ x$DISTRO = xdebian ] || [ x$DISTRO = xubuntu ]; then" +
            " mkdir -p $DESTDIR/etc/ssh/sshd_config.d &&" +
            " printf 'PermitRootLogin yes\\nPermitEmptyPasswords yes\\n'" +
            " > $DESTDIR/etc/ssh/sshd_config.d/10-yoe-dev.conf; fi",
        ]

    unit(
        name = name,
        # yoe ships its own base-files in place of the distro's. On
        # Debian that means the real Debian packages (libc6, dbus, …)
        # apply their versioned constraints against *this* package:
        # libc6 carries `Breaks: base-files (< 13.3~)` and dbus carries
        # `Depends: base-files (>= 13.4~)`. A low version (1.0.0) sorts
        # below that floor, so apt rejects it and the rootfs solve
        # fails. Track Debian's base-files major (currently 13 in
        # trixie) and stay one ahead so those constraints are satisfied;
        # bump again if Debian's base-files reaches 14. Inert on Alpine,
        # where nothing constrains base-files' version.
        version = "14.0",
        release = 14,
        scope = "machine",
        license = "MIT",
        description = "Base filesystem skeleton: users, groups, dirs, inittab, boot config",
        deps = deps,
        # openrc is yoe's init system on Alpine, and the /etc/runlevels
        # symlinks this unit lays down are OpenRC's. Debian images boot
        # systemd instead (the closure already pulls systemd, udev, and
        # systemd-resolved), so pulling openrc there is wrong twice
        # over: it has no role as init, and openrc's `Depends: insserv`
        # collides with systemd-sysv's `Conflicts: insserv`, making the
        # rootfs apt solve unsatisfiable. Scope the dep to alpine.
        distro_runtime_deps = {"alpine": ["openrc"]},
        container = "toolchain",
        container_arch = "target",
        tasks = [
            task("build", steps = (
                [
                    # FHS filesystem skeleton. The /var subtree (/var/tmp,
                    # /var/log, /var/cache, /var/lib, /var/spool) is
                    # standard on every distro and required by package
                    # maintainer scripts: Debian's update-initramfs
                    # mktemps under /var/tmp, and many postinsts write
                    # /var/log and /var/lib/<pkg>. /tmp and /var/tmp are
                    # world-writable with the sticky bit (1777) per FHS.
                    "mkdir -p $DESTDIR/etc $DESTDIR/root $DESTDIR/proc $DESTDIR/sys"
                    + " $DESTDIR/dev $DESTDIR/tmp $DESTDIR/run $DESTDIR/run/lock"
                    + " $DESTDIR/var/tmp $DESTDIR/var/log $DESTDIR/var/cache"
                    + " $DESTDIR/var/lib $DESTDIR/var/spool"
                    + " $DESTDIR/boot/extlinux"
                    + " $DESTDIR/etc/apk/keys",
                    "chmod 1777 $DESTDIR/tmp $DESTDIR/var/tmp",
                    # /var/run and /var/lock are symlinks into /run, the
                    # convention every modern distro relies on — dbus,
                    # systemd, and OpenRC all place runtime sockets and
                    # pidfiles under /run. A real /var/run directory
                    # splits them: a socket created at
                    # /run/dbus/system_bus_socket is then invisible at
                    # /var/run/dbus, and clients that resolve the
                    # /var/run path (NetworkManager reaching the system
                    # bus) fail with "No such file or directory".
                    "ln -sf /run $DESTDIR/var/run",
                    "ln -sf /run/lock $DESTDIR/var/lock",
                ]
                + _runlevel_commands()
                + users_commands(users)
                + [
                    # Root's login shell is bash on the apt distros (the
                    # distro convention; bash is in every Debian and
                    # Ubuntu image's essential set) but stays the busybox
                    # /bin/sh default on Alpine. users_commands wrote
                    # /bin/sh above; rewrite root's entry to bash when
                    # this build targets Debian or Ubuntu. $DISTRO is the
                    # consuming image's effective distro, set by the build.
                    "if [ x$DISTRO = xdebian ] || [ x$DISTRO = xubuntu ]; then" +
                    " sed -i '/^root:/ s#:/bin/sh$#:/bin/bash#'" +
                    " $DESTDIR/etc/passwd; fi",
                    install_template("inittab.tmpl", "$DESTDIR/etc/inittab"),
                    install_template("os-release.tmpl", "$DESTDIR/etc/os-release"),
                    install_file("extlinux.conf",
                                 "$DESTDIR/boot/extlinux/extlinux.conf"),
                    # Default /etc/apk/repositories — a commented-out
                    # template. Operators populate this with their actual
                    # repo URL via an overlay or by overriding base-files
                    # in their project module.
                    install_file("repositories", "$DESTDIR/etc/apk/repositories"),
                    # Ship the project's apk signing public key so on-target
                    # `apk add`/`apk upgrade` verify packages without
                    # --allow-untrusted. yoe writes the key under
                    # <repo>/keys/<name>.rsa.pub before any unit builds; the
                    # paths come in via $YOE_KEYS_DIR / $YOE_KEY_NAME.
                    "cp \"$YOE_KEYS_DIR/$YOE_KEY_NAME\" \"$DESTDIR/etc/apk/keys/$YOE_KEY_NAME\"",
                ]
                + ssh_dev_steps
            )),
        ],
    )

# Default: base-files with just root (blank password)
base_files()
