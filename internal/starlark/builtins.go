package starlark

import (
	"fmt"
	"os"
	"path/filepath"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// builtins returns the predeclared names available in all .star files.
func (e *Engine) builtins() starlark.StringDict {
	d := starlark.StringDict{
		"project":          starlark.NewBuiltin("project", e.fnProject),
		"defaults":         starlark.NewBuiltin("defaults", fnDefaults),
		"cache":            starlark.NewBuiltin("cache", fnCache),
		"module":           starlark.NewBuiltin("module", fnModule),
		"module_info":      starlark.NewBuiltin("module_info", e.fnModuleInfo),
		"machine":          starlark.NewBuiltin("machine", e.fnMachine),
		"kernel":           starlark.NewBuiltin("kernel", fnKernel),
		"qemu_config":      starlark.NewBuiltin("qemu_config", fnQEMUConfig),
		"unit":             starlark.NewBuiltin("unit", e.fnUnit),
		"image":            starlark.NewBuiltin("image", e.fnImage),
		"partition":        starlark.NewBuiltin("partition", fnPartition),
		"task":             starlark.NewBuiltin("task", fnTask),
		"command":          starlark.NewBuiltin("command", e.fnCommand),
		"arg":              starlark.NewBuiltin("arg", fnArg),
		"run":              starlark.NewBuiltin("run", fnRunPlaceholder),
		"dir_size_mb":      starlark.NewBuiltin("dir_size_mb", fnDirSizeMBPlaceholder),
		"install_file":     starlark.NewBuiltin("install_file", fnInstallFile),
		"install_template": starlark.NewBuiltin("install_template", fnInstallTemplate),
		"resolve_closure":  starlark.NewBuiltin("resolve_closure", e.fnResolveClosure),
		"True":             starlark.True,
		"False":            starlark.False,
	}

	// Merge engine variables (e.g., ARCH set after machine loading).
	for k, v := range e.vars {
		d[k] = v
	}

	// Merge extra builtins registered via WithBuiltin LoadOption.
	// Materialized once (SetExtraBuiltins) so each factory runs against
	// the live Engine without re-allocating per ExecFile call.
	for k, v := range e.extraBuiltins {
		d[k] = v
	}

	return d
}

// fnRunPlaceholder is registered as a global so that lambda closures can
// capture the name "run" at evaluation time.  When called from a build
// thread (with sandbox config in thread-local storage) it delegates to the
// real implementation in the build package.  When called outside a build
// thread it returns an error.
func fnRunPlaceholder(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Check if we're in a build thread by looking for the sandbox key.
	if thread.Local("yoe.sandbox") != nil {
		// Delegate to the real run() registered via BuildPredeclared.
		// The build package sets "yoe.run" on the thread.
		if fn := thread.Local("yoe.run"); fn != nil {
			if callable, ok := fn.(starlark.Callable); ok {
				return starlark.Call(thread, callable, args, kwargs)
			}
		}
	}
	return nil, fmt.Errorf("run() can only be called at build time (inside a task function)")
}

// fnDirSizeMBPlaceholder mirrors fnRunPlaceholder so dir_size_mb() captures
// at evaluation time and dispatches to the build-package implementation
// at call time via thread-local lookup.
func fnDirSizeMBPlaceholder(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if thread.Local("yoe.sandbox") != nil {
		if fn := thread.Local("yoe.dir_size_mb"); fn != nil {
			if callable, ok := fn.(starlark.Callable); ok {
				return starlark.Call(thread, callable, args, kwargs)
			}
		}
	}
	return nil, fmt.Errorf("dir_size_mb() can only be called at build time (inside a task function)")
}

// --- Helper: extract keyword args ---

func kwString(kwargs []starlark.Tuple, key string) string {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if s, ok := kv[1].(starlark.String); ok {
				return string(s)
			}
		}
	}
	return ""
}

