load("//classes/tasks.star", "merge_tasks")

# binary class — install prebuilt binaries from upstream release URLs.
#
# Resolves URL + SHA per ARCH at Starlark eval time, fetches the asset
# (yoe's source workspace handles tar/zip extraction or bare-file copy
# automatically), and generates a single install task that copies or
# symlinks files from $SRCDIR into $DESTDIR.
#
# Two URL shapes:
#   asset = "{arch}/foo"    — templated; arch comes from arch_map (default
#                              x86_64→amd64, arm64→arm64) and {version}
#                              expands to the unit's version
#   assets = {ARCH: "..."}  — literal per-arch path (no {arch} substitution)
#
# Layout knobs:
#   binaries     — None (default to a single $PREFIX/bin/<name>),
#                  list ["bin/go", "bin/gofmt"] (basenames become
#                  install names, src paths stay verbatim), or
#                  dict {"go": "bin/go", "gofmt": "bin/gofmt"} for
#                  explicit install-name → src mapping.
#   install_tree — bundle-style: copy the entire extracted tree into a
#                  destination directory and emit relative symlinks from
#                  $PREFIX/bin into it. Used for toolchains (go, helix
#                  with its runtime/) where the binaries reference
#                  sibling files.
#   extras       — extra (src, dst) or (src, dst, mode) tuples for
#                  non-binary assets (man pages, license files, runtime
#                  data) that should be installed verbatim.
#   symlinks     — additional symlink overrides {dst: target} applied
#                  after the primary install steps.

# _DEFAULT_ARCH_MAP maps yoe canonical arches to the tokens most upstreams
# use in their asset filenames (Go-style amd64/arm64).
_DEFAULT_ARCH_MAP = {
    "x86_64": "amd64",
    "arm64":  "arm64",
}

def _subst(s, version, arch_token):
    return s.replace("{version}", version).replace("{arch}", arch_token)

def _basename(path):
    if "/" not in path:
        return path
    return path.rsplit("/", 1)[1]

def _relpath(from_dir, to_path):
    # Compute a relative path from from_dir to to_path. Both must start
    # the same way (e.g., both anchored under $PREFIX). Used to build
    # relocatable symlink targets — `ln -s ../lib/go/bin/go usr/bin/go`
    # rather than absolute `/usr/lib/go/bin/go`.
    fp = from_dir.split("/")
    tp = to_path.split("/")
    i = 0
    common = min(len(fp), len(tp))
    for j in range(common):
        if fp[j] != tp[j]:
            break
        i = j + 1
    ups = [".."] * (len(fp) - i)
    rest = tp[i:]
    if not ups and not rest:
        return "."
    return "/".join(ups + rest)

def _normalise_binaries(binaries, default_name, version, arch_token):
    # Returns a list of (install_name, src_path) tuples with templating
    # already applied. install_name is always literal (no /).
    if binaries == None:
        return [(default_name, _subst(default_name, version, arch_token))]
    if type(binaries) == "list":
        out = []
        for entry in binaries:
            if type(entry) != "string":
                fail("binary: 'binaries' list entries must be strings, got %r" % entry)
            src = _subst(entry, version, arch_token)
            out.append((_basename(entry), src))
        return out
    if type(binaries) == "dict":
        out = []
        for k, v in binaries.items():
            if type(k) != "string" or type(v) != "string":
                fail("binary: 'binaries' dict entries must be string→string")
            if "/" in k:
                fail("binary: 'binaries' install name %r cannot contain '/'" % k)
            out.append((k, _subst(v, version, arch_token)))
        return out
    fail("binary: 'binaries' must be list, dict, or omitted")

