load("//classes/tasks.star", "merge_tasks")

def autotools(name, version, source, sha256="", deps=[], runtime_deps=[],
              configure_args=[], patches=[], services=[], conffiles=[],
              license="", description="", tasks=[], scope="",
              container="toolchain-musl", container_arch="target", **kwargs):
    base_tasks = [
        task("build", steps=[
            # Run autoreconf if configure.ac exists: git doesn't preserve
            # timestamps so configure may be stale relative to m4 files.
            "test -f configure.ac && autoreconf -fi || true",
            "./configure --prefix=$PREFIX " + " ".join(configure_args),
            "make -j$NPROC ACLOCAL=true AUTOCONF=true AUTOMAKE=true AUTOHEADER=true MAKEINFO=true",
            "make DESTDIR=$DESTDIR install ACLOCAL=true AUTOCONF=true AUTOMAKE=true AUTOHEADER=true MAKEINFO=true",
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
