// Package alpine implements the `alpine_feed(...)` Starlark builtin.
//
// alpine_feed turns an in-tree directory of APKINDEX files into a
// lazily-materialized SyntheticModule that yoe's resolver consults
// alongside real modules. The builtin lives in its own package — not
// in internal/starlark — to keep internal/starlark from importing the
// APKINDEX parser (which itself imports starlark for *Unit), avoiding
// an import cycle.
//
// Wire it from cmd/yoe (or tests) via:
//
//	yoestar.WithBuiltin("alpine_feed", alpine.Builtin)
//
// The factory closure runs against the loading Engine; alpine_feed
// invocations during MODULE.star evaluation hand the engine a
// SyntheticModule whose Lookup callback fronts the cached APKINDEX
// data.
package alpine

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"go.starlark.net/starlark"

	"github.com/yoebuild/yoe/internal/apkindex"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// engineFeeds maps each Engine to the archStates registered against
// it. Cross-feed dep resolution walks every state in this list so a
// community package's so:libcrypto.so.3 finds main's openssl-libs
// (the canonical R7/AE4 case). The map is per-process and keyed by
// pointer so independent engines (e.g. multiple test fixtures in one
// run) don't interfere.
var (
	engineFeedsMu sync.Mutex
	engineFeeds   = map[*yoestar.Engine][]*archState{}
)

func registerFeedState(eng *yoestar.Engine, s *archState) {
	engineFeedsMu.Lock()
	defer engineFeedsMu.Unlock()
	engineFeeds[eng] = append(engineFeeds[eng], s)
}

func feedStatesFor(eng *yoestar.Engine) []*archState {
	engineFeedsMu.Lock()
	defer engineFeedsMu.Unlock()
	src := engineFeeds[eng]
	out := make([]*archState, len(src))
	copy(out, src)
	return out
}

// archMap mirrors module-alpine/classes/alpine_pkg.star's _ARCH_MAP:
// yoe canonical arches → Alpine arch tokens used in repo URLs and as
// directory names under feed indices.
var archMap = map[string]string{
	"x86_64":  "x86_64",
	"arm64":   "aarch64",
	"riscv64": "riscv64",
}

// Builtin is the BuiltinFactory passed to yoestar.WithBuiltin. The
// returned *starlark.Builtin captures the engine so each alpine_feed
// call can register a SyntheticModule against it.
func Builtin(eng *yoestar.Engine) *starlark.Builtin {
	return starlark.NewBuiltin("alpine_feed", makeAlpineFeed(eng))
}

// makeAlpineFeed produces the alpine_feed function. Parameters mirror
// the spec's alpine_feed signature:
//
//	alpine_feed(
//	    name    = "main",                          # feed name; becomes <parent>.<name>
//	    url     = "https://dl-cdn.alpinelinux.org/alpine",
//	    branch  = "v3.21",                         # Alpine release tag
//	    section = "main",                          # main / community / testing
//	    index   = "feeds/main",                    # in-tree dir holding <arch>/APKINDEX
//	    keys    = ["keys/alpine-devel@lists.alpinelinux.org-*.rsa.pub"],
//	)
//
// `index` is resolved relative to the module's MODULE.star directory.
// Inside `index`, the loader expects one subdirectory per arch (alpine
// arch token, not yoe arch); the active arch's APKINDEX is parsed
// lazily on first Lookup.
func makeAlpineFeed(eng *yoestar.Engine) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		args, err := parseKwargs(kwargs)
		if err != nil {
			return nil, fmt.Errorf("alpine_feed: %w", err)
		}

		// Compose the synthetic module name: <parent>.<feed-name>.
		// The parent is the module whose MODULE.star is currently
		// being evaluated (set by the loader via SetCurrentModule).
		parent := eng.CurrentModule()
		if parent == "" {
			return nil, fmt.Errorf("alpine_feed: must be called from a module's MODULE.star (not the project root)")
		}
		composedName := parent + "." + args.name

		// Resolve the index directory against the caller's .star file
		// directory (the module's MODULE.star). CallFrame(0) is the
		// builtin itself; CallFrame(1) is the caller — same pattern
		// install_file uses.
		var moduleDir string
		if thread.CallStackDepth() >= 2 {
			if caller := thread.CallFrame(1).Pos.Filename(); caller != "" && caller != "<builtin>" {
				moduleDir = filepath.Dir(caller)
			}
		}
		indexRoot := args.index
		if !filepath.IsAbs(indexRoot) {
			indexRoot = filepath.Join(moduleDir, indexRoot)
		}

		sm := buildSyntheticModule(eng, composedName, parent, indexRoot, args)
		if err := eng.RegisterSyntheticModule(sm); err != nil {
			return nil, fmt.Errorf("alpine_feed %q: %w", composedName, err)
		}
		return starlark.None, nil
	}
}

