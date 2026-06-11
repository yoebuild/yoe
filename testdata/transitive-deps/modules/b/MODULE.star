module_info(
    name = "b",
    deps = [
        module("https://example.com/c.git", local = "modules/c"),
    ],
)
