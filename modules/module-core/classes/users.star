def user(name, uid, gid, home = "", shell = "/bin/sh", password = "", gecos = "", groups = None):
    """Returns a dict describing a user account.

    Args:
        name: login name
        uid: numeric user ID
        gid: numeric group ID
        home: home directory (default: /root for uid 0, /home/<name> otherwise)
        shell: login shell (default: /bin/sh)
        password: plaintext password (hashed at build time via openssl); empty = no password
        gecos: comment/full name field
        groups: list of secondary group names to add the user to (e.g. ["docker"]).
                Each named group is created in /etc/group with a system GID if
                it doesn't already exist; later package install scripts that
                create the same group (e.g. docker-engine's `addgroup -S
                docker`) become idempotent no-ops.
    """
    if not home:
        if uid == 0:
            home = "/root"
        else:
            home = "/home/" + name
    if groups == None:
        groups = []
    return {
        "name": name,
        "uid": uid,
        "gid": gid,
        "home": home,
        "shell": shell,
        "password": password,
        "gecos": gecos,
        "groups": groups,
    }

def users_commands(users):
    """Returns shell commands to populate /etc/passwd, /etc/group, /etc/shadow."""
    cmds = [
        "true > $DESTDIR/etc/passwd",
        "true > $DESTDIR/etc/group",
        "true > $DESTDIR/etc/shadow",
        "chmod 0600 $DESTDIR/etc/shadow",
    ]
    for u in users:
        cmds.append(
            "echo '" + u["name"] + ":x:" + str(u["uid"]) + ":" +
            str(u["gid"]) + ":" + u["gecos"] + ":" + u["home"] + ":" +
            u["shell"] + "' >> $DESTDIR/etc/passwd",
        )
        cmds.append(
            "echo '" + u["name"] + ":x:" + str(u["gid"]) +
            ":' >> $DESTDIR/etc/group",
        )
        # `lstchg=0` is sshd's "must change password on next login" trigger:
        # logins then fail with "Your password has expired" and refuse
        # non-TTY sessions. Use lstchg=1 (epoch+1 day) so the field is
        # non-zero — combined with max=99999 (~273 years), the password
        # never effectively expires. Leaving the field entirely blank
        # would also disable aging, but busybox login rejects shadow
        # entries with all-empty trailing fields.
        if u["password"]:
            cmds.append(
                "PW=$(openssl passwd -6 '" +
                u["password"] + "') && " +
                "echo '" + u["name"] + ":'\"$PW\"':1:0:99999:7:::' >> $DESTDIR/etc/shadow",
            )
        else:
            cmds.append(
                "echo '" + u["name"] +
                "::1:0:99999:7:::' >> $DESTDIR/etc/shadow",
            )
        if u["uid"] != 0:
            cmds.append("mkdir -p $DESTDIR" + u["home"])
    for u in users:
        if u["uid"] == 0:
            cmds.append("chmod 0700 $DESTDIR" + u["home"])

    # Secondary group memberships. Build a {group: [user, ...]} map across
    # all users, then emit one /etc/group line per group. We assign GIDs
    # in Alpine's system-group convention (high, descending from 982) so
    # they don't collide with primary user GIDs (which start at 1000).
    secondary = {}
    for u in users:
        for g in u["groups"]:
            if g not in secondary:
                secondary[g] = []
            secondary[g].append(u["name"])
    gid = 982
    for g in secondary:
        cmds.append(
            "echo '" + g + ":x:" + str(gid) + ":" +
            ",".join(secondary[g]) + "' >> $DESTDIR/etc/group",
        )
        gid = gid - 1
    return cmds
