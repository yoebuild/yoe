package starlark

import (
	"strings"
	"testing"
)

func TestEffectiveDistroForImage_Cascade(t *testing.T) {
	cases := []struct {
		name              string
		imageDistro       string
		overrideDistro    string
		projectDistro     string
		wantDistro        string
		wantErr           bool
		wantErrContains   string
	}{
		{
			name:        "image.distro wins",
			imageDistro: "debian", overrideDistro: "alpine", projectDistro: "alpine",
			wantDistro: "debian",
		},
		{
			name:           "override wins over project default",
			overrideDistro: "debian", projectDistro: "alpine",
			wantDistro: "debian",
		},
		{
			name:          "project default applies when image+override empty",
			projectDistro: "alpine",
			wantDistro:    "alpine",
		},
		{
			name:            "no distro anywhere is an error",
			wantErr:         true,
			wantErrContains: "no distro",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Project{
				DefaultDistro:         tc.projectDistro,
				DefaultDistroOverride: tc.overrideDistro,
				UnitsByModule: map[string]map[string]*Unit{"": {
					"img": {Name: "img", Class: "image", Distro: tc.imageDistro},
				}},
			}
			got, err := p.EffectiveDistroForImage("img")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error; got distro %q", got)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("error %q missing %q", err, tc.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantDistro {
				t.Errorf("got %q, want %q", got, tc.wantDistro)
			}
		})
	}
}

func TestEffectiveDistroForImage_NotAnImage(t *testing.T) {
	p := &Project{
		UnitsByModule: map[string]map[string]*Unit{"": {
			"foo": {Name: "foo", Class: "unit"},
		}},
	}
	if _, err := p.EffectiveDistroForImage("foo"); err == nil {
		t.Fatal("expected error for non-image unit")
	}
}

func TestEffectiveDistroForImage_Missing(t *testing.T) {
	p := &Project{UnitsByModule: map[string]map[string]*Unit{"": {}}}
	if _, err := p.EffectiveDistroForImage("absent"); err == nil {
		t.Fatal("expected error for missing image")
	}
}

// TestEffectiveDistroForImage_PrefersProjectDistroVariant: when two
// modules ship same-named images, the variant matching the project's
// effective distro wins over module priority. Without this, an alpine
// project with module-debian also loaded would resolve `dev-image` to
// debian.dev-image (higher module index) and `yoe build dev-image`
// would silently build the wrong backend.
func TestEffectiveDistroForImage_PrefersProjectDistroVariant(t *testing.T) {
	p := &Project{
		DefaultDistro: "alpine",
		UnitsByModule: map[string]map[string]*Unit{
			"alpine.main": {
				"dev-image": {Name: "dev-image", Class: "image", Distro: "alpine", Module: "alpine.main", ModuleIndex: 1},
			},
			"debian.main": {
				"dev-image": {Name: "dev-image", Class: "image", Distro: "debian", Module: "debian.main", ModuleIndex: 2},
			},
		},
	}
	p.DistroViews = buildDistroViews(p)
	got, err := p.EffectiveDistroForImage("dev-image")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "alpine" {
		t.Errorf("alpine project should resolve dev-image to alpine variant; got %q", got)
	}

	// Override flips it: debian project with same modules now picks
	// debian.dev-image even though module-alpine still ships its own.
	p.DefaultDistroOverride = "debian"
	got, err = p.EffectiveDistroForImage("dev-image")
	if err != nil {
		t.Fatalf("unexpected error after override: %v", err)
	}
	if got != "debian" {
		t.Errorf("debian-overridden project should resolve dev-image to debian variant; got %q", got)
	}
}

// TestEffectiveDistroForImage_CrossDistroFallback: a project default
// of "alpine" with no alpine variant of the named image (only debian
// ships it) falls back to AnyUnit's pick. The user named a
// debian-only image deliberately; the cascade should land on its
// distro instead of erroring.
func TestEffectiveDistroForImage_CrossDistroFallback(t *testing.T) {
	p := &Project{
		DefaultDistro: "alpine",
		UnitsByModule: map[string]map[string]*Unit{
			"debian.main": {
				"debian-only-image": {Name: "debian-only-image", Class: "image", Distro: "debian", Module: "debian.main", ModuleIndex: 2},
			},
		},
	}
	p.DistroViews = buildDistroViews(p)
	got, err := p.EffectiveDistroForImage("debian-only-image")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "debian" {
		t.Errorf("cross-distro image should resolve via AnyUnit fallback; got %q", got)
	}
}
