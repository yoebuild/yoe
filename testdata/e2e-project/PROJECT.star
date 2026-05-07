project(
    name = "e2e-test",
    version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64", image = "base-image"),
    # modules listed in priority order: later entries shadow earlier ones,
    # so module-core wins over module-rpi and the Alpine/Jetson prebuilts.
    modules = [
        module("https://github.com/yoebuild/module-alpine.git",
              ref = "main"),
        module("https://github.com/yoebuild/module-jetson.git",
              ref = "main"),
        module("github.com/yoebuild/yoe",
              local = "../..",
              path = "modules/module-rpi"),
        module("github.com/yoebuild/yoe",
              local = "../..",
              path = "modules/module-core"),
    ],
    # Per-unit pins that override the default last-module-wins shadowing.
    # Use these when module-core's source-built version of a package is
    # broken or under-configured and Alpine's prebuilt is the right
    # answer (e.g. xz is built static-only in module-core, but kmod's
    # depmod needs the shared liblzma.so.5).
    prefer_modules = {
        "xz": "alpine",
    },
)
