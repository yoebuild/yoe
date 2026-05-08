package resolve

import (
	"fmt"
	"io"
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// Describe prints detailed information about a unit.
func Describe(w io.Writer, proj *yoestar.Project, name string, arch string) error {
	unit, ok := proj.Units[name]
	if !ok {
		return fmt.Errorf("unit %q not found", name)
	}

	dag, err := BuildDAG(proj)
	if err != nil {
		return err
	}

	hashes, err := ComputeAllHashes(dag, arch, "", nil)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "Unit:         %s\n", unit.Name)
	fmt.Fprintf(w, "Version:      %s\n", unit.Version)
	fmt.Fprintf(w, "Class:        %s\n", unit.Class)
	if unit.Description != "" {
		fmt.Fprintf(w, "Description:  %s\n", unit.Description)
	}
	if unit.License != "" {
		fmt.Fprintf(w, "License:      %s\n", unit.License)
	}
	if unit.Source != "" {
		fmt.Fprintf(w, "Source:       %s\n", unit.Source)
	}
	if unit.SHA256 != "" {
		fmt.Fprintf(w, "SHA256:       %s\n", unit.SHA256)
	}

	if len(unit.Deps) > 0 {
		fmt.Fprintf(w, "Build deps:   %s\n", strings.Join(unit.Deps, ", "))
	}
	if len(unit.RuntimeDeps) > 0 {
		fmt.Fprintf(w, "Runtime deps: %s\n", strings.Join(unit.RuntimeDeps, ", "))
	}

	fmt.Fprintf(w, "Input hash:   %s\n", hashes[name])
	fmt.Fprintf(w, "Architecture: %s\n", arch)

	if unit.Class == "image" {
		if len(unit.Artifacts) > 0 {
			fmt.Fprintf(w, "Artifacts:     %s\n", strings.Join(unit.Artifacts, ", "))
		}
		if unit.Hostname != "" {
			fmt.Fprintf(w, "Hostname:     %s\n", unit.Hostname)
		}
	}

	return nil
}

// Refs prints what depends on a given unit (reverse dependencies).
func Refs(w io.Writer, proj *yoestar.Project, name string, direct bool) error {
	dag, err := BuildDAG(proj)
	if err != nil {
		return err
	}

	if _, ok := dag.Nodes[name]; !ok {
		return fmt.Errorf("unit %q not found", name)
	}

	if direct {
		node := dag.Nodes[name]
		if len(node.Rdeps) == 0 {
			fmt.Fprintf(w, "Nothing depends on %s\n", name)
			return nil
		}
		fmt.Fprintf(w, "Direct dependents of %s:\n", name)
		for _, rdep := range node.Rdeps {
			r := proj.Units[rdep]
			fmt.Fprintf(w, "  %s [%s]\n", rdep, r.Class)
		}
	} else {
		rdeps, err := dag.RdepsOf(name)
		if err != nil {
			return err
		}
		if len(rdeps) == 0 {
			fmt.Fprintf(w, "Nothing depends on %s\n", name)
			return nil
		}
		fmt.Fprintf(w, "All dependents of %s (transitive):\n", name)
		for _, rdep := range rdeps {
			r := proj.Units[rdep]
			fmt.Fprintf(w, "  %s [%s]\n", rdep, r.Class)
		}
	}

	return nil
}

// Graph prints the dependency graph in text or DOT format.
func Graph(w io.Writer, proj *yoestar.Project, format string, filter string) error {
	dag, err := BuildDAG(proj)
	if err != nil {
		return err
	}

	order, err := dag.TopologicalSort()
	if err != nil {
		return err
	}

	if format == "dot" {
		return graphDOT(w, dag, order, filter)
	}
	return graphText(w, dag, order, filter)
}

func graphText(w io.Writer, dag *DAG, order []string, filter string) error {
	for _, name := range order {
		if filter != "" && name != filter {
			// If filtering, only show the filtered unit and its deps
			deps, _ := dag.DepsOf(filter)
			found := name == filter
			for _, d := range deps {
				if d == name {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		node := dag.Nodes[name]
		if len(node.Deps) == 0 {
			fmt.Fprintf(w, "%s\n", name)
		} else {
			fmt.Fprintf(w, "%s → %s\n", name, strings.Join(node.Deps, ", "))
		}
	}
	return nil
}

func graphDOT(w io.Writer, dag *DAG, order []string, filter string) error {
	fmt.Fprintln(w, "digraph deps {")
	fmt.Fprintln(w, "  rankdir=LR;")

	var nodes []string
	if filter != "" {
		deps, _ := dag.DepsOf(filter)
		nodes = append([]string{filter}, deps...)
	} else {
		nodes = order
	}

	for _, name := range nodes {
		node := dag.Nodes[name]
		label := fmt.Sprintf("%s\\n%s", name, node.Unit.Version)
		shape := "box"
		if node.Unit.Class == "image" {
			shape = "box3d"
		}
		fmt.Fprintf(w, "  %q [label=%q, shape=%s];\n", name, label, shape)
		for _, dep := range node.Deps {
			fmt.Fprintf(w, "  %q -> %q;\n", name, dep)
		}
	}

	fmt.Fprintln(w, "}")
	return nil
}
