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
// modules for a same-name unit that does match.
//
// Cross-distro name collisions (e.g. alpine.main and debian.main both
// export a "libcap2") are resolved by prefer_modules pins or by
// module priority, not by probing every synthetic on every lookup. The
// probe approach was tried and pulled in the full per-call cost of
// dpkg.MaterializeUnit (Provides resolution, Depends parsing) for names
// the walker would discard — a multi-GB hot loop. Keep the walker linear:
// one lookup, first match wins.
//
// Returns (nil, nil) when no provider has the name; the caller decides
// whether that's an error or a search miss.
func (e *Engine) lookupOrMaterialize(rawName, effectiveDistro string) (*Unit, error) {
	name := e.resolveProvidesForDistro(rawName, effectiveDistro)

	// prefer_modules per-distro pin: when the consuming closure's
	// effective distro has a pin for this name, look the pinned module
	// up first. A pin to a synthetic feed module (alpine.main,
	// debian.main) materializes the feed's unit; a pin to a real
	// module returns the unit registered from that module. Either way
	// the pin overrides the default catalog lookup so the pinned
	// module wins even when a higher-priority real module would
	// otherwise satisfy the name.
	if effectiveDistro != "" && e.project != nil {
		if pins, ok := e.project.PreferModules[effectiveDistro]; ok {
			if pinned, ok := pins[name]; ok && pinned != "" {
				u, err := e.lookupInModule(name, pinned, effectiveDistro)
				if err != nil {
					return nil, err
				}
				if u != nil {
					return u, nil
				}
				// Pin's target couldn't satisfy (filtered out or
				// missing). Fall through to default resolution.
			}
		}
	}

	if u, ok := e.units[name]; ok {
		if visibleToDistro(u, effectiveDistro) {
			return u, nil
		}
		// A real unit exists but is tagged for a different distro.
		// First check the per-module catalog for a same-name unit
		// matching effectiveDistro that's already been registered or
		// materialized by an earlier walk — this is the cross-distro
		// collision case (alpine.main and debian.main both define
		// libssl3). Falling straight through to synthetic walk would
		// re-materialize on every lookup; the per-module catalog
		// caches all variants once each.
		if alt := e.findVisibleByName(name, effectiveDistro); alt != nil {
			return alt, nil
		}
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
			continue
		}
		e.mu.Lock()
		u.ModuleIndex = sm.Priority
		// Register under the bare name only if not already taken,
		// so the first-evaluated image's resolution stays visible
		// to legacy consumers.
		if _, ok := e.units[name]; !ok {
			e.units[name] = u
		}
		// Always store in the per-module catalog under the synthetic
		// module's name. This is what enables the cross-distro
		// fallback above: even when e.units holds a different
		// distro's variant, the per-module map has every
		// materialization keyed by its source module.
		u.Module = sm.Name
		e.storeByModule(u)
		existing := e.units[name]
		e.mu.Unlock()
		if visibleToDistro(existing, effectiveDistro) && existing.Distro == effectiveDistro {
			return existing, nil
		}
		return u, nil
	}
	return nil, nil
}

// findVisibleByName scans the per-module catalog for any unit named
// `name` that's visible to effectiveDistro. Returns the highest-
// priority (highest ModuleIndex; later-declared modules win, and
// real modules always outrank synthetics) match, or nil. Holds e.mu.
func (e *Engine) findVisibleByName(name, effectiveDistro string) *Unit {
	e.mu.Lock()
	defer e.mu.Unlock()
	var best *Unit
	for _, byName := range e.unitsByModule {
		u, ok := byName[name]
		if !ok {
			continue
		}
		if !visibleToDistro(u, effectiveDistro) {
			continue
		}
		if best == nil || u.ModuleIndex > best.ModuleIndex {
			best = u
		}
	}
	return best
}

// lookupInModule resolves name through a specific module — either a
// real module (consult e.units, accept the registration if
// u.Module == moduleName), or a synthetic feed module (materialize
// via sm.Lookup). Returns (nil, nil) when the named module doesn't
// satisfy the request — the caller falls through to default lookup.
func (e *Engine) lookupInModule(name, moduleName, effectiveDistro string) (*Unit, error) {
	// Synthetic module path first — feed modules satisfy most pins.
	for _, sm := range e.syntheticModules {
		if sm.Name != moduleName {
			continue
		}
		u, err := sm.Lookup(name)
		if err != nil {
			return nil, fmt.Errorf("synthetic module %q lookup %q: %w", sm.Name, name, err)
		}
		if u == nil {
			return nil, nil
		}
		if !visibleToDistro(u, effectiveDistro) {
			return nil, nil
		}
		u.ModuleIndex = sm.Priority
		u.Module = sm.Name
		// Cache the materialization in the per-module catalog so the
		// next walk for any distro finds it without re-running
		// sm.Lookup.
		e.mu.Lock()
		e.storeByModule(u)
		e.mu.Unlock()
		return u, nil
	}
	// Real module path — the unit must already be registered under
	// the bare name from the named module. Consult the per-module
	// catalog so cross-distro siblings are reachable even when
	// e.units[name] holds a different module's variant.
	if u := e.findInModuleByName(name, moduleName); u != nil && visibleToDistro(u, effectiveDistro) {
		return u, nil
	}
	return nil, nil
}

// findInModuleByName returns the unit named `name` from `moduleName`
// via the per-module catalog. Holds e.mu briefly.
func (e *Engine) findInModuleByName(name, moduleName string) *Unit {
	e.mu.Lock()
	defer e.mu.Unlock()
	if byName, ok := e.unitsByModule[moduleName]; ok {
		return byName[name]
	}
	return nil
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
