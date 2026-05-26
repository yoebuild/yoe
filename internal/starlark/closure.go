package starlark

import (
	"fmt"

	"go.starlark.net/starlark"
)

// fnResolveClosure implements the resolve_closure(artifacts) Starlark
// builtin. Replaces the old Starlark-side BFS in
// module-core/classes/image.star:_resolve_runtime_deps, which iterated
// `ctx.runtime_deps` (a pre-populated dict spanning every registered
// unit). That eager dict defeated R20: with 60k+ synthetic units in
// scope it would have allocated 60k *Unit pointers just to drive a
// 300-unit closure.
//
// The Go-side walk uses Engine.Lookup so each referenced name
// materializes once on demand. Synthetic units (materialized via
// SyntheticModule.Lookup) get registered into the engine's units map
// as a side effect, so the build executor's DAG sees them through
// proj.Units exactly like real units.
//
// Returns a topologically sorted list of unit names. Cycles in
// runtime_deps fall through to the tail in arbitrary order — yoe
// surfaces nothing today on dep cycles; if R20's test scenarios call
// for it we can add explicit cycle detection later.
func (e *Engine) fnResolveClosure(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("resolve_closure: takes exactly one positional argument (the list of root names)")
	}
	list, ok := args[0].(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("resolve_closure: argument must be a list of strings, got %s", args[0].Type())
	}
	roots := make([]string, 0, list.Len())
	iter := list.Iterate()
	defer iter.Done()
	var item starlark.Value
	for iter.Next(&item) {
		s, ok := item.(starlark.String)
		if !ok {
			return nil, fmt.Errorf("resolve_closure: list element must be string, got %s", item.Type())
		}
		roots = append(roots, string(s))
	}

	ordered, err := e.closure(roots)
	if err != nil {
		return nil, fmt.Errorf("resolve_closure: %w", err)
	}
	vals := make([]starlark.Value, len(ordered))
	for i, n := range ordered {
		vals[i] = starlark.String(n)
	}
	return starlark.NewList(vals), nil
}

// closure walks the runtime-dep graph rooted at `roots` and returns
// every reachable unit name in topological order (deps before
// dependents). On the way it:
//
//   - Resolves provides — a name like "linux" routes through the
//     engine's provides table to "linux-rpi4" (or whichever unit
//     declares that virtual).
//   - Materializes synthetic units on first reference: when a name
//     isn't in e.units but is exposed by one of the engine's
//     SyntheticModules, the Lookup callback runs and the result is
//     registered into e.units so subsequent BuildDAG sees it.
//
// Missing names (no real unit, no provides match, no synthetic
// provider) error with the offending name in the message — apk would
// have failed at install time otherwise; surfacing here makes the
// build's failure mode obvious.
func (e *Engine) closure(roots []string) ([]string, error) {
	// First pass: BFS to materialize every reachable unit.
	seen := make(map[string]bool, len(roots)*4)
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		u, err := e.lookupOrMaterialize(name)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, fmt.Errorf("unresolved name %q (not in any module, no provider)", name)
		}
		seen[u.Name] = true
		for _, dep := range u.RuntimeDeps {
			if seen[dep] {
				continue
			}
			queue = append(queue, dep)
		}
	}

	// Second pass: topological sort. Emit any unit whose deps are
	// all already emitted; iterate until fixpoint. Bounds on iters
	// match Starlark's old `len(remaining) + 1` for parity.
	remaining := make([]string, 0, len(seen))
	for n := range seen {
		remaining = append(remaining, n)
	}
	emitted := make(map[string]bool, len(remaining))
	ordered := make([]string, 0, len(remaining))
	for range len(remaining) + 1 {
		next := remaining[:0]
		for _, name := range remaining {
			u, _ := e.lookupOrMaterialize(name)
			ready := true
			if u != nil {
				for _, dep := range u.RuntimeDeps {
					resolved := e.resolveProvides(dep)
					if seen[resolved] && !emitted[resolved] {
						ready = false
						break
					}
				}
			}
			if ready {
				ordered = append(ordered, name)
				emitted[name] = true
			} else {
				next = append(next, name)
			}
		}
		if len(next) == len(remaining) {
			// No progress this round — append the rest (cycle or
			// degenerate case) and stop. Matches Starlark's behavior.
			ordered = append(ordered, next...)
			return ordered, nil
		}
		remaining = next
		if len(remaining) == 0 {
			return ordered, nil
		}
	}
	return ordered, nil
}

// lookupOrMaterialize returns the *Unit registered under name. It first
// consults e.units (the catalog of real units), then walks the engine's
// synthetic modules in priority order. Successful synthetic lookups
// register the materialized *Unit into e.units so subsequent calls hit
// the catalog and BuildDAG sees them.
//
// Returns (nil, nil) when no provider has the name; the caller decides
// whether that's an error or a search miss.
func (e *Engine) lookupOrMaterialize(rawName string) (*Unit, error) {
	name := e.resolveProvides(rawName)
	if u, ok := e.units[name]; ok {
		return u, nil
	}
	// Walk synthetics in priority order.
	for _, sm := range e.syntheticModules {
		u, err := sm.Lookup(name)
		if err != nil {
			return nil, fmt.Errorf("synthetic module %q lookup %q: %w", sm.Name, name, err)
		}
		if u == nil {
			continue
		}
		// Register into the catalog so BuildDAG sees it. Use a
		// minimal subset of registerUnit's logic — synthetic units
		// don't compete for `prefer_modules` pins (those name real
		// modules) and they always rank below real modules, so the
		// shadow logic doesn't apply.
		e.mu.Lock()
		if existing, ok := e.units[name]; ok {
			e.mu.Unlock()
			return existing, nil
		}
		u.ModuleIndex = sm.Priority
		e.units[name] = u
		e.mu.Unlock()
		return u, nil
	}
	return nil, nil
}

// resolveProvides walks the engine's provides map once: if `name` is
// the alias side of a provides entry, return the providing unit's
// canonical name. Otherwise return name unchanged.
//
// The Go-side mirror of the provides map is maintained on Project
// (proj.Provides), but the Starlark-side ctx.provides dict is the
// authoritative one during evaluation; we read directly from the
// engine's project field.
func (e *Engine) resolveProvides(name string) string {
	if e.project == nil {
		return name
	}
	if mapped, ok := e.project.Provides[name]; ok && mapped != "" {
		return mapped
	}
	return name
}