// ParseTaskList converts a Starlark list of task structs into Go Task values.
func ParseTaskList(list *starlark.List) []Task {
	var tasks []Task
	iter := list.Iterate()
	defer iter.Done()
	var v starlark.Value
	for iter.Next(&v) {
		s, ok := v.(*starlarkstruct.Struct)
		if !ok {
			continue
		}
		t := Task{
			Name:      structString(s, "name"),
			Container: structString(s, "container"),
		}
		if rv, err := s.Attr("run"); err == nil {
			if cmd, ok := rv.(starlark.String); ok {
				t.Steps = []Step{{Command: string(cmd)}}
			}
		}
		if rv, err := s.Attr("fn"); err == nil {
			if fn, ok := rv.(starlark.Callable); ok {
				t.Steps = []Step{{Fn: fn}}
			}
		}
		if rv, err := s.Attr("steps"); err == nil {
			if list, ok := rv.(*starlark.List); ok {
				si := list.Iterate()
				var sv starlark.Value
				for si.Next(&sv) {
					switch val := sv.(type) {
					case starlark.String:
						t.Steps = append(t.Steps, Step{Command: string(val)})
					case *InstallStep:
						t.Steps = append(t.Steps, Step{Install: val})
					case starlark.Callable:
						t.Steps = append(t.Steps, Step{Fn: val})
					}
				}
				si.Done()
			}
		}
		tasks = append(tasks, t)
	}
	return tasks
}

func kwBool(kwargs []starlark.Tuple, key string) bool {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if b, ok := kv[1].(starlark.Bool); ok {
				return bool(b)
			}
		}
	}
	return false
}

func kwInt(kwargs []starlark.Tuple, key string) int {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if n, ok := kv[1].(starlark.Int); ok {
				v, _ := n.Int64()
				return int(v)
			}
		}
	}
	return 0
}

func kwStringList(kwargs []starlark.Tuple, key string) []string {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if list, ok := kv[1].(*starlark.List); ok {
				var result []string
				iter := list.Iterate()
				defer iter.Done()
				var v starlark.Value
				for iter.Next(&v) {
					if s, ok := v.(starlark.String); ok {
						result = append(result, string(s))
					}
				}
				return result
			}
		}
	}
	return nil
}

// kwStringListMap parses a kwarg shaped like
// `{"alpine": ["a", "b"], "debian": ["c"]}` into map[string][]string.
// Used for distro_deps / distro_runtime_deps where each distro key
// names additional deps that apply only to that distro's closure.
func kwStringListMap(kwargs []starlark.Tuple, key string) map[string][]string {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) != key {
			continue
		}
		d, ok := kv[1].(*starlark.Dict)
		if !ok {
			return nil
		}
		m := make(map[string][]string, d.Len())
		for _, item := range d.Items() {
			k, ok := item[0].(starlark.String)
			if !ok {
				continue
			}
			list, ok := item[1].(*starlark.List)
			if !ok {
				continue
			}
			var values []string
			iter := list.Iterate()
			var v starlark.Value
			for iter.Next(&v) {
				if s, ok := v.(starlark.String); ok {
					values = append(values, string(s))
				}
			}
			iter.Done()
			m[string(k)] = values
		}
		return m
	}
	return nil
}

func kwStringMap(kwargs []starlark.Tuple, key string) map[string]string {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if d, ok := kv[1].(*starlark.Dict); ok {
				m := make(map[string]string, d.Len())
				for _, item := range d.Items() {
					if k, ok := item[0].(starlark.String); ok {
						if v, ok := item[1].(starlark.String); ok {
							m[string(k)] = string(v)
						}
					}
				}
				return m
			}
		}
	}
	return nil
}

