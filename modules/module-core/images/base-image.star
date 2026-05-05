load("//classes/image.star", "image")

image(
    name = "base-image",
    artifacts = ["musl", "base-files", "busybox", "linux", "apk-tools", "network-config"],
    hostname = "yoe",
)
