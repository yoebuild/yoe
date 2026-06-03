package dpkg

import (
	"fmt"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// Providers resolves a dpkg dep token (a bare package name or virtual)
// to the bare package name that satisfies it. The resolver picks the
// first Possibility in each Relation that the providers know about.
//
// Implementations live with the caller: debian_feed wraps a merged
// view across every registered feed (cross-feed deps — security
// packages depending on main libraries). Tests pass a *ProvidesTable
// wrapped via TableProviders.
type Providers interface {
	Resolve(token string) (pkgName string, ok bool)
}

// TableProviders adapts a single ProvidesTable to the Providers
// interface. The wrapper is used by tests and by callers that resolve
// against a single feed; production code wraps multiple tables to
// satisfy cross-feed deps.
type TableProviders struct{ Table *ProvidesTable }

// Resolve walks the table for token. Returns the providing entry's
// Package field.
func (t TableProviders) Resolve(token string) (string, bool) {
	if t.Table == nil {
		return "", false
	}
	e := t.Table.Lookup(token)
	if e == nil {
		return "", false
	}
	return e.Package, true
}

// MaterializeUnit produces a *Unit from one Packages Entry, resolving
// the entry's runtime deps (Depends + Pre-Depends) through the supplied
// Providers.
//
// Conflicts/Breaks/Replaces are not modeled in yoe's resolver — they
// affect install ordering on the target, not the build closure.
// Unresolved tokens skip silently: the closure walker surfaces them
// later, or apt sorts them out at install time when the .deb is
// extracted into a partial rootfs.
//
// Returns the package-metadata portion of a synthetic unit. The caller
// (debian_feed's Lookup wrapper) adds feed-specific transport fields —
// Source URL, container, install task — before handing the unit to the
// build executor.
func MaterializeUnit(entry Entry, providers Providers, moduleName string) (*yoestar.Unit, error) {
	if providers == nil {
		return nil, fmt.Errorf("dpkg: materialize %s: nil Providers", entry.Package)
	}

	depTokens, err := relationTokens(entry.PreDepends + ", " + entry.Depends)
	if err != nil {
		return nil, fmt.Errorf("dpkg: materialize %s: %w", entry.Package, err)
	}
	runtimeDeps := make([]string, 0, len(depTokens))
	seen := make(map[string]struct{}, len(depTokens))
	for _, t := range depTokens {
		pkg, ok := providers.Resolve(t)
		if !ok {
			continue
		}
		if pkg == entry.Package {
			continue
		}
		if _, dup := seen[pkg]; dup {
			continue
		}
		seen[pkg] = struct{}{}
		runtimeDeps = append(runtimeDeps, pkg)
	}

	// Provides list: extract bare names from the Provides line. Drop
	// version qualifiers; the resolver keys on the virtual name only.
	provides, err := bareProvides(entry.Provides)
	if err != nil {
		return nil, fmt.Errorf("dpkg: materialize %s: provides: %w", entry.Package, err)
	}

	u := &yoestar.Unit{
		Name:        entry.Package,
		Class:       "unit",
		Description: entry.Description,
		Version:     entry.Version,
		RuntimeDeps: runtimeDeps,
		Provides:    provides,
		Module:      moduleName,
		Distro:      "debian",
	}
	return u, nil
}

// relationTokens walks every Relation in a dep line and emits the bare
// name of the first Possibility in each (yoe's resolve-by-name model;
// alternatives "foo | bar" resolve to whichever name a provider matches
// first). Empty strings between commas are dropped.
func relationTokens(line string) ([]string, error) {
	dep, err := ParseDependency(line)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(dep.Relations))
	for _, rel := range dep.Relations {
		for _, p := range rel.Possibilities {
			if p.Name == "" {
				continue
			}
			out = append(out, p.Name)
			break
		}
	}
	return out, nil
}

// bareProvides parses a Provides line and returns the bare virtual
// names. "libfoo-abi-1 (= 1.0), libbar" -> ["libfoo-abi-1", "libbar"].
func bareProvides(line string) ([]string, error) {
	if line == "" {
		return nil, nil
	}
	provs, err := ParseProvides(line)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(provs))
	for _, p := range provs {
		if p.Name != "" {
			out = append(out, p.Name)
		}
	}
	return out, nil
}
