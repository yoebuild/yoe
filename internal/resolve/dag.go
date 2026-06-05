package resolve

import (
	"fmt"
	"sort"
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// DAG represents the dependency graph of all units in a project.
type DAG struct {
	Nodes map[string]*Node
}

// Node represents a unit in the dependency graph.
type Node struct {
	Unit *yoestar.Unit
	Deps   []string // build-time dependency names
	Rdeps  []string // reverse dependencies (computed)
}

// BuildDAG constructs a dependency graph from a loaded project. When
// effectiveDistro is non-empty, deps are resolved through the per-
// distro view of proj.Provides so virtual references (e.g.
// container="toolchain") pick the variant matching the consuming
// closure. Per-distro filtering of which units appear in the DAG is
// applied AFTER iteration — same-name cross-distro collisions land
// in the catalog at most once, and the DAG passes them through; the
// build executor's per-name filter restricts what actually builds.
func BuildDAG(proj *yoestar.Project, effectiveDistro string) (*DAG, error) {
	dag := &DAG{Nodes: make(map[string]*Node)}

	// Pick the unit source. Per-distro view when distro is known —
	// the closure walker's resolution has already settled — so
	// cross-distro same-name collisions yield the right variant in
	// the DAG node.
	//
	// When iterating the per-distro view, unresolvable deps are
	// SKIPPED rather than erroring: an untagged unit (module-core's
	// nodejs-hello) may pull in a dep (nodejs) provided only by a
	// different distro's feed. That's fine — nodejs-hello isn't in
	// any debian image's closure, the build executor's filter
	// prunes it, and the dep validation needn't second-guess
	// catalog completeness. For the distro-less iteration path,
	// missing deps still error to preserve the older invariant
	// callers (describe, graph, refs) rely on.
	var units map[string]*yoestar.Unit
	allowMissingDeps := false
	if effectiveDistro != "" && proj.DistroViews != nil {
		if view, ok := proj.DistroViews[effectiveDistro]; ok {
			units = view
			allowMissingDeps = true
		}
	}
	if units == nil {
		// Distro-less path: collect one entry per unit name across
		// every module via AllUnits. First match wins — same as the
		// legacy flat catalog's registration-order-wins behavior.
		units = map[string]*yoestar.Unit{}
		for name, u := range proj.AllUnits() {
			if _, ok := units[name]; !ok {
				units[name] = u
			}
		}
	}

	// Add all units as nodes.
	// For image units, Artifacts are also dependencies (they must be built
	// before the image can be assembled).
	//
	// A unit's build/task container, when it names a container *unit*
	// (not an external image like "golang:1.24"), is also a build-time
	// dependency: the container image must exist before any task runs
	// inside it. Classes that compile (module-core) declare this in deps
	// explicitly, but prebuilt classes like alpine_pkg set deps=[] and
	// only container="toolchain-musl" — without this implicit edge the
	// container is never scheduled and `docker run` fails on a missing
	// image. (The old EnsureImage() was removed when containers became
	// DAG-participating units.)
	for name, unit := range units {
		// Resolve distro context once: prefer the caller's
		// effectiveDistro (the image being built), then the unit's
		// own tag for untagged-source-unit standalone builds.
		resolveDistro := effectiveDistro
		if resolveDistro == "" {
			resolveDistro = unit.Distro
		}
		// DepsForDistro merges unit.Deps with any distro-specific
		// additions (distro_deps[resolveDistro]) so a unit that says
		// "python3" on alpine and "python3.11" on debian gets the
		// right per-consumer build edges. Plain unit.Deps when no
		// per-distro entry exists.
		deps := append([]string{}, unit.DepsForDistro(resolveDistro)...)
		if unit.Class == "image" {
			deps = append(deps, unit.Artifacts...)
		}
		deps = appendContainerDeps(deps, proj, units, unit, resolveDistro)
		// Build-time dep on a feed-materialized split package (e.g.
		// Debian's python3.11 wrapper) pulls in the package's runtime
		// closure as additional build-time edges, so the actual
		// interpreter (python3.11-minimal), library (libpython3.11-
		// minimal), and stdlib (libpython3.11-stdlib) are scheduled
		// and staged before this unit runs its tasks. Alpine's
		// monolithic apks usually have an empty runtime closure of
		// their own, so the union is a no-op for the alpine path.
		// Image artifacts use their own runtime closure pass at
		// rootfs assembly time, so skip the expansion for them.
		if unit.Class != "image" {
			deps = appendRuntimeClosureOfDeps(deps, units, name, resolveDistro)
		}
		dag.Nodes[name] = &Node{
			Unit: unit,
			Deps: resolveDeps(deps, proj, resolveDistro),
		}
	}

	// Validate that all dependencies exist and compute reverse deps.
	// In the per-distro-view iteration mode, drop edges to missing
	// deps silently: an untagged unit's dep on a feed-only name (e.g.
	// nodejs-hello → nodejs from alpine.main) is naturally
	// unresolvable in the debian view but doesn't represent a real
	// failure — the unit isn't reached by any debian image's closure.
	for name, node := range dag.Nodes {
		filtered := node.Deps[:0]
		for _, dep := range node.Deps {
			target, ok := dag.Nodes[dep]
			if !ok {
				if allowMissingDeps {
					continue
				}
				return nil, fmt.Errorf("unit %q depends on %q, which does not exist", name, dep)
			}
			filtered = append(filtered, dep)
			target.Rdeps = append(target.Rdeps, name)
		}
		node.Deps = filtered
	}

	// Sort rdeps for deterministic output
	for _, node := range dag.Nodes {
		sort.Strings(node.Rdeps)
	}

	return dag, nil
}

// resolveDeps walks a deps list and replaces any virtual names with the
// concrete unit providing them. Per R9, the resolution is distro-aware
// — when distro is set, ResolveProvidesForDistro picks the candidate
// whose Distro matches. When distro is "", falls back to the global
// proj.Provides table.
func resolveDeps(deps []string, proj *yoestar.Project, distro string) []string {
	out := make([]string, 0, len(deps))
	seen := make(map[string]bool, len(deps))
	for _, d := range deps {
		resolved := proj.ResolveProvidesForDistro(d, distro)
		if resolved == "" {
			resolved = d
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, resolved)
	}
	return out
}

// appendRuntimeClosureOfDeps walks the runtime_deps of each entry
// already in deps and appends every transitively-reachable runtime
// dep. The walk is bounded by `units` (the per-distro view from
// BuildDAG), and the consuming unit's own name is excluded so a
// unit that depends on itself's runtime closure doesn't loop. The
// per-distro runtime-deps merge (RuntimeDepsForDistro) applies, so
// distro_runtime_deps additions on a transitive dep show up in the
// consumer's closure under the right consuming distro.
//
// Only runtime deps that name a unit present in `units` become build
// edges: the expansion exists to schedule those units before the
// consumer builds, so a name with no unit in this view can't be
// scheduled and would only create a dangling edge. This matters for
// the distro-less union catalog, where a same-name unit (e.g.
// "python3") may non-deterministically resolve to the Debian variant
// whose runtime closure references split-package names (python3-
// minimal, …) that only exist in the Debian view. Skipping the
// unmaterialized names keeps the distro-less DAG valid regardless of
// which variant the union catalog happened to pick, while the per-
// distro views — where the split packages do resolve — still pull
// them in.
func appendRuntimeClosureOfDeps(deps []string, units map[string]*yoestar.Unit, self, distro string) []string {
	seen := make(map[string]bool, len(deps))
	for _, d := range deps {
		seen[d] = true
	}
	queue := append([]string{}, deps...)
	for i := 0; i < len(queue); i++ {
		u, ok := units[queue[i]]
		if !ok {
			continue
		}
		for _, r := range u.RuntimeDepsForDistro(distro) {
			if r == self || seen[r] {
				continue
			}
			seen[r] = true
			if _, ok := units[r]; !ok {
				continue // no unit to schedule in this view; don't add a dangling edge
			}
			deps = append(deps, r)
			queue = append(queue, r)
		}
	}
	return deps
}

// appendContainerDeps adds the unit's container (and any per-task container
// overrides) to deps when the container names a container *unit* in the
// project. External image references (containing ":" or "/", e.g.
// "golang:1.24") and self-references are ignored, and existing entries are
// not duplicated — TopologicalSort's in-degree bookkeeping counts
// len(node.Deps), so a duplicate edge would corrupt ordering.
//
// `units` is the per-distro view BuildDAG selected (or proj.Units for
// distro-less callers); container deps are validated against this same
// view so the dep edges match the graph nodes.
func appendContainerDeps(deps []string, proj *yoestar.Project, units map[string]*yoestar.Unit, unit *yoestar.Unit, distro string) []string {
	seen := make(map[string]bool, len(deps))
	for _, d := range deps {
		seen[d] = true
	}
	add := func(container string) {
		if container == "" || container == unit.Name {
			return
		}
		if strings.Contains(container, ":") || strings.Contains(container, "/") {
			return // external image reference, not a project unit
		}
		// A virtual container name (e.g. "toolchain") is not itself a unit;
		// resolve it through the provides table to the concrete per-distro
		// container unit (toolchain-debian-13, toolchain-ubuntu-26.04, …)
		// before checking membership, so the container is scheduled as a
		// build dep. resolveDeps dedupes by resolved name, so a source unit
		// that also lists "toolchain" in its deps collapses to one edge.
		if resolved := proj.ResolveProvidesForDistro(container, distro); resolved != "" {
			container = resolved
		}
		if _, ok := units[container]; !ok {
			return // not a known unit; leave dep validation untouched
		}
		if seen[container] {
			return
		}
		seen[container] = true
		deps = append(deps, container)
	}
	add(unit.Container)
	for _, t := range unit.Tasks {
		add(t.Container)
	}
	return deps
}

// TopologicalSort returns units in build order (dependencies before dependents).
// Returns an error if the graph contains a cycle.
func (d *DAG) TopologicalSort() ([]string, error) {
	// Kahn's algorithm
	inDegree := make(map[string]int)
	for name := range d.Nodes {
		inDegree[name] = 0
	}
	for _, node := range d.Nodes {
		for _, dep := range node.Deps {
			inDegree[dep]++ // note: reversed — dep must come first
		}
	}

	// Actually we want: inDegree[x] = number of deps x has (not rdeps)
	// Kahn's: start with nodes that have no dependencies
	inDegree = make(map[string]int)
	for name, node := range d.Nodes {
		inDegree[name] = len(node.Deps)
	}

	// Queue starts with nodes that have no dependencies
	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue) // deterministic order

	var order []string
	for len(queue) > 0 {
		// Pop first
		name := queue[0]
		queue = queue[1:]
		order = append(order, name)

		// For each node that depends on this one, decrement in-degree
		node := d.Nodes[name]
		for _, rdep := range node.Rdeps {
			inDegree[rdep]--
			if inDegree[rdep] == 0 {
				queue = append(queue, rdep)
				sort.Strings(queue) // keep deterministic
			}
		}
	}

	if len(order) != len(d.Nodes) {
		// Find the cycle for a useful error message
		var cycleNodes []string
		for name, deg := range inDegree {
			if deg > 0 {
				cycleNodes = append(cycleNodes, name)
			}
		}
		sort.Strings(cycleNodes)
		return nil, fmt.Errorf("dependency cycle detected involving: %s", strings.Join(cycleNodes, ", "))
	}

	return order, nil
}