// buildSyntheticModule assembles a SyntheticModule whose Lookup
// resolves package names against the lazily-loaded APKINDEX for the
// engine's active arch. Names enumerates the entire catalog for the
// TUI search surface (U8) without materializing units.
//
// Both callbacks share a state struct that holds the loaded entries
// and provides table, cached in-memory across calls. The first Lookup
// or Names call for a given arch parses the on-disk APKINDEX text;
// subsequent calls hit the in-memory state directly.
func buildSyntheticModule(eng *yoestar.Engine, composedName, parent, indexRoot string, args alpineFeedArgs) *yoestar.SyntheticModule {
	s := &archState{
		indexRoot: indexRoot,
		eng:       eng,
		byArch:    make(map[string]*archCache),
		feedArgs:  args,
	}
	registerFeedState(eng, s)

	return &yoestar.SyntheticModule{
		Name:   composedName,
		Parent: parent,
		Lookup: func(name string) (*yoestar.Unit, error) {
			return s.lookup(composedName, name)
		},
		Names: func() []string {
			return s.names()
		},
	}
}

// archState holds the lazy per-arch APKINDEX cache. The state survives
// across resolver Lookup calls, so an image referencing 300 packages
// triggers one parse-and-cache load (per arch) and 300 cheap map
// lookups.
type archState struct {
	indexRoot string
	eng       *yoestar.Engine
	byArch    map[string]*archCache
	feedArgs  alpineFeedArgs // mirror url/branch/section needed to build per-unit apk Source URLs
}

type archCache struct {
	entries  []apkindex.Entry
	provides *apkindex.ProvidesTable
	byName   map[string]*apkindex.Entry
}

func (s *archState) cacheFor(arch string) (*archCache, error) {
	if c, ok := s.byArch[arch]; ok {
		return c, nil
	}
	alpineArch, ok := archMap[arch]
	if !ok {
		return nil, fmt.Errorf("alpine_feed: unsupported arch %q (supported: %s)",
			arch, strings.Join(supportedArches(), ", "))
	}
	indexPath := filepath.Join(s.indexRoot, alpineArch, "APKINDEX")
	entries, err := apkindex.ParseIndexFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("alpine_feed: load %s: %w", indexPath, err)
	}
	table := apkindex.BuildProvidesTable(entries)
	byName := make(map[string]*apkindex.Entry, len(entries))
	for i := range entries {
		byName[entries[i].Name] = &entries[i]
	}
	c := &archCache{entries: entries, provides: table, byName: byName}
	s.byArch[arch] = c
	return c, nil
}

func (s *archState) lookup(moduleName, name string) (*yoestar.Unit, error) {
	arch := s.eng.ActiveArch()
	if arch == "" {
		return nil, fmt.Errorf("alpine_feed: no active arch (machine not loaded?)")
	}
	c, err := s.cacheFor(arch)
	if err != nil {
		return nil, err
	}
	entry, ok := c.byName[name]
	if !ok {
		return nil, nil // miss — resolver continues to the next module
	}
	// Build a project-wide providers view: this feed's table first,
	// then every sibling feed registered against the same engine.
	// Closes the cross-feed gap (community openssh-server depends on
	// so:libcrypto.so.3 which lives in main's openssl-libs).
	providers := newMultiFeedProviders(s.eng, arch, c.provides)
	u, err := apkindex.MaterializeUnit(*entry, providers, moduleName)
	if err != nil {
		return nil, err
	}
	s.populateBuildFields(u, entry, arch)
	return u, nil
}

// populateBuildFields adds the transport metadata the build executor
// needs to fetch + repack an upstream apk: Source URL, PassthroughAPK
// filename, container + install task. Mirrors what
// classes/alpine_pkg.star sets in the per-package wrapper — keeping
// the same shape means the executor's existing apk-passthrough path
// (internal/build/executor.go:709) handles synthetic units without
// special-case branching.
func (s *archState) populateBuildFields(u *yoestar.Unit, entry *apkindex.Entry, arch string) {
	alpineArch := archMap[arch]
	// Asset filename uses upstream's combined pkgver (including -rN)
	// so the URL matches what Alpine's mirror serves.
	asset := fmt.Sprintf("%s-%s.apk", entry.Name, entry.Version)
	u.Source = fmt.Sprintf("%s/%s/%s/%s/%s",
		strings.TrimSuffix(s.feedArgs.url, "/"),
		s.feedArgs.branch,
		s.feedArgs.section,
		alpineArch,
		asset)
	u.PassthroughAPK = asset
	u.Container = "toolchain-musl"
	u.ContainerArch = "target"
	u.Sandbox = false
	u.Tasks = []yoestar.Task{
		{
			Name: "install",
			Steps: []yoestar.Step{
				{Command: "mkdir -p $DESTDIR"},
				// Extract the apk's data segment into DESTDIR while
				// excluding apk control files (.PKGINFO, install
				// scripts, .SIGN.*) — they ride through to on-target
				// install via RepackAPK and shouldn't pollute the
				// downstream per-unit sysroot.
				{Command: "tar -xzpf ./" + asset + " -C $DESTDIR " +
					"--exclude=.PKGINFO " +
					"--exclude=.pre-install --exclude=.post-install " +
					"--exclude=.pre-upgrade --exclude=.post-upgrade " +
					"--exclude=.pre-deinstall --exclude=.post-deinstall " +
					"--exclude=.trigger " +
					"--exclude=.SIGN.*"},
			},
		},
	}
}