// unitFields is the single table describing how a unit() kwarg becomes a
// typed field on Unit. Each entry both reads the value and reserves the
// kwarg name, so a field cannot be added without its name also being
// excluded from Unit.Extra.
//
// That mattered: the reservation used to be a separate hand-maintained
// list, and it drifted. artifacts_explicit was read into a field but
// missing from the list, so it was also captured into Extra — which is
// part of the content-addressed hash. Every image sets it, so a purely
// presentational value silently became part of every image's cache key.
var unitFields = []struct {
	kwarg string
	set   func(*Unit, []starlark.Tuple)
}{
	{"version", func(u *Unit, kw []starlark.Tuple) { u.Version = kwString(kw, "version") }},
	{"release", func(u *Unit, kw []starlark.Tuple) { u.Release = kwInt(kw, "release") }},
	{"scope", func(u *Unit, kw []starlark.Tuple) { u.Scope = kwString(kw, "scope") }},
	{"description", func(u *Unit, kw []starlark.Tuple) { u.Description = kwString(kw, "description") }},
	{"license", func(u *Unit, kw []starlark.Tuple) { u.License = kwString(kw, "license") }},
	{"distro", func(u *Unit, kw []starlark.Tuple) { u.Distro = kwString(kw, "distro") }},
	{"source", func(u *Unit, kw []starlark.Tuple) { u.Source = kwString(kw, "source") }},
	{"sha256", func(u *Unit, kw []starlark.Tuple) { u.SHA256 = kwString(kw, "sha256") }},
	{"apk_checksum", func(u *Unit, kw []starlark.Tuple) { u.APKChecksum = kwString(kw, "apk_checksum") }},
	{"passthrough_apk", func(u *Unit, kw []starlark.Tuple) { u.PassthroughAPK = kwString(kw, "passthrough_apk") }},
	{"tag", func(u *Unit, kw []starlark.Tuple) { u.Tag = kwString(kw, "tag") }},
	{"branch", func(u *Unit, kw []starlark.Tuple) { u.Branch = kwString(kw, "branch") }},
	{"patches", func(u *Unit, kw []starlark.Tuple) { u.Patches = kwStringList(kw, "patches") }},
	{"deps", func(u *Unit, kw []starlark.Tuple) { u.Deps = kwStringList(kw, "deps") }},
	{"runtime_deps", func(u *Unit, kw []starlark.Tuple) { u.RuntimeDeps = kwStringList(kw, "runtime_deps") }},
	{"distro_deps", func(u *Unit, kw []starlark.Tuple) { u.DistroDeps = kwStringListMap(kw, "distro_deps") }},
	{"distro_runtime_deps", func(u *Unit, kw []starlark.Tuple) { u.DistroRuntimeDeps = kwStringListMap(kw, "distro_runtime_deps") }},
	{"container", func(u *Unit, kw []starlark.Tuple) { u.Container = kwString(kw, "container") }},
	{"container_arch", func(u *Unit, kw []starlark.Tuple) { u.ContainerArch = kwString(kw, "container_arch") }},
	{"sandbox", func(u *Unit, kw []starlark.Tuple) { u.Sandbox = kwBool(kw, "sandbox") }},
	{"shell", func(u *Unit, kw []starlark.Tuple) { u.Shell = kwString(kw, "shell") }},
	{"provides", func(u *Unit, kw []starlark.Tuple) { u.Provides = kwStringList(kw, "provides") }},
	{"replaces", func(u *Unit, kw []starlark.Tuple) { u.Replaces = kwStringList(kw, "replaces") }},
	{"services", func(u *Unit, kw []starlark.Tuple) { u.Services = kwStringList(kw, "services") }},
	{"conffiles", func(u *Unit, kw []starlark.Tuple) { u.Conffiles = kwStringList(kw, "conffiles") }},
	{"environment", func(u *Unit, kw []starlark.Tuple) { u.Environment = kwStringMap(kw, "environment") }},
	{"cache_dirs", func(u *Unit, kw []starlark.Tuple) { u.CacheDirs = kwStringMap(kw, "cache_dirs") }},
	{"artifacts", func(u *Unit, kw []starlark.Tuple) { u.Artifacts = kwStringList(kw, "artifacts") }},
	{"artifacts_explicit", func(u *Unit, kw []starlark.Tuple) { u.ArtifactsExplicit = kwStringList(kw, "artifacts_explicit") }},
	{"exclude", func(u *Unit, kw []starlark.Tuple) { u.Exclude = kwStringList(kw, "exclude") }},
	{"hostname", func(u *Unit, kw []starlark.Tuple) { u.Hostname = kwString(kw, "hostname") }},
	{"timezone", func(u *Unit, kw []starlark.Tuple) { u.Timezone = kwString(kw, "timezone") }},
	{"locale", func(u *Unit, kw []starlark.Tuple) { u.Locale = kwString(kw, "locale") }},
	// Set by image() only on the skip path — the selected machine's
	// kernel has no entry for this image's distro, so the image is
	// registered inert instead of resolved. See Unit.NotBuildable.
	{"unbuildable_machine", func(u *Unit, kw []starlark.Tuple) { u.UnbuildableMachine = kwString(kw, "unbuildable_machine") }},
	{"machine_kernel_distros", func(u *Unit, kw []starlark.Tuple) {
		u.MachineKernelDistros = kwStringList(kw, "machine_kernel_distros")
	}},
	{"tasks", func(u *Unit, kw []starlark.Tuple) {
		for _, kv := range kw {
			if string(kv[0].(starlark.String)) == "tasks" {
				if list, ok := kv[1].(*starlark.List); ok {
					u.Tasks = append(u.Tasks, ParseTaskList(list)...)
				}
			}
		}
	}},
	{"partitions", func(u *Unit, kw []starlark.Tuple) {
		for _, kv := range kw {
			if string(kv[0].(starlark.String)) == "partitions" {
				u.Partitions = append(u.Partitions, parsePartitions(kv[1])...)
			}
		}
	}},
}

