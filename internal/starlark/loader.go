package starlark

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// LoadOption configures optional behavior for LoadProject / LoadProjectFromRoot.
type LoadOption func(*loadConfig)

type loadConfig struct {
	moduleSync             func([]ModuleRef, io.Writer) error
	machine                string // override default machine before evaluating units/images
	projectFile            string // alternative project file (instead of PROJECT.star)
	showShadows            bool   // emit shadow / provides-override notices (default off)
	allowDuplicateProvides bool   // accept multiple intra-module providers of the same virtual
}

// WithModuleSync provides a callback that is invoked after PROJECT.star is
// evaluated to ensure all declared modules are available (e.g. cloned).
// The callback receives the module list and a writer for progress output.
func WithModuleSync(fn func([]ModuleRef, io.Writer) error) LoadOption {
	return func(c *loadConfig) { c.moduleSync = fn }
}

// WithMachine overrides the project's default machine before units and
// images are evaluated. This allows target_arch() in Starlark to return
// the correct architecture for the specified machine.
func WithMachine(name string) LoadOption {
	return func(c *loadConfig) { c.machine = name }
}

// WithProjectFile specifies an alternative project file to evaluate instead
// of PROJECT.star at the project root.
func WithProjectFile(path string) LoadOption {
	return func(c *loadConfig) { c.projectFile = path }
}

// WithShowShadows enables stderr notices about cross-module unit shadowing
// and intra-module `provides` overrides. Default is off; the shadowing/
// override behavior itself is unchanged either way.
func WithShowShadows(v bool) LoadOption {
	return func(c *loadConfig) { c.showShadows = v }
}

// WithAllowDuplicateProvides relaxes the intra-module `provides` collision
// check. When true, multiple units in the same module may declare the same
// virtual; the first one registered wins for PROVIDES lookup, matching
// apk's "any of these satisfies the dep" semantics.
func WithAllowDuplicateProvides(v bool) LoadOption {
	return func(c *loadConfig) { c.allowDuplicateProvides = v }
}

// LoadProject finds the project root, evaluates all .star files, and returns
// a fully populated Project.
func LoadProject(startDir string, opts ...LoadOption) (*Project, error) {
	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	return LoadProjectFromRoot(root, opts...)
}

// findProjectRoot walks up from startDir looking for PROJECT.star.
func findProjectRoot(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}

	for {
		candidate := filepath.Join(dir, "PROJECT.star")
		if _, err := os.Stat(candidate); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("no PROJECT.star found in %s or any parent directory", startDir)
}

