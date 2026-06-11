// Package apt implements the `apt_feed(...)` Starlark builtin.
//
// apt_feed is the dpkg/apt-family analog of alpine_feed: it turns an
// in-tree directory of decompressed Packages files into a
// lazily-materialized SyntheticModule that yoe's resolver consults
// alongside real modules. One call registers one synthetic module per
// component, named "<parent>.<component>" — e.g. "debian.main",
// "ubuntu.main". The suite kwarg picks which on-disk Packages file is
// parsed but does not appear in the module's identity (one suite per
// distro per project, enforced at evaluation).
//
// The same builtin serves every apt-based distro; the required `distro`
// kwarg ("debian", "ubuntu", …) is stamped onto each materialized
// unit's Distro tag. The closure-walk visibility filter then keeps a
// feed's units inside their own distro's closures only — that is what
// lets a project declare both a Debian and an Ubuntu feed without the
// two colliding, and lets an image select among distros.
//
// Wire it from cmd/yoe (or tests) via:
//
//	yoestar.WithBuiltin("apt_feed", apt.Builtin)
package apt

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
// returned *starlark.Builtin captures the engine so each apt_feed
// call registers a SyntheticModule against it.
func Builtin(eng *yoestar.Engine) *starlark.Builtin {
	return starlark.NewBuiltin("apt_feed", makeAptFeed(eng))
}

// makeAptFeed produces the apt_feed function. Parameters:
//
//	apt_feed(
//	    name      = "main",                         # feed name; becomes <parent>.<name>
//	    distro    = "debian",                       # apt-family distro tag stamped on units
//	    url       = "https://deb.debian.org/debian",
//	    arch_urls = {"arm64": "http://ports..."},   # optional per-arch mirror override
//	    suite     = "bookworm",                     # release codename
//	    component = "main",                         # main / contrib / non-free / universe
//	    arches    = ["amd64", "arm64"],             # arches present in the index
//	    index     = "feeds/main",                   # in-tree dir holding <arch>/Packages
//	    keyring   = "keys/debian-archive-keyring.gpg",
//	)
//
// `index` is resolved relative to the module's MODULE.star directory.
// Inside `index`, the loader expects one subdirectory per Debian arch
// containing a decompressed `Packages` file; the active arch's index
// is parsed lazily on first Lookup.
func makeAptFeed(eng *yoestar.Engine) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		args, err := parseKwargs(kwargs)
		if err != nil {
			return nil, fmt.Errorf("apt_feed: %w", err)
		}

		parent := eng.CurrentModule()
		if parent == "" {
			return nil, fmt.Errorf("apt_feed: must be called from a module's MODULE.star (not the project root)")
		}
		// Composed name: <parent>.<component>, matching alpine_feed's
		// one-segment shape. The suite is feed configuration, not
		// module identity (one suite per distro per project).
		composedName := parent + "." + args.name

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
			return nil, fmt.Errorf("apt_feed %q: %w", composedName, err)
		}
		return starlark.None, nil
	}
}

func buildSyntheticModule(eng *yoestar.Engine, composedName, parent, indexRoot string, args aptFeedArgs) *yoestar.SyntheticModule {
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
		Suite:  args.suite,
		Distro: args.distro,
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
	feedArgs  aptFeedArgs
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
		return nil, fmt.Errorf("apt_feed: unsupported arch %q (supported: %s)",
			arch, strings.Join(supportedArches(), ", "))
	}
	indexPath := filepath.Join(s.indexRoot, debArch, "Packages")
	entries, err := dpkg.ParseIndexFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("apt_feed: load %s: %w", indexPath, err)
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
		return nil, fmt.Errorf("apt_feed: no active arch (machine not loaded?)")
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
	u, err := dpkg.MaterializeUnit(*entry, providers, moduleName, s.feedArgs.distro)
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
func (s *archState) populateBuildFields(u *yoestar.Unit, entry *dpkg.Entry, arch string) {
	asset := filepath.Base(entry.Filename)
	if asset == "." || asset == "" {
		// fall back to a Debian-conventional filename if the upstream
		// Packages stanza somehow omits Filename
		asset = fmt.Sprintf("%s_%s_%s.deb", entry.Package, entry.Version, entry.Architecture)
	}
	u.Source = fmt.Sprintf("%s/%s",
		strings.TrimSuffix(s.feedArgs.baseURLFor(arch), "/"),
		entry.Filename,
	)
	u.SHA256 = entry.SHA256
	u.PassthroughAPK = ""    // not an apk
	u.PassthroughDeb = asset // mirror the upstream .deb into the project pool verbatim
	// Use the virtual "toolchain" name, not a concrete one: it resolves
	// per-distro through the provides table to the consuming distro's glibc
	// toolchain (toolchain-debian-13, toolchain-ubuntu-26.04, …). Each
	// apt-family toolchain carries its distro+release in its unit name so
	// their container image tags don't collide, so a literal name here would
	// only resolve for one distro.
	u.Container = "toolchain"
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

// multiFeedProviders implements dpkg.Providers across every apt_feed
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

// aptFeedArgs is the parsed kwargs from an apt_feed call.
type aptFeedArgs struct {
	name      string
	distro    string
	url       string
	archURLs  map[string]string
	suite     string
	component string
	arches    []string
	index     string
	keyring   string
}

// baseURLFor returns the mirror base URL serving deb downloads for a
// given yoe-canonical arch. A per-arch override in archURLs wins;
// otherwise the feed's default url is used. This is what lets one feed
// span Ubuntu's split archive — amd64/i386 on archive.ubuntu.com,
// arm64 and the other ports arches on ports.ubuntu.com — while Debian,
// whose single mirror serves every arch, sets no override and stays
// cache-identical.
func (a aptFeedArgs) baseURLFor(arch string) string {
	if u, ok := a.archURLs[arch]; ok && u != "" {
		return u
	}
	return a.url
}

func parseKwargs(kwargs []starlark.Tuple) (aptFeedArgs, error) {
	var a aptFeedArgs
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
		case "distro":
			if v, ok := kv[1].(starlark.String); ok {
				a.distro = string(v)
			}
		case "url":
			if v, ok := kv[1].(starlark.String); ok {
				a.url = string(v)
			}
		case "arch_urls":
			if d, ok := kv[1].(*starlark.Dict); ok {
				a.archURLs = stringDictFrom(d)
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
	if a.distro == "" {
		return a, fmt.Errorf("distro is required (e.g. \"debian\" or \"ubuntu\")")
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

// stringDictFrom converts a Starlark dict of {arch: url} into a Go map,
// keeping only string→string entries. Used for the optional arch_urls
// kwarg.
func stringDictFrom(d *starlark.Dict) map[string]string {
	out := make(map[string]string, d.Len())
	for _, item := range d.Items() {
		k, kok := item[0].(starlark.String)
		v, vok := item[1].(starlark.String)
		if kok && vok {
			out[string(k)] = string(v)
		}
	}
	return out
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
