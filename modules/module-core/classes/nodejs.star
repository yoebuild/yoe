load("//classes/tasks.star", "merge_tasks")

# nodejs_app class — package a Node.js application and its npm
# dependencies as a regular yoe unit.
#
# Each app lives in its own source directory next to the unit's .star file
# and ships a normal Node.js project layout: `package.json` declares the
# npm deps (and optionally a `package-lock.json` pins them), plus whatever
# JS source the app uses. The unit's tasks copy those files into
# `$APP_BUILD` with install_file(); the class then runs `npm ci` (or
# `npm install`) to populate node_modules and rewrites any baked
# build-time paths so the resulting tree is relocatable to /.
#
# The class:
#   1. (task "nodejs-setup") creates the app directory under `install_path`
#      (default /usr/lib/node-apps/<name>) inside $DESTDIR.
#   2. (user-supplied task) copies package.json + sources into $APP_BUILD
#      via install_file().
#   3. (task "nodejs-install") runs npm to materialise node_modules,
#      rewrites any reference to the build-time $DESTDIR-prefixed path
#      back to the on-target absolute path, and emits /usr/bin wrappers
#      for `entry_points`.
#
# Runtime needs nodejs on the target; the class adds it to runtime_deps
# automatically.
#
# Pure-JavaScript packages work out of the box. Packages with native
# bindings (node-gyp builds, prebuild downloads) need their build-time
# libs/headers added via `deps` so npm can compile them in the toolchain
# container.

def nodejs_app(name, version,
               install_path = "",
               entry_points = {},
               deps = [], runtime_deps = [],
               services = [], conffiles = [],
               license = "", description = "",
               tasks = [], scope = "",
               container = "toolchain-musl", container_arch = "target",
               **kwargs):
    if not install_path:
        install_path = "/usr/lib/node-apps/" + name

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

    # All install steps run in their own shell invocation, so the npm
    # install + post-install rewrites + wrapper emission have to live in
    # one task with a single step so $APP_BUILD survives across them.
    install_script = """
set -e
APP_INSTALL=%s
APP_BUILD=$DESTDIR$APP_INSTALL
test -f "$APP_BUILD/package.json" || {
    echo "nodejs_app %s: expected package.json at $APP_BUILD/package.json" >&2
    echo "  (use install_file(\\"package.json\\", \\"$APP_BUILD/package.json\\") in your task)" >&2
    exit 1
}
cd "$APP_BUILD"
if [ -f package-lock.json ]; then
    npm ci --omit=dev --no-audit --no-fund --loglevel=error
else
    npm install --omit=dev --no-audit --no-fund --loglevel=error
fi
# npm sometimes embeds the install-time absolute path into package metadata
# (e.g. .package-lock.json) and into shebangs of binaries it links into
# .bin/. Rewriting any $APP_BUILD reference back to the on-target
# $APP_INSTALL keeps the tree relocatable to /. Pure-JS packages mostly
# don't bake paths in, but this is cheap insurance.
if [ -d node_modules ]; then
    grep -rIlF "$APP_BUILD" node_modules 2>/dev/null \\
        | xargs -r sed -i "s|$APP_BUILD|$APP_INSTALL|g" || true
fi
%s""" % (install_path, name, wrapper_block)

    setup_task = task("nodejs-setup", steps = [setup_script])
    install_task = task("nodejs-install", steps = [install_script])

    # Order: class setup -> user-supplied tasks (which install_file
    # package.json + sources into $APP_BUILD) -> class install. The user's
    # tasks come from `tasks=`; merge_tasks lets them override either
    # bookend by name if they really need to.
    final_tasks = merge_tasks([setup_task] + list(tasks) + [install_task], [])

    all_runtime_deps = list(runtime_deps)
    if "nodejs" not in all_runtime_deps:
        all_runtime_deps.append("nodejs")

    all_deps = list(deps)
    if container and ":" not in container and container not in all_deps:
        all_deps.append(container)
    # nodejs and npm aren't in the toolchain container — pull them into
    # the build sysroot so `npm install` / `npm ci` run here. The same
    # apks are used at runtime via runtime_deps.
    if "nodejs" not in all_deps:
        all_deps.append("nodejs")
    if "npm" not in all_deps:
        all_deps.append("npm")

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
    # "pkg" → exec the package's default bin: `node_modules/.bin/<pkg>`.
    # "pkg:script" → exec `node node_modules/<pkg>/<script>`.
    if ":" in entry:
        pkg, script = entry.split(":", 1)
        body = "exec node %s/node_modules/%s/%s \"$@\"" % (
            install_path, pkg, script,
        )
    else:
        body = "exec %s/node_modules/.bin/%s \"$@\"" % (install_path, entry)
    dest = "$DESTDIR/usr/bin/" + bin_name
    return (
        "cat > %s <<'__YOE_NODE_WRAP_EOF__'\n" % dest +
        "#!/bin/sh\n" +
        "%s\n" % body +
        "__YOE_NODE_WRAP_EOF__\n" +
        "chmod 0755 %s\n" % dest
    )

def _parent_dir(path):
    if "/" not in path:
        return "."
    return path.rsplit("/", 1)[0]
