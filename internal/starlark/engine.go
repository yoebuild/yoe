package starlark

import (
	"fmt"
	"path/filepath"
	"sync"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

var fileOpts = &syntax.FileOptions{}

// Engine evaluates .star files and collects results.
type Engine struct {
	mu        sync.Mutex
	project   *Project
	machines  map[string]*Machine
	units     map[string]*Unit
	commands  map[string]*Command
	moduleInfo *ModuleInfo

	// Current module context — set by the loader before evaluating each
	// module's directories so registerUnit can tag units.
	currentModule      string
	currentModuleIndex int

	// globals stores the top-level bindings from the last ExecFile/ExecString,
	// used to retrieve the run() function for custom commands.
	globals starlark.StringDict

	// Predeclared variables set after engine creation (e.g., ARCH).
	vars map[string]starlark.Value

	// load() support
	projectRoot string
	moduleRoots map[string]string
	loadCache   *loadCache

	// Tracks the directory of the currently executing .star file
	currentFile string

	// When true, emits stderr notices for cross-module unit shadowing and
	// intra-module `provides` overrides. Default false: notices are off.
	// The shadowing/override behavior itself is independent of this flag.
	showShadows bool

	// When true, multiple units within the same module may declare the same
	// `provides` virtual without erroring. The first provider seen wins for
	// the PROVIDES lookup; subsequent providers are silently accepted (apk
	// "any of these satisfies" semantics).
	allowDuplicateProvides bool

	// shadows accumulates cross-module unit name collisions in registration
	// order. Surfaced via Project.Diagnostics for the TUI's Diagnostics tab.
	shadows []ShadowEvent
}

func NewEngine() *Engine {
	return &Engine{
		machines: make(map[string]*Machine),
		units:    make(map[string]*Unit),
		commands: make(map[string]*Command),
		vars:     make(map[string]starlark.Value),
	}
}

// SetVar sets a predeclared variable available in all subsequently evaluated
// .star files. Used to inject ARCH after machines are loaded.
func (e *Engine) SetVar(name string, value starlark.Value) {
	e.vars[name] = value
}

func (e *Engine) Project() *Project              { return e.project }
func (e *Engine) Machines() map[string]*Machine   { return e.machines }
func (e *Engine) Units() map[string]*Unit     { return e.units }
func (e *Engine) Commands() map[string]*Command   { return e.commands }
func (e *Engine) ModuleInfo() *ModuleInfo         { return e.moduleInfo }
func (e *Engine) Globals() starlark.StringDict    { return e.globals }

// SetCurrentModule sets the module context for subsequent unit registrations.
func (e *Engine) SetCurrentModule(name string, index int) {
	e.currentModule = name
	e.currentModuleIndex = index
}

// SetShowShadows toggles emission of stderr notices about cross-module unit
// shadowing and intra-module `provides` overrides. Default is off.
func (e *Engine) SetShowShadows(v bool) { e.showShadows = v }

// SetAllowDuplicateProvides toggles intra-module `provides` collision
// handling. When true, multiple units in the same module may declare the
// same virtual without erroring (first-wins for PROVIDES lookup).
func (e *Engine) SetAllowDuplicateProvides(v bool) { e.allowDuplicateProvides = v }

// Shadows returns the cross-module unit name collisions observed during
// evaluation, in registration order.
func (e *Engine) Shadows() []ShadowEvent { return e.shadows }

// ExecString evaluates Starlark source code with built-in functions available.
func (e *Engine) ExecString(filename, src string) error {
	thread := &starlark.Thread{Name: filename}
	thread.Load = e.makeLoadFunc(filename)
	predeclared := e.builtins()

	globals, err := starlark.ExecFileOptions(fileOpts, thread, filename, src, predeclared)
	if err != nil {
		return fmt.Errorf("evaluating %s: %w", filename, err)
	}
	e.globals = globals
	return nil
}

// ExecFile evaluates a .star file from disk.
// Results are added to the load cache so that a subsequent load() of the
// same file returns the cached globals instead of re-executing (which would
// cause duplicate unit registrations).
func (e *Engine) ExecFile(path string) error {
	prev := e.currentFile
	e.currentFile = path
	defer func() { e.currentFile = prev }()
	thread := &starlark.Thread{Name: path}
	thread.Load = e.makeLoadFunc(path)
	predeclared := e.builtins()

	globals, err := starlark.ExecFileOptions(fileOpts, thread, path, nil, predeclared)
	if err != nil {
		return fmt.Errorf("evaluating %s: %w", path, err)
	}
	e.globals = globals

	// Populate load cache so load() of this file won't re-execute it.
	absPath, _ := filepath.Abs(path)
	if absPath != "" {
		if e.loadCache == nil {
			e.loadCache = newLoadCache()
		}
		e.loadCache.mu.Lock()
		e.loadCache.entries[absPath] = &loadResult{globals: globals}
		e.loadCache.mu.Unlock()
	}

	return nil
}
