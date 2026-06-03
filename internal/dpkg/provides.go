package dpkg

import (
	"strings"

	"pault.ag/go/debian/version"
)

// ProvidesTable maps a virtual name (a package's own bare name or any
// token from its Provides list) to the Entry that provides it. Multiple
// providers of the same virtual resolve to the newest-version entry
// per Debian Policy 7.5.
type ProvidesTable struct {
	byName map[string]*Entry
}

// Lookup returns the provider Entry for name, or nil if no entry in the
// indexed set provides it.
func (p *ProvidesTable) Lookup(name string) *Entry {
	if p == nil {
		return nil
	}
	return p.byName[name]
}

// Names returns every virtual lookup token in the table. Used by the
// TUI search surface — does not materialize any units.
func (p *ProvidesTable) Names() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.byName))
	for n := range p.byName {
		out = append(out, n)
	}
	return out
}

// BuildProvidesTable walks every entry, registers each as a provider of
// its own bare name, then registers every Provides token. Multiple
// providers of the same virtual resolve to the newest-version entry.
//
// The entries slice is borrowed — pointers into it are stored in the
// table, so callers must not mutate or reuse the underlying array after
// building.
func BuildProvidesTable(entries []Entry) *ProvidesTable {
	t := &ProvidesTable{byName: make(map[string]*Entry, len(entries)*2)}
	register := func(token, ver string, e *Entry) {
		token = strings.TrimSpace(token)
		if token == "" {
			return
		}
		if cur, ok := t.byName[token]; ok {
			if newerVersion(ver, cur.Version) {
				t.byName[token] = e
			}
			return
		}
		t.byName[token] = e
	}
	for i := range entries {
		e := &entries[i]
		register(e.Package, e.Version, e)
		if e.Provides == "" {
			continue
		}
		// Provides syntax allows "name (= ver)" entries. Strip any
		// version constraint on the provider side; lookups key on the
		// bare virtual name. Constraint checks stay on the consumer
		// side at resolution time.
		possibilities, err := ParseProvides(e.Provides)
		if err != nil {
			continue
		}
		for _, p := range possibilities {
			register(p.Name, e.Version, e)
		}
	}
	return t
}

// newerVersion returns true if a is strictly newer than b. Malformed
// versions on either side fall back to lexical comparison so a stray
// upstream stanza doesn't kill the build of the provides table.
func newerVersion(a, b string) bool {
	av, errA := version.Parse(a)
	bv, errB := version.Parse(b)
	if errA != nil || errB != nil {
		return a > b
	}
	return version.Compare(av, bv) > 0
}
