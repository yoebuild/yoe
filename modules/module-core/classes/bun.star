load("//classes/tasks.star", "merge_tasks")

# bun_app class — package a Bun application and its npm dependencies as
# a regular yoe unit.
#
# Each app lives in its own source directory next to the unit's .star
# file and ships a normal Bun project layout: `package.json` declares
# the dependencies (and optionally a `bun.lockb` pins them), plus
# whatever JS/TS source the app uses. Bun runs TypeScript directly, so
# the entry point can be `.ts` without a separate compile step.
#
# The class:
#   1. (task "bun-setup") creates the app directory under `install_path`
#      (default /usr/lib/bun-apps/<name>) inside $DESTDIR.
#   2. (user-supplied task) copies package.json + sources into $APP_BUILD
#      via install_file().
#   3. (task "bun-install") runs `bun install` to populate node_modules,
#      rewrites any baked build-time paths back to the on-target path,
#      and emits /usr/bin wrappers for `entry_points`.
#
# Runtime needs bun on the target; the class adds it to runtime_deps
# automatically.
#
# Pure-JavaScript / pure-TypeScript packages work out of the box. Bun
# can also build native bindings (it bundles a clang); packages that
# need extra build-time libs/headers add them via `deps`.

def bun_app(name, version,
            install_path = "",
            entry_points = {},
            deps = [], runtime_deps = [],
            services = [], conffiles = [],
            license = "", description = "",
            tasks = [], scope = "",
            container = "toolchain-musl", container_arch = "target",
            **kwargs):
    if not install_path:
        install_path = "/usr/lib/bun-apps/" + name

    parent = _parent_dir(install_path)

    wrapper_block = ""
    if entry_points:
        wrapper_block = "mkdir -p $DESTDIR/usr/bin\n"
        for bin_name, entry in entry_points.items():
            wrapper_block += _entry_point_script(install_path, bin_name, entry)

    setup_script = """
set -e
APP_INSTALL=%s
APP_BUILD=$DESTDIR$APP_INSTALL
mkdir -p $DESTDIR%s
mkdir -p "$APP_BUILD"
""" % (install_path, parent)

    # All install steps run in their own shell invocation, so the bun
    # install + post-install rewrites + wrapper emission have to live in
    # one task with a single step so $APP_BUILD survives across them.
    install_script = """
set -e
APP_INSTALL=%s
APP_BUILD=$DESTDIR$APP_INSTALL
test -f "$APP_BUILD/package.json" || {
    echo "bun_app %s: expected package.json at $APP_BUILD/package.json" >&2
    echo "  (use install_file(\\"package.json\\", \\"$APP_BUILD/package.json\\") in your task)" >&2
    exit 1
}
cd "$APP_BUILD"
bun install --production --no-progress --silent
# bun (and the packages it installs) can bake the install-time absolute
# path into metadata and binary shebangs under node_modules/.bin/.
# Rewriting any $APP_BUILD reference back to the on-target $APP_INSTALL
# keeps the tree relocatable to /.
if [ -d node_modules ]; then
    grep -rIlF "$APP_BUILD" node_modules 2>/dev/null \\
        | xargs -r sed -i "s|$APP_BUILD|$APP_INSTALL|g" || true
fi
%s""" % (install_path, name, wrapper_block)

    setup_task = task("bun-setup", steps = [setup_script])
    install_task = task("bun-install", steps = [install_script])

    # Order: class setup -> user-supplied tasks (which install_file
    # package.json + sources into $APP_BUILD) -> class install. The user's
    # tasks come from `tasks=`; merge_tasks lets them override either
    # bookend by name if they really need to.
    final_tasks = merge_tasks([setup_task] + list(tasks) + [install_task], [])

    all_runtime_deps = list(runtime_deps)
    if "bun" not in all_runtime_deps:
        all_runtime_deps.append("bun")

    all_deps = list(deps)
    if container and ":" not in container and container not in all_deps:
        all_deps.append(container)
    # Bun must be available in the build container so `bun install` runs
    # there. The runtime bun unit is the same artifact used at build time.
    if "bun" not in all_deps:
        all_deps.append("bun")

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
    # "file.ts" or "file.js" → exec bun on that entry under the app dir.
    # "pkg" → exec the package's default bin: `node_modules/.bin/<pkg>`.
    # "pkg:script" → exec `bun node_modules/<pkg>/<script>`.
    if ":" in entry:
        pkg, script = entry.split(":", 1)
        body = "exec bun %s/node_modules/%s/%s \"$@\"" % (
            install_path, pkg, script,
        )
    elif entry.endswith(".ts") or entry.endswith(".js") or entry.endswith(".mjs"):
        body = "exec bun %s/%s \"$@\"" % (install_path, entry)
    else:
        body = "exec %s/node_modules/.bin/%s \"$@\"" % (install_path, entry)
    dest = "$DESTDIR/usr/bin/" + bin_name
    return (
        "cat > %s <<'__YOE_BUN_WRAP_EOF__'\n" % dest +
        "#!/bin/sh\n" +
        "%s\n" % body +
        "__YOE_BUN_WRAP_EOF__\n" +
        "chmod 0755 %s\n" % dest
    )

def _parent_dir(path):
    if "/" not in path:
        return "."
    return path.rsplit("/", 1)[0]
