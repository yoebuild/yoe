load("//classes/image.star", "image")

image(
    name = "base-image",
    artifacts = ["musl", "base-files", "busybox", "busybox-binsh",
                 "linux", "apk-tools", "openrc", "network-config"],
    hostname = "yoe",
)
