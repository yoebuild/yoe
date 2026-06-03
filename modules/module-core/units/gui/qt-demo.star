# qt-demo — a small Qt 6 Quick scene that proves an image's graphical
# stack works end-to-end (kernel framebuffer → Qt linuxfb plugin → Qt
# Quick software scene graph → rendered text + animation). The scene is
# a hand-written demo.qml shipped with this unit; at boot, OpenRC
# launches Alpine's prebuilt `qmlscene` against it.
#
# No project-side compilation is involved — `qmlscene` and every Qt 6
# module the demo touches come from the apks listed in runtime_deps.
# `services = ["qt-demo"]` follows yoe's "units enable their own
# services" rule: installing this package enables the demo at boot.
unit(
    name = "qt-demo",
    version = "0.1.0",
    license = "Apache-2.0",
    description = "Tiny Qt 6 Quick demo rendered to /dev/fb0 via qmlscene",
    container = "toolchain",
    container_arch = "target",
    sandbox = True,
    shell = "bash",
    deps = ["toolchain"],
    # qt6-qtdeclarative ships /usr/lib/qt6/bin/qmlscene plus libQt6Qml /
    # libQt6Quick. qt6-qtbase-x11 (an Alpine packaging quirk — it owns
    # libQt6Gui, libQt6Widgets, and *every* QPA platform plugin
    # including linuxfb, despite the "x11" name) pulls in the graphical
    # Qt stack. ttf-liberation + fontconfig supply a default font so
    # Text glyphs actually render. openrc executes the init script at
    # boot.
    runtime_deps = [
        "qt6-qtdeclarative",
        "qt6-qtbase-x11",
        "ttf-liberation",
        "fontconfig",
        "openrc",
    ],
    services = ["qt-demo"],
    tasks = [
        task("build", steps = [
            # Stage demo.qml under /usr/share/qt-demo/. mode=0o644
            # is install_file's default; spelt out for clarity since
            # the init script does not chmod on first boot.
            "mkdir -p $DESTDIR/usr/share/qt-demo",
            install_file("demo.qml",
                         "$DESTDIR/usr/share/qt-demo/demo.qml"),
            # OpenRC init script. `services = [...]` on this unit bakes
            # the runlevel symlink into the resulting apk.
            "mkdir -p $DESTDIR/etc/init.d",
            install_file("qt-demo.init",
                         "$DESTDIR/etc/init.d/qt-demo", mode = 0o755),
        ]),
    ],
)
