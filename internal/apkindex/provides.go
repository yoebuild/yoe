package apkindex

import (
	"strings"
	"unicode"
)

// ProvidesTable maps a virtual name (the dep Name field — bare package
// name, "so:libfoo.so.3", "cmd:gpg", "pc:libfoo", or "/file/path") to
// the package Entry that provides it.
//
// Multiple entries can declare the same virtual (e.g., several mailer
// daemons provide "smtp-daemon"); the tiebreaker picks the entry with
// the newest version per R7.
type ProvidesTable struct {
	// byName is the resolved provider for each virtual lookup token.
	// The Entry pointer is stable for the lifetime of the table.
	byName map[string]*Entry
}

// Lookup returns the provider Entry for name, or nil if no entry in the
// indexed set provides it.
func (p *ProvidesTable) Lookup(name string) *Entry {
	if p == nil {
		return nil
	}
	return p.byName[name]
}

// Names returns every virtual lookup token in the table. Used by the
// TUI search surface (R17) — does not materialize any units.
func (p *ProvidesTable) Names() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.byName))
	for n := range p.byName {
		out = append(out, n)
	}
	return out
}

// BuildProvidesTable walks every entry, registers each as a provider of
// its own bare name, then registers every `p:` provides token. Multiple
// providers of the same virtual resolve to the newest-version entry
// (R7).
//
// The entries slice is borrowed — pointers into it are stored in the
// table, so callers must not mutate or reuse the underlying array after
// building.
func BuildProvidesTable(entries []Entry) *ProvidesTable {
	t := &ProvidesTable{byName: make(map[string]*Entry, len(entries)*2)}
	register := func(token string, e *Entry) {
		if token == "" {
			return
		}
		// Strip "=version" suffix on the provider side. The table
		// keys on the virtual name; constraints stay on the consumer
		// side and are checked by the resolver when (and if) yoe ever
		// enables version-aware resolution.
		if i := strings.IndexByte(token, '='); i >= 0 {
			token = token[:i]
		}
		if cur, ok := t.byName[token]; ok {
			if compareVersions(e.Version, cur.Version) > 0 {
				t.byName[token] = e
			}
			return
		}
		t.byName[token] = e
	}

	for i := range entries {
		e := &entries[i]
		register(e.Name, e)
		for _, p := range e.Provides {
			register(p, e)
		}
	}
	return t
}

// compareVersions implements a "good enough" Alpine apk version
// comparison. Alpine's full algorithm (`apk version`) parses suffix
// tags (_pre, _rc, _git, _p, _hotfix), the "-r" release counter, and
// trailing letter qualifiers. We cover the cases that show up in real
// APKINDEX data: digit-segment compare with letter suffix awareness,
// `_pre` / `_rc` / `_alpha` / `_beta` ordering, and the "-r<N>" release
// tail.
//
// Returns -1 if a < b, 0 if equal, +1 if a > b. Used only as a
// tiebreaker when two entries declare the same virtual.
func compareVersions(a, b string) int {
	if a == b {
		return 0
	}
	av, ar := splitRelease(a)
	bv, br := splitRelease(b)
	if c := comparePkgver(av, bv); c != 0 {
		return c
	}
	return compareInts(ar, br)
}

// splitRelease splits "1.2.3-r4" into ("1.2.3", 4). Missing "-r" yields
// release 0. Malformed release stays in the version part.
func splitRelease(v string) (pkgver string, release int) {
	i := strings.LastIndex(v, "-r")
	if i < 0 {
		return v, 0
	}
	tail := v[i+2:]
	if tail == "" || !allDigits(tail) {
		return v, 0
	}
	n := 0
	for _, c := range tail {
		n = n*10 + int(c-'0')
	}
	return v[:i], n
}

// comparePkgver compares the upstream-version portion (no `-rN` tail).
// Splits on `.`/`_`/`+` separators and compares numeric segments
// numerically, alpha segments lexically; `_pre`/`_rc`/`_alpha`/`_beta`
// sort before the otherwise-equal release version.
func comparePkgver(a, b string) int {
	as := splitVerSegments(a)
	bs := splitVerSegments(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := compareSegment(as[i], bs[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(as) < len(bs):
		if isPrereleaseSegment(bs[len(as)]) {
			return +1
		}
		return -1
	case len(as) > len(bs):
		if isPrereleaseSegment(as[len(bs)]) {
			return -1
		}
		return +1
	}
	return 0
}

func splitVerSegments(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	var prevKind int // 0=none, 1=digit, 2=alpha
	for _, r := range v {
		switch {
		case r == '.' || r == '_' || r == '+' || r == '-':
			flush()
			if r == '_' {
				out = append(out, "_")
			}
			prevKind = 0
			continue
		case unicode.IsDigit(r):
			if prevKind == 2 {
				flush()
			}
			cur.WriteRune(r)
			prevKind = 1
		default:
			if prevKind == 1 {
				flush()
			}
			cur.WriteRune(r)
			prevKind = 2
		}
	}
	flush()
	return out
}

func compareSegment(a, b string) int {
	aPre := isPrereleaseSegment(a)
	bPre := isPrereleaseSegment(b)
	if aPre && !bPre {
		return -1
	}
	if bPre && !aPre {
		return +1
	}
	if allDigits(a) && allDigits(b) {
		return compareNumericString(a, b)
	}
	return strings.Compare(a, b)
}

func isPrereleaseSegment(s string) bool {
	switch s {
	case "alpha", "beta", "pre", "rc", "_":
		return true
	}
	// "_pre1" arrives as ["_", "pre1"]; "pre1" matches by prefix.
	if strings.HasPrefix(s, "alpha") || strings.HasPrefix(s, "beta") ||
		strings.HasPrefix(s, "pre") || strings.HasPrefix(s, "rc") {
		return true
	}
	return false
}

func compareNumericString(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return +1
	}
	return strings.Compare(a, b)
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return +1
	}
	return 0
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
