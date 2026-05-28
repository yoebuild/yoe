package query

import (
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func depProject() *yoestar.Project {
	return &yoestar.Project{
		// DefaultDistro must be set so the runtime-closure walker has
		// a distro to filter against (R21a). All test units are
		// untagged, so the filter is a no-op here.
		DefaultDistro: "alpine",
		UnitsByModule: map[string]map[string]*yoestar.Unit{"": {
			"toolchain-musl": {Name: "toolchain-musl", Class: "container"},
			"zlib":           {Name: "zlib", Class: "unit", Deps: []string{"toolchain-musl"}},
			"openssl":        {Name: "openssl", Class: "unit", Deps: []string{"zlib", "toolchain-musl"}, RuntimeDeps: []string{"zlib"}},
			"apk-tools":      {Name: "apk-tools", Class: "unit", Deps: []string{"openssl", "zlib", "toolchain-musl"}, RuntimeDeps: []string{"openssl", "zlib"}},
			"base-image":     {Name: "base-image", Class: "image", Artifacts: []string{"openssl", "apk-tools"}},
		}},
		Provides: map[string]string{},
	}
}

func TestClosure_Leaf(t *testing.T) {
	proj := depProject()
	got := BuildInClosure(proj, "toolchain-musl")
	if !got["toolchain-musl"] || len(got) != 1 {
		t.Fatalf("toolchain-musl closure: %v", got)
	}
}

func TestClosure_BuildDeps(t *testing.T) {
	proj := depProject()
	got := BuildInClosure(proj, "openssl")
	for _, want := range []string{"openssl", "zlib", "toolchain-musl"} {
		if !got[want] {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	if got["apk-tools"] {
		t.Fatalf("closure leaked upward: %v", got)
	}
}

func TestClosure_RuntimeDepsViaProvides(t *testing.T) {
	proj := depProject()
	proj.UnitsByModule[""]["libcrypto3"] = &yoestar.Unit{Name: "libcrypto3"}
	proj.UnitsByModule[""]["consumer"] = &yoestar.Unit{Name: "consumer", RuntimeDeps: []string{"libcrypto3"}}
	proj.Provides["libcrypto3"] = "openssl"
	got := BuildInClosure(proj, "consumer")
	if !got["openssl"] {
		t.Fatalf("expected libcrypto3 → openssl via Provides, got %v", got)
	}
	if got["libcrypto3"] {
		t.Fatalf("Provides routing should redirect, not include the virtual: %v", got)
	}
}

func TestClosure_ImageArtifacts(t *testing.T) {
	proj := depProject()
	got := BuildInClosure(proj, "base-image")
	for _, want := range []string{"base-image", "openssl", "apk-tools", "zlib", "toolchain-musl"} {
		if !got[want] {
			t.Fatalf("base-image closure missing %q in %v", want, got)
		}
	}
}

func TestClosure_Cycle(t *testing.T) {
	proj := &yoestar.Project{UnitsByModule: map[string]map[string]*yoestar.Unit{"": {
		"a": {Name: "a", Deps: []string{"b"}},
		"b": {Name: "b", Deps: []string{"a"}},
	}}}
	got := BuildInClosure(proj, "a")
	if !got["a"] || !got["b"] || len(got) != 2 {
		t.Fatalf("cycle: %v", got)
	}
}

func TestClosure_MissingRoot(t *testing.T) {
	proj := depProject()
	if got := BuildInClosure(proj, "nonexistent"); got != nil {
		t.Fatalf("missing root: expected nil, got %v", got)
	}
}

// TestClosure_VirtualResolutionIsDistroAware: a virtual like "toolchain"
// has two distro-scoped providers; the closure walker must pick the
// provider whose Distro matches the walker's effective distro instead
// of whichever provider happens to win the global proj.Provides slot.
// Without distro-aware resolution, a debian dev-image closure would
// pull toolchain-musl through the global table's winner — exactly the
// shape that originally let alpine-tagged toolchains leak into debian
// closures.
func TestClosure_VirtualResolutionIsDistroAware(t *testing.T) {
	proj := &yoestar.Project{
		DefaultDistro: "debian",
		UnitsByModule: map[string]map[string]*yoestar.Unit{
			"alpine.main": {
				"toolchain-musl": {Name: "toolchain-musl", Class: "container", Distro: "alpine", Module: "alpine.main", ModuleIndex: -1, Provides: []string{"toolchain"}},
			},
			"debian.main": {
				"toolchain-glibc": {Name: "toolchain-glibc", Class: "container", Distro: "debian", Module: "debian.main", ModuleIndex: -2, Provides: []string{"toolchain"}},
			},
			"module-core": {
				"consumer":  {Name: "consumer", Class: "unit", Deps: []string{"toolchain"}, ModuleIndex: 5},
				"dev-image": {Name: "dev-image", Class: "image", Distro: "debian", Artifacts: []string{"consumer"}, ModuleIndex: 5},
			},
		},
		// Mimic the loader: the global wins by registration order;
		// here alpine.main loaded first leaves toolchain-musl in the
		// global slot. A naive (non-distro-aware) walker would then
		// pull toolchain-musl into the debian closure.
		Provides: map[string]string{"toolchain": "toolchain-musl"},
	}
	proj.DistroViews = map[string]map[string]*yoestar.Unit{}
	// Hand-build the per-distro views since we're not running the loader.
	for _, distro := range []string{"alpine", "debian"} {
		view := map[string]*yoestar.Unit{}
		for _, byName := range proj.UnitsByModule {
			for n, u := range byName {
				if u.Distro == "" || u.Distro == distro {
					if cur, ok := view[n]; !ok || u.ModuleIndex > cur.ModuleIndex {
						view[n] = u
					}
				}
			}
		}
		proj.DistroViews[distro] = view
	}

	got := BuildInClosure(proj, "dev-image")
	if !got["toolchain-glibc"] {
		t.Fatalf("debian closure should include toolchain-glibc; got %v", got)
	}
	if got["toolchain-musl"] {
		t.Fatalf("debian closure should NOT include toolchain-musl (it's alpine-tagged); got %v", got)
	}
}
