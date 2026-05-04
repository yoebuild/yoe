package resolve

import (
	"strings"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func makeProject(units map[string]*yoestar.Unit) *yoestar.Project {
	return &yoestar.Project{
		Name:    "test",
		Units: units,
	}
}

func TestBuildDAG(t *testing.T) {
	proj := makeProject(map[string]*yoestar.Unit{
		"zlib":    {Name: "zlib", Deps: nil},
		"openssl": {Name: "openssl", Deps: []string{"zlib"}},
		"openssh": {Name: "openssh", Deps: []string{"zlib", "openssl"}},
	})

	dag, err := BuildDAG(proj)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	if len(dag.Nodes) != 3 {
		t.Errorf("got %d nodes, want 3", len(dag.Nodes))
	}

	// Check reverse deps
	zlibRdeps := dag.Nodes["zlib"].Rdeps
	if len(zlibRdeps) != 2 {
		t.Errorf("zlib rdeps = %v, want 2 entries", zlibRdeps)
	}
}

func TestBuildDAG_MissingDep(t *testing.T) {
	proj := makeProject(map[string]*yoestar.Unit{
		"openssh": {Name: "openssh", Deps: []string{"nonexistent"}},
	})

	_, err := BuildDAG(proj)
	if err == nil {
		t.Fatal("expected error for missing dependency, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error %q should mention missing dep", err)
	}
}

func TestTopologicalSort(t *testing.T) {
	proj := makeProject(map[string]*yoestar.Unit{
		"zlib":    {Name: "zlib", Deps: nil},
		"openssl": {Name: "openssl", Deps: []string{"zlib"}},
		"openssh": {Name: "openssh", Deps: []string{"zlib", "openssl"}},
		"myapp":   {Name: "myapp", Deps: []string{"openssh"}},
	})

	dag, err := BuildDAG(proj)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	order, err := dag.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}

	if len(order) != 4 {
		t.Fatalf("order has %d entries, want 4", len(order))
	}

	// Verify ordering constraints: each dep must come before its dependent
	pos := make(map[string]int)
	for i, name := range order {
		pos[name] = i
	}

	assertBefore := func(a, b string) {
		t.Helper()
		if pos[a] >= pos[b] {
			t.Errorf("%s (pos %d) should come before %s (pos %d) in %v", a, pos[a], b, pos[b], order)
		}
	}

	assertBefore("zlib", "openssl")
	assertBefore("zlib", "openssh")
	assertBefore("openssl", "openssh")
	assertBefore("openssh", "myapp")
}

func TestTopologicalSort_Cycle(t *testing.T) {
	proj := makeProject(map[string]*yoestar.Unit{
		"a": {Name: "a", Deps: []string{"b"}},
		"b": {Name: "b", Deps: []string{"c"}},
		"c": {Name: "c", Deps: []string{"a"}},
	})

	dag, err := BuildDAG(proj)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	_, err = dag.TopologicalSort()
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q should mention cycle", err)
	}
}

func TestTopologicalSort_NoDeps(t *testing.T) {
	proj := makeProject(map[string]*yoestar.Unit{
		"a": {Name: "a", Deps: nil},
		"b": {Name: "b", Deps: nil},
		"c": {Name: "c", Deps: nil},
	})

	dag, err := BuildDAG(proj)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	order, err := dag.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}

	if len(order) != 3 {
		t.Errorf("order has %d entries, want 3", len(order))
	}
}

func TestDepsOf(t *testing.T) {
	proj := makeProject(map[string]*yoestar.Unit{
		"zlib":    {Name: "zlib", Deps: nil},
		"openssl": {Name: "openssl", Deps: []string{"zlib"}},
		"openssh": {Name: "openssh", Deps: []string{"openssl"}},
	})

	dag, _ := BuildDAG(proj)

	deps, err := dag.DepsOf("openssh")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}

	// openssh -> openssl -> zlib (transitive)
	if len(deps) != 2 {
		t.Errorf("deps = %v, want [openssl, zlib]", deps)
	}
}

func TestRdepsOf(t *testing.T) {
	proj := makeProject(map[string]*yoestar.Unit{
		"zlib":    {Name: "zlib", Deps: nil},
		"openssl": {Name: "openssl", Deps: []string{"zlib"}},
		"openssh": {Name: "openssh", Deps: []string{"openssl"}},
		"curl":    {Name: "curl", Deps: []string{"openssl"}},
	})

	dag, _ := BuildDAG(proj)

	rdeps, err := dag.RdepsOf("zlib")
	if err != nil {
		t.Fatalf("RdepsOf: %v", err)
	}

	// zlib is depended on by openssl, which is depended on by openssh and curl
	if len(rdeps) != 3 {
		t.Errorf("rdeps = %v, want 3 entries (curl, openssl, openssh)", rdeps)
	}
}

func TestDepsOf_NotFound(t *testing.T) {
	dag := &DAG{Nodes: make(map[string]*Node)}
	_, err := dag.DepsOf("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent unit, got nil")
	}
}