// reservedUnitKwargs is derived from unitFields, plus the two kwargs
// registerUnit reads directly (name, and unit_class which overrides the
// registering builtin's class). Nothing here is maintained by hand.
var reservedUnitKwargs = func() map[string]bool {
	m := map[string]bool{"name": true, "unit_class": true}
	for _, f := range unitFields {
		m[f.kwarg] = true
	}
	return m
}()

// parsePartitions reads a list of partition(...) structs into Partitions.
// Shared by machine() (the board's default layout) and image() (an
// image overriding it), which must agree on the shape or an image's
// layout would quietly differ from the machine's.
//
// A non-list value, or a list element that is not a struct, yields
// nothing rather than an error: the surrounding kwarg loops accept
// whatever Starlark handed them, and partition() is the only thing that
// produces the struct this reads.
func parsePartitions(v starlark.Value) []Partition {
	list, ok := v.(*starlark.List)
	if !ok {
		return nil
	}
	var out []Partition
	iter := list.Iterate()
	defer iter.Done()
	var item starlark.Value
	for iter.Next(&item) {
		st, ok := item.(*starlarkstruct.Struct)
		if !ok {
			continue
		}
		p := Partition{
			Label:    structString(st, "label"),
			Type:     structString(st, "type"),
			Size:     structString(st, "size"),
			Contents: structStringList(st, "contents"),
		}
		if rv, err := st.Attr("root"); err == nil {
			if b, ok := rv.(starlark.Bool); ok {
				p.Root = bool(b)
			}
		}
		out = append(out, p)
	}
	return out
}

// starlarkToGo converts a Starlark value into a Go value suitable for JSON
// serialization and Go template rendering. Returns an error for unsupported
// types so unit definitions fail loudly instead of silently dropping data.
func starlarkToGo(v starlark.Value) (any, error) {
	switch x := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(x), nil
	case starlark.String:
		return string(x), nil
	case starlark.Int:
		n, ok := x.Int64()
		if !ok {
			return nil, fmt.Errorf("int value out of int64 range")
		}
		return n, nil
	case starlark.Float:
		return float64(x), nil
	case *starlark.List:
		out := make([]any, 0, x.Len())
		it := x.Iterate()
		defer it.Done()
		var item starlark.Value
		for it.Next(&item) {
			g, err := starlarkToGo(item)
			if err != nil {
				return nil, err
			}
			out = append(out, g)
		}
		return out, nil
	case *starlark.Dict:
		out := make(map[string]any, x.Len())
		for _, kv := range x.Items() {
			ks, ok := kv[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("dict key must be string, got %s", kv[0].Type())
			}
			g, err := starlarkToGo(kv[1])
			if err != nil {
				return nil, err
			}
			out[string(ks)] = g
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported type %s", v.Type())
	}
}

