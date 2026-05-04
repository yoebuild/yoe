package resolve

import (
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// RuntimeClosure returns the unit names reachable from `roots` by walking
// `runtime_deps`, with each dep routed through `proj.Provides` so that
// virtual names resolve to the concrete unit that won override resolution.
//
// The returned slice includes the roots themselves and every transitive
// runtime dep, deduplicated. Order is not significant — callers that need
// build ordering pass the result to BuildDAG / TopologicalSort.
//
// Names not present in `proj.Units` after provides routing are silently
// skipped: a missing runtime dep is the build planner's job to flag, not
// this walker's. Same goes for cycles — a `seen` set prevents infinite
// recursion; cycles in runtime_deps are not currently a build error.
//
// Use this from any caller that needs to ensure the runtime closure of a
// unit is built and published — most importantly `yoe deploy`, where a
// single-unit deploy must drag in everything `apk add` will need on the
// device, since image() does the same expansion in Starlark for image
// builds but the deploy path bypasses image().
func RuntimeClosure(proj *yoestar.Project, roots []string) []string {
	seen := make(map[string]bool, len(roots))
	var queue []string

	visit := func(name string) {
		if real, ok := proj.Provides[name]; ok {
			name = real
		}
		if _, ok := proj.Units[name]; !ok {
			return
		}
		if seen[name] {
			return
		}
		seen[name] = true
		queue = append(queue, name)
	}

	for _, r := range roots {
		visit(r)
	}

	for i := 0; i < len(queue); i++ {
		u := proj.Units[queue[i]]
		for _, dep := range u.RuntimeDeps {
			visit(dep)
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}
