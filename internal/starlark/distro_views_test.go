package starlark

import "testing"

// TestBuildDistroViews_CrossDistroCoexistence: alpine.main and
// debian.main both register a unit named "libssl3" under their own
// module keys; the per-distro views resolve each to its matching
// variant without collision.
func TestBuildDistroViews_CrossDistroCoexistence(t *testing.T) {
	proj := &Project{
		DefaultDistro: "alpine",
		UnitsByModule: map[string]map[string]*Unit{
			"alpine.main": {
				"libssl3": {Name: "libssl3", Distro: "alpine", Module: "alpine.main", ModuleIndex: -1},
			},
			"debian.main": {
				"libssl3": {Name: "libssl3", Distro: "debian", Module: "debian.main", ModuleIndex: -2},
			},
		},
	}
	views := buildDistroViews(proj)
	if got := views["alpine"]["libssl3"]; got == nil || got.Distro != "alpine" {
		t.Errorf("alpine view should hold alpine variant; got %+v", got)
	}
	if got := views["debian"]["libssl3"]; got == nil || got.Distro != "debian" {
		t.Errorf("debian view should hold debian variant; got %+v", got)
	}
}

// TestBuildDistroViews_UntaggedSatisfiesBoth: an untagged module-core
// unit reaches both alpine and debian views.
func TestBuildDistroViews_UntaggedSatisfiesBoth(t *testing.T) {
	proj := &Project{
		DefaultDistro: "alpine",
		PreferModules: map[string]map[string]string{"debian": {}},
		UnitsByModule: map[string]map[string]*Unit{
			"module-core": {
				"openssl": {Name: "openssl", Module: "module-core", ModuleIndex: 5},
			},
		},
	}
	views := buildDistroViews(proj)
	for _, d := range []string{"alpine", "debian"} {
		if got := views[d]["openssl"]; got == nil || got.Distro != "" {
			t.Errorf("%s view should hold untagged openssl; got %+v", d, got)
		}
	}
}

// TestBuildDistroViews_PreferModulesPin: a pinned module wins over
// the default highest-priority resolution for its distro only.
func TestBuildDistroViews_PreferModulesPin(t *testing.T) {
	proj := &Project{
		DefaultDistro: "alpine",
		PreferModules: map[string]map[string]string{
			"alpine": {"xz": "alpine.main"},
		},
		UnitsByModule: map[string]map[string]*Unit{
			"module-core": {
				"xz": {Name: "xz", Module: "module-core", ModuleIndex: 5},
			},
			"alpine.main": {
				"xz": {Name: "xz", Distro: "alpine", Module: "alpine.main", ModuleIndex: -1},
			},
		},
	}
	views := buildDistroViews(proj)
	// Alpine view: pin wins → alpine.main's xz, even though
	// module-core has higher numeric priority.
	if got := views["alpine"]["xz"]; got == nil || got.Module != "alpine.main" {
		t.Errorf("alpine view should hold pinned alpine.main xz; got %+v", got)
	}
}

// TestBuildDistroViews_LookupUnit: the LookupUnit accessor returns
// the resolved unit for a (distro, name) pair.
func TestBuildDistroViews_LookupUnit(t *testing.T) {
	proj := &Project{
		DefaultDistro: "alpine",
		UnitsByModule: map[string]map[string]*Unit{
			"module-core": {
				"openssl": {Name: "openssl", Module: "module-core"},
			},
		},
	}
	proj.DistroViews = buildDistroViews(proj)
	if got := proj.LookupUnit("alpine", "openssl"); got == nil {
		t.Errorf("LookupUnit(alpine, openssl) should return unit, got nil")
	}
	if got := proj.LookupUnit("nonexistent", "openssl"); got != nil {
		t.Errorf("LookupUnit(nonexistent, openssl) should return nil, got %+v", got)
	}
	if got := proj.LookupUnit("alpine", "missing"); got != nil {
		t.Errorf("LookupUnit(alpine, missing) should return nil, got %+v", got)
	}
}

// TestProject_AllUnits: AllUnits iterates over every (name, *Unit)
// pair across modules.
func TestProject_AllUnits(t *testing.T) {
	proj := &Project{
		UnitsByModule: map[string]map[string]*Unit{
			"module-core": {
				"openssl": {Name: "openssl"},
				"zlib":    {Name: "zlib"},
			},
			"alpine.main": {
				"libssl3": {Name: "libssl3", Distro: "alpine"},
			},
		},
	}
	seen := map[string]int{}
	for name := range proj.AllUnits() {
		seen[name]++
	}
	if seen["openssl"] != 1 || seen["zlib"] != 1 || seen["libssl3"] != 1 {
		t.Errorf("AllUnits should yield each name once; got %+v", seen)
	}
}
