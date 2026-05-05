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
)