func kwStruct(kwargs []starlark.Tuple, key string) *starlarkstruct.Struct {
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == key {
			if s, ok := kv[1].(*starlarkstruct.Struct); ok {
				return s
			}
		}
	}
	return nil
}

func structString(s *starlarkstruct.Struct, field string) string {
	if s == nil {
		return ""
	}
	v, err := s.Attr(field)
	if err != nil {
		return ""
	}
	if str, ok := v.(starlark.String); ok {
		return string(str)
	}
	return ""
}

func structStringMap(s *starlarkstruct.Struct, field string) map[string]string {
	if s == nil {
		return nil
	}
	v, err := s.Attr(field)
	if err != nil {
		return nil
	}
	dict, ok := v.(*starlark.Dict)
	if !ok {
		return nil
	}
	result := make(map[string]string, dict.Len())
	for _, item := range dict.Items() {
		k, kok := item[0].(starlark.String)
		val, vok := item[1].(starlark.String)
		if kok && vok {
			result[string(k)] = string(val)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func structStringList(s *starlarkstruct.Struct, field string) []string {
	if s == nil {
		return nil
	}
	v, err := s.Attr(field)
	if err != nil {
		return nil
	}
	if list, ok := v.(*starlark.List); ok {
		var result []string
		iter := list.Iterate()
		defer iter.Done()
		var item starlark.Value
		for iter.Next(&item) {
			if str, ok := item.(starlark.String); ok {
				result = append(result, string(str))
			}
		}
		return result
	}
	return nil
}

// --- Built-in functions that return structs (data constructors) ---

func makeStruct(name string, kwargs []starlark.Tuple) *starlarkstruct.Struct {
	d := make(starlark.StringDict, len(kwargs))
	for _, kv := range kwargs {
		d[string(kv[0].(starlark.String))] = kv[1]
	}
	return starlarkstruct.FromStringDict(starlark.String(name), d)
}

func fnDefaults(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("defaults", kwargs), nil
}

func fnCache(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("cache", kwargs), nil
}

func fnModule(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("module() requires a URL argument")
	}
	url, ok := args[0].(starlark.String)
	if !ok {
		return nil, fmt.Errorf("module() URL must be a string")
	}
	d := starlark.StringDict{"url": url}
	for _, kv := range kwargs {
		d[string(kv[0].(starlark.String))] = kv[1]
	}
	return starlarkstruct.FromStringDict(starlark.String("module"), d), nil
}

func fnKernel(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("kernel", kwargs), nil
}

func fnQEMUConfig(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("qemu_config", kwargs), nil
}

func fnPartition(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return makeStruct("partition", kwargs), nil
}

// --- Built-in functions that register module info ---

func (e *Engine) fnModuleInfo(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {

	name := kwString(kwargs, "name")
	if name == "" {
		return nil, fmt.Errorf("module_info() requires name")
	}

	info := &ModuleInfo{
		Name:        name,
		Description: kwString(kwargs, "description"),
	}

	// Parse deps list of module() structs
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == "deps" {
			if list, ok := kv[1].(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var v starlark.Value
				for iter.Next(&v) {
					if s, ok := v.(*starlarkstruct.Struct); ok {
						info.Deps = append(info.Deps, ModuleRef{
							URL: structString(s, "url"),
							Ref: structString(s, "ref"),
						})
					}
				}
			}
		}
	}

	e.moduleInfo = info
	return starlark.None, nil
}

// --- Built-in functions that register targets (side-effecting) ---

func (e *Engine) fnProject(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {

	if e.project != nil {
		return nil, fmt.Errorf("project() called more than once")
	}

	defs := kwStruct(kwargs, "defaults")
	cacheS := kwStruct(kwargs, "cache")

	p := &Project{
		Name:    kwString(kwargs, "name"),
		Version: kwString(kwargs, "version"),
		Defaults: Defaults{
			Machine: structString(defs, "machine"),
			Image:   structString(defs, "image"),
		},
		Cache: CacheConfig{
			Path: structString(cacheS, "path"),
		},
		SigningKey:    kwString(kwargs, "signing_key"),
		DefaultDistro: structString(defs, "distro"),
	}

	// Parse modules list
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == "modules" {
			if list, ok := kv[1].(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var v starlark.Value
				for iter.Next(&v) {
					if s, ok := v.(*starlarkstruct.Struct); ok {
						p.Modules = append(p.Modules, ModuleRef{
							URL:   structString(s, "url"),
							Ref:   structString(s, "ref"),
							Path:  structString(s, "path"),
							Local: structString(s, "local"),
						})
					}
				}
			}
		}
	}

	// Parse prefer_modules: nested per-distro dict
	//   {"<distro>": {"<unit>": "<module>", ...}, ...}
	// Outer key is the consuming image's effective distro; inner key
	// is the unit name; value is the pinned module. A pin only fires
	// for closures matching its outer key. This shape keeps Alpine
	// and Debian pins from interfering with each other in mixed-distro
	// projects — pinning xz to alpine.main has no effect on a debian
	// closure walk where alpine.main is filtered out by R21a.
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) != "prefer_modules" {
			continue
		}
		d, ok := kv[1].(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("project: prefer_modules must be a dict")
		}
		p.PreferModules = make(map[string]map[string]string, d.Len())
		for _, pair := range d.Items() {
			distroKey, dok := pair.Index(0).(starlark.String)
			inner, iok := pair.Index(1).(*starlark.Dict)
			if !dok || !iok {
				return nil, fmt.Errorf("project: prefer_modules outer keys must be distro strings and values must be dicts of {unit: module}")
			}
			distroName := string(distroKey)
			pins := make(map[string]string, inner.Len())
			for _, ipair := range inner.Items() {
				k, kok := ipair.Index(0).(starlark.String)
				v, vok := ipair.Index(1).(starlark.String)
				if !kok || !vok {
					return nil, fmt.Errorf("project: prefer_modules[%q] keys and values must be strings", distroName)
				}
				pins[string(k)] = string(v)
			}
			p.PreferModules[distroName] = pins
		}
	}

	e.project = p
	return starlark.None, nil
}

