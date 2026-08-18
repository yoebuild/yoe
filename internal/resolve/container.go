package resolve

import (
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// ContainerRef is a resolved container reference.
type ContainerRef struct {
	// Name is the reference after virtual-provides resolution: the
	// concrete container unit's name, or the external image string
	// unchanged.
	Name string

	// External reports that Name is an image published elsewhere
	// ("golang:1.24", "docker.io/library/alpine") rather than a unit this
	// project builds. External images are never scheduled.
	External bool

	// Unit is the container unit Name resolves to, or nil when the
	// reference is external or names nothing this project defines.
	Unit *yoestar.Unit
}

// ResolveContainer resolves the container reference `ref` as seen by
// `consumer`, a unit whose build (or one of whose tasks) runs inside it.
//
// Two things ask this question and they have to give the same answer:
// the DAG decides which container unit gets *built*, and the executor
// decides which image docker *runs*. When those disagree, yoe builds
// container X and runs container Y — a failure that looks like a broken
// toolchain rather than a resolution bug.
//
// A bare name may be virtual: `container = "toolchain"` resolves through
// the provides table to the concrete per-distro unit (toolchain-musl on
// Alpine, toolchain-debian-13 on Debian). Resolution is distro-aware,
// and the distro context is the consuming closure's — an untagged source
// unit pulled into a Debian image must get Debian's toolchain, not
// whichever one the distro-blind provides table happens to list first.
// A unit carrying its own distro tag is only ever reached by that
// distro's closure, so the two agree; the consumer's distro is used
// because it is the one that is always meaningful.
//
// A reference that resolves to nothing is returned as-is with a nil
// Unit: `container = "toolchain-musl"` written literally still works,
// and dep validation is left to decide whether the name exists.
func ResolveContainer(proj *yoestar.Project, consumer *yoestar.Unit, ref, effectiveDistro string) ContainerRef {
	if ref == "" {
		return ContainerRef{}
	}
	// An external image reference carries a tag or a registry path;
	// a project container unit is a bare name.
	if strings.Contains(ref, ":") || strings.Contains(ref, "/") {
		return ContainerRef{Name: ref, External: true}
	}

	distro := effectiveDistro
	if distro == "" && consumer != nil {
		distro = consumer.Distro
	}

	name := ref
	if resolved := proj.ResolveProvidesForDistro(name, distro); resolved != "" {
		name = resolved
	}

	u := proj.LookupUnit(distro, name)
	if u == nil {
		// Fall back across modules so a literal reference finds its
		// container whichever distro registered it.
		u = proj.AnyUnit(name)
	}
	return ContainerRef{Name: name, Unit: u}
}

// ContainerRefsOf returns every container reference a unit's build
// needs: its own, plus any per-task override. Used to add build edges so
// each container is built before the tasks that run inside it.
//
// A reference that resolves back to the consuming unit is included —
// whether that is a self-edge to drop or a real image name to run is the
// caller's call, and the two callers answer it differently.
func ContainerRefsOf(proj *yoestar.Project, unit *yoestar.Unit, effectiveDistro string) []ContainerRef {
	var out []ContainerRef
	add := func(ref string) {
		if r := ResolveContainer(proj, unit, ref, effectiveDistro); r.Name != "" {
			out = append(out, r)
		}
	}
	add(unit.Container)
	for _, t := range unit.Tasks {
		add(t.Container)
	}
	return out
}
