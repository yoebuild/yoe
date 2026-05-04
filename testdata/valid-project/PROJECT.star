project(
    name = "test-distro",
    version = "0.1.0",
    description = "Test distribution",
    defaults = defaults(machine = "qemu-x86_64", image = "base-image"),
    cache = cache(
        path = "/var/cache/yoe/build",
        remote = [
            s3_cache(name="team", bucket="yoe-cache",
                     endpoint="https://minio.internal:9000", region="us-east-1"),
        ],
    ),
    sources = sources(go_proxy = "https://proxy.golang.org"),
    modules = [
        module("github.com/yoe/units-core", ref = "v1.0.0"),
    ],
)
