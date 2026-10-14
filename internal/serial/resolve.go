package serial

import (
	"fmt"
	"path/filepath"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// DirFor is where a project keeps its serial logs and sockets.
func DirFor(projectDir string) string {
	return filepath.Join(projectDir, "build", "serial")
}

// ResolveConfig builds the Config for a machine from the project and the
// developer's local.star. machine may be "" for the project's effective
// machine (the local.star override already applied by the loader). The
// errors say what to add and where.
func ResolveConfig(proj *yoestar.Project, ov yoestar.LocalOverrides, machine, projectDir string) (Config, error) {
	if machine == "" {
		machine = proj.Defaults.Machine
	}
	if machine == "" {
		return Config{}, fmt.Errorf("no machine selected; pass --machine or set one in local.star")
	}
	mc, ok := proj.Machines[machine]
	if !ok {
		return Config{}, fmt.Errorf("machine %q is not defined in this project", machine)
	}
	if mc.Serial == nil {
		return Config{}, fmt.Errorf("machine %q declares no serial console; add serial = serial_config(baud = ...) to its machine file", machine)
	}
	mapped := ov.SerialPorts[machine]
	if mapped == "" {
		return Config{}, fmt.Errorf("no serial adapter mapped for %q; run yoe serial list, then yoe config set serial-port <path>", machine)
	}
	return Config{
		Machine: machine,
		Mapped:  mapped,
		Baud:    mc.Serial.Baud,
		Dir:     DirFor(projectDir),
		Listen:  ov.SerialListen,
	}, nil
}
