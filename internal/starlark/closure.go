package starlark

import (
	"fmt"
	"sort"

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
	// First pass: BFS, materializing every reachable unit and recording
	// its resolved dependency names as it goes. Resolving deps here
	// rather than again in the sort keeps one resolver — and one
	// distro context — across both passes; using the distro-blind one
	// for the sort could have ordered against edges the walk never
	// followed.
	deps := make(map[string][]string, len(roots)*4)
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		u, err := e.lookupOrMaterialize(name, effectiveDistro)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, fmt.Errorf("unresolved name %q (not in any module, no provider, or filtered by distro=%q)%s",
				name, effectiveDistro, e.flatKernelHint(name, effectiveDistro))
		}
		if _, ok := deps[u.Name]; ok {
			continue
		}
		resolved := make([]string, 0, len(u.RuntimeDepsForDistro(effectiveDistro)))
		for _, dep := range u.RuntimeDepsForDistro(effectiveDistro) {
			du, err := e.lookupOrMaterialize(dep, effectiveDistro)
			if err != nil {
				return nil, err
			}
			if du == nil {
				return nil, fmt.Errorf("unresolved name %q (runtime dep of %q; not in any module, no provider, or filtered by distro=%q)%s",
					dep, u.Name, effectiveDistro, e.flatKernelHint(dep, effectiveDistro))
			}
			resolved = append(resolved, du.Name)
		}
		deps[u.Name] = resolved
		queue = append(queue, resolved...)
	}

	return topoOrder(deps), nil
}

// topoOrder returns the names in deps ordered so a unit follows
// everything it depends on. deps maps each unit to its already-resolved
// dependency names.
//
// Kahn's algorithm, emitted in waves: every unit whose dependencies are
// all satisfied goes out together, sorted. The sort is what makes the
// result reproducible — this list becomes an image's artifact list, and
// an order that varied between runs would be a difference with no cause.
//
// A cycle leaves units that never reach in-degree zero. They are
// appended in sorted order rather than reported: a runtime-dep cycle
// (two packages depending on each other) is normal in a distribution,
// and both package managers resolve installation order themselves.
func topoOrder(deps map[string][]string) []string {
	indeg := make(map[string]int, len(deps))
	rdeps := make(map[string][]string, len(deps))
	for name, ds := range deps {
		indeg[name] += 0
		counted := make(map[string]bool, len(ds))
		for _, d := range ds {
			// A self-dependency, a dep outside the closure, or a
			// repeated dep would each inflate the in-degree and strand
			// the unit at the end.
			if d == name || counted[d] {
				continue
			}
			if _, ok := deps[d]; !ok {
				continue
			}
			counted[d] = true
			indeg[name]++
			rdeps[d] = append(rdeps[d], name)
		}
	}

	var ready []string
	for name, n := range indeg {
		if n == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)

	ordered := make([]string, 0, len(deps))
	for len(ready) > 0 {
		ordered = append(ordered, ready...)
		var next []string
		for _, name := range ready {
			for _, rd := range rdeps[name] {
				indeg[rd]--
				if indeg[rd] == 0 {
					next = append(next, rd)
				}
			}
		}
		sort.Strings(next)
		ready = next
	}

	if len(ordered) < len(deps) {
		emitted := make(map[string]bool, len(ordered))
		for _, n := range ordered {
			emitted[n] = true
		}
		var stuck []string
		for name := range deps {
			if !emitted[name] {
				stuck = append(stuck, name)
			}
		}
		sort.Strings(stuck)
		ordered = append(ordered, stuck...)
	}
	return ordered
}

// flatKernelHint returns a suffix pointing the machine author at
// distro_unit when the unresolved name is the selected machine's flat
// kernel unit. A machine writing kernel(unit = ...) asserts that one
// kernel works on every distro; when the kernel really is distro-
// specific the assertion breaks here, with a name the reader has no
// reason to connect to their machine definition. Naming the fix is the
// difference between an opaque miss and an actionable one.
//
// Returns "" for every other unresolved name, so the common case reads
// exactly as before.
func (e *Engine) flatKernelHint(name, effectiveDistro string) string {
	if e.project == nil {
		return ""
	}
	m, ok := e.machines[e.project.Defaults.Machine]
	if !ok || m.Kernel.Unit != name {
		return ""
	}
	return fmt.Sprintf("\n  %q is machine %q's kernel, declared as kernel(unit = %q), which claims the same kernel"+
		"\n  works on every distro. If it exists only for some, declare it per distro instead:"+
		"\n      kernel(distro_unit = {%q: %q, ...}, provides = ...)"+
		"\n  Images for the distros it does not list are then marked not-buildable on this machine"+
		"\n  rather than failing here.",
		name, m.Name, name, effectiveDistro, name)
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
		// u is what this name resolves to for this distro. Re-reading
		// e.units[name] here would add nothing: either it was unset and
		// now holds u, or it holds a variant the checks above already
		// established is not visible to effectiveDistro.
		return u, nil
	}
	return nil, nil
}

// findVisibleByName scans the per-module catalog for any unit named
// `name` that's visible to effectiveDistro. Returns the highest-
// priority (highest ModuleIndex; later-declared modules win, and
// real modules always outrank synthetics) match, or nil.
func (e *Engine) findVisibleByName(name, effectiveDistro string) *Unit {
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
		e.storeByModule(u)
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
// via the per-module catalog.
func (e *Engine) findInModuleByName(name, moduleName string) *Unit {
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
