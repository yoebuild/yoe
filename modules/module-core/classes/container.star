def container(name, version, dockerfile="Dockerfile", scope="arch", **kwargs):
    unit(
        name=name,
        version=version,
        unit_class="container",
        scope=scope,
        tasks=[
            task("build", fn=lambda: _build_container(name, version, dockerfile)),
        ],
        **kwargs,
    )

def _build_container(name, version, dockerfile):
    arch = ctx.arch
    tag = "yoe/%s:%s-%s" % (name, version, arch)
    # Use buildx for cross-arch builds
    host_arch = run("uname -m", host=True).stdout.strip()
    if host_arch == "aarch64":
        host_arch = "arm64"
    if arch != host_arch:
        run("docker buildx build --platform linux/%s --load -t %s -f %s/%s %s" % (
            arch, tag, name, dockerfile, name), host=True)
    else:
        run("docker build -t %s -f %s/%s %s" % (
            tag, name, dockerfile, name), host=True)
