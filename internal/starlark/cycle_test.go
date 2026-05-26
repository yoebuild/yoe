package starlark

import (
	"errors"
	"testing"
)

func TestDetectCycles_None(t *testing.T) {
	graph := map[string][]string{
		"a": {"b", "c"},
		"b": {"d"},
		"c": {"d"},
		"d": {},
	}
	if err := DetectCycles(graph); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestDetectCycles_DirectCycle(t *testing.T) {
	graph := map[string][]string{
		"a": {"b"},
		"b": {"a"},
	}
	err := DetectCycles(graph)
	if err == nil {
		t.Fatal("want cycle error")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Errorf("want *CycleError, got %T", err)
	}
	// Deterministic traversal: sorted roots, so we start with "a" → "b" → "a".
	want := []string{"a", "b", "a"}
	if !slicesEqual(ce.Path, want) {
		t.Errorf("path: got %v, want %v", ce.Path, want)
	}
}

func TestDetectCycles_LongerCycle(t *testing.T) {
	graph := map[string][]string{
		"a": {"b"},
		"b": {"c"},
		"c": {"a"},
	}
	err := DetectCycles(graph)
	if err == nil {
		t.Fatal("want cycle error")
	}
	var ce *CycleError
	errors.As(err, &ce)
	want := []string{"a", "b", "c", "a"}
	if !slicesEqual(ce.Path, want) {
		t.Errorf("path: got %v, want %v", ce.Path, want)
	}
}

func TestDetectCycles_IgnoresMissingDeps(t *testing.T) {
	// "b" is not in the graph (e.g., declared dep wasn't loaded yet);
	// DetectCycles ignores it — the loader surfaces missing-module
	// errors separately.
	graph := map[string][]string{
		"a": {"b"},
	}
	if err := DetectCycles(graph); err != nil {
		t.Errorf("got %v, want nil (missing deps are ignored)", err)
	}
}

func TestDetectCycles_SelfLoop(t *testing.T) {
	graph := map[string][]string{
		"a": {"a"},
	}
	err := DetectCycles(graph)
	if err == nil {
		t.Fatal("want cycle error")
	}
	var ce *CycleError
	errors.As(err, &ce)
	want := []string{"a", "a"}
	if !slicesEqual(ce.Path, want) {
		t.Errorf("path: got %v, want %v", ce.Path, want)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
