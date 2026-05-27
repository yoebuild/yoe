package starlark

import (
	"fmt"

	"go.starlark.net/starlark"
)

// fnResolveClosure implements the resolve_closure(artifacts, distro=...)
// Starlark builtin. The image class computes the consuming image's
// effective distro from the R20a/R21 cascade and passes it as a kwarg;
// the walker uses it to filter R21a-tagged units that don't match.
//
// Replaces the old Starlark-side BFS in module-core/classes/image.star;
// see the long-form rationale below in closure().
func (e *Engine) fnResolveClosure(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
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
	effectiveDistro := kwString(kwargs, "distro")
	if effectiveDistro == "" {
		return nil, fmt.Errorf("resolve_closure: distro kwarg required (the consuming image's effective distro from the R20a/R21 cascade)")
	}

	ordered, err := e.closure(roots, effectiveDistro)
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
//   - Filters per R21a: a unit whose Distro is set and != effectiveDistro
//     is invisible to this walk. Synthetic units still register into
//     e.units (so other walks can find them) but the per-walk filter
//     drops them from this closure.
//
// Missing names (no real unit, no provides match, no synthetic
// provider, or filtered out by distro) error with the offending name
// in the message — apk/dpkg would have failed at install time
// otherwise; surfacing here makes the build's failure mode obvious.
//
// effectiveDistro panics when empty — every closure walk happens in
// the context of an image, and the image's effective distro must
// resolve via the R20a/R21 cascade before the walker runs. The only
// caller without an image scope is `yoe init`-style bootstrap, which
// never walks a closure.
func (e *Engine) closure(roots []string, effectiveDistro string) ([]string, error) {
	if effectiveDistro == "" {
		panic("starlark: closure walker called with empty effectiveDistro (programmer error — R21a requires per-image scope)")
	}
	// First pass: BFS to materialize every reachable unit.
	seen := make(map[string]bool, len(roots)*4)
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		u, err := e.lookupOrMaterialize(name, effectiveDistro)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, fmt.Errorf("unresolved name %q (not in any module, no provider, or filtered by distro=%q)", name, effectiveDistro)
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
			u, _ := e.lookupOrMaterialize(name, effectiveDistro)
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
// Per R21a, a unit whose Distro is set and doesn't match effectiveDistro
// is invisible to this walk — the walker keeps searching synthetic
// modules for a same-name unit that does match. When two real units of
// the same name but different distros register (rare; usually one is
// synthetic), only the last-registered wins; that's a recognized
// limitation of the single-keyed catalog — real/synthetic and
// synthetic/synthetic collisions across distros are the common shape
// and are handled here.
//
// Returns (nil, nil) when no provider has the name; the caller decides
// whether that's an error or a search miss.
func (e *Engine) lookupOrMaterialize(rawName, effectiveDistro string) (*Unit, error) {
	// Distro-aware provides resolution: a virtual like "toolchain"
	// maps to different concrete units across distros. The R9
	// dispatch reads off proj.Provides plus the per-distro index
	// computed from unit metadata.
	name := e.resolveProvidesForDistro(rawName, effectiveDistro)
	if u, ok := e.units[name]; ok {
		if visibleToDistro(u, effectiveDistro) {
			return u, nil
		}
		// A real unit exists but is tagged for a different distro;
		// fall through so a same-name unit in a synthetic module can
		// still satisfy this walk if one exists.
	}
	// Walk synthetics in priority order. A synthetic module that
	// returns a unit matching the effective distro wins even if
	// e.units already has a same-name registration for a different
	// distro.
	for _, sm := range e.syntheticModules {
		u, err := sm.Lookup(name)
		if err != nil {
			return nil, fmt.Errorf("synthetic module %q lookup %q: %w", sm.Name, name, err)
		}
		if u == nil {
			continue
		}
		if !visibleToDistro(u, effectiveDistro) {
			// Wrong distro for this walk; keep searching siblings.
			continue
		}
		// Register into the catalog so BuildDAG sees it. Use a
		// minimal subset of registerUnit's logic — synthetic units
		// don't compete for `prefer_modules` pins (those name real
		// modules) and they always rank below real modules, so the
		// shadow logic doesn't apply. Skip the registration when a
		// same-name unit (for a different distro) is already in the
		// catalog to avoid clobbering the prior registration.
		e.mu.Lock()
		if existing, ok := e.units[name]; ok {
			e.mu.Unlock()
			if visibleToDistro(existing, effectiveDistro) {
				return existing, nil
			}
			// Existing entry is for a different distro; return the
			// synthetic-side match without overwriting the catalog.
			return u, nil
		}
		u.ModuleIndex = sm.Priority
		e.units[name] = u
		e.mu.Unlock()
		return u, nil
	}
	return nil, nil
}

// visibleToDistro returns true when u is visible to a closure walk
// whose consuming image's effective distro is effectiveDistro. A unit
// with empty Distro is visible to every distro (the common case for
// untagged units like openssh-server source builds); a tagged unit is
// visible only to its matching distro.
//
// effectiveDistro == "" means "no filter" — used by build-time
// dep materialization at load time (loader.go), which has no image
// scope. The R21a filter applies only to runtime closure walks.
func visibleToDistro(u *Unit, effectiveDistro string) bool {
	if u == nil {
		return false
	}
	if effectiveDistro == "" {
		return true
	}
	return u.Distro == "" || u.Distro == effectiveDistro
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

// resolveProvidesForDistro is the distro-aware sibling of
// resolveProvides. When a virtual has multiple candidates across
// distros (e.g. "toolchain" provided by both toolchain-musl with
// distro=alpine and toolchain-glibc with distro=debian), picks the
// candidate whose Distro matches effectiveDistro. Falls back to
// proj.Provides when no distro-specific match exists.
func (e *Engine) resolveProvidesForDistro(name, effectiveDistro string) string {
	if e.project == nil {
		return name
	}
	if mapped := e.project.ResolveProvidesForDistro(name, effectiveDistro); mapped != "" {
		return mapped
	}
	return name
}
