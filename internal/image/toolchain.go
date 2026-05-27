package image

import (
	"fmt"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// ResolveToolchainImage returns the Docker image tag for the toolchain
// container matching the consuming image's effective distro. Used by
// disk-image assembly and partition tooling, where the work runs in
// the toolchain container but no source unit drives the choice.
//
// Walks proj.Units for a class="container" unit whose Provides list
// contains "toolchain" AND whose Distro matches effectiveDistro. With
// R21a in place, exactly one such candidate is visible per closure;
// this function reproduces the same selection outside a closure walk.
//
// Returns an error if no toolchain is found — image assembly cannot
// continue without a container to run mkfs/dd/syslinux in.
func ResolveToolchainImage(proj *yoestar.Project, effectiveDistro, arch string) (string, error) {
	if proj == nil {
		return "", fmt.Errorf("image: ResolveToolchainImage: nil project")
	}
	if effectiveDistro == "" {
		return "", fmt.Errorf("image: ResolveToolchainImage: empty effectiveDistro")
	}
	for _, u := range proj.Units {
		if u.Class != "container" {
			continue
		}
		if u.Distro != effectiveDistro {
			continue
		}
		for _, p := range u.Provides {
			if p == "toolchain" {
				return fmt.Sprintf("yoe/%s:%s-%s", u.Name, u.Version, arch), nil
			}
		}
	}
	return "", fmt.Errorf("image: no toolchain container found for distro=%q (expected a container unit with provides=[\"toolchain\"] and matching distro)", effectiveDistro)
}