def _install_steps(name, binaries_pairs, install_tree, extras, symlinks):
    # Build steps run with CWD set to the unit's source directory
    # (sandbox.go's `cd /build/src && ...`), so source paths are relative
    # to the extracted tree. Using $SRCDIR here would silently expand to
    # an empty string and cp would walk the whole rootfs from /.
    steps = []
    if install_tree:
        steps.append("mkdir -p $DESTDIR%s" % install_tree)
        steps.append("cp -aT . $DESTDIR%s" % install_tree)

    # Primary binaries — symlinks into $PREFIX/bin when install_tree is
    # set, direct install -m0755 copies otherwise.
    for install_name, src in binaries_pairs:
        dst_dir = "$DESTDIR$PREFIX/bin"
        dst = "%s/%s" % (dst_dir, install_name)
        if install_tree:
            target_abs = "%s/%s" % (install_tree, src)
            target_rel = _relpath("$PREFIX/bin", target_abs)
            steps.append("mkdir -p %s" % dst_dir)
            steps.append("ln -sfn %s %s" % (target_rel, dst))
        else:
            steps.append("mkdir -p %s" % dst_dir)
            steps.append("install -m0755 ./%s %s" % (src, dst))

    for entry in extras:
        if len(entry) == 2:
            src, dst = entry[0], entry[1]
            mode = None
        elif len(entry) == 3:
            src, dst, mode = entry[0], entry[1], entry[2]
        else:
            fail("binary: extras entries must be (src, dst) or (src, dst, mode)")
        steps.append("mkdir -p $(dirname $DESTDIR%s)" % dst)
        steps.append("cp -aT ./%s $DESTDIR%s" % (src, dst))
        if mode != None:
            steps.append("chmod %o $DESTDIR%s" % (mode, dst))

    for dst, target in symlinks.items():
        steps.append("mkdir -p $(dirname $DESTDIR%s)" % dst)
        steps.append("ln -sfn %s $DESTDIR%s" % (target, dst))

    if not steps:
        fail("binary %s: no install steps — set 'binaries' or 'extras'" % name)
    return steps

def binary(name, version, base_url, sha256,
           asset = None, assets = None, arch_map = None,
           binaries = None, install_tree = "",
           extras = [], symlinks = {},
           container = "toolchain-musl",
           container_arch = "target",
           deps = [], runtime_deps = [],
           license = "", description = "",
           services = [], conffiles = [], scope = "",
           tasks = [], **kwargs):
    # ARCH is predeclared by the engine.
    if ARCH not in sha256:
        fail("binary %s: sha256 has no entry for ARCH=%s" % (name, ARCH))

    if (asset == None) == (assets == None):
        fail("binary %s: set exactly one of 'asset' (template) or 'assets' (per-arch dict)" % name)

    amap = arch_map if arch_map != None else _DEFAULT_ARCH_MAP

    if assets != None:
        if ARCH not in assets:
            fail("binary %s: assets has no entry for ARCH=%s" % (name, ARCH))
        # arch token isn't used for literal assets, but {version} still substitutes.
        arch_token = ""
        asset_path = _subst(assets[ARCH], version, arch_token)
    else:
        if ARCH not in amap:
            fail("binary %s: arch_map has no entry for ARCH=%s" % (name, ARCH))
        arch_token = amap[ARCH]
        asset_path = _subst(asset, version, arch_token)

    # In src paths inside the archive, {arch} substitutes with the same
    # token the URL used (templated form) or with arch_map[ARCH] for the
    # literal-assets form (consistent default).
    src_arch_token = arch_token
    if src_arch_token == "":
        src_arch_token = amap[ARCH] if ARCH in amap else ARCH

    binaries_pairs = _normalise_binaries(binaries, name, version, src_arch_token)
    sha = sha256[ARCH]

    final_base_url = _subst(base_url, version, src_arch_token)
    source_url = final_base_url + "/" + asset_path

    final_install_tree = _subst(install_tree, version, src_arch_token) if install_tree else ""
    final_extras = []
    for e in extras:
        if len(e) == 2:
            final_extras.append((_subst(e[0], version, src_arch_token), e[1]))
        else:
            final_extras.append((_subst(e[0], version, src_arch_token), e[1], e[2]))
    final_symlinks = {}
    for k, v in symlinks.items():
        final_symlinks[k] = _subst(v, version, src_arch_token)

    install_task = task("install", steps = _install_steps(
        name, binaries_pairs, final_install_tree, final_extras, final_symlinks,
    ))
    final_tasks = merge_tasks([install_task], tasks)

    all_deps = list(deps)
    if container and ":" not in container and container not in all_deps:
        all_deps.append(container)

    unit(
        name = name,
        version = version,
        source = source_url,
        sha256 = sha,
        deps = all_deps,
        runtime_deps = runtime_deps,
        tasks = final_tasks,
        services = services,
        conffiles = conffiles,
        license = license,
        description = description,
        scope = scope,
        container = container,
        container_arch = container_arch,
        sandbox = False,
        **kwargs
    )
