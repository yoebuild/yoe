load("//classes/tasks.star", "merge_tasks")

# _goarch maps Yoe canonical architecture names to GOARCH values.
_goarch = {
    "x86_64": "amd64",
    "arm64": "arm64",
    "riscv64": "riscv64",
}

def go_binary(name, version, source, tag="", sha256="",
              go_package="", binary="", deps=[], runtime_deps=[],
              services=[], conffiles=[], environment={},
              license="", description="", tasks=[], scope="",
              container="golang:1.24", container_arch="host",
              go_version="", **kwargs):
    if not go_package:
        go_package = "./cmd/" + name
    # The installed binary's filename. Defaults to the unit name; override
    # with `binary` when the upstream command name differs from the apk
    # package name (e.g., simpleiot installs as siot).
    if not binary:
        binary = name
    # Build the GOARCH mapping as a shell case statement so the
    # correct value is resolved at build time from $ARCH.
    case_arms = " ".join([
        "%s) goarch=%s;;" % (k, v) for k, v in _goarch.items()
    ])
    cross_setup = (
        'case "$ARCH" in ' + case_arms +
        ' *) echo "unsupported ARCH=$ARCH" >&2; exit 1;; esac'
    )
    base_tasks = [
        task("build", steps=[
            cross_setup +
            " && export PATH=/usr/local/go/bin:$PATH" +
            " && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch" +
            " go build -o $DESTDIR$PREFIX/bin/" + binary + " " + go_package,
        ]),
    ]
    final_tasks = merge_tasks(base_tasks, tasks)
    # External container images (containing ":") are pulled by Docker
    # directly and don't need a DAG dependency.
    all_deps = list(deps)
    if container and ":" not in container and container not in all_deps:
        all_deps.append(container)
    unit(
        name=name, version=version, source=source, sha256=sha256,
        tag=tag, deps=all_deps, runtime_deps=runtime_deps,
        tasks=final_tasks, services=services, conffiles=conffiles,
        license=license, description=description, scope=scope,
        container=container, container_arch=container_arch,
        environment={"GOMODCACHE": "/go/cache/mod", "GOCACHE": "/go/cache/build"},
        cache_dirs={"/go/cache": "go"},
        **kwargs,
    )
