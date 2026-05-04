package internal

import (
	"fmt"
	"io"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func ShowConfig(dir string, w io.Writer) error {
	proj, err := yoestar.LoadProject(dir)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "Project:    %s %s\n", proj.Name, proj.Version)
	fmt.Fprintf(w, "Machine:    %s (default)\n", proj.Defaults.Machine)
	fmt.Fprintf(w, "Image:      %s (default)\n", proj.Defaults.Image)
	fmt.Fprintf(w, "Cache:      %s\n", proj.Cache.Path)
	fmt.Fprintf(w, "Machines:   %d defined\n", len(proj.Machines))
	fmt.Fprintf(w, "Units:    %d defined\n", len(proj.Units))

	if len(proj.Machines) > 0 {
		fmt.Fprintln(w, "\nMachines:")
		for name, m := range proj.Machines {
			fmt.Fprintf(w, "  %-20s %s\n", name, m.Arch)
		}
	}

	if len(proj.Units) > 0 {
		fmt.Fprintln(w, "\nUnits:")
		for name, r := range proj.Units {
			fmt.Fprintf(w, "  %-20s [%s] %s\n", name, r.Class, r.Version)
		}
	}

	return nil
}
