package internal

import (
	"fmt"
	"io"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func ShowConfig(proj *yoestar.Project, dir string, w io.Writer) error {
	fmt.Fprintf(w, "Project:    %s %s\n", proj.Name, proj.Version)
	fmt.Fprintf(w, "Machine:    %s (default)\n", proj.Defaults.Machine)

	ov, _ := yoestar.LoadLocalOverrides(dir)

	// proj is a machines-only load, which does not apply local.star's
	// image override (that needs the unit catalog), so read it here.
	if ov.Image != "" {
		fmt.Fprintf(w, "Image:      %s (local.star)\n", ov.Image)
	} else {
		fmt.Fprintf(w, "Image:      %s (default)\n", proj.Defaults.Image)
	}
	fmt.Fprintf(w, "Cache:      %s\n", proj.Cache.Path)

	parallel := yoestar.DefaultParallelBuilds
	parallelNote := "default"
	if ov.ParallelBuilds > 0 {
		parallel = ov.ParallelBuilds
		parallelNote = "local.star"
	}
	fmt.Fprintf(w, "Parallel:   %d (%s)\n", parallel, parallelNote)

	// QEMU memory `yoe run` gives the guest: local.star qemu_memory wins,
	// otherwise the default machine's own qemu memory.
	qemuMem, qemuNote := "unset", "machine default"
	if m, ok := proj.Machines[proj.Defaults.Machine]; ok && m.QEMU != nil && m.QEMU.Memory != "" {
		qemuMem, qemuNote = m.QEMU.Memory, "machine default"
	}
	if ov.QEMUMemory != "" {
		qemuMem, qemuNote = ov.QEMUMemory, "local.star"
	}
	fmt.Fprintf(w, "QEMU mem:   %s (%s)\n", qemuMem, qemuNote)

	// Serial console for the default machine: the machine's declared
	// speed plus the developer's adapter mapping.
	if m, ok := proj.Machines[proj.Defaults.Machine]; ok && m.Serial != nil {
		mapped := ov.SerialPorts[proj.Defaults.Machine]
		if mapped == "" {
			mapped = "unmapped (yoe serial list; yoe config set serial-port <path>)"
		}
		fmt.Fprintf(w, "Serial:     %d baud, %s\n", m.Serial.Baud, mapped)
	}

	fmt.Fprintf(w, "Machines:   %d defined\n", len(proj.Machines))

	if len(proj.Machines) > 0 {
		fmt.Fprintln(w, "\nMachines:")
		for name, m := range proj.Machines {
			fmt.Fprintf(w, "  %-20s %s\n", name, m.Arch)
		}
	}

	return nil
}
