package internal

import (
	"fmt"
	"io"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func ListModules(dir string, w io.Writer) error {
	proj, err := yoestar.LoadProject(dir)
	if err != nil {
		return err
	}

	if len(proj.Modules) == 0 {
		fmt.Fprintln(w, "No modules declared in PROJECT.star")
		return nil
	}

	fmt.Fprintf(w, "%-40s %-12s %s\n", "Module", "Ref", "Status")
	for _, m := range proj.Modules {
		status := "not synced"
		if m.Local != "" {
			status = fmt.Sprintf("(local: %s)", m.Local)
		}
		ref := m.Ref
		if ref == "" {
			ref = "(none)"
		}
		fmt.Fprintf(w, "%-40s %-12s %s\n", m.URL, ref, status)
	}

	return nil
}
