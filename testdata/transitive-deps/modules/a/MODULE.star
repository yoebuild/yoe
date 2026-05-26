module_info(
    name = "a",
    deps = [
        module("https://example.com/b.git", local = "modules/b"),
    ],
)
