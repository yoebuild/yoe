// qt-demo / demo.qml — Qt Quick scene rendered to /dev/fb0 by qmlscene.
//
// Root is a Rectangle (not a Window). qmlscene wraps a non-Window root
// in a QQuickView whose default resizeMode is SizeRootObjectToView, so
// the root is sized to the view — which on linuxfb is the framebuffer's
// logical-pixel dimensions, fullscreen by default with no window
// manager in the picture. `anchors.centerIn: parent` then lands the
// column in the framebuffer's true centre.
//
// The companion qemu-x86_64 boot path adds `video=1280x768` to the
// kernel cmdline so virtio-gpu's DRM driver sets a usable mode at boot
// instead of falling back to 640×480 — without that the visible
// scanout is only the top-left corner of the framebuffer and centred
// UI lands off-screen.

import QtQuick

Rectangle {
    color: "#1e2a3a"

    Column {
        anchors.centerIn: parent
        spacing: 24

        Text {
            text: "Hello from yoe!"
            color: "#f5f7fb"
            font.pixelSize: 56
            font.bold: true
            horizontalAlignment: Text.AlignHCenter
            anchors.horizontalCenter: parent.horizontalCenter
        }

        Text {
            text: "Qt 6 Quick · linuxfb · software scene graph"
            color: "#9fb1c7"
            font.pixelSize: 22
            horizontalAlignment: Text.AlignHCenter
            anchors.horizontalCenter: parent.horizontalCenter
        }

        Rectangle {
            width: 320
            height: 6
            radius: 3
            color: "#3a78c2"
            anchors.horizontalCenter: parent.horizontalCenter

            SequentialAnimation on opacity {
                loops: Animation.Infinite
                NumberAnimation { from: 1.0; to: 0.2; duration: 1200; easing.type: Easing.InOutSine }
                NumberAnimation { from: 0.2; to: 1.0; duration: 1200; easing.type: Easing.InOutSine }
            }
        }
    }
}
