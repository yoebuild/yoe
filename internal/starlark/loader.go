package starlark

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	extraBuiltins          []extraBuiltin
}

// extraBuiltin pairs a Starlark name with a factory that produces the
// builtin once the Engine exists. The factory is called from
// Engine.builtins() with the Engine instance so closures (like
// alpine_feed) can hold a reference to it without an import cycle
// between internal/starlark and the package that defines the builtin.
type extraBuiltin struct {
	name    string
	factory BuiltinFactory
}

// BuiltinFactory produces a Starlark builtin closed over the loading
// Engine. External packages (internal/feeds/alpine, etc.) use this to
// register their own builtins via WithBuiltin without forcing
// internal/starlark to import the heavy parser packages those builtins
// pull in (apkindex, dpkg, etc.).
type BuiltinFactory func(*Engine) *starlark.Builtin

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

// WithBuiltin adds an extra Starlark builtin to the engine's predeclared
// set before evaluation. The factory is invoked from inside builtins()
// with the Engine instance so the builtin can hold a closure over it —
// alpine_feed (internal/feeds/alpine) uses this to call
// Engine.RegisterSyntheticModule without internal/starlark importing
// internal/apkindex.
func WithBuiltin(name string, factory BuiltinFactory) LoadOption {
	return func(c *loadConfig) {
		c.extraBuiltins = append(c.extraBuiltins, extraBuiltin{name: name, factory: factory})
	}
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

// ProjectModuleRefs finds the project root and evaluates only PROJECT.star,
// returning the declared module list. It does NOT evaluate MODULE.star or any
// unit files, so it succeeds even when module contents have errors. Use this
// when you need the module declarations alone — `yoe module sync` relies on
// it so a broken module can still be re-synced to pull in a fix.
//
// Honored LoadOptions: WithProjectFile. Others (machine, shadows, sync
// callback) don't apply because no module content is loaded.
func ProjectModuleRefs(startDir string, opts ...LoadOption) ([]ModuleRef, error) {
	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	var cfg loadConfig
	for _, o := range opts {
		o(&cfg)
	}

	return projectModuleRefsFromRoot(root, cfg)
}

// ResolveProjectModules finds the project root, evaluates only PROJECT.star,
// and resolves the declared module refs to names and on-disk paths when they
// are available. It intentionally avoids evaluating module unit files so CLI
// metadata commands keep working when a module checkout is broken.
func ResolveProjectModules(startDir string, opts ...LoadOption) ([]ResolvedModule, error) {
	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	var cfg loadConfig
	for _, o := range opts {
		o(&cfg)
	}

	refs, err := projectModuleRefsFromRoot(root, cfg)
	if err != nil {
		return nil, err
	}

	resolved := make([]ResolvedModule, 0, len(refs))
	for _, m := range refs {
		modulePath, cloneDir, ok := locateModulePath(m, root)
		rm := ResolvedModule{
			URL:       m.URL,
			Ref:       m.Ref,
			Path:      m.Path,
			Local:     m.Local,
			Available: ok,
		}
		if ok {
			rm.Name = peekModuleName(modulePath)
			rm.Dir = modulePath
			rm.CloneDir = cloneDir
		}
		if rm.Name == "" {
			rm.Name = pathBasename(m)
		}
		resolved = append(resolved, rm)
	}
	return resolved, nil
}

func projectModuleRefsFromRoot(root string, cfg loadConfig) ([]ModuleRef, error) {
	eng := NewEngine()
	eng.SetProjectRoot(root)

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
	proj := eng.Project()
	if proj == nil {
		return nil, nil
	}
	return proj.Modules, nil
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

	// Materialize any caller-supplied builtins (alpine_feed, etc.) so
	// they're predeclared before any .star file evaluates.
	if len(cfg.extraBuiltins) > 0 {
		specs := make(map[string]BuiltinFactory, len(cfg.extraBuiltins))
		for _, b := range cfg.extraBuiltins {
			specs[b.name] = b.factory
		}
		eng.SetExtraBuiltins(specs)
	}

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

	// Layer local.star's per-developer overrides on top of the
	// project's committed defaults. Today only DefaultDistroOverride
	// flows through here; the other fields (machine, image, qemu_*,
	// parallel_builds) are consumed by their own callsites via
	// LoadLocalOverrides directly.
	if proj := eng.Project(); proj != nil {
		if ov, err := LoadLocalOverrides(root); err == nil {
			if ov.DefaultDistroOverride != "" {
				proj.DefaultDistroOverride = ov.DefaultDistroOverride
			}
		}
	}

	// Sync modules + walk their MODULE.star for transitive deps in an
	// iterated sync↔peek fixpoint. Each round: (1) sync the current set,
	// (2) peek each module for its declared `module_info(deps=...)`,
	// (3) accumulate new deps, (4) repeat until no new deps appear.
	//
	// Cycle detection runs on the final dep graph; same-name + different
	// ref collisions raise a clear error (project-level always wins
	// against transitive collisions).
	if proj := eng.Project(); proj != nil {
		expanded, err := expandTransitiveDeps(proj.Modules, root, cfg.moduleSync, os.Stderr)
		if err != nil {
			return nil, err
		}
		proj.Modules = expanded
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
	var resolvedForProject []ResolvedModule
	if proj := eng.Project(); proj != nil {
		for _, m := range proj.Modules {
			modulePath, cloneDir, ok := locateModulePath(m, root)
			rm := ResolvedModule{
				URL:       m.URL,
				Ref:       m.Ref,
				Path:      m.Path,
				Local:     m.Local,
				Available: ok,
			}
			if !ok {
				rm.Name = pathBasename(m)
				resolvedForProject = append(resolvedForProject, rm)
				continue
			}
			name := peekModuleName(modulePath)
			if name == "" {
				name = pathBasename(m)
			}
			rm.Name = name
			rm.Dir = modulePath
			rm.CloneDir = cloneDir
			resolvedForProject = append(resolvedForProject, rm)
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

	// Build ctx — a single Starlark struct exposing the active machine /
	// project context to unit and image definitions. Defaults: arch =
	// x86_64 if no machine is configured; machine and project_version
	// are empty strings; machine_config is absent if no machine is
	// configured.
	//
	// ctx.provides is a mutable *starlark.Dict; the Go-side `provides`
	// reference embedded in the struct lets Phase 2 mutations flow
	// through to image-time lookups because the struct holds dict
	// identity, not a snapshot.
	//
	// ctx.runtime_deps was removed in the feeds-as-modules cutover —
	// the Starlark-side dict required eagerly materializing every
	// registered unit's deps, which defeats R20's "closure size bounds
	// memory" promise at Debian-class scale. Image classes now call
	// the resolve_closure(artifacts) builtin instead, which walks the
	// dep graph in Go and materializes only the units it reaches.
	arch := "x86_64"
	machine := ""
	projectVersion := ""
	var activeMachine *Machine
	if proj := eng.Project(); proj != nil {
		machine = proj.Defaults.Machine
		projectVersion = proj.Version
		if m, ok := eng.Machines()[proj.Defaults.Machine]; ok {
			arch = m.Arch
			activeMachine = m
		}
	}

	provides := starlark.NewDict(4)
	if activeMachine != nil && activeMachine.Kernel.Provides != "" {
		_ = provides.SetKey(
			starlark.String(activeMachine.Kernel.Provides),
			starlark.String(activeMachine.Kernel.Unit),
		)
	}

	var (
		defaultDistro         string
		defaultDistroOverride string
	)
	if proj := eng.Project(); proj != nil {
		defaultDistro = proj.DefaultDistro
		defaultDistroOverride = proj.DefaultDistroOverride
	}
	ctxFields := starlark.StringDict{
		"arch":                    starlark.String(arch),
		"machine":                 starlark.String(machine),
		"project_version":         starlark.String(projectVersion),
		"provides":                provides,
		"default_distro":          starlark.String(defaultDistro),
		"default_distro_override": starlark.String(defaultDistroOverride),
	}
	if activeMachine != nil {
		ctxFields["machine_config"] = buildMachineConfigStruct(activeMachine)
	}
	eng.SetVar("ctx", starlarkstruct.FromStringDict(starlark.String("ctx"), ctxFields))

	// Phase 1c: Evaluate each module's MODULE.star fully (not just the
	// module_info peek used for the dep walk). This is where alpine_feed,
	// apt_feed, and other feed-declaring builtins run — they register
	// SyntheticModules against the engine. Runs after machines + ctx
	// build so the feed builtins see the active arch.
	//
	// The project itself doesn't have a MODULE.star; only declared
	// modules do. Set arch on the engine so lazy feed lookups can filter
	// per-arch entries without arch threading.
	eng.SetActiveArch(arch)
	for i, rm := range resolvedModules {
		modFile := filepath.Join(rm.path, "MODULE.star")
		if _, statErr := os.Stat(modFile); statErr != nil {
			continue // module without a MODULE.star — rare, but tolerated
		}
		eng.SetCurrentModule(rm.name, i+1)
		if err := eng.ExecFile(modFile); err != nil {
			return nil, fmt.Errorf("evaluating %s: %w", modFile, err)
		}
	}

	// Preflight prefer_modules pins now that real + synthetic module
	// names are known. Running before the unit/image phases means a
	// stale pin (e.g., "alpine" → "alpine.main" after a
	// feeds-as-modules cutover) surfaces with the helpful fixit
	// message instead of the cryptic "unresolved name X" the closure
	// walk would emit when it discovers the pinned-but-not-registered
	// unit is missing.
	if proj := eng.Project(); proj != nil && len(proj.PreferModules) > 0 {
		known := make(map[string]struct{}, len(resolvedModules)+len(eng.SyntheticModules()))
		for _, rm := range resolvedModules {
			known[rm.name] = struct{}{}
		}
		for _, sm := range eng.SyntheticModules() {
			known[sm.Name] = struct{}{}
		}
		if err := preflightPreferModules(proj.PreferModules, known); err != nil {
			return nil, err
		}
	}

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

	// Add unit provides to ctx.provides (mutating the dict in place — the
	// ctx struct already holds a reference to it). A unit may declare
	// multiple virtual names (apk-style); each is registered independently
	// with the same module-priority conflict rules.
	//
	// Iterate units in a stable, name-sorted order. eng.Units() is a map,
	// and the conflict rule below is "first registered wins" among
	// same-module claimants of a virtual. Ranging the map directly made
	// that winner depend on Go's randomized map iteration, so a virtual
	// like `ifupdown-any` (claimed by ifupdown-ng, busybox-ifupdown, and
	// openrc in the same module) resolved to a different unit run-to-run,
	// silently changing every image's runtime closure and churning the
	// build cache. Sorting makes the resolution reproducible.
	unitsByName := eng.Units()
	sortedUnitNames := make([]string, 0, len(unitsByName))
	for name := range unitsByName {
		sortedUnitNames = append(sortedUnitNames, name)
	}
	sort.Strings(sortedUnitNames)
	for _, uname := range sortedUnitNames {
		u := unitsByName[uname]
		for _, virt := range u.Provides {
			if virt == "" {
				continue
			}
			if existing, found, _ := provides.Get(starlark.String(virt)); found {
				existingName := string(existing.(starlark.String))
				// Look up the existing unit to compare module priority.
				existingUnit := eng.Units()[existingName]
				// Two units providing the same virtual but with different
				// Distro tags are not a collision per R21a — each is
				// visible only to its own distro's closure, and the
				// closure walker dispatches via ResolveProvidesForDistro.
				// The single-keyed proj.Provides table picks one as the
				// global default; the distro-aware lookup overrides it
				// per-walk.
				if existingUnit != nil && u.Distro != "" && existingUnit.Distro != "" && u.Distro != existingUnit.Distro {
					continue
				}
				if existingUnit == nil || u.ModuleIndex == existingUnit.ModuleIndex {
					if !eng.allowDuplicateProvides {
						return nil, fmt.Errorf("virtual package %q provided by both %q and %q",
							virt, existingName, u.Name)
					}
					// First-wins: leave provides pointing at existingName.
					continue
				}
				if u.ModuleIndex > existingUnit.ModuleIndex {
					if eng.showShadows {
						fmt.Fprintf(os.Stderr, "notice: %q from %s overrides %q via provides %q\n",
							u.Name, moduleSource(u.Module), existingName, virt)
					}
					_ = provides.SetKey(starlark.String(virt), starlark.String(u.Name))
				}
				// If u.ModuleIndex < existingUnit.ModuleIndex, skip — higher priority already won.
				continue
			}
			_ = provides.SetKey(starlark.String(virt), starlark.String(u.Name))
		}
	}

	// (Pre-feeds-as-modules this block populated ctx.runtime_deps —
	// removed; image classes call resolve_closure(artifacts) instead.)

	// Mirror the Starlark provides dict onto proj.Provides before the
	// image phase so the closure walk (resolve_closure / Engine.closure)
	// can consult it. This used to happen later, after the image phase,
	// which worked because the Starlark-side closure walk read provides
	// directly off ctx; the Go-side walk reads from proj.Provides and
	// runs during image() evaluation, so the mirror must move earlier.
	if proj := eng.Project(); proj != nil {
		proj.Provides = map[string]string{}
		for _, item := range provides.Items() {
			k, kok := item[0].(starlark.String)
			v, vok := item[1].(starlark.String)
			if kok && vok {
				proj.Provides[string(k)] = string(v)
			}
		}
	}

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
	proj.UnitsByModule = eng.UnitsByModule()
	proj.ResolvedModules = resolvedForProject
	proj.Diagnostics.Shadows = eng.Shadows()

	// Synthetic modules (alpine_feed, apt_feed): rank strictly below
	// every real module per R5. Assign Priority in registration order so
	// the first-registered synthetic outranks later ones, mirroring the
	// real-module "last-wins among modules" convention (1..N): synthetic
	// indices live below 1 (0, -1, -2, ...) so the existing higher-wins
	// comparison still routes correctly. Priorities are negative because
	// the lowest valid real-module index is 1; using zero or negative
	// values keeps the relative ordering "any real module wins over any
	// synthetic" trivially true without coupling to the project-root
	// index value.
	synths := eng.SyntheticModules()
	if len(synths) > 0 {
		for i, sm := range synths {
			// First-registered gets the highest synthetic priority (0),
			// last-registered the lowest. This matches the existing
			// real-module "later-declared wins" tiebreak but keeps every
			// synthetic strictly below every real module.
			sm.Priority = -i
		}
		proj.SyntheticModules = synths
	}

	// Re-mirror the Starlark ctx.provides dict onto the Go side. The
	// pre-image-phase mirror above seeded proj.Provides so the closure
	// walk could resolve virtuals; image() definitions can declare
	// `provides` too, so re-sync here to capture any late additions.
	proj.Provides = map[string]string{}
	for _, item := range provides.Items() {
		k, kok := item[0].(starlark.String)
		v, vok := item[1].(starlark.String)
		if kok && vok {
			proj.Provides[string(k)] = string(v)
		}
	}

	// Compute duplicate-provides diagnostics: every virtual claimed by
	// more than one unit. The active provider in proj.Provides is the
	// winner; the rest go in Others. Sorted by virtual name and then by
	// unit name so the diagnostics tab is deterministic. Walk the
	// per-module catalog so cross-distro siblings each contribute
	// their own Provides claims.
	virtToUnits := map[string][]string{}
	for _, u := range proj.AllUnits() {
		for _, virt := range u.Provides {
			if virt == "" {
				continue
			}
			virtToUnits[virt] = append(virtToUnits[virt], u.Name)
		}
	}
	var virts []string
	for v := range virtToUnits {
		if len(virtToUnits[v]) > 1 {
			virts = append(virts, v)
		}
	}
	sort.Strings(virts)
	for _, v := range virts {
		claimants := virtToUnits[v]
		sort.Strings(claimants)
		active := proj.Provides[v]
		var others []string
		for _, c := range claimants {
			if c != active {
				others = append(others, c)
			}
		}
		proj.Diagnostics.DuplicateProvides = append(proj.Diagnostics.DuplicateProvides, ProvidesEvent{
			Virtual: v,
			Active:  active,
			Others:  others,
		})
	}

	// Validate: units with tasks must have container and
	// container_arch. Walk every registered unit (across modules)
	// since the requirement applies to all variants, not just the
	// project default's view.
	for name, u := range proj.AllUnits() {
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

	// Materialize build-time deps that reference synthetic units. The
	// image-phase closure walk pulls in runtime_deps; build-time deps
	// (`deps = [...]` on source-built units) are separate. A
	// source-built unit may declare a build-time dep on a name now
	// served only by alpine_feed / apt_feed, and BuildDAG below
	// would fail with "depends on X, which does not exist" unless we
	// materialize those names here. Iterate to fixpoint in case a
	// newly-materialized unit pulls in further synthetic deps.
	//
	// Distro-aware: an untagged unit consumed by both alpine and
	// debian closures needs each dep materialized in each distro
	// context, because the synthetic feed for that distro is the
	// only thing that can satisfy alpine vs debian package names
	// (e.g. py3-setuptools vs python3-setuptools). Without this, the
	// first walk wins eng.units[name] for one distro and the other
	// distro's BuildDAG silently drops the dep as missing.
	distroSet := map[string]struct{}{}
	if proj := eng.Project(); proj != nil {
		if proj.DefaultDistro != "" {
			distroSet[proj.DefaultDistro] = struct{}{}
		}
		if proj.DefaultDistroOverride != "" {
			distroSet[proj.DefaultDistroOverride] = struct{}{}
		}
	}
	// Scan every image variant in the per-module catalog, not just the
	// bare-name winners in eng.Units(): when two modules define the same
	// image name under different distros (module-debian and module-ubuntu
	// both ship base-image/dev-image/ssh-image), only the higher-priority
	// module's variant survives in eng.units. Deriving distroSet from the
	// shadowed map would drop the losing distro entirely, so this
	// fixpoint never materializes its feed-only build deps (e.g. a source
	// unit's distro_deps["debian"] = ["zlib1g-dev"]). The dep then
	// resolves to nothing at BuildDAG time and is silently dropped,
	// leaving an empty sysroot. A build can still select the shadowed
	// distro via --distro or per-image resolution, so every distro any
	// image targets must be pre-materialized regardless of which variant
	// won the bare name.
	for _, byName := range eng.UnitsByModule() {
		for _, u := range byName {
			if u.Class == "image" && u.Distro != "" {
				distroSet[u.Distro] = struct{}{}
			}
		}
	}
	for {
		added := 0
		for d := range distroSet {
			for name, unit := range eng.Units() {
				if !visibleToDistro(unit, d) {
					continue
				}
				// Walk both Deps (build-time) and RuntimeDeps for
				// the consuming distro, including any distro_deps /
				// distro_runtime_deps additions. For debian-style
				// split packages, build-time consumers need the
				// runtime closure of each build dep to be materialized
				// so AssembleSysroot can stage every piece
				// (libpython3.11-stdlib, libpython3.11-minimal,
				// libssl3, libexpat1, ...) — not just the wrapper deb
				// the unit names directly. Alpine's monolithic apks
				// converge in one hop; debian's fan-out walks deeper.
				edges := append(append([]string{}, unit.DepsForDistro(d)...), unit.RuntimeDepsForDistro(d)...)
				for _, dep := range edges {
					resolved := eng.resolveProvidesForDistro(dep, d)
					if eng.findVisibleByName(resolved, d) != nil {
						continue
					}
					u, err := eng.lookupOrMaterialize(resolved, d)
					if err != nil {
						return nil, fmt.Errorf("materializing dep %q of unit %q (distro %q): %w", dep, name, d, err)
					}
					if u != nil {
						added++
					}
				}
			}
		}
		if added == 0 {
			break
		}
	}

	// Validate prefer_modules: every value must name a known module
	// (real or synthetic). Surfaces a fixit message with the closest
	// candidates when a pin lands on a name no module advertises —
	// catches the common "alpine" → "alpine.main" / "alpine.community"
	// confusion after the feeds-as-modules cutover, where the parent
	// module-alpine no longer registers units directly.
	if err := validatePreferModules(proj); err != nil {
		return nil, err
	}

	// Refresh UnitsByModule after the build-time dep fixpoint may
	// have materialized additional synthetic units. The eng map is
	// the source of truth; reassign so proj.UnitsByModule reflects
	// every materialization that happened during this load.
	proj.UnitsByModule = eng.UnitsByModule()

	// Build the per-distro views (R14b). Each consuming image's
	// effective distro gets its own pre-resolved [name → *Unit] map
	// so closure-walk and BuildDAG lookups are O(1) without
	// re-running prefer_modules + module-priority + R21a visibility
	// on every probe. Cross-distro same-name collisions resolve here
	// once, not on every closure walk.
	proj.DistroViews = buildDistroViews(proj)

	return proj, nil
}

// buildDistroViews precomputes a per-distro resolved [name → *Unit]
// map from UnitsByModule. For each (name, distro) pair, applies:
//
//  1. prefer_modules[distro][name] pin (if set and the pinned module
//     has a visible unit for this name) — pin wins.
//  2. Module priority: highest-priority module's unit visible to the
//     distro wins. Untagged units are visible to every distro; tagged
//     units only their own.
//
// A distro is included in the result if any unit in UnitsByModule is
// either untagged or tagged for that distro. The project's
// DefaultDistro and DefaultDistroOverride are also included so
// callers can rely on those keys existing even if no unit references
// the distro yet.
func buildDistroViews(proj *Project) map[string]map[string]*Unit {
	if proj == nil {
		return nil
	}
	// Collect every distinct distro seen across registered units, plus
	// the project-level fallbacks.
	distros := map[string]struct{}{}
	if proj.DefaultDistro != "" {
		distros[proj.DefaultDistro] = struct{}{}
	}
	if proj.DefaultDistroOverride != "" {
		distros[proj.DefaultDistroOverride] = struct{}{}
	}
	for d := range proj.PreferModules {
		distros[d] = struct{}{}
	}
	allNames := map[string]struct{}{}
	for _, byName := range proj.UnitsByModule {
		for name, u := range byName {
			allNames[name] = struct{}{}
			if u.Distro != "" {
				distros[u.Distro] = struct{}{}
			}
		}
	}

	views := make(map[string]map[string]*Unit, len(distros))
	for distro := range distros {
		view := make(map[string]*Unit, len(allNames))
		for name := range allNames {
			if u := resolveForDistro(proj, distro, name); u != nil {
				view[name] = u
			}
		}
		views[distro] = view
	}
	return views
}

// resolveForDistro picks the right unit for (distro, name) from
// UnitsByModule. Applies prefer_modules pins first, then falls back
// to module priority (highest ModuleIndex wins) + R21a visibility.
// Returns nil when no candidate exists.
func resolveForDistro(proj *Project, distro, name string) *Unit {
	// Pin check.
	if pins, ok := proj.PreferModules[distro]; ok {
		if pinned, ok := pins[name]; ok && pinned != "" {
			if byName, ok := proj.UnitsByModule[pinned]; ok {
				if u, ok := byName[name]; ok && unitVisibleToDistro(u, distro) {
					return u
				}
			}
		}
	}
	// Default: highest-priority module's unit visible to distro.
	var best *Unit
	for _, byName := range proj.UnitsByModule {
		u, ok := byName[name]
		if !ok {
			continue
		}
		if !unitVisibleToDistro(u, distro) {
			continue
		}
		if best == nil || u.ModuleIndex > best.ModuleIndex {
			best = u
		}
	}
	return best
}

// unitVisibleToDistro mirrors the closure walker's visibleToDistro
// for use from buildDistroViews (which is in the same package but
// can't import a private walker helper without rearranging).
// Untagged units are visible to every distro; tagged units only
// their own.
func unitVisibleToDistro(u *Unit, distro string) bool {
	if u == nil {
		return false
	}
	if distro == "" {
		return true
	}
	return u.Distro == "" || u.Distro == distro
}

// validatePreferModules walks proj.PreferModules and errors when a
// pin's value doesn't match any known module name. The error includes
// up to three nearest-match suggestions (substring or prefix matches
// against the union of real-module + synthetic-module names) so the
// user can see immediately whether they meant a feed name they
// forgot to qualify.
func validatePreferModules(proj *Project) error {
	if proj == nil || len(proj.PreferModules) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(proj.ResolvedModules)+len(proj.SyntheticModules))
	for _, rm := range proj.ResolvedModules {
		if rm.Name != "" {
			known[rm.Name] = struct{}{}
		}
	}
	for _, sm := range proj.SyntheticModules {
		if sm.Name != "" {
			known[sm.Name] = struct{}{}
		}
	}
	return preflightPreferModules(proj.PreferModules, known)
}

// preflightPreferModules is the core check used by both
// validatePreferModules (at end of load) and the early preflight in
// LoadProjectFromRoot (before resolve_closure runs, so the user sees
// the helpful fixit instead of a confusing "unresolved name" from
// the closure walk). Walks the nested-per-distro shape and errors on
// the first pin whose module name doesn't appear in the known set.
func preflightPreferModules(prefer map[string]map[string]string, known map[string]struct{}) error {
	for distro, pins := range prefer {
		for unit, modName := range pins {
			if modName == "" {
				continue
			}
			if _, ok := known[modName]; ok {
				continue
			}
			suggestions := suggestModuleNames(modName, known)
			hint := ""
			switch len(suggestions) {
			case 0:
				// nothing to suggest
			case 1:
				hint = fmt.Sprintf(" Did you mean %q?", suggestions[0])
			default:
				quoted := make([]string, len(suggestions))
				for i, s := range suggestions {
					quoted[i] = fmt.Sprintf("%q", s)
				}
				hint = fmt.Sprintf(" Did you mean one of: %s?", strings.Join(quoted, ", "))
			}
			return fmt.Errorf(
				`prefer_modules[%q] entry %q: %q — module %q not found.%s See docs/module-alpine.md "alpine_feed: declaring a whole repo as one module entry" for the alpine → alpine.main/alpine.community migration.`,
				distro, unit, modName, modName, hint)
		}
	}
	return nil
}

// suggestModuleNames picks up to three module names that are close
// to `target` — prefix match wins, then substring match. Empty
// suggestions returned when nothing matches.
func suggestModuleNames(target string, known map[string]struct{}) []string {
	var prefixed, contained []string
	for name := range known {
		switch {
		case name == target:
			continue
		case strings.HasPrefix(name, target+"."):
			// Exact qualifier promotion ("alpine" → "alpine.main"):
			// always rank these first.
			prefixed = append(prefixed, name)
		case strings.Contains(name, target):
			contained = append(contained, name)
		}
	}
	sort.Strings(prefixed)
	sort.Strings(contained)
	out := append(prefixed, contained...)
	if len(out) > 3 {
		out = out[:3]
	}
	return out
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

// locateModulePath returns the on-disk MODULE.star directory and the
// git clone root for a module — either the local override or the cache
// directory under YOE_CACHE/modules. The two paths differ when the
// module declares a `path = "..."` subdir; otherwise they are equal.
// The boolean is false when neither location exists (the module hasn't
// been synced yet).
func locateModulePath(m ModuleRef, projectRoot string) (modulePath, cloneDir string, ok bool) {
	base := pathBasename(m)
	if m.Local != "" {
		cloneDir = m.Local
		if !filepath.IsAbs(cloneDir) {
			cloneDir = filepath.Join(projectRoot, cloneDir)
		}
		modulePath = cloneDir
		if m.Path != "" {
			modulePath = filepath.Join(cloneDir, m.Path)
		}
		return modulePath, cloneDir, true
	}
	cacheDir := os.Getenv("YOE_CACHE")
	if cacheDir == "" {
		cacheDir = "cache"
	}
	cloneDir = filepath.Join(cacheDir, "modules", base)
	modulePath = cloneDir
	if m.Path != "" {
		modulePath = filepath.Join(cloneDir, m.Path)
	}
	if _, err := os.Stat(modulePath); err != nil {
		return "", "", false
	}
	return modulePath, cloneDir, true
}

// peekModuleName evaluates MODULE.star at modulePath in an isolated thread
// and returns the name declared via module_info(name=...). Returns "" if
// MODULE.star is missing, fails to parse, or doesn't call module_info.
// This is intentionally separate from the main engine eval so the canonical
// name is known before any registration happens.
//
// Use peekModuleInfo when you also need the declared transitive deps (the
// recursive-module walking path in LoadProjectFromRoot).
func peekModuleName(modulePath string) string {
	info := peekModuleInfo(modulePath)
	if info == nil {
		return ""
	}
	return info.Name
}

// peekModuleInfo evaluates MODULE.star in an isolated thread and returns
// the declared name + transitive deps. Returns nil when MODULE.star is
// missing or fails to parse. Errors inside module_info or module() are
// swallowed — peek is a best-effort pre-evaluation pass and the real
// evaluation later surfaces any syntax issues with proper error context.
//
// The captured Deps slice is what the recursive-module walker (U4)
// uses to extend the project's module list to its transitive closure.
func peekModuleInfo(modulePath string) *ModuleInfo {
	file := filepath.Join(modulePath, "MODULE.star")
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	info := &ModuleInfo{}
	moduleInfo := starlark.NewBuiltin("module_info",
		func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			for _, kv := range kwargs {
				key, ok := kv[0].(starlark.String)
				if !ok {
					continue
				}
				switch string(key) {
				case "name":
					if v, ok := kv[1].(starlark.String); ok {
						info.Name = string(v)
					}
				case "description":
					if v, ok := kv[1].(starlark.String); ok {
						info.Description = string(v)
					}
				case "deps":
					info.Deps = parsePeekDeps(kv[1])
				}
			}
			return starlark.None, nil
		})
	// `module(url=..., ref=..., path=..., local=...)` is the builder
	// invoked inside module_info(deps=[module(...), ...]). The deps
	// list captures ModuleRef structs; the builtin records them via a
	// closure-shared slot so module_info's `deps=` arm can pick them
	// up after evaluation.
	moduleBuiltin := starlark.NewBuiltin("module",
		func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			ref := ModuleRef{}
			if len(args) >= 1 {
				if v, ok := args[0].(starlark.String); ok {
					ref.URL = string(v)
				}
			}
			for _, kv := range kwargs {
				key, ok := kv[0].(starlark.String)
				if !ok {
					continue
				}
				switch string(key) {
				case "url":
					if v, ok := kv[1].(starlark.String); ok {
						ref.URL = string(v)
					}
				case "ref":
					if v, ok := kv[1].(starlark.String); ok {
						ref.Ref = string(v)
					}
				case "path":
					if v, ok := kv[1].(starlark.String); ok {
						ref.Path = string(v)
					}
				case "local":
					if v, ok := kv[1].(starlark.String); ok {
						ref.Local = string(v)
					}
				}
			}
			return moduleRefValue{ref: ref}, nil
		})
	// alpine_feed / apt_feed / etc. are no-ops during the peek —
	// we only need to capture module_info(). Without these stubs
	// Starlark's compile-time resolver aborts before module_info()
	// runs, falling back to the basename and breaking synthetic
	// module names (alpine_feed registers under <parent>.<feed>,
	// where parent comes from this peek).
	noop := starlark.NewBuiltin("noop",
		func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			return starlark.None, nil
		})
	thread := &starlark.Thread{Name: file}
	_, _ = starlark.ExecFileOptions(fileOpts, thread, file, src, starlark.StringDict{
		"module_info": moduleInfo,
		"module":      moduleBuiltin,
		"alpine_feed": noop,
		"apt_feed":    noop,
	})
	return info
}

// moduleRefValue carries a ModuleRef through Starlark evaluation. It
// satisfies starlark.Value so module_info(deps=[module(...), ...]) can
// store it in a list without Starlark complaining about an unknown type.
type moduleRefValue struct{ ref ModuleRef }

func (moduleRefValue) String() string        { return "module_ref" }
func (moduleRefValue) Type() string          { return "module_ref" }
func (moduleRefValue) Freeze()               {}
func (moduleRefValue) Truth() starlark.Bool  { return starlark.True }
func (moduleRefValue) Hash() (uint32, error) { return 0, fmt.Errorf("module_ref is not hashable") }

// parsePeekDeps unwraps a Starlark list of module() values into a Go
// slice of ModuleRef. Anything that isn't a moduleRefValue is silently
// skipped — the peek pass is best-effort and the real evaluation will
// catch malformed entries with a proper error.
func parsePeekDeps(v starlark.Value) []ModuleRef {
	list, ok := v.(*starlark.List)
	if !ok {
		return nil
	}
	out := make([]ModuleRef, 0, list.Len())
	iter := list.Iterate()
	defer iter.Done()
	var item starlark.Value
	for iter.Next(&item) {
		if mr, ok := item.(moduleRefValue); ok {
			out = append(out, mr.ref)
		}
	}
	return out
}

// buildMachineConfigStruct produces the ctx.machine_config struct from a
// resolved *Machine. Exposes name, arch, packages, partitions, and (when
// declared) kernel info to Starlark unit/image definitions.
func buildMachineConfigStruct(m *Machine) *starlarkstruct.Struct {
	machineDict := starlark.StringDict{
		"name":     starlark.String(m.Name),
		"arch":     starlark.String(m.Arch),
		"packages": toStarlarkStringList(m.Packages),
	}
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

	if m.Kernel.Unit != "" {
		machineDict["kernel"] = starlarkstruct.FromStringDict(
			starlark.String("kernel"), starlark.StringDict{
				"unit":      starlark.String(m.Kernel.Unit),
				"provides":  starlark.String(m.Kernel.Provides),
				"defconfig": starlark.String(m.Kernel.Defconfig),
				"cmdline":   starlark.String(m.Kernel.Cmdline),
			})
	}
	return starlarkstruct.FromStringDict(starlark.String("machine_config"), machineDict)
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
