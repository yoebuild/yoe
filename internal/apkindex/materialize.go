package apkindex

import (
	"fmt"
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// Providers resolves an APKINDEX dep token (bare name, "so:libfoo.so",
// "cmd:gpg", "pc:libfoo", or "/file/path") to the bare package name that
// satisfies it.
//
// The Resolve return is the satisfying package's Name field — the
// resolver uses bare package names internally and discards the virtual
// token. Returns ("", false) when no provider satisfies the token; the
// materializer then surfaces a clear error.
//
// Implementations live with the caller: alpine_feed (U6) wraps a merged
// view across every registered synthetic module plus the real-module
// proj.Provides. Tests pass a *ProvidesTable wrapped via TableProviders.
type Providers interface {
	Resolve(token string) (pkgName string, ok bool)
}

// TableProviders adapts a single ProvidesTable to the Providers
// interface. The wrapper is used by tests and by callers that resolve
// against a single feed; production code wraps multiple tables to
// satisfy cross-feed deps (community → main libcrypto, etc.).
type TableProviders struct{ Table *ProvidesTable }

// Resolve walks the table for token. Returns the providing entry's
// bare Name field.
func (t TableProviders) Resolve(token string) (string, bool) {
	if t.Table == nil {
		return "", false
	}
	e := t.Table.Lookup(token)
	if e == nil {
		return "", false
	}
	return e.Name, true
}

// MaterializeUnit produces a *Unit from one APKINDEX Entry, resolving
// the entry's runtime deps through the supplied Providers.
//
// The result is the package-metadata portion of a synthetic unit. The
// caller (alpine_feed's Lookup wrapper) adds feed-specific transport
// fields — Source URL, PassthroughAPK filename, the install task that
// extracts the apk — before handing the unit to the build executor.
// Keeping those out of the materializer means the same code synthesizes
// units regardless of which mirror or release a feed pins.
//
// Conflict tokens (`!something`) and explicit file paths (`/etc/foo`)
// are dropped — yoe's resolver doesn't track package conflicts, and
// file-path deps express install-time ordering that materializes from
// the runtime closure naturally. Unresolved tokens (no provider for a
// so:/cmd:/pc: virtual or for a bare package name not present in any
// feed) return an error naming the unresolved token so the failing
// dependency surfaces at first reference rather than late at build
// time.
func MaterializeUnit(entry Entry, providers Providers, moduleName string) (*yoestar.Unit, error) {
	deps, err := resolveRuntimeDeps(entry, providers)
	if err != nil {
		return nil, fmt.Errorf("apkindex: materialize %s: %w", entry.Name, err)
	}

	u := &yoestar.Unit{
		Name:          entry.Name,
		Class:         "unit",
		Description:   entry.Description,
		License:       entry.License,
		APKChecksum:   entry.ChecksumText,
		RuntimeDeps:   deps,
		Provides:      filterProvides(entry.Provides),
		Replaces:      filterProvides(entry.Replaces),
		Module:        moduleName,
		PassthroughAPK: "", // set by the alpine_feed wrapper
	}

	// Split upstream pkgver "1.2.3-r4" into yoe's separate Version +
	// Release fields. Matches alpine_pkg.star's _split_pkgver logic so
	// the published apk filename agrees with what apk-tools constructs
	// from the passthrough PKGINFO.
	u.Version, u.Release = splitPkgver(entry.Version)

	return u, nil
}

// resolveRuntimeDeps walks every dep token in entry.Deps, parses each,
// and resolves it through providers. Conflict and path tokens are
// dropped; unresolved tokens return an error.
//
// The result is de-duplicated in encounter order — `D:` lines can name
// the same package twice through different virtuals (a bare name plus
// one of its sonames) and the resolver expects each name to appear
// once.
func resolveRuntimeDeps(entry Entry, providers Providers) ([]string, error) {
	if providers == nil {
		return nil, fmt.Errorf("nil Providers")
	}
	var (
		out  []string
		seen = make(map[string]struct{}, len(entry.Deps))
	)
	for _, raw := range entry.Deps {
		d, err := ParseDep(raw)
		if err != nil {
			return nil, fmt.Errorf("dep %q: %w", raw, err)
		}
		if d.Conflict {
			continue
		}
		if d.Kind == DepKindPath {
			continue
		}
		// A package's own bare name in its dep list is a no-op (Alpine
		// sometimes lists the package's origin among deps).
		if d.Kind == DepKindName && d.Name == entry.Name {
			continue
		}
		pkg, ok := providers.Resolve(d.Name)
		if !ok {
			return nil, fmt.Errorf("unresolved dep %q (no provider for %q)",
				raw, d.Name)
		}
		if pkg == entry.Name {
			continue
		}
		if _, dup := seen[pkg]; dup {
			continue
		}
		seen[pkg] = struct{}{}
		out = append(out, pkg)
	}
	return out, nil
}

// filterProvides drops so:/cmd:/pc: virtuals from a provides list and
// strips any "=version" suffix from the survivors. yoe's resolver only
// tracks bare-name and pkg-config-like virtuals on the consumer side;
// emitting every soname as a yoe-side virtual would clutter the
// provides map and conflict with the bare-name registrations the
// resolver actually uses.
//
// Mirrors the equivalent filter in gen-unit.py so the synthetic units
// produce the same provides set as today's generated alpine_pkg files.
func filterProvides(provides []string) []string {
	if len(provides) == 0 {
		return nil
	}
	out := make([]string, 0, len(provides))
	for _, p := range provides {
		if strings.HasPrefix(p, "so:") || strings.HasPrefix(p, "cmd:") {
			continue
		}
		if i := strings.IndexByte(p, '='); i >= 0 {
			p = p[:i]
		}
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// splitPkgver mirrors alpine_pkg.star:_split_pkgver. Splits "1.2.5-r11"
// into ("1.2.5", 11); falls back to (pkgver, 0) when no "-rN" suffix is
// present.
func splitPkgver(pkgver string) (string, int) {
	i := strings.LastIndex(pkgver, "-r")
	if i < 0 {
		return pkgver, 0
	}
	tail := pkgver[i+2:]
	if tail == "" {
		return pkgver, 0
	}
	n := 0
	for _, c := range tail {
		if c < '0' || c > '9' {
			return pkgver, 0
		}
		n = n*10 + int(c-'0')
	}
	return pkgver[:i], n
}
