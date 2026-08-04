package starlark

import (
	"slices"
	"testing"
)

// The closure order becomes an image's artifact list. Running the same
// walk twice must give the same list, or a rebuild would show a
// difference with no cause behind it. Map iteration in Go is randomized,
// so this catches an ordering that leaks it.
func TestTopoOrder_Deterministic(t *testing.T) {
	deps := map[string][]string{
		"base-files": nil,
		"musl":       nil,
		"busybox":    {"musl"},
		"openssl":    {"musl"},
		"curl":       {"openssl", "musl"},
		"dev-image":  {"curl", "busybox", "base-files"},
		"zlib":       nil,
		"libxml":     {"zlib", "musl"},
	}

	first := topoOrder(deps)
	for range 50 {
		if got := topoOrder(deps); !slices.Equal(got, first) {
			t.Fatalf("order varies between runs:\n  %v\n  %v", first, got)
		}
	}
	if len(first) != len(deps) {
		t.Errorf("got %d names, want %d", len(first), len(deps))
	}
}

// Dependencies must precede the units that need them.
func TestTopoOrder_DepsFirst(t *testing.T) {
	deps := map[string][]string{
		"musl":      nil,
		"openssl":   {"musl"},
		"curl":      {"openssl"},
		"dev-image": {"curl"},
	}
	got := topoOrder(deps)
	pos := map[string]int{}
	for i, n := range got {
		pos[n] = i
	}
	for name, ds := range deps {
		for _, d := range ds {
			if pos[d] >= pos[name] {
				t.Errorf("%s (at %d) must come before %s (at %d)", d, pos[d], name, pos[name])
			}
		}
	}
}

// A runtime-dep cycle is ordinary in a distribution — two packages can
// depend on each other, and the package manager sorts installation out
// itself. Every unit must still be returned, in a stable order.
func TestTopoOrder_CycleStillReturnsEverything(t *testing.T) {
	deps := map[string][]string{
		"a":    {"b"},
		"b":    {"a"},
		"musl": nil,
		"c":    {"musl"},
	}
	got := topoOrder(deps)
	if len(got) != 4 {
		t.Fatalf("got %v, want all 4 units", got)
	}
	for range 20 {
		if again := topoOrder(deps); !slices.Equal(again, got) {
			t.Fatalf("cycle handling is not deterministic:\n  %v\n  %v", got, again)
		}
	}
}

// A unit listing itself, or listing the same dep twice, must not be
// stranded behind an in-degree that never reaches zero.
func TestTopoOrder_SelfAndDuplicateDeps(t *testing.T) {
	deps := map[string][]string{
		"musl":  nil,
		"weird": {"musl", "musl", "weird"},
	}
	got := topoOrder(deps)
	want := []string{"musl", "weird"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A dep naming something outside the closure (filtered by distro, or
// simply never walked) must not hold its dependent back.
func TestTopoOrder_DepOutsideClosure(t *testing.T) {
	deps := map[string][]string{
		"hello": {"not-in-closure"},
	}
	got := topoOrder(deps)
	if !slices.Equal(got, []string{"hello"}) {
		t.Errorf("got %v, want [hello]", got)
	}
}