func (e *Engine) fnMachine(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	name := kwString(kwargs, "name")
	arch := kwString(kwargs, "arch")

	if name == "" {
		return nil, fmt.Errorf("machine() requires name")
	}
	if !validArchitectures[arch] {
		return nil, fmt.Errorf("machine %q: invalid arch %q (valid: arm64, riscv64, x86_64)", name, arch)
	}

	kernelS := kwStruct(kwargs, "kernel")

	kc := KernelConfig{
		Defconfig:  structString(kernelS, "defconfig"),
		Unit:       structString(kernelS, "unit"),
		Cmdline:    structString(kernelS, "cmdline"),
		Provides:   structString(kernelS, "provides"),
		DistroUnit: structStringMap(kernelS, "distro_unit"),
	}
	// `unit` and `distro_unit` are two spellings of "which unit provides this
	// kernel" — one flat, one per-distro. Setting both is ambiguous.
	if kc.Unit != "" && len(kc.DistroUnit) > 0 {
		return nil, fmt.Errorf("machine %q: kernel sets both unit and distro_unit (use one)", name)
	}

	m := &Machine{
		Name:        name,
		Arch:        arch,
		Description: kwString(kwargs, "description"),
		Kernel:      kc,
		Packages:    kwStringList(kwargs, "packages"),
		// distro_packages is parsed-and-stored here; the build distro is not in
		// scope at machine-parse time, so the per-distro selection happens later
		// in image(), the only place the effective distro is known.
		DistroPackages: kwStringListMap(kwargs, "distro_packages"),
	}

	// Handle bootloader, qemu, and partitions from kwargs
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		switch key {
		case "qemu":
			s, ok := kv[1].(*starlarkstruct.Struct)
			if !ok {
				continue
			}
			switch key {
			case "qemu":
				m.QEMU = &QEMUConfig{
					Machine:  structString(s, "machine"),
					CPU:      structString(s, "cpu"),
					Memory:   structString(s, "memory"),
					Firmware: structString(s, "firmware"),
					Display:  structString(s, "display"),
					Ports:    structStringList(s, "ports"),
				}
			}
		case "partitions":
			m.Partitions = append(m.Partitions, parsePartitions(kv[1])...)
		}
	}

	e.machines[name] = m

	return starlark.None, nil
}

