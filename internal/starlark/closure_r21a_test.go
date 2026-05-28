package starlark

import (
	"slices"
	"strings"
	"testing"
)

// TestClosure_R21a_TaggedVisibleToOwnDistro: a unit tagged
// distro="alpine" is reachable from an alpine closure.
func TestClosure_R21a_TaggedVisibleToOwnDistro(t *testing.T) {
	e := NewEngine()
	e.project = &Project{Provides: map[string]string{}}
	e.units["apk-tools"] = &Unit{Name: "apk-tools", Class: "unit", Distro: "alpine"}

	got, err := e.closure([]string{"apk-tools"}, "alpine")
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	if !slices.Contains(got, "apk-tools") {
		t.Errorf("apk-tools should be in alpine closure; got %v", got)
	}
}

// TestClosure_R21a_TaggedInvisibleToWrongDistro: a unit tagged
// distro="alpine" is invisible to a debian closure.
func TestClosure_R21a_TaggedInvisibleToWrongDistro(t *testing.T) {
	e := NewEngine()
	e.project = &Project{Provides: map[string]string{}}
	e.units["apk-tools"] = &Unit{Name: "apk-tools", Class: "unit", Distro: "alpine"}

	_, err := e.closure([]string{"apk-tools"}, "debian")
	if err == nil {
		t.Fatal("apk-tools (distro=alpine) should be invisible to debian closure")
	}
	if !strings.Contains(err.Error(), "apk-tools") {
		t.Errorf("error %q should name the offending unit", err)
	}
}

// TestClosure_R21a_UntaggedVisibleToBoth: an untagged unit (the common
// case for source-built libraries like openssl) reaches both alpine
// and debian closures.
func TestClosure_R21a_UntaggedVisibleToBoth(t *testing.T) {
	e := NewEngine()
	e.project = &Project{Provides: map[string]string{}}
	e.units["openssl"] = &Unit{Name: "openssl", Class: "unit"} // empty Distro

	for _, d := range []string{"alpine", "debian"} {
		got, err := e.closure([]string{"openssl"}, d)
		if err != nil {
			t.Errorf("closure(distro=%s): %v", d, err)
			continue
		}
		if !slices.Contains(got, "openssl") {
			t.Errorf("openssl should be in %s closure; got %v", d, got)
		}
	}
}

// TestClosure_R21a_FeedDistroInheritance: an alpine_feed-materialized
// unit (synthesized with Distro="alpine") is filtered out of debian
// closures even though it registers globally.
func TestClosure_R21a_FeedDistroInheritance(t *testing.T) {
	e := NewEngine()
	e.project = &Project{Provides: map[string]string{}}

	cache := map[string]*Unit{
		"busybox": {Name: "busybox", Class: "unit", Distro: "alpine", Module: "alpine.main"},
	}
	sm := &SyntheticModule{
		Name:   "alpine.main",
		Lookup: func(n string) (*Unit, error) { return cache[n], nil },
		Names:  func() []string { return []string{"busybox"} },
	}
	e.syntheticModules = []*SyntheticModule{sm}

	// alpine closure: busybox visible.
	got, err := e.closure([]string{"busybox"}, "alpine")
	if err != nil {
		t.Fatalf("alpine closure: %v", err)
	}
	if !slices.Contains(got, "busybox") {
		t.Errorf("busybox should be in alpine closure; got %v", got)
	}

	// debian closure: busybox invisible — same registration, different walk.
	if _, err := e.closure([]string{"busybox"}, "debian"); err == nil {
		t.Errorf("busybox (alpine-tagged via feed) should be invisible to debian closure")
	}
}

// TestClosure_R21a_TaggedCollisionByDistro: two units share a name but
// different distros — each visible only to its matching closure.
func TestClosure_R21a_TaggedCollisionByDistro(t *testing.T) {
	e := NewEngine()
	e.project = &Project{Provides: map[string]string{}}
	// Real unit registered with one distro; synthetic module provides
	// the other distro under the same name.
	e.units["package-mgr"] = &Unit{Name: "package-mgr", Class: "unit", Distro: "alpine"}

	cache := map[string]*Unit{
		"package-mgr": {Name: "package-mgr", Class: "unit", Distro: "debian"},
	}
	sm := &SyntheticModule{
		Name:   "debian.main",
		Lookup: func(n string) (*Unit, error) { return cache[n], nil },
		Names:  func() []string { return []string{"package-mgr"} },
	}
	e.syntheticModules = []*SyntheticModule{sm}

	if got, err := e.closure([]string{"package-mgr"}, "alpine"); err != nil {
		t.Errorf("alpine closure: %v (got %v)", err, got)
	}
	if got, err := e.closure([]string{"package-mgr"}, "debian"); err != nil {
		t.Errorf("debian closure: %v (got %v)", err, got)
	}
}

// TestClosure_R21a_PanicOnEmptyDistro: the walker panics when called
// with empty effectiveDistro — a programmer error.
func TestClosure_R21a_PanicOnEmptyDistro(t *testing.T) {
	e := NewEngine()
	e.project = &Project{}
	defer func() {
		if r := recover(); r == nil {
			t.Error("closure with empty effectiveDistro should panic")
		}
	}()
	_, _ = e.closure([]string{"anything"}, "")
}
