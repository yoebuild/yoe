# service_gate — keep only the service files the target init actually reads.
#
# A unit is evaluated once and built once per distro, so it cannot know at
# Starlark time which init system it is packaging for. It therefore installs
# both descriptions of its service — an OpenRC script and a systemd unit —
# and this step, appended last, deletes the set that does not apply. $DISTRO
# is the consuming image's effective distro, exported to every build step and
# already part of the unit hash, so branching on it is cache-correct.
#
# The settings file is written once as `/etc/conf.d/<name>` and moved to
# `/etc/default/<name>` on the apt-family bases, which is where a Debian or
# Ubuntu administrator looks for it. Its contents are plain KEY=VALUE lines,
# which OpenRC sources directly and systemd reads through EnvironmentFile, so
# only the path differs. A unit using this gate declares
# `conffiles = ["/etc/default/<name>"]` — conffiles is read only when building
# a .deb, so the Alpine path ignores it.
#
# Pass sysusers = True for a service that ships a systemd-sysusers file; it is
# meaningless without systemd and goes away with the rest of the systemd set.

def service_gate(name, sysusers = False):
    systemd_only = "$DESTDIR/lib/systemd"
    if sysusers:
        systemd_only += " $DESTDIR$PREFIX/lib/sysusers.d"
    return (
        "if [ x$DISTRO = xdebian ] || [ x$DISTRO = xubuntu ]; then" +
        " mkdir -p $DESTDIR/etc/default &&" +
        " mv $DESTDIR/etc/conf.d/" + name + " $DESTDIR/etc/default/" + name + " &&" +
        " rm -rf $DESTDIR/etc/init.d $DESTDIR/etc/conf.d;" +
        " else rm -rf " + systemd_only + "; fi"
    )
