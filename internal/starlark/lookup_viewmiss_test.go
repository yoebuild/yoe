package starlark

import "testing"

// A unit materialized after buildDistroViews ran lands in UnitsByModule
// (which shares the engine's live map) but not in the frozen per-distro
// view. LookupUnit must still find it by falling through to catalog
// resolution — otherwise it silently vanishes from the DAG, closure
// filters, and dependency trees even though AllUnits/refs can see it.
func TestLookupUnit_ViewMissFallsThroughToCatalog(t *testing.T) {
	lateUnit := &Unit{Name: "libexpat", Class: "unit", Distro: "alpine", Module: "alpine.main"}
	proj := &Project{
		UnitsByModule: map[string]map[string]*Unit{
			"alpine.main": {
				"python3":  {Name: "python3", Class: "unit", Distro: "alpine", Module: "alpine.main"},
				"libexpat": lateUnit,
			},
		},
		// The view was built before libexpat materialized, so it only
		// holds python3.
		DistroViews: map[string]map[string]*Unit{
			"alpine": {
				"python3": {Name: "python3", Class: "unit", Distro: "alpine", Module: "alpine.main"},
			},
		},
	}

	if got := proj.LookupUnit("alpine", "libexpat"); got != lateUnit {
		t.Fatalf("LookupUnit(alpine, libexpat) = %v, want the catalog unit (view-miss fallthrough)", got)
	}

	// A name that isn't in the catalog at all still returns nil.
	if got := proj.LookupUnit("alpine", "nonexistent"); got != nil {
		t.Fatalf("LookupUnit(alpine, nonexistent) = %v, want nil", got)
	}

	// A unit tagged for a different distro must NOT leak into this
	// distro via the fallthrough.
	proj.UnitsByModule["debian.main"] = map[string]*Unit{
		"libc6": {Name: "libc6", Class: "unit", Distro: "debian", Module: "debian.main"},
	}
	if got := proj.LookupUnit("alpine", "libc6"); got != nil {
		t.Fatalf("LookupUnit(alpine, libc6[debian]) = %v, want nil (visibility)", got)
	}
}
