def user(name, uid, gid, home = "", shell = "/bin/sh", password = "", gecos = ""):
    """Returns a dict describing a user account.

    Args:
        name: login name
        uid: numeric user ID
        gid: numeric group ID
        home: home directory (default: /root for uid 0, /home/<name> otherwise)
        shell: login shell (default: /bin/sh)
        password: plaintext password (hashed at build time via openssl); empty = no password
        gecos: comment/full name field
    """
    if not home:
        if uid == 0:
            home = "/root"
        else:
            home = "/home/" + name
    return {
        "name": name,
        "uid": uid,
        "gid": gid,
        "home": home,
        "shell": shell,
        "password": password,
        "gecos": gecos,
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
    return cmds
