package starlark

import (
	"slices"
	"sort"
	"testing"
)

// makeTestEngine returns an engine pre-populated with a tiny project
// + units suitable for closure-walk testing.
func makeTestEngine() *Engine {
	e := NewEngine()
	e.project = &Project{
		Provides: map[string]string{
			"linux": "linux-generic",
		},
	}
	register := func(u *Unit) {
		e.units[u.Name] = u
	}
	register(&Unit{Name: "musl", Class: "unit"})
	register(&Unit{Name: "zlib", Class: "unit", RuntimeDeps: []string{"musl"}})
	register(&Unit{Name: "openssl", Class: "unit", RuntimeDeps: []string{"musl", "zlib"}})
	register(&Unit{Name: "openssh", Class: "unit", RuntimeDeps: []string{"openssl", "musl"}})
	register(&Unit{Name: "linux-generic", Class: "unit"})
	return e
}

func TestClosure_TransitiveResolution(t *testing.T) {
	e := makeTestEngine()
	got, err := e.closure([]string{"openssh"}, "alpine")
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	want := []string{"musl", "zlib", "openssl", "openssh"}
	if !sameSet(got, want) {
		t.Errorf("got %v, want %v (same set)", got, want)
	}

	// Topo order: dep must come before dependent.
	if idx(got, "musl") > idx(got, "zlib") {
		t.Errorf("musl must come before zlib in %v", got)
	}
	if idx(got, "zlib") > idx(got, "openssl") {
		t.Errorf("zlib must come before openssl in %v", got)
	}
	if idx(got, "openssl") > idx(got, "openssh") {
		t.Errorf("openssl must come before openssh in %v", got)
	}
}

func TestClosure_ProvidesResolution(t *testing.T) {
	e := makeTestEngine()
	// Root "linux" should resolve via provides → "linux-generic".
	got, err := e.closure([]string{"linux"}, "alpine")
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	if !slices.Contains(got, "linux-generic") {
		t.Errorf("got %v, want linux-generic in result", got)
	}
}

func TestClosure_UnresolvedName(t *testing.T) {
	e := makeTestEngine()
	_, err := e.closure([]string{"never-heard-of-it"}, "alpine")
	if err == nil {
		t.Fatal("want error for unresolved name")
	}
}

func TestClosure_MaterializesSynthetic(t *testing.T) {
	e := makeTestEngine()
	// A synthetic module that provides "openssh-server" (NOT in
	// e.units) and "musl-extra" with a dep on the existing "musl".
	cache := map[string]*Unit{
		"openssh-server": {Name: "openssh-server", Class: "unit", RuntimeDeps: []string{"musl"}, Module: "alpine.main"},
		"musl-extra":     {Name: "musl-extra", Class: "unit", RuntimeDeps: []string{"musl"}, Module: "alpine.main"},
	}
	sm := &SyntheticModule{
		Name:   "alpine.main",
		Lookup: func(n string) (*Unit, error) { return cache[n], nil },
		Names:  func() []string { return []string{"openssh-server", "musl-extra"} },
	}
	e.syntheticModules = []*SyntheticModule{sm}

	got, err := e.closure([]string{"openssh-server"}, "alpine")
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	want := []string{"musl", "openssh-server"}
	if !sameSet(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// Materialization side effect: openssh-server should now be in
	// e.units so BuildDAG sees it.
	if _, ok := e.units["openssh-server"]; !ok {
		t.Error("openssh-server should have been registered into e.units after Lookup")
	}
	// Untouched names stay out of e.units (laziness check).
	if _, ok := e.units["musl-extra"]; ok {
		t.Error("musl-extra was never referenced; should not be in e.units")
	}
}

func TestClosure_PointerStability(t *testing.T) {
	e := makeTestEngine()
	cache := map[string]*Unit{
		"feed-pkg": {Name: "feed-pkg", Class: "unit", Module: "alpine.main"},
	}
	var lookupCount int
	sm := &SyntheticModule{
		Name: "alpine.main",
		Lookup: func(n string) (*Unit, error) {
			lookupCount++
			return cache[n], nil
		},
		Names: func() []string { return []string{"feed-pkg"} },
	}
	e.syntheticModules = []*SyntheticModule{sm}

	_, err := e.closure([]string{"feed-pkg"}, "alpine")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.closure([]string{"feed-pkg"}, "alpine")
	if err != nil {
		t.Fatal(err)
	}
	// First closure call materializes (Lookup #1 — synthetic walk on
	// empty e.units). Second call holds the untagged unit in e.units;
	// the R21a tagged-wins probe walks synthetics once looking for a
	// distro-tagged variant (Lookup #2), caches the negative result
	// in distroTagCache, and returns the untagged unit. Subsequent
	// closure walks hit the negative cache and avoid Lookup entirely.
	if lookupCount > 2 {
		t.Errorf("lookupCount = %d, want <= 2 (distroTagCache should serialize the tagged-wins probe)", lookupCount)
	}

	// Third call: distroTagCache hit, no Lookup.
	before := lookupCount
	_, err = e.closure([]string{"feed-pkg"}, "alpine")
	if err != nil {
		t.Fatal(err)
	}
	if lookupCount != before {
		t.Errorf("third closure called Lookup %d times; distroTagCache should have prevented it", lookupCount-before)
	}
}

func TestClosure_EmptyRoots(t *testing.T) {
	e := makeTestEngine()
	got, err := e.closure(nil, "alpine")
	if err != nil {
		t.Fatalf("closure(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want []", got)
	}
}

func TestClosure_Cycle(t *testing.T) {
	// A cycle in runtime_deps: a → b → a. Both must surface in the
	// result (no infinite loop, no error — matching Starlark's old
	// behavior of "append remaining" at the tail).
	e := NewEngine()
	e.project = &Project{Provides: map[string]string{}}
	e.units["a"] = &Unit{Name: "a", RuntimeDeps: []string{"b"}}
	e.units["b"] = &Unit{Name: "b", RuntimeDeps: []string{"a"}}

	got, err := e.closure([]string{"a"}, "alpine")
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	sort.Strings(got)
	want := []string{"a", "b"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func idx(ss []string, target string) int {
	for i, s := range ss {
		if s == target {
			return i
		}
	}
	return -1
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	return slices.Equal(as, bs)
}