// LoadProjectFromRoot evaluates all .star files under a known project root
// and returns a fully populated Project. Unlike LoadProject, it does not
// search for PROJECT.star — the caller must provide the exact root directory.
func LoadProjectFromRoot(root string, opts ...LoadOption) (*Project, error) {
	var cfg loadConfig
	for _, o := range opts {
		o(&cfg)
	}

	eng := NewEngine()
	eng.SetProjectRoot(root)
	eng.SetShowShadows(cfg.showShadows)
	eng.SetAllowDuplicateProvides(cfg.allowDuplicateProvides)

	// Evaluate project file (PROJECT.star or --project override)
	projFile := filepath.Join(root, "PROJECT.star")
	if cfg.projectFile != "" {
		projFile = cfg.projectFile
		if !filepath.IsAbs(projFile) {
			projFile = filepath.Join(root, projFile)
		}
	}
	if err := eng.ExecFile(projFile); err != nil {
		return nil, fmt.Errorf("evaluating %s: %w", projFile, err)
	}

	// Sync modules if a sync callback was provided (auto-clone missing modules).
	if cfg.moduleSync != nil {
		if proj := eng.Project(); proj != nil && len(proj.Modules) > 0 {
			if err := cfg.moduleSync(proj.Modules, os.Stderr); err != nil {
				return nil, fmt.Errorf("syncing modules: %w", err)
			}
		}
	}

	// Resolve each declared module to a canonical name and on-disk path.
	// Canonical name comes from MODULE.star's module_info(name=...) when
	// present; otherwise it falls back to the path/URL basename. The same
	// name is used for "@name//..." load references, u.Module tags, and
	// TUI / diagnostic display.
	type resolvedModule struct {
		name string
		path string
	}
	var resolvedModules []resolvedModule
	if proj := eng.Project(); proj != nil {
		for _, m := range proj.Modules {
			modulePath, ok := locateModulePath(m, root)
			if !ok {
				continue
			}
			name := peekModuleName(modulePath)
			if name == "" {
				name = pathBasename(m)
			}
			eng.SetModuleRoot(name, modulePath)
			resolvedModules = append(resolvedModules, resolvedModule{name: name, path: modulePath})
		}
	}

	// Compute project priority: strictly higher than any module so that a
	// project-level unit shadows the same name from any included module.
	// Modules use 1..N (declaration order, last wins among modules); the
	// project root uses N+1 — the highest priority overall.
	projectIdx := len(resolvedModules) + 1

	// Phase 1: Evaluate all machine definitions (project + modules).
	// Machines must be loaded before units/images so that target_arch()
	// returns the correct value during Starlark evaluation.
	eng.SetCurrentModule("", projectIdx)
	if err := evalDir(eng, root, "machines"); err != nil {
		return nil, err
	}
	for i, rm := range resolvedModules {
		eng.SetCurrentModule(rm.name, i+1)
		if err := evalDir(eng, rm.path, "machines"); err != nil {
			return nil, err
		}
	}

	// Apply machine override before evaluating units/images.
	if cfg.machine != "" {
		if _, ok := eng.Machines()[cfg.machine]; !ok {
			return nil, fmt.Errorf("machine %q not found", cfg.machine)
		}
		if proj := eng.Project(); proj != nil {
			proj.Defaults.Machine = cfg.machine
		}
	}

	// Set ARCH variable for phase 2 so Starlark files can use it
	// (e.g., conditional artifacts in image definitions).
	// Always set a value — default to x86_64 if no machine is configured.
	arch := "x86_64"
	if proj := eng.Project(); proj != nil {
		if m, ok := eng.Machines()[proj.Defaults.Machine]; ok {
			arch = m.Arch
		}
	}
	eng.SetVar("ARCH", starlark.String(arch))

	// Set MACHINE variable so image definitions can conditionally include
	// board-specific units (e.g., different kernels per RPi board).
	machine := ""
	if proj := eng.Project(); proj != nil {
		machine = proj.Defaults.Machine
	}
	eng.SetVar("MACHINE", starlark.String(machine))

	// Set MACHINE_CONFIG — a Starlark struct exposing the active machine's
	// configuration to unit and image definitions.
	if proj := eng.Project(); proj != nil {
		if m, ok := eng.Machines()[proj.Defaults.Machine]; ok {
			machineDict := starlark.StringDict{
				"name":     starlark.String(m.Name),
				"arch":     starlark.String(m.Arch),
				"packages": toStarlarkStringList(m.Packages),
			}
			// Add partitions as a Starlark list
			var partList []starlark.Value
			for _, p := range m.Partitions {
				fields := starlark.StringDict{
					"label": starlark.String(p.Label),
					"type":  starlark.String(p.Type),
					"size":  starlark.String(p.Size),
					"root":  starlark.Bool(p.Root),
				}
				if len(p.Contents) > 0 {
					fields["contents"] = toStarlarkStringList(p.Contents)
				}
				partList = append(partList, starlarkstruct.FromStringDict(starlark.String("partition"), fields))
			}
			machineDict["partitions"] = starlark.NewList(partList)

			// Add kernel info
			if m.Kernel.Unit != "" {
				machineDict["kernel"] = starlarkstruct.FromStringDict(
					starlark.String("kernel"), starlark.StringDict{
						"unit":      starlark.String(m.Kernel.Unit),
						"provides":  starlark.String(m.Kernel.Provides),
						"defconfig": starlark.String(m.Kernel.Defconfig),
						"cmdline":   starlark.String(m.Kernel.Cmdline),
					})
			}

			eng.SetVar("MACHINE_CONFIG", starlarkstruct.FromStringDict(
				starlark.String("machine_config"), machineDict))
		}
	}

	// Set PROVIDES — a Starlark dict mapping virtual package names to concrete
	// unit names. Initially populated from kernel.provides; updated after phase 2
	// with unit provides.
	provides := starlark.NewDict(4)
	if proj := eng.Project(); proj != nil {
		if m, ok := eng.Machines()[proj.Defaults.Machine]; ok {
			if m.Kernel.Provides != "" {
				_ = provides.SetKey(starlark.String(m.Kernel.Provides),
					starlark.String(m.Kernel.Unit))
			}
		}
	}
	eng.SetVar("PROVIDES", provides)

	// Phase 1b: Evaluate container definitions (project + modules).
	// Containers must be loaded before units so that units can reference them.
	eng.SetCurrentModule("", projectIdx)
	if err := evalDir(eng, root, "containers"); err != nil {
		return nil, err
	}
	for i, rm := range resolvedModules {
		eng.SetCurrentModule(rm.name, i+1)
		if err := evalDir(eng, rm.path, "containers"); err != nil {
			return nil, err
		}
	}

	// Phase 2a: Evaluate all unit definitions (project + modules).
	eng.SetCurrentModule("", projectIdx)
	if err := evalDir(eng, root, "units"); err != nil {
		return nil, err
	}
	for i, rm := range resolvedModules {
		eng.SetCurrentModule(rm.name, i+1)
		if err := evalDir(eng, rm.path, "units"); err != nil {
			return nil, err
		}
	}

	// Now that all units are loaded, update predeclared variables before
	// evaluating images (phase 2b).

	// Add unit provides to PROVIDES dict, checking for conflicts. A unit may
	// declare multiple virtual names (apk-style); each is registered
	// independently with the same module-priority conflict rules.
	if prov, ok := eng.vars["PROVIDES"].(*starlark.Dict); ok {
		for _, u := range eng.Units() {
			for _, virt := range u.Provides {
				if virt == "" {
					continue
				}
				if existing, found, _ := prov.Get(starlark.String(virt)); found {
					existingName := string(existing.(starlark.String))
					// Look up the existing unit to compare module priority.
					existingUnit := eng.Units()[existingName]
					if existingUnit == nil || u.ModuleIndex == existingUnit.ModuleIndex {
						if !eng.allowDuplicateProvides {
							return nil, fmt.Errorf("virtual package %q provided by both %q and %q",
								virt, existingName, u.Name)
						}
						// First-wins: leave PROVIDES pointing at existingName.
						continue
					}
					if u.ModuleIndex > existingUnit.ModuleIndex {
						if eng.showShadows {
							fmt.Fprintf(os.Stderr, "notice: %q from %s overrides %q via provides %q\n",
								u.Name, moduleSource(u.Module), existingName, virt)
						}
						_ = prov.SetKey(starlark.String(virt), starlark.String(u.Name))
					}
					// If u.ModuleIndex < existingUnit.ModuleIndex, skip — higher priority already won.
					continue
				}
				_ = prov.SetKey(starlark.String(virt), starlark.String(u.Name))
			}
		}
	}

	// Set RUNTIME_DEPS: unit name → list of runtime dep names.
	runtimeDeps := starlark.NewDict(len(eng.Units()))
	for _, u := range eng.Units() {
		if len(u.RuntimeDeps) > 0 {
			_ = runtimeDeps.SetKey(starlark.String(u.Name), toStarlarkStringList(u.RuntimeDeps))
		}
	}
	eng.SetVar("RUNTIME_DEPS", runtimeDeps)

	// Phase 2b: Evaluate image definitions (project + modules).
	eng.SetCurrentModule("", projectIdx)
	if err := evalDir(eng, root, "images"); err != nil {
		return nil, err
	}
	for i, rm := range resolvedModules {
		eng.SetCurrentModule(rm.name, i+1)
		if err := evalDir(eng, rm.path, "images"); err != nil {
			return nil, err
		}
	}

	proj := eng.Project()
	if proj == nil {
		return nil, fmt.Errorf("PROJECT.star did not call project()")
	}

	proj.Machines = eng.Machines()
	proj.Units = eng.Units()

	// Mirror the Starlark PROVIDES dict onto the Go side so callers that
	// don't run inside a Starlark thread (the build executor, deploy path,
	// describe command) can route virtual deps to concrete units.
	proj.Provides = map[string]string{}
	if prov, ok := eng.vars["PROVIDES"].(*starlark.Dict); ok {
		for _, item := range prov.Items() {
			k, kok := item[0].(starlark.String)
			v, vok := item[1].(starlark.String)
			if kok && vok {
				proj.Provides[string(k)] = string(v)
			}
		}
	}

	// Validate: units with tasks must have container and container_arch.
	for name, u := range proj.Units {
		if len(u.Tasks) == 0 {
			continue // metadata-only units
		}
		if u.Class == "container" {
			continue // container units build on host
		}
		if u.Container == "" {
			return nil, fmt.Errorf("unit %q has tasks but no container — set container in the unit or class", name)
		}
		if u.ContainerArch == "" {
			return nil, fmt.Errorf("unit %q has tasks but no container_arch — set container_arch in the unit or class", name)
		}
	}

	return proj, nil
}

