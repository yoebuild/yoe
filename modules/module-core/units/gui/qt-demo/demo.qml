// qt-demo / demo.qml — Qt Quick scene rendered to /dev/fb0 by qmlscene.
//
// Layout uses anchors.centerIn so the greeting always lands at the
// middle of the root, no matter the framebuffer size. The four corner
// rectangles are diagnostic: if the centred greeting drifts off-screen,
// the visible corners tell us at a glance whether the QQuickView is
// sized to match the framebuffer or whether Qt sees a larger logical
// canvas. The "Screen WxH" readout below the title prints the root's
// width/height so we can compare against /sys/class/graphics/fb0.

// hi cliff

import QtQuick

Rectangle {
    id: root
    color: "#1e2a3a"

    // Corner markers. If all four are visible inside the QEMU window,
    // root.width/height matches the framebuffer. If only the top-left
    // red square is visible, root is larger than the visible area and
    // the centred greeting is being drawn off-screen.
    Rectangle {
        width: 60; height: 60; color: "#e74c3c"
        anchors.left: parent.left; anchors.top: parent.top
    }
    Rectangle {
        width: 60; height: 60; color: "#2ecc71"
        anchors.right: parent.right; anchors.top: parent.top
    }
    Rectangle {
        width: 60; height: 60; color: "#f1c40f"
        anchors.left: parent.left; anchors.bottom: parent.bottom
    }
    Rectangle {
        width: 60; height: 60; color: "#9b59b6"
        anchors.right: parent.right; anchors.bottom: parent.bottom
    }

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

        Text {
            text: "Root size: " + root.width + " × " + root.height
            color: "#9fb1c7"
            font.pixelSize: 18
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
