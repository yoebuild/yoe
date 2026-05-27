// Package debian implements the `debian_feed(...)` Starlark builtin.
//
// debian_feed is the Debian analog of alpine_feed: it turns an in-tree
// directory of decompressed Packages files into a lazily-materialized
// SyntheticModule that yoe's resolver consults alongside real modules.
// One call registers one synthetic module per (suite, component) tuple
// — typically named "debian.<suite>.<component>" such as
// "debian.bookworm.main".
//
// Wire it from cmd/yoe (or tests) via:
//
//	yoestar.WithBuiltin("debian_feed", debian.Builtin)
//
// Synthesized units carry Distro = "debian" so the closure-walk
// visibility filter (R21a) keeps them inside debian closures only.
package debian

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"go.starlark.net/starlark"

	"github.com/yoebuild/yoe/internal/dpkg"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// engineFeeds tracks the archStates registered against each engine
// so cross-feed dep resolution (a bookworm-security package depending
// on a libssl3 in bookworm-main) can walk every sibling table.
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

// archMap maps yoe canonical arches to Debian arch tokens used in URLs
// and as directory names under feed indices.
var archMap = map[string]string{
	"x86_64": "amd64",
	"arm64":  "arm64",
}

// Builtin is the BuiltinFactory passed to yoestar.WithBuiltin. The
// returned *starlark.Builtin captures the engine so each debian_feed
// call registers a SyntheticModule against it.
func Builtin(eng *yoestar.Engine) *starlark.Builtin {
	return starlark.NewBuiltin("debian_feed", makeDebianFeed(eng))
}

// makeDebianFeed produces the debian_feed function. Parameters:
//
//	debian_feed(
//	    name      = "main",                         # feed name; becomes <parent>.<suite>.<name>
//	    url       = "https://deb.debian.org/debian",
//	    suite     = "bookworm",                     # Debian release codename
//	    component = "main",                         # main / contrib / non-free
//	    arches    = ["amd64", "arm64"],             # Debian arches present in the index
//	    index     = "feeds/main",                   # in-tree dir holding <arch>/Packages
//	    keyring   = "keys/debian-archive-keyring.gpg",
//	)
//
// `index` is resolved relative to the module's MODULE.star directory.
// Inside `index`, the loader expects one subdirectory per Debian arch
// containing a decompressed `Packages` file; the active arch's index
// is parsed lazily on first Lookup.
func makeDebianFeed(eng *yoestar.Engine) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		args, err := parseKwargs(kwargs)
		if err != nil {
			return nil, fmt.Errorf("debian_feed: %w", err)
		}

		parent := eng.CurrentModule()
		if parent == "" {
			return nil, fmt.Errorf("debian_feed: must be called from a module's MODULE.star (not the project root)")
		}
		// Composed name: <parent>.<suite>.<component>, mirroring the
		// spec's "debian.<suite>.<component>" convention.
		composedName := parent + "." + args.suite + "." + args.name

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
			return nil, fmt.Errorf("debian_feed %q: %w", composedName, err)
		}
		return starlark.None, nil
	}
}

func buildSyntheticModule(eng *yoestar.Engine, composedName, parent, indexRoot string, args debianFeedArgs) *yoestar.SyntheticModule {
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

// archState holds the lazy per-arch Packages cache. Parsing 50k+
// entries from a Debian Packages file costs ~150ms; we do it once per
// arch per process.
type archState struct {
	indexRoot string
	eng       *yoestar.Engine
	byArch    map[string]*archCache
	feedArgs  debianFeedArgs
}

type archCache struct {
	entries  []dpkg.Entry
	provides *dpkg.ProvidesTable
	byName   map[string]*dpkg.Entry
}

func (s *archState) cacheFor(arch string) (*archCache, error) {
	if c, ok := s.byArch[arch]; ok {
		return c, nil
	}
	debArch, ok := archMap[arch]
	if !ok {
		return nil, fmt.Errorf("debian_feed: unsupported arch %q (supported: %s)",
			arch, strings.Join(supportedArches(), ", "))
	}
	indexPath := filepath.Join(s.indexRoot, debArch, "Packages")
	entries, err := dpkg.ParseIndexFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("debian_feed: load %s: %w", indexPath, err)
	}
	table := dpkg.BuildProvidesTable(entries)
	byName := make(map[string]*dpkg.Entry, len(entries))
	for i := range entries {
		byName[entries[i].Package] = &entries[i]
	}
	c := &archCache{entries: entries, provides: table, byName: byName}
	s.byArch[arch] = c
	return c, nil
}

func (s *archState) lookup(moduleName, name string) (*yoestar.Unit, error) {
	arch := s.eng.ActiveArch()
	if arch == "" {
		return nil, fmt.Errorf("debian_feed: no active arch (machine not loaded?)")
	}
	c, err := s.cacheFor(arch)
	if err != nil {
		return nil, err
	}
	entry, ok := c.byName[name]
	if !ok {
		return nil, nil // miss — resolver continues to the next module
	}
	providers := newMultiFeedProviders(s.eng, arch, c.provides)
	u, err := dpkg.MaterializeUnit(*entry, providers, moduleName)
	if err != nil {
		return nil, err
	}
	s.populateBuildFields(u, entry, arch)
	return u, nil
}

