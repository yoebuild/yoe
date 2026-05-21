load("//classes/go.star", "go_binary")

# The yoe binary itself. Pure Go, CGO_ENABLED=0, single ./cmd/yoe target.
# Pins to a tagged release; bump `version` alongside a CHANGELOG entry.
# On-device, `apk upgrade yoe` from the project's feed swaps the binary
# baked into the image at flash time.
#
# go.mod requires Go >= 1.25; the go_binary class default container
# (golang:1.24) is too old. Override to golang:1.26 to match the
# on-device `go` toolchain unit (1.26.2).
go_binary(
    name = "yoe",
    version = "0.10.11",
    source = "https://github.com/yoebuild/yoe.git",
    tag = "v0.10.11",
    container = "golang:1.26",
    license = "Apache-2.0",
    description = "yoe build system CLI",
)
