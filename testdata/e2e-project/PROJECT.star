project(
    name = "e2e-test",
    version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64", image = "base-image"),
    cache = cache(path = "build/cache"),
    modules = [
        module("github.com/yoebuild/yoe",
              local = "../..",
              path = "modules/units-alpine"),
        module("github.com/yoebuild/yoe",
              local = "../..",
              path = "modules/units-core"),
        module("github.com/yoebuild/yoe",
              local = "../..",
              path = "modules/units-rpi"),
    ],
)
