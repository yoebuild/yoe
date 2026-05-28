package main

import (
	"fmt"
	"github.com/yoebuild/yoe/internal/feeds/alpine"
	"github.com/yoebuild/yoe/internal/feeds/debian"
	"github.com/yoebuild/yoe/internal/module"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func main() {
	proj, err := yoestar.LoadProject(".",
		yoestar.WithModuleSync(module.SyncIfNeeded),
		yoestar.WithAllowDuplicateProvides(true),
		yoestar.WithBuiltin("alpine_feed", alpine.Builtin),
		yoestar.WithBuiltin("debian_feed", debian.Builtin),
	)
	if err != nil { fmt.Println("ERR:", err); return }
	// Find anything with xz in RuntimeDeps across every registered
	// module — AllUnits iterates UnitsByModule, yielding entries that
	// might shadow each other in a per-distro view.
	for name, u := range proj.AllUnits() {
		for _, d := range u.RuntimeDeps {
			if d == "xz" {
				fmt.Printf("%s (Distro=%q Module=%s) -> xz\n", name, u.Distro, u.Module)
			}
		}
	}
}
