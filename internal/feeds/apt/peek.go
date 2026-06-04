package apt

import (
	"fmt"
	"path/filepath"
	"sort"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// FeedDecl is one apt_feed(...) call recorded by PeekFeedDecls.
// Carries the kwargs the live builtin parses plus the absolute paths
// the maintainer playbook needs to fetch and write feed contents.
type FeedDecl struct {
	Name      string            // feed name (becomes <parent>.<name>)
	Distro    string            // apt-family distro tag, e.g. debian / ubuntu
	URL       string            // mirror root URL, e.g. https://deb.debian.org/debian
	ArchURLs  map[string]string // optional per-arch mirror overrides (yoe arch → base URL); for Ubuntu's split archive/ports mirrors
	Suite     string            // release codename, e.g. bookworm / resolute
	Component string            // archive component, e.g. main / contrib / universe
	Arches    []string          // arch tokens present in the index
	Index     string            // in-module directory holding <arch>/Packages
	Keyring   string            // GPG keyring file for signature verification (relative to MODULE.star)
}

// baseURLFor returns the mirror base URL for a yoe-canonical arch: a
// per-arch override from ArchURLs when present, else the default URL.
func (d FeedDecl) baseURLFor(yoeArch string) string {
	if u, ok := d.ArchURLs[yoeArch]; ok && u != "" {
		return u
	}
	return d.URL
}

// PeekFeedDecls evaluates the MODULE.star at modulePath in an
// isolated thread with stub module_info / module builtins and a
// recording apt_feed. Returns every apt_feed call in declaration
// order.
//
// Used by `yoe update-feeds` so the command can run inside a module
// repo without spinning up a full project. Side-effects-free — nothing
// is loaded, fetched, or registered with any engine.
func PeekFeedDecls(modulePath string) ([]FeedDecl, error) {
	file := filepath.Join(modulePath, "MODULE.star")
	var (
		decls    []FeedDecl
		seenName = map[string]bool{}
	)

	noop := starlark.NewBuiltin("noop",
		func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			return starlark.None, nil
		})

	feed := starlark.NewBuiltin("apt_feed",
		func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			d := FeedDecl{}
			for _, kv := range kwargs {
				k, ok := kv[0].(starlark.String)
				if !ok {
					continue
				}
				switch string(k) {
				case "name":
					if v, ok := kv[1].(starlark.String); ok {
						d.Name = string(v)
					}
				case "distro":
					if v, ok := kv[1].(starlark.String); ok {
						d.Distro = string(v)
					}
				case "url":
					if v, ok := kv[1].(starlark.String); ok {
						d.URL = string(v)
					}
				case "arch_urls":
					if dict, ok := kv[1].(*starlark.Dict); ok {
						d.ArchURLs = stringDictFrom(dict)
					}
				case "suite":
					if v, ok := kv[1].(starlark.String); ok {
						d.Suite = string(v)
					}
				case "component":
					if v, ok := kv[1].(starlark.String); ok {
						d.Component = string(v)
					}
				case "arches":
					if list, ok := kv[1].(*starlark.List); ok {
						d.Arches = stringListFrom(list)
					}
				case "index":
					if v, ok := kv[1].(starlark.String); ok {
						d.Index = string(v)
					}
				case "keyring":
					if v, ok := kv[1].(starlark.String); ok {
						d.Keyring = string(v)
					}
				}
			}
			if d.Name == "" {
				return nil, fmt.Errorf("apt_feed: name is required")
			}
			if seenName[d.Name] {
				return nil, fmt.Errorf("apt_feed: duplicate feed name %q in this module", d.Name)
			}
			seenName[d.Name] = true
			decls = append(decls, d)
			return starlark.None, nil
		})

	thread := &starlark.Thread{Name: file}
	predeclared := starlark.StringDict{
		"module_info": noop,
		"module":      noop,
		"apt_feed":    feed,
		// Tolerate alpine_feed calls in the same MODULE.star so a
		// module that ships both types can be peeked without errors.
		"alpine_feed": noop,
	}
	if _, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, file, nil, predeclared); err != nil {
		return nil, fmt.Errorf("apt: peek %s: %w", file, err)
	}
	sort.SliceStable(decls, func(i, j int) bool { return decls[i].Name < decls[j].Name })
	return decls, nil
}