func toStarlarkStringList(ss []string) *starlark.List {
	vals := make([]starlark.Value, len(ss))
	for i, s := range ss {
		vals[i] = starlark.String(s)
	}
	return starlark.NewList(vals)
}

// pathBasename returns the fallback module name derived from a ModuleRef:
// the last component of m.Path if set, otherwise the URL's basename with
// any trailing .git stripped.
func pathBasename(m ModuleRef) string {
	if m.Path != "" {
		return filepath.Base(m.Path)
	}
	return filepath.Base(strings.TrimSuffix(m.URL, ".git"))
}

// locateModulePath returns the on-disk directory for a module — either the
// local override or the cache directory under YOE_CACHE/modules. The
// boolean is false when neither location exists (the module hasn't been
// synced yet).
func locateModulePath(m ModuleRef, projectRoot string) (string, bool) {
	base := pathBasename(m)
	if m.Local != "" {
		modulePath := m.Local
		if !filepath.IsAbs(modulePath) {
			modulePath = filepath.Join(projectRoot, modulePath)
		}
		if m.Path != "" {
			modulePath = filepath.Join(modulePath, m.Path)
		}
		return modulePath, true
	}
	cacheDir := os.Getenv("YOE_CACHE")
	if cacheDir == "" {
		cacheDir = "cache"
	}
	moduleDir := filepath.Join(cacheDir, "modules", base)
	if m.Path != "" {
		moduleDir = filepath.Join(moduleDir, m.Path)
	}
	if _, err := os.Stat(moduleDir); err != nil {
		return "", false
	}
	return moduleDir, true
}

