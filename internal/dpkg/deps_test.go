package dpkg

import "testing"

func TestParseDependency_Alternatives(t *testing.T) {
	// "libssl3 (>= 3.0.0) | libssl1.1": 1 Relation, 2 Possibilities,
	// version constraint on first.
	dep, err := ParseDependency("libssl3 (>= 3.0.0) | libssl1.1")
	if err != nil {
		t.Fatalf("ParseDependency: %v", err)
	}
	if len(dep.Relations) != 1 {
		t.Fatalf("Relations: got %d, want 1", len(dep.Relations))
	}
	if got, want := len(dep.Relations[0].Possibilities), 2; got != want {
		t.Fatalf("Possibilities: got %d, want %d", got, want)
	}
	first := dep.Relations[0].Possibilities[0]
	if first.Name != "libssl3" {
		t.Errorf("first.Name: got %q", first.Name)
	}
	if first.Op != OpGe {
		t.Errorf("first.Op: got %q, want %q", first.Op, OpGe)
	}
	if first.Version != "3.0.0" {
		t.Errorf("first.Version: got %q", first.Version)
	}
	second := dep.Relations[0].Possibilities[1]
	if second.Name != "libssl1.1" {
		t.Errorf("second.Name: got %q", second.Name)
	}
	if second.Op != OpNone {
		t.Errorf("second.Op: got %q, want OpNone", second.Op)
	}
}

func TestParseDependency_ArchQualifier(t *testing.T) {
	dep, err := ParseDependency("libc6:any (>= 2.31)")
	if err != nil {
		t.Fatalf("ParseDependency: %v", err)
	}
	if len(dep.Relations) != 1 || len(dep.Relations[0].Possibilities) != 1 {
		t.Fatalf("unexpected shape: %+v", dep)
	}
	p := dep.Relations[0].Possibilities[0]
	if p.Name != "libc6" {
		t.Errorf("Name: got %q", p.Name)
	}
	if p.Arch != "any" {
		t.Errorf("Arch: got %q, want any", p.Arch)
	}
	if p.Op != OpGe || p.Version != "2.31" {
		t.Errorf("version constraint: got op=%q ver=%q", p.Op, p.Version)
	}
}

func TestParseDependency_Empty(t *testing.T) {
	dep, err := ParseDependency("")
	if err != nil {
		t.Fatalf("ParseDependency on empty input: %v", err)
	}
	if len(dep.Relations) != 0 {
		t.Errorf("Relations on empty input: got %d, want 0", len(dep.Relations))
	}
}

func TestParseProvides_Versioned(t *testing.T) {
	provs, err := ParseProvides("libfoo-abi-1 (= 1.0)")
	if err != nil {
		t.Fatalf("ParseProvides: %v", err)
	}
	if len(provs) != 1 {
		t.Fatalf("provs: got %d, want 1", len(provs))
	}
	if provs[0].Name != "libfoo-abi-1" {
		t.Errorf("Name: got %q", provs[0].Name)
	}
	if provs[0].Op != OpEq || provs[0].Version != "1.0" {
		t.Errorf("version: op=%q ver=%q", provs[0].Op, provs[0].Version)
	}
}

func TestFlattenNames_PicksFirstPossibility(t *testing.T) {
	dep, err := ParseDependency("foo, bar | baz, qux (>= 1)")
	if err != nil {
		t.Fatalf("ParseDependency: %v", err)
	}
	names := dep.FlattenNames()
	want := []string{"foo", "bar", "qux"}
	if len(names) != len(want) {
		t.Fatalf("names: got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d]: got %q, want %q", i, names[i], want[i])
		}
	}
}
