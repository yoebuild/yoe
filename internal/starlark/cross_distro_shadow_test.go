package starlark

import "testing"

// Two modules shipping a same-named unit for different distros is the
// exact coexistence the per-module catalog exists for — module-debian
// and module-ubuntu both define dev-image. Neither shadows the other:
// each is only ever reached by its own distro's closure.
//
// Registration must keep both, and must not report a shadow. Reporting
// one is misleading on its own; dropping the loser from the per-module
// catalog would make that distro's image unresolvable.
func TestRegisterUnit_CrossDistroSameNameCoexists(t *testing.T) {
	eng := NewEngine()

	eng.SetCurrentModule("module-debian", 1)
	if err := eng.ExecString("images/dev-image.star",
		`image(name = "dev-image", version = "1.0", distro = "debian")`+"\n"); err != nil {
		t.Fatalf("registering debian dev-image: %v", err)
	}

	eng.SetCurrentModule("module-ubuntu", 2)
	if err := eng.ExecString("images/dev-image.star",
		`image(name = "dev-image", version = "1.0", distro = "ubuntu")`+"\n"); err != nil {
		t.Fatalf("registering ubuntu dev-image: %v", err)
	}

	byModule := eng.UnitsByModule()
	for _, mod := range []string{"module-debian", "module-ubuntu"} {
		u, ok := byModule[mod]["dev-image"]
		if !ok || u == nil {
			t.Errorf("%s's dev-image is missing from the per-module catalog", mod)
			continue
		}
		want := map[string]string{"module-debian": "debian", "module-ubuntu": "ubuntu"}[mod]
		if u.Distro != want {
			t.Errorf("%s's dev-image has distro %q, want %q", mod, u.Distro, want)
		}
	}

	if len(eng.Shadows()) != 0 {
		t.Errorf("units for different distros were reported as shadowing: %+v", eng.Shadows())
	}
}

// Same as above but with the lower-priority module registering second.
// That order takes the shadow-loser branch, which returns before storing
// into the per-module catalog — so the second distro's image would be
// dropped outright rather than merely mis-reported.
func TestRegisterUnit_CrossDistroSameNameCoexists_LowPriorityLast(t *testing.T) {
	eng := NewEngine()

	eng.SetCurrentModule("module-ubuntu", 2)
	if err := eng.ExecString("images/dev-image.star",
		`image(name = "dev-image", version = "1.0", distro = "ubuntu")`+"\n"); err != nil {
		t.Fatalf("registering ubuntu dev-image: %v", err)
	}
	eng.SetCurrentModule("module-debian", 1)
	if err := eng.ExecString("images/dev-image.star",
		`image(name = "dev-image", version = "1.0", distro = "debian")`+"\n"); err != nil {
		t.Fatalf("registering debian dev-image: %v", err)
	}

	byModule := eng.UnitsByModule()
	if u, ok := byModule["module-debian"]["dev-image"]; !ok || u == nil {
		t.Error("debian's dev-image was dropped from the per-module catalog")
	}
	if u, ok := byModule["module-ubuntu"]["dev-image"]; !ok || u == nil {
		t.Error("ubuntu's dev-image is missing from the per-module catalog")
	}
	if len(eng.Shadows()) != 0 {
		t.Errorf("units for different distros were reported as shadowing: %+v", eng.Shadows())
	}
}

// A same-name collision between two units that really can be reached by
// the same closure — both untagged, or both tagged for one distro — is
// still a shadow.
func TestRegisterUnit_SameDistroStillShadows(t *testing.T) {
	eng := NewEngine()

	eng.SetCurrentModule("module-a", 1)
	if err := eng.ExecString("units/hello.star",
		`unit(name = "hello", version = "1.0")`+"\n"); err != nil {
		t.Fatalf("registering module-a hello: %v", err)
	}
	eng.SetCurrentModule("module-b", 2)
	if err := eng.ExecString("units/hello.star",
		`unit(name = "hello", version = "2.0")`+"\n"); err != nil {
		t.Fatalf("registering module-b hello: %v", err)
	}

	if len(eng.Shadows()) != 1 {
		t.Fatalf("got %d shadow events, want 1: %+v", len(eng.Shadows()), eng.Shadows())
	}
	if got := eng.Shadows()[0].WinnerModule; got != "module-b" {
		t.Errorf("winner = %q, want module-b (higher priority)", got)
	}
}