// peekModuleName evaluates MODULE.star at modulePath in an isolated thread
// and returns the name declared via module_info(name=...). Returns "" if
// MODULE.star is missing, fails to parse, or doesn't call module_info.
// This is intentionally separate from the main engine eval so the canonical
// name is known before any registration happens.
func peekModuleName(modulePath string) string {
	file := filepath.Join(modulePath, "MODULE.star")
	src, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	var captured string
	moduleInfo := starlark.NewBuiltin("module_info",
		func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			for _, kv := range kwargs {
				if k, ok := kv[0].(starlark.String); ok && string(k) == "name" {
					if v, ok := kv[1].(starlark.String); ok {
						captured = string(v)
					}
				}
			}
			return starlark.None, nil
		})
	moduleStub := starlark.NewBuiltin("module",
		func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			return starlark.None, nil
		})
	thread := &starlark.Thread{Name: file}
	_, _ = starlark.ExecFileOptions(fileOpts, thread, file, src, starlark.StringDict{
		"module_info": moduleInfo,
		"module":      moduleStub,
	})
	return captured
}

func evalDir(eng *Engine, root, subdir string) error {
	base := filepath.Join(root, subdir)
	return filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".star") {
			return nil
		}
		if err := eng.ExecFile(path); err != nil {
			return fmt.Errorf("evaluating %s: %w", path, err)
		}
		return nil
	})
}
