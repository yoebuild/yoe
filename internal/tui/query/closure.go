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
	if proj == nil {
		return nil
	}
	// The TUI query has no image scope, so resolve through the
	// project's effective distro. If neither default_distro nor an
	// override is set, fall back to scanning any module for the root
	// — search-as-you-type UX is best-effort.
	distro, _ := proj.EffectiveDistro()
	rootUnit := proj.LookupUnit(distro, root)
	if rootUnit == nil {
		rootUnit = proj.AnyUnit(root)
	}
	if rootUnit == nil {
		return nil
	}

	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if real, ok := proj.Provides[name]; ok {
			name = real
		}
		u := proj.LookupUnit(distro, name)
		if u == nil {
			u = proj.AnyUnit(name)
		}
		if u == nil || seen[name] {
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
	// already routes through proj.Provides. Empty distro short-circuits
	// (the walker would panic on it).
	if distro != "" {
		for _, name := range resolve.RuntimeClosure(proj, []string{root}, distro) {
			seen[name] = true
		}
	}
	return seen
}
