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
				Units: map[string]*Unit{
					"img": {Name: "img", Class: "image", Distro: tc.imageDistro},
				},
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
		Units: map[string]*Unit{
			"foo": {Name: "foo", Class: "unit"},
		},
	}
	if _, err := p.EffectiveDistroForImage("foo"); err == nil {
		t.Fatal("expected error for non-image unit")
	}
}

func TestEffectiveDistroForImage_Missing(t *testing.T) {
	p := &Project{Units: map[string]*Unit{}}
	if _, err := p.EffectiveDistroForImage("absent"); err == nil {
		t.Fatal("expected error for missing image")
	}
}
