package starlark

import (
	"fmt"
	"sort"
	"strings"
)

// DetectCycles runs a DFS over the module dep graph and returns a
// descriptive error naming the first cycle path it finds.
//
// `graph` maps a module's canonical name to the canonical names of the
// modules it declares as deps. Missing nodes are treated as having no
// outgoing edges; spurious entries (a dep that names a module not in
// the map) are ignored — the caller surfaces those as "unresolved
// module" errors separately.
//
// The traversal walks roots in sorted order so the chosen cycle path
// is deterministic across runs (cycle path A→B→A is fixed regardless
// of map iteration order).
func DetectCycles(graph map[string][]string) error {
	roots := make([]string, 0, len(graph))
	for n := range graph {
		roots = append(roots, n)
	}
	sort.Strings(roots)

	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(graph))

	var stack []string
	var visit func(node string) error
	visit = func(node string) error {
		switch state[node] {
		case visiting:
			// Slice the stack from where node first appears to here,
			// then append node again to close the cycle visually.
			start := 0
			for i, n := range stack {
				if n == node {
					start = i
					break
				}
			}
			path := append(append([]string(nil), stack[start:]...), node)
			return &CycleError{Path: path}
		case visited:
			return nil
		}
		state[node] = visiting
		stack = append(stack, node)
		deps := append([]string(nil), graph[node]...)
		sort.Strings(deps)
		for _, dep := range deps {
			if _, ok := graph[dep]; !ok {
				continue // dep not declared in graph — caller's problem
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[node] = visited
		return nil
	}
	for _, r := range roots {
		if state[r] == visited {
			continue
		}
		if err := visit(r); err != nil {
			return err
		}
	}
	return nil
}

// CycleError reports a module-dep cycle. Path holds the cycle as a list
// of canonical module names with the offending module repeated at both
// ends so the user sees A → B → C → A.
type CycleError struct{ Path []string }

func (e *CycleError) Error() string {
	if len(e.Path) == 0 {
		return "module dep cycle (empty path)"
	}
	return fmt.Sprintf("module dep cycle: %s", strings.Join(e.Path, " → "))
}