// populateBuildFields adds the transport metadata the build executor
// needs to fetch + republish an upstream .deb: Source URL, container,
// install task that extracts the data tar into DESTDIR.
//
// R15 mirror-time SHA256 verify rides on Unit.SHA256 — set here from
// the upstream Packages entry; internal/source/fetch.go compares the
// downloaded bytes against this hash before yoe writes anything into
// pool/, and a mismatch refuses to publish the project InRelease.
func (s *archState) populateBuildFields(u *yoestar.Unit, entry *dpkg.Entry, _ string) {
	asset := filepath.Base(entry.Filename)
	if asset == "." || asset == "" {
		// fall back to a Debian-conventional filename if the upstream
		// Packages stanza somehow omits Filename
		asset = fmt.Sprintf("%s_%s_%s.deb", entry.Package, entry.Version, entry.Architecture)
	}
	u.Source = fmt.Sprintf("%s/%s",
		strings.TrimSuffix(s.feedArgs.url, "/"),
		entry.Filename,
	)
	u.SHA256 = entry.SHA256
	u.PassthroughAPK = ""    // not an apk
	u.PassthroughDeb = asset // mirror the upstream .deb into the project pool verbatim
	u.Container = "toolchain-glibc"
	u.ContainerArch = "target"
	u.Sandbox = false
	u.Tasks = []yoestar.Task{
		{
			Name: "install",
			Steps: []yoestar.Step{
				{Command: "mkdir -p $DESTDIR"},
				// Extract the .deb's data tar into DESTDIR. dpkg-deb
				// handles the ar framing and the inner data.tar
				// compression (xz/gz/zst) transparently.
				{Command: "dpkg-deb --fsys-tarfile ./" + asset + " | tar -xpf - -C $DESTDIR"},
			},
		},
	}
}

// multiFeedProviders implements dpkg.Providers across every debian_feed
// registered against an engine. The local feed's table wins ties;
// siblings are consulted in registration order. Closes the cross-feed
// gap (a bookworm-security package depending on libssl3 in bookworm-main).
type multiFeedProviders struct {
	primary  *dpkg.ProvidesTable
	siblings []*dpkg.ProvidesTable
}

func newMultiFeedProviders(eng *yoestar.Engine, arch string, primary *dpkg.ProvidesTable) multiFeedProviders {
	out := multiFeedProviders{primary: primary}
	for _, sibling := range feedStatesFor(eng) {
		if sibling.provides(arch) == primary {
			continue
		}
		if t := sibling.provides(arch); t != nil {
			out.siblings = append(out.siblings, t)
		}
	}
	return out
}

func (m multiFeedProviders) Resolve(token string) (string, bool) {
	if e := m.primary.Lookup(token); e != nil {
		return e.Package, true
	}
	for _, t := range m.siblings {
		if e := t.Lookup(token); e != nil {
			return e.Package, true
		}
	}
	return "", false
}

// provides returns the cached provides table for arch, loading the
// Packages file lazily on first call. Returns nil when the index is
// missing or the arch isn't supported.
func (s *archState) provides(arch string) *dpkg.ProvidesTable {
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
		out = append(out, c.entries[i].Package)
	}
	return out
}

// debianFeedArgs is the parsed kwargs from a debian_feed call.
type debianFeedArgs struct {
	name      string
	url       string
	suite     string
	component string
	arches    []string
	index     string
	keyring   string
}

func parseKwargs(kwargs []starlark.Tuple) (debianFeedArgs, error) {
	var a debianFeedArgs
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
		case "suite":
			if v, ok := kv[1].(starlark.String); ok {
				a.suite = string(v)
			}
		case "component":
			if v, ok := kv[1].(starlark.String); ok {
				a.component = string(v)
			}
		case "arches":
			if list, ok := kv[1].(*starlark.List); ok {
				a.arches = stringListFrom(list)
			}
		case "index":
			if v, ok := kv[1].(starlark.String); ok {
				a.index = string(v)
			}
		case "keyring":
			if v, ok := kv[1].(starlark.String); ok {
				a.keyring = string(v)
			}
		}
	}
	if a.name == "" {
		return a, fmt.Errorf("name is required")
	}
	if a.url == "" {
		return a, fmt.Errorf("url is required")
	}
	if a.suite == "" {
		return a, fmt.Errorf("suite is required")
	}
	if a.component == "" {
		return a, fmt.Errorf("component is required")
	}
	if a.index == "" {
		return a, fmt.Errorf("index is required")
	}
	if len(a.arches) == 0 {
		return a, fmt.Errorf("arches is required")
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
