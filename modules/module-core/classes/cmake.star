load("//classes/tasks.star", "merge_tasks")

def cmake(name, version, source, sha256="", deps=[], runtime_deps=[],
          cmake_args=[], patches=[], services=[], conffiles=[],
          license="", description="", tasks=[], scope="",
          container="toolchain-musl", container_arch="target", **kwargs):
    base_tasks = [
        task("build", steps=[
            "cmake -B build -S . -DCMAKE_INSTALL_PREFIX=$PREFIX " +
                " ".join(["-D" + a for a in cmake_args]),
            "cmake --build build -j$NPROC",
            "DESTDIR=$DESTDIR cmake --install build",
        ]),
    ]
    final_tasks = merge_tasks(base_tasks, tasks)
    # Merge class deps with user deps
    all_deps = list(deps)
    if container and container not in all_deps:
        all_deps.append(container)
    unit(
        name=name, version=version, source=source, sha256=sha256,
        deps=all_deps, runtime_deps=runtime_deps, patches=patches,
        tasks=final_tasks, services=services, conffiles=conffiles,
        license=license, description=description, scope=scope,
        container=container, container_arch=container_arch,
        sandbox=True, shell="bash",
        **kwargs,
    )
