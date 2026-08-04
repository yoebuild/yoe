package starlark

import (
	"errors"
	"testing"
)

func TestSyntheticModule_RegisterAndList(t *testing.T) {
	e := NewEngine()
	main := &SyntheticModule{
		Name:   "alpine.main",
		Parent: "alpine",
		Lookup: func(string) (*Unit, error) { return nil, nil },
		Names:  func() []string { return nil },
	}
	community := &SyntheticModule{
		Name:   "alpine.community",
		Parent: "alpine",
		Lookup: func(string) (*Unit, error) { return nil, nil },
		Names:  func() []string { return nil },
	}
	if err := e.RegisterSyntheticModule(main); err != nil {
		t.Fatalf("register main: %v", err)
	}
	if err := e.RegisterSyntheticModule(community); err != nil {
		t.Fatalf("register community: %v", err)
	}
	got := e.SyntheticModules()
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}
	if got[0] != main || got[1] != community {
		t.Errorf("order: got %v, %v; want main, community", got[0].Name, got[1].Name)
	}
}

func TestSyntheticModule_DuplicateName(t *testing.T) {
	e := NewEngine()
	a := &SyntheticModule{Name: "alpine.main",
		Lookup: func(string) (*Unit, error) { return nil, nil },
		Names:  func() []string { return nil }}
	b := &SyntheticModule{Name: "alpine.main",
		Lookup: func(string) (*Unit, error) { return nil, nil },
		Names:  func() []string { return nil }}
	if err := e.RegisterSyntheticModule(a); err != nil {
		t.Fatalf("first: %v", err)
	}
	err := e.RegisterSyntheticModule(b)
	if err == nil {
		t.Fatal("want duplicate error")
	}
	var dup *duplicateSyntheticModuleError
	if !errors.As(err, &dup) {
		t.Errorf("want duplicateSyntheticModuleError, got %T", err)
	}
	if dup.Name != "alpine.main" {
		t.Errorf("Name: got %q", dup.Name)
	}
}

func TestSyntheticModule_MissingCallbacks(t *testing.T) {
	e := NewEngine()
	cases := []struct {
		desc string
		sm   *SyntheticModule
		want error
	}{
		{"nil", nil, errSyntheticModuleMissingName},
		{"empty name", &SyntheticModule{}, errSyntheticModuleMissingName},
		{"no Lookup",
			&SyntheticModule{Name: "a", Names: func() []string { return nil }},
			errSyntheticModuleMissingCallbacks},
		{"no Names",
			&SyntheticModule{Name: "a", Lookup: func(string) (*Unit, error) { return nil, nil }},
			errSyntheticModuleMissingCallbacks},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			err := e.RegisterSyntheticModule(c.sm)
			if !errors.Is(err, c.want) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
}
