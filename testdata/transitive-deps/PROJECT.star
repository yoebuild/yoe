project(name = "transitive-deps", version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64"),
    modules = [
        module("https://example.com/a.git", local = "modules/a"),
    ],
)
