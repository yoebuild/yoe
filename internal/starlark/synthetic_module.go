package starlark

// SyntheticModule is a module-priority entry whose units are materialized
// on demand rather than enumerated up front. Used by `alpine_feed(...)`
// (U6) and `apt_feed(...)` (sibling Debian plan) to absorb upstream
// package indices into yoe's resolver without paying the cost of
// allocating a *Unit for every name in a multi-thousand-entry catalog.
//
// The loader treats a SyntheticModule like any other module entry in the
// priority list — `r.Module` attribution on materialized units points
// back to the synthetic module's Name, `prefer_modules` works, the TUI
// surfaces source-tagged entries. The difference is purely in *when* the
// *Unit pointer comes into existence:
//
//   real modules:      every .star in units/ evaluates at load time,
//                      registering its *Unit into the engine's catalog.
//   synthetic modules: Lookup(name) is called from the closure walk
//                      (U7); materialization happens on first reference.
//
// All fields are required. A nil Lookup or Names is a programmer error,
// not a runtime condition.
type SyntheticModule struct {
	// Name is the fully composed module name surfaced to the resolver,
	// prefer_modules, and TUI display. Convention is `<parent>.<feed>`
	// (e.g., `alpine.main`, `debian.main`) so consumers can tell at a
	// glance which physical module declared the feed.
	Name string

	// Parent is the canonical name of the module whose MODULE.star
	// declared this feed (e.g., `alpine`). Used by the TUI module-list
	// view (R17) to group feeds under their parent.
	Parent string

	// Suite is the release codename this feed declares (apt_feed's
	// `suite` kwarg, e.g. "bookworm", "resolute"). Empty for non-apt
	// feeds — alpine_feed leaves it unset. Project.SuiteForDistro reads
	// it as the source of the codename the repo emitter, image assembly,
	// and the on-device apt sources.list all stamp, matched to the
	// feed's Distro so a project with both a Debian and an Ubuntu feed
	// resolves the right suite per distro.
	Suite string

	// Distro is the apt-family distro this feed targets (apt_feed's
	// `distro` kwarg, e.g. "debian", "ubuntu"). Empty for non-apt feeds.
	// Matches the Distro tag stamped on the feed's materialized units;
	// SuiteForDistro uses it to pick this feed's suite for a given
	// distro's build.
	Distro string

	// Priority is the resolver-priority index of this synthetic module.
	// Synthetic modules rank below every non-feed module per R5; the
	// loader assigns each registered synthetic an index that is
	// guaranteed lower than any real module's index under the current
	// "higher index wins" convention.
	Priority int

	// Lookup materializes a *Unit for name when the resolver references
	// it. Returns (nil, nil) for a miss — the resolver continues to the
	// next module in priority order. Returns (nil, err) only for parse
	// or I/O failures the caller should surface to the user.
	//
	// Implementations are free to return a fresh *Unit on every call;
	// pointer identity across repeated Lookups is NOT required. The
	// closure walk (U7) caches materialized units in the Engine's
	// proj.Units catalog after the first call, so subsequent references
	// to the same name never re-enter Lookup.
	Lookup func(name string) (*Unit, error)

	// Names enumerates every name this synthetic module can materialize.
	// Used by the TUI search surface (U8) for "I want to find package
	// X" workflows. Must NOT trigger Lookup or any *Unit allocation —
	// the whole point of synthetic modules is that catalog size is
	// decoupled from working-set size.
	Names func() []string
}

// RegisterSyntheticModule records sm for the loader to attach to the
// project's module list. Safe for concurrent use — alpine_feed and
// apt_feed both call this from inside Starlark evaluation, which
// runs single-threaded per module, but engines may serve multiple
// projects sequentially in tests.
//
// A duplicate Name is a programmer error and surfaces at registration:
// the user wouldn't have written two alpine_feed("main", ...) calls in
// the same module, but a malformed test fixture might. Erroring early
// beats a silent overwrite that hides the second call's intent.
func (e *Engine) RegisterSyntheticModule(sm *SyntheticModule) error {
	if sm == nil || sm.Name == "" {
		return errSyntheticModuleMissingName
	}
	if sm.Lookup == nil || sm.Names == nil {
		return errSyntheticModuleMissingCallbacks
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, existing := range e.syntheticModules {
		if existing.Name == sm.Name {
			return &duplicateSyntheticModuleError{Name: sm.Name}
		}
	}
	e.syntheticModules = append(e.syntheticModules, sm)
	return nil
}

// SyntheticModules returns the registered synthetic modules in
// registration order. The loader uses this after evaluating MODULE.star
// files to assign Priority values and attach the list to the project.
func (e *Engine) SyntheticModules() []*SyntheticModule {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Return a copy so callers iterating during further evaluation
	// don't race with concurrent registrations.
	out := make([]*SyntheticModule, len(e.syntheticModules))
	copy(out, e.syntheticModules)
	return out
}

// LookupInSynthetics walks the project's synthetic modules in priority
// order (highest Priority first) and returns the first *Unit whose name
// matches. Used by the closure walk (U7) when a referenced name isn't
// in proj.Units.
//
// The loader assigns Priority = -registration_index, so the slice is
// already in high-to-low priority order and a forward walk gives the
// correct precedence.
//
// Returns (nil, nil) when no synthetic module provides the name —
// distinguished from (nil, err) on parse/cache failure.
func LookupInSynthetics(synths []*SyntheticModule, name string) (*Unit, error) {
	for _, sm := range synths {
		u, err := sm.Lookup(name)
		if err != nil {
			return nil, err
		}
		if u != nil {
			return u, nil
		}
	}
	return nil, nil
}

// Error sentinels — exported via the dedicated types below so callers can
// distinguish them with errors.As/errors.Is when needed.
type syntheticModuleError string

func (e syntheticModuleError) Error() string { return string(e) }

const (
	errSyntheticModuleMissingName      = syntheticModuleError("synthetic module: Name is required")
	errSyntheticModuleMissingCallbacks = syntheticModuleError("synthetic module: Lookup and Names callbacks are required")
)

// duplicateSyntheticModuleError is returned when two alpine_feed (or
// equivalent) calls register the same Name in one project. Keeping the
// Name on the error type means downstream tests / TUI surfaces can
// format the message however they like instead of parsing strings.
type duplicateSyntheticModuleError struct{ Name string }

func (d *duplicateSyntheticModuleError) Error() string {
	return "synthetic module already registered: " + d.Name
}
