load("//classes/tasks.star", "merge_tasks")

# python_venv class — package a Python virtual environment containing one or
# more pip dependencies as a regular yoe unit.
#
# The class:
#   1. creates a venv under `install_path` (default /usr/lib/python-venvs/<name>)
#      inside $DESTDIR using the python3 apk installed into the build sysroot
#      via deps. The same apk is what the runtime image gets via runtime_deps,
#      so absolute paths baked into the venv resolve identically on-target.
#   2. pip-installs the packages listed in `pip_packages`,
#   3. rewrites every reference to the build-time $DESTDIR-prefixed path back
#      to the on-target absolute path so the venv is relocatable to /,
#   4. re-points $VENV/bin/python at /usr/bin/python3 so the venv keeps
#      working even if the toolchain's python3 absolute path changes,
#   5. optionally emits /usr/bin wrappers for `entry_points`.
#
# Runtime needs python3 on the target; the class adds it to runtime_deps
# automatically.
#
# Pure-Python wheels work out of the box. Wheels with C extensions need their
# build-time libs/headers added via `deps` so pip can compile them in the
# toolchain container.

def python_venv(name, version, pip_packages,
                install_path = "",
                entry_points = {},
                deps = [], runtime_deps = [],
                services = [], conffiles = [],
                license = "", description = "",
                tasks = [], scope = "",
                container = "toolchain-musl", container_arch = "target",
                **kwargs):
    if not pip_packages:
        fail("python_venv %s: pip_packages must not be empty" % name)

    if not install_path:
        install_path = "/usr/lib/python-venvs/" + name

    parent = _parent_dir(install_path)
    pkg_args = " ".join(["'" + p + "'" for p in pip_packages])

    # Each entry in tasks[].steps runs in its own shell invocation (see
    # internal/build/sandbox.go RunSimple), so shell variables set in one
    # step do not carry over to the next. The whole venv build needs to
    # live inside a single step so $VENV_BUILD survives across pip and
    # the post-install rewrites.
    wrapper_block = ""
    if entry_points:
        wrapper_block = "mkdir -p $DESTDIR/usr/bin\n"
        for bin_name, entry in entry_points.items():
            wrapper_block += _entry_point_script(install_path, bin_name, entry)

    venv_script = """
set -e
VENV_INSTALL=%s
VENV_BUILD=$DESTDIR$VENV_INSTALL
mkdir -p $DESTDIR%s
python3 -m venv "$VENV_BUILD"
"$VENV_BUILD/bin/pip" install --no-cache-dir --disable-pip-version-check --upgrade pip
"$VENV_BUILD/bin/pip" install --no-cache-dir --disable-pip-version-check %s
find "$VENV_BUILD" -type d -name __pycache__ -prune -exec rm -rf {} +
grep -rIlF "$VENV_BUILD" "$VENV_BUILD" | xargs -r sed -i "s|$VENV_BUILD|$VENV_INSTALL|g"
ln -sfn /usr/bin/python3 "$VENV_BUILD/bin/python"
ln -sfn python "$VENV_BUILD/bin/python3"
%s""" % (install_path, parent, pkg_args, wrapper_block)

    base_tasks = [task("build", steps = [venv_script])]
    final_tasks = merge_tasks(base_tasks, tasks)

    all_runtime_deps = list(runtime_deps)
    if "python3" not in all_runtime_deps:
        all_runtime_deps.append("python3")

    all_deps = list(deps)
    if container and ":" not in container and container not in all_deps:
        all_deps.append(container)
    # python3 and py3-pip aren't in the toolchain container — pull them
    # into the build sysroot so `python3 -m venv` / pip install run here.
    # The same apks are used at runtime via runtime_deps.
    if "python3" not in all_deps:
        all_deps.append("python3")
    if "py3-pip" not in all_deps:
        all_deps.append("py3-pip")

    unit(
        name = name,
        version = version,
        deps = all_deps,
        runtime_deps = all_runtime_deps,
        tasks = final_tasks,
        services = services,
        conffiles = conffiles,
        license = license,
        description = description,
        scope = scope,
        container = container,
        container_arch = container_arch,
        sandbox = False,
        shell = "bash",
        **kwargs
    )

def _entry_point_script(install_path, bin_name, entry):
    # "pkg.module" → exec the module via `python -m`.
    # "pkg.module:func" → exec a callable; pass argv through.
    if ":" in entry:
        module, func = entry.split(":", 1)
        body = "exec %s/bin/python -c \"import sys; from %s import %s; sys.exit(%s(*sys.argv[1:]))\" \"$@\"" % (
            install_path, module, func, func,
        )
    else:
        body = "exec %s/bin/python -m %s \"$@\"" % (install_path, entry)
    dest = "$DESTDIR/usr/bin/" + bin_name
    return (
        "cat > %s <<'__YOE_PY_WRAP_EOF__'\n" % dest +
        "#!/bin/sh\n" +
        "%s\n" % body +
        "__YOE_PY_WRAP_EOF__\n" +
        "chmod 0755 %s\n" % dest
    )

def _parent_dir(path):
    if "/" not in path:
        return "."
    return path.rsplit("/", 1)[0]