// DepsOf returns the transitive dependencies of a unit (not including itself).
func (d *DAG) DepsOf(name string) ([]string, error) {
	if _, ok := d.Nodes[name]; !ok {
		return nil, fmt.Errorf("unit %q not found", name)
	}

	visited := make(map[string]bool)
	var result []string

	var walk func(n string)
	walk = func(n string) {
		node := d.Nodes[n]
		for _, dep := range node.Deps {
			if !visited[dep] {
				visited[dep] = true
				result = append(result, dep)
				walk(dep)
			}
		}
	}

	walk(name)
	sort.Strings(result)
	return result, nil
}

// TransitiveDeps returns all transitive dependencies of a node
// in topological order (deepest deps first). This is used to assemble
// per-unit sysroots from declared dependencies.
func (d *DAG) TransitiveDeps(name string) []string {
	visited := map[string]bool{}
	var result []string
	var walk func(n string)
	walk = func(n string) {
		if visited[n] {
			return
		}
		visited[n] = true
		if node, ok := d.Nodes[n]; ok {
			for _, dep := range node.Deps {
				walk(dep)
			}
		}
		result = append(result, n)
	}
	// Walk deps of the target, not the target itself
	if node, ok := d.Nodes[name]; ok {
		for _, dep := range node.Deps {
			walk(dep)
		}
	}
	return result
}

// RdepsOf returns the transitive reverse dependencies (what depends on name).
func (d *DAG) RdepsOf(name string) ([]string, error) {
	if _, ok := d.Nodes[name]; !ok {
		return nil, fmt.Errorf("unit %q not found", name)
	}

	visited := make(map[string]bool)
	var result []string

	var walk func(n string)
	walk = func(n string) {
		node := d.Nodes[n]
		for _, rdep := range node.Rdeps {
			if !visited[rdep] {
				visited[rdep] = true
				result = append(result, rdep)
				walk(rdep)
			}
		}
	}

	walk(name)
	sort.Strings(result)
	return result, nil
}