// multiFeedProviders implements apkindex.Providers across every
// alpine_feed registered against an Engine. The local feed's table
// wins ties; siblings are consulted in registration order. This is
// the practical realization of the plan's "project-wide provides
// table merged from every registered synthetic module's per-feed
// table in resolver priority order" rule.
type multiFeedProviders struct {
	primary  *apkindex.ProvidesTable
	siblings []*apkindex.ProvidesTable
}

func newMultiFeedProviders(eng *yoestar.Engine, arch string, primary *apkindex.ProvidesTable) multiFeedProviders {
	out := multiFeedProviders{primary: primary}
	for _, sibling := range feedStatesFor(eng) {
		if sibling.provides(arch) == primary {
			continue // skip self
		}
		if t := sibling.provides(arch); t != nil {
			out.siblings = append(out.siblings, t)
		}
	}
	return out
}

// Resolve consults primary first, then siblings. Returns the bare
// package name of whichever entry first provides the token.
func (m multiFeedProviders) Resolve(token string) (string, bool) {
	if e := m.primary.Lookup(token); e != nil {
		return e.Name, true
	}
	for _, t := range m.siblings {
		if e := t.Lookup(token); e != nil {
			return e.Name, true
		}
	}
	return "", false
}

// provides returns the cached provides table for the given arch,
// loading the APKINDEX lazily on first call. Returns nil when the
// feed has no entries for the arch (or the index is missing) —
// caller treats that as "no sibling contribution" rather than an
// error.
func (s *archState) provides(arch string) *apkindex.ProvidesTable {
	c, err := s.cacheFor(arch)
	if err != nil {
		return nil
	}
	return c.provides
}

func (s *archState) names() []string {
	arch := s.eng.ActiveArch()
	if arch == "" {
		return nil
	}
	c, err := s.cacheFor(arch)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(c.entries))
	for i := range c.entries {
		out = append(out, c.entries[i].Name)
	}
	return out
}

// alpineFeedArgs is the parsed kwargs from an alpine_feed call.
type alpineFeedArgs struct {
	name    string
	url     string
	branch  string
	section string
	index   string
	keys    []string
}

// parseKwargs unpacks the alpine_feed kwargs into a typed struct.
// Required fields (name, url, branch, section, index) error when
// missing — explicit is better than implicit for feed declarations per
// CLAUDE.md's "Explicit over implicit" rule. `keys` is optional today
// (no signature verification at resolver time) but recorded so U10's
// `yoe update-feeds` can read it.
func parseKwargs(kwargs []starlark.Tuple) (alpineFeedArgs, error) {
	var a alpineFeedArgs
	for _, kv := range kwargs {
		k, ok := kv[0].(starlark.String)
		if !ok {
			continue
		}
		switch string(k) {
		case "name":
			if v, ok := kv[1].(starlark.String); ok {
				a.name = string(v)
			}
		case "url":
			if v, ok := kv[1].(starlark.String); ok {
				a.url = string(v)
			}
		case "branch":
			if v, ok := kv[1].(starlark.String); ok {
				a.branch = string(v)
			}
		case "section":
			if v, ok := kv[1].(starlark.String); ok {
				a.section = string(v)
			}
		case "index":
			if v, ok := kv[1].(starlark.String); ok {
				a.index = string(v)
			}
		case "keys":
			if list, ok := kv[1].(*starlark.List); ok {
				a.keys = stringListFrom(list)
			}
		}
	}
	if a.name == "" {
		return a, fmt.Errorf("name is required")
	}
	if a.url == "" {
		return a, fmt.Errorf("url is required")
	}
	if a.branch == "" {
		return a, fmt.Errorf("branch is required")
	}
	if a.section == "" {
		return a, fmt.Errorf("section is required")
	}
	if a.index == "" {
		return a, fmt.Errorf("index is required")
	}
	return a, nil
}

func stringListFrom(list *starlark.List) []string {
	out := make([]string, 0, list.Len())
	iter := list.Iterate()
	defer iter.Done()
	var v starlark.Value
	for iter.Next(&v) {
		if s, ok := v.(starlark.String); ok {
			out = append(out, string(s))
		}
	}
	return out
}

func supportedArches() []string {
	out := make([]string, 0, len(archMap))
	for a := range archMap {
		out = append(out, a)
	}
	return out
}
