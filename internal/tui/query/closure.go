package query

import (
	"github.com/yoebuild/yoe/internal/resolve"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// BuildInClosure returns the set of unit names reachable from root by
// walking build-time deps (Unit.Deps), runtime deps (Unit.RuntimeDeps,
// routed through proj.Provides), and — for image units — the artifact
// list. The root is included.
//
// Returns nil if root is not a unit in proj. Callers treat nil as "match
// nothing", which matches the spec's `in:nonexistent` failure mode.
//
// Cycles and missing dependency names are tolerated silently: the walker
// never recurses into a name it has already visited, and missing names
// are skipped (the build planner is responsible for flagging them).
func BuildInClosure(proj *yoestar.Project, root string) map[string]bool {
	if proj == nil || proj.Units == nil {
		return nil
	}
	if _, ok := proj.Units[root]; !ok {
		return nil
	}

	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if real, ok := proj.Provides[name]; ok {
			name = real
		}
		u, ok := proj.Units[name]
		if !ok || seen[name] {
			return
		}
		seen[name] = true
		for _, dep := range u.Deps {
			walk(dep)
		}
		// image units carry their package list in Artifacts; treat those
		// as deps for closure purposes (resolve.BuildDAG does the same
		// promotion when constructing the build graph).
		if u.Class == "image" {
			for _, a := range u.Artifacts {
				walk(a)
			}
		}
	}
	walk(root)

	// Union runtime-dep closure rooted at the same unit. RuntimeClosure
	// already routes through proj.Provides.
	for _, name := range resolve.RuntimeClosure(proj, []string{root}) {
		seen[name] = true
	}
	return seen
}
