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

// BuildDAG constructs a dependency graph from a loaded project.
func BuildDAG(proj *yoestar.Project) (*DAG, error) {
	dag := &DAG{Nodes: make(map[string]*Node)}

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
	for name, unit := range proj.Units {
		deps := append([]string{}, unit.Deps...)
		if unit.Class == "image" {
			deps = append(deps, unit.Artifacts...)
		}
		deps = appendContainerDeps(deps, proj, unit)
		dag.Nodes[name] = &Node{
			Unit: unit,
			Deps:   deps,
		}
	}

	// Validate that all dependencies exist and compute reverse deps
	for name, node := range dag.Nodes {
		for _, dep := range node.Deps {
			target, ok := dag.Nodes[dep]
			if !ok {
				return nil, fmt.Errorf("unit %q depends on %q, which does not exist", name, dep)
			}
			target.Rdeps = append(target.Rdeps, name)
		}
	}

	// Sort rdeps for deterministic output
	for _, node := range dag.Nodes {
		sort.Strings(node.Rdeps)
	}

	return dag, nil
}

// appendContainerDeps adds the unit's container (and any per-task container
// overrides) to deps when the container names a container *unit* in the
// project. External image references (containing ":" or "/", e.g.
// "golang:1.24") and self-references are ignored, and existing entries are
// not duplicated — TopologicalSort's in-degree bookkeeping counts
// len(node.Deps), so a duplicate edge would corrupt ordering.
func appendContainerDeps(deps []string, proj *yoestar.Project, unit *yoestar.Unit) []string {
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
		if _, ok := proj.Units[container]; !ok {
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
