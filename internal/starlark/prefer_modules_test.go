package starlark

import (
	"strings"
	"testing"
)

func TestValidatePreferModules_KnownNamePasses(t *testing.T) {
	proj := &Project{
		PreferModules: map[string]map[string]string{
			"alpine": {"xz": "alpine"},
		},
		ResolvedModules: []ResolvedModule{
			{Name: "alpine"},
			{Name: "module-core"},
		},
	}
	if err := validatePreferModules(proj); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestValidatePreferModules_SyntheticNamePasses(t *testing.T) {
	proj := &Project{
		PreferModules: map[string]map[string]string{
			"alpine": {"openssh": "alpine.main"},
		},
		SyntheticModules: []*SyntheticModule{
			{Name: "alpine.main"},
			{Name: "alpine.community"},
		},
	}
	if err := validatePreferModules(proj); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestValidatePreferModules_UnknownSuggestsQualifiedFeed(t *testing.T) {
	// The classic post-cutover error: project pins to "alpine" but
	// only "alpine.main" and "alpine.community" exist. Error message
	// should name BOTH candidates.
	proj := &Project{
		ResolvedModules: []ResolvedModule{{Name: "module-core"}},
		SyntheticModules: []*SyntheticModule{
			{Name: "alpine.main"},
			{Name: "alpine.community"},
		},
		PreferModules: map[string]map[string]string{
			"alpine": {"xz": "alpine"},
		},
	}

	err := validatePreferModules(proj)
	if err == nil {
		t.Fatal("want fixit error")
	}
	msg := err.Error()
	for _, want := range []string{`"xz"`, `"alpine"`, "alpine.main", "alpine.community"} {
		if !strings.Contains(msg, want) {
			t.Errorf("err missing %q in: %s", want, msg)
		}
	}
	if !strings.Contains(msg, "docs/module-alpine.md") {
		t.Errorf("err missing doc reference: %s", msg)
	}
}

func TestValidatePreferModules_UnknownNoCandidates(t *testing.T) {
	proj := &Project{
		PreferModules: map[string]map[string]string{
			"alpine": {"foo": "completely-unrelated"},
		},
		ResolvedModules:  []ResolvedModule{{Name: "module-core"}},
		SyntheticModules: nil,
	}
	err := validatePreferModules(proj)
	if err == nil {
		t.Fatal("want fixit error")
	}
	if !strings.Contains(err.Error(), `"completely-unrelated"`) {
		t.Errorf("err missing the bogus module name: %s", err)
	}
	if strings.Contains(err.Error(), "Did you mean") {
		t.Errorf("no candidates available; should not suggest: %s", err)
	}
}

func TestValidatePreferModules_NilProject(t *testing.T) {
	if err := validatePreferModules(nil); err != nil {
		t.Errorf("nil project: want nil, got %v", err)
	}
}

func TestValidatePreferModules_EmptyMap(t *testing.T) {
	proj := &Project{ResolvedModules: []ResolvedModule{{Name: "core"}}}
	if err := validatePreferModules(proj); err != nil {
		t.Errorf("empty PreferModules: want nil, got %v", err)
	}
}

func TestSuggestModuleNames_PrefixWinsOverSubstring(t *testing.T) {
	known := map[string]struct{}{
		"alpine.main":      {},
		"alpine.community": {},
		"my-alpine-fork":   {},
		"unrelated":        {},
	}
	got := suggestModuleNames("alpine", known)
	if len(got) < 2 {
		t.Fatalf("want at least 2 suggestions, got %v", got)
	}
	// alpine.* (prefix matches) must rank before my-alpine-fork (substring).
	for _, want := range []string{"alpine.community", "alpine.main"} {
		if !contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
