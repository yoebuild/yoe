load("//classes/users.star", "user", "users_commands")

# Alpine-style OpenRC runlevel membership. OpenRC's apk ships the
# /etc/init.d/<svc> scripts but not the runlevel symlinks — distros wire
# those up. base-files owns this configuration because it's the boot-time
# baseline every yoe image inherits. Per-unit `services = [...]` adds
# additional default-runlevel entries on top of these.
_RUNLEVELS = {
    # `cgroups` mounts /sys/fs/cgroup before anything else looks at it —
    # required for container runtimes (dockerd, containerd) which refuse
    # to start with "Devices cgroup isn't mounted" otherwise. Harmless on
    # non-container images.
    "sysinit": ["cgroups", "devfs", "dmesg"],
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
    if "toolchain-musl" not in deps:
        deps.append("toolchain-musl")

    unit(
        name = name,
        version = "1.0.0",
        release = 9,
        scope = "machine",
        license = "MIT",
        description = "Base filesystem skeleton: users, groups, dirs, inittab, boot config",
        deps = deps,
        runtime_deps = ["openrc"],
        container = "toolchain-musl",
        container_arch = "target",
        tasks = [
            task("build", steps = (
                [
                    "mkdir -p $DESTDIR/etc $DESTDIR/root $DESTDIR/proc $DESTDIR/sys"
                    + " $DESTDIR/dev $DESTDIR/tmp $DESTDIR/run $DESTDIR/var/run"
                    + " $DESTDIR/boot/extlinux"
                    + " $DESTDIR/etc/apk/keys",
                ]
                + _runlevel_commands()
                + users_commands(users)
                + [
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
            )),
        ],
    )

# Default: base-files with just root (blank password)
base_files()
