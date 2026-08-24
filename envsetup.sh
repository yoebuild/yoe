#!/usr/bin/env bash
# this file should be sourced (.), not run as a script

OE_BASE=$(readlink -f $(dirname ${BASH_SOURCE[0]:-$0}))

yoe_build() {
	local version
	version=$(cd "${OE_BASE}" && git describe --tags --always --dirty 2>/dev/null || echo "dev")
	CGO_ENABLED=0 go build -ldflags "-X main.version=${version}" -o "${OE_BASE}/yoe" "${OE_BASE}/cmd/yoe" || return 1
}

yoe_test() {
	(cd "${OE_BASE}" && go test ./...) || return 1
}

# Enumerate markdown files via git so the build/ tree (root-owned files
# from image-class builds) and other gitignored caches are never walked.
# Plain `**/*.md` trips EACCES on directories the host user can't scandir.
# Symlinks (e.g. docs/CHANGELOG.md → CHANGELOG.md) are dropped so prettier
# doesn't error on "explicitly specified pattern is a symbolic link".
_yoe_md_files() {
	(cd "${OE_BASE}" && git ls-files '*.md' | while IFS= read -r f; do
		[ -L "$f" ] && continue
		printf '%s\0' "$f"
	done)
}

yoe_format() {
	if command -v prettier >/dev/null 2>&1; then
		(cd "${OE_BASE}" && _yoe_md_files | xargs -0 -r prettier --write) || return 1
	else
		(cd "${OE_BASE}" && _yoe_md_files | xargs -0 -r \
			docker run --rm -i -v "${OE_BASE}:/work" -w /work node:20-alpine \
			npx --yes prettier --write) || return 1
	fi
}

yoe_format_check() {
	if command -v prettier >/dev/null 2>&1; then
		(cd "${OE_BASE}" && _yoe_md_files | xargs -0 -r prettier --check) || return 1
	else
		(cd "${OE_BASE}" && _yoe_md_files | xargs -0 -r \
			docker run --rm -i -v "${OE_BASE}:/work" -w /work node:20-alpine \
			npx --yes prettier --check) || return 1
	fi
}

yoe_sloc() {
	(cd "${OE_BASE}" && scc --count-as 'star:py') || return 1
}

yoe_e2e() {
	yoe_build || return 1
	echo "=== e2e: base-image (x86_64) ==="
	(cd "${OE_BASE}/testdata/e2e-project" && "${OE_BASE}/yoe" build --machine qemu-x86_64 base-image) || return 1
	echo "=== e2e: base-image (arm64 cross) ==="
	(cd "${OE_BASE}/testdata/e2e-project" && "${OE_BASE}/yoe" build --machine qemu-arm64 base-image) || return 1
	echo "=== e2e: all passed ==="
}

yoe_e2e_x86_64() {
	yoe_build || return 1
	echo "=== e2e: base-image (x86_64) ==="
	(cd "${OE_BASE}/testdata/e2e-project" && "${OE_BASE}/yoe" build --machine qemu-x86_64 base-image) || return 1
}

yoe_e2e_arm64() {
	yoe_build || return 1
	echo "=== e2e: base-image (arm64 cross) ==="
	(cd "${OE_BASE}/testdata/e2e-project" && "${OE_BASE}/yoe" build --machine qemu-arm64 base-image) || return 1
}

# --- Release -------------------------------------------------------------------

# Read the version to release out of CHANGELOG.md.
#
# The version is the first numbered section. Entries still sitting under
# [Unreleased] mean the release commit that moves them under a version heading
# has not been written yet, so there is nothing to tag.
_yoe_release_version() {
	local changelog pending version
	changelog="${OE_BASE}/CHANGELOG.md"

	if ! grep -q '^## \[Unreleased\]' "${changelog}"; then
		echo "no [Unreleased] section in CHANGELOG.md" >&2
		return 1
	fi

	pending=$(sed -n '/^## \[Unreleased\]/,/^## \[/p' "${changelog}" |
		sed '1d;$d' | tr -d '[:space:]')
	if [ -n "${pending}" ]; then
		echo "CHANGELOG.md has entries under [Unreleased]; give them a version heading first" >&2
		return 1
	fi

	version=$(grep -m1 -oE '^## \[[0-9]+\.[0-9]+\.[0-9]+\]' "${changelog}" | tr -d '#[] ')
	if [ -z "${version}" ]; then
		echo "no released version section in CHANGELOG.md" >&2
		return 1
	fi

	printf '%s\n' "${version}"
}

# Everything yoe_tag_release does while sitting on main. Split out so the
# caller restores the original branch on every exit path, success or not.
_yoe_tag_release_on_main() {
	local version tag

	(cd "${OE_BASE}" && git merge --ff-only origin/main) || return 1

	version=$(_yoe_release_version) || return 1
	tag="v${version}"

	if (cd "${OE_BASE}" && git rev-parse -q --verify "refs/tags/${tag}" >/dev/null); then
		echo "tag ${tag} already exists locally" >&2
		return 1
	fi
	if (cd "${OE_BASE}" && git ls-remote --exit-code --tags origin "refs/tags/${tag}" >/dev/null 2>&1); then
		echo "tag ${tag} already exists on origin" >&2
		return 1
	fi

	(cd "${OE_BASE}" && git tag "${tag}") || return 1
	if ! (cd "${OE_BASE}" && git push origin "${tag}"); then
		(cd "${OE_BASE}" && git tag -d "${tag}")
		return 1
	fi

	echo "=== tagged and pushed ${tag} ==="
}

# Tag the version named at the top of CHANGELOG.md on main and push the tag.
yoe_tag_release() {
	local branch rc

	if ! (cd "${OE_BASE}" && git diff --quiet && git diff --cached --quiet); then
		echo "yoe_tag_release: uncommitted changes; commit or stash them first" >&2
		return 1
	fi

	branch=$(cd "${OE_BASE}" && git symbolic-ref --quiet --short HEAD)
	if [ -z "${branch}" ]; then
		echo "yoe_tag_release: HEAD is detached; check out a branch first" >&2
		return 1
	fi

	(cd "${OE_BASE}" && git fetch --tags origin) || return 1
	(cd "${OE_BASE}" && git checkout main) || return 1

	_yoe_tag_release_on_main
	rc=$?

	(cd "${OE_BASE}" && git checkout "${branch}") || return 1
	return ${rc}
}

# --- Documentation site (mdBook) ----------------------------------------------
# Requires: cargo install mdbook mdbook-toc

# Generate docs/intro.md from README.md, rewriting paths so links work when
# README is rendered as the docs site's intro page (which lives in docs/).
yoe_gen_intro() {
	(cd "${OE_BASE}" && sed \
		-e 's|](docs/|](|g' \
		-e 's|src="docs/|src="|g' \
		-e 's|](LICENSE)|](https://github.com/yoebuild/yoe/blob/main/LICENSE)|g' \
		README.md > docs/intro.md) || return 1
}

yoe_mdbook() {
	yoe_gen_intro || return 1
	(cd "${OE_BASE}" && mdbook serve -p 3333)
}

yoe_mdbook_build() {
	yoe_gen_intro || return 1
	(cd "${OE_BASE}" && mdbook build) || return 1
}

yoe_mdbook_cleanup() {
	rm -rf "${OE_BASE}/book"
}

yoe_deploy_docs() {
	yoe_mdbook_build || return 1
	rsync -av --delete "${OE_BASE}/book/" yoedistro.org:/srv/http/yoebuild/docs/ || return 1
}