func (e *Engine) registerUnit(class string, kwargs []starlark.Tuple) (*Unit, error) {
	name := kwString(kwargs, "name")
	if name == "" {
		return nil, fmt.Errorf("%s() requires name", class)
	}

	// Allow Starlark to override class (e.g., image() class calls unit() with unit_class="image")
	cls := kwString(kwargs, "unit_class")
	if cls == "" {
		cls = class
	}

	// Scope decides which build subtree the unit lands in and which repo
	// arch dir its package publishes to. A typo silently falls through to
	// arch scoping, which builds and publishes to the wrong place — so
	// reject unknown values the way an invalid machine arch is rejected.
	if scope := kwString(kwargs, "scope"); !validScopes[scope] {
		return nil, fmt.Errorf("unit %q: invalid scope %q (valid: arch, machine, noarch; unset means arch)", name, scope)
	}

	r := &Unit{Name: name, Class: cls}
	for _, f := range unitFields {
		f.set(r, kwargs)
	}

	// Capture unrecognized kwargs into Extra (used for template context + hash).
	for _, kv := range kwargs {
		k := string(kv[0].(starlark.String))
		if reservedUnitKwargs[k] {
			continue
		}
		v, err := starlarkToGo(kv[1])
		if err != nil {
			return nil, fmt.Errorf("%s() kwarg %q: %w", class, k, err)
		}
		if r.Extra == nil {
			r.Extra = make(map[string]any)
		}
		r.Extra[k] = v
	}

	r.Module = e.currentModule
	r.ModuleIndex = e.currentModuleIndex
	if e.currentFile != "" {
		r.DefinedIn = filepath.Dir(e.currentFile)
	}

	// prefer_modules pins are consulted at closure-walk time, not
	// here. Registration logic only needs to decide which module's
	// unit wins by module priority when two registrations collide on
	// the same name. The per-distro pins in proj.PreferModules then
	// shadow the priority choice at lookup time for the matching
	// distro only — alpine pins don't interfere with debian closures
	// and vice versa.
	// Two units of the same name tagged for different distros are not a
	// collision — each is only ever reached by its own distro's closure,
	// which is what the per-module catalog exists to allow (module-debian
	// and module-ubuntu both define dev-image). This mirrors the
	// exemption the provides-collision path already applies.
	//
	// Skipping the shadow machinery matters for more than the report: the
	// shadow-loser branch returns before storing into the per-module
	// catalog, so the lower-priority module's unit was dropped outright
	// and its distro's image became unresolvable.
	crossDistro := false
	if existing, ok := e.units[name]; ok {
		crossDistro = r.Distro != "" && existing.Distro != "" && r.Distro != existing.Distro
	}
	if existing, ok := e.units[name]; ok && !crossDistro {
		// Same priority (same module, or both project root) → hard error.
		// Cross-priority collisions are shadows: highest priority wins, with
		// a stderr notice. Project priority is set strictly above any module
		// in loader.go, so project units always win.
		if r.ModuleIndex == existing.ModuleIndex {
			return nil, fmt.Errorf("unit %q already defined (first defined in %s)",
				name, moduleSource(existing.Module))
		}
		if r.ModuleIndex < existing.ModuleIndex {
			e.shadows = append(e.shadows, ShadowEvent{
				Unit:         name,
				WinnerModule: existing.Module,
				WinnerDir:    existing.DefinedIn,
				LoserModule:  r.Module,
				LoserDir:     r.DefinedIn,
			})
			if e.showShadows {
				fmt.Fprintf(os.Stderr,
					"notice: unit %q from %s is shadowed by %s\n",
					name, moduleSource(r.Module), moduleSource(existing.Module))
			}
			return existing, nil
		}
		// New unit has higher priority — replace, log the displacement.
		e.shadows = append(e.shadows, ShadowEvent{
			Unit:         name,
			WinnerModule: r.Module,
			WinnerDir:    r.DefinedIn,
			LoserModule:  existing.Module,
			LoserDir:     existing.DefinedIn,
		})
		if e.showShadows {
			fmt.Fprintf(os.Stderr,
				"notice: unit %q from %s shadows the same name from %s\n",
				name, moduleSource(r.Module), moduleSource(existing.Module))
		}
	}
	// The flat catalog holds one unit per name. For a cross-distro pair
	// there is no right answer there, so the first registration keeps the
	// slot; anything that needs the distro-correct variant goes through
	// the per-module catalog below.
	if !crossDistro {
		e.units[name] = r
	}
	// Also store in the per-module catalog. Same-named units from
	// different modules coexist here (alpine.main's libssl3 doesn't
	// shadow debian.main's); the closure walker picks per consuming
	// distro at lookup time.
	e.storeByModule(r)

	return r, nil
}

