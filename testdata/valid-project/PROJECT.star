project(
    name = "test-distro",
    version = "0.1.0",
    description = "Test distribution",
    defaults = defaults(machine = "qemu-x86_64", image = "base-image"),
    cache = cache(path = "/var/cache/yoe/build"),
    modules = [
        module("github.com/yoe/module-core", ref = "v1.0.0"),
    ],
)