// moduleSource formats a module name for diagnostic messages. The empty
// module string represents the project root.
func moduleSource(m string) string {
	if m == "" {
		return "project root"
	}
	return fmt.Sprintf("module %q", m)
}

func (e *Engine) fnUnit(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	_, err := e.registerUnit("unit", kwargs)
	return starlark.None, err
}

func (e *Engine) fnImage(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	_, err := e.registerUnit("image", kwargs)
	return starlark.None, err
}

// --- Task builtin ---

func fnTask(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name starlark.String
	if err := starlark.UnpackPositionalArgs("task", args, nil, 1, &name); err != nil {
		return nil, err
	}

	fields := starlark.StringDict{
		"name": name,
	}

	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		fields[key] = kv[1]
	}

	return starlarkstruct.FromStringDict(starlark.String("task"), fields), nil
}

// --- Custom commands ---

func fnArg(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("arg() requires a name")
	}
	name, ok := args[0].(starlark.String)
	if !ok {
		return nil, fmt.Errorf("arg() name must be a string")
	}
	d := starlark.StringDict{"name": name}
	for _, kv := range kwargs {
		d[string(kv[0].(starlark.String))] = kv[1]
	}
	return starlarkstruct.FromStringDict(starlark.String("arg"), d), nil
}

func (e *Engine) fnCommand(thread *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	name := kwString(kwargs, "name")
	if name == "" {
		return nil, fmt.Errorf("command() requires name")
	}

	cmd := &Command{
		Name:        name,
		Description: kwString(kwargs, "description"),
		SourceFile:  thread.Name,
	}

	// Parse args list
	for _, kv := range kwargs {
		if string(kv[0].(starlark.String)) == "args" {
			if list, ok := kv[1].(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var v starlark.Value
				for iter.Next(&v) {
					if s, ok := v.(*starlarkstruct.Struct); ok {
						a := CommandArg{
							Name:    structString(s, "name"),
							Help:    structString(s, "help"),
							Default: structString(s, "default"),
						}
						if rv, err := s.Attr("required"); err == nil {
							if b, ok := rv.(starlark.Bool); ok {
								a.Required = bool(b)
							}
						}
						if rv, err := s.Attr("type"); err == nil {
							if str, ok := rv.(starlark.String); ok && string(str) == "bool" {
								a.IsBool = true
							}
						}
						cmd.Args = append(cmd.Args, a)
					}
				}
			}
		}
	}

	e.commands[name] = cmd

	return starlark.None, nil
}
