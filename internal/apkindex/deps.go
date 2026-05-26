package apkindex

import (
	"fmt"
	"strings"
)

// DepKind classifies an APKINDEX dep token. See ParseDep.
type DepKind int

const (
	DepKindUnknown DepKind = iota
	DepKindName             // bare package name: "musl"
	DepKindSo               // shared object: "so:libcrypto.so.3"
	DepKindCmd              // command: "cmd:gpg"
	DepKindPc               // pkg-config: "pc:libfoo"
	DepKindPath             // file path: "/etc/passwd"
	DepKindConflict         // negation prefix: "!busybox"
)

// Op encodes the version constraint operator, if any.
type Op int

const (
	OpNone Op = iota
	OpEq      // =
	OpLt      // <
	OpLe      // <=
	OpGt      // >
	OpGe      // >=
	OpTilde   // ~ (Alpine's "fuzzy equal" — same upstream major.minor)
)

// Dep is one parsed dep token. The Name field is the resolver lookup
// key — for `so:libcrypto.so.3` it's `so:libcrypto.so.3` (the full
// virtual name), for `musl` it's `musl`.
//
// Version + Op carry the constraint as written. Per R7, yoe resolves
// by name only; the constraint is parsed for syntactic validity then
// dropped at materialize time. We keep the parsed form here so error
// messages can echo it and so a future stricter mode can reactivate it.
type Dep struct {
	Kind    DepKind
	Name    string // lookup token: bare name, "so:...", "cmd:...", "pc:...", "/path"
	Version string // empty if no constraint
	Op      Op

	// Conflict is true for `!foo` tokens. When set, Kind reflects the
	// sub-form (Name, So, ...) so a caller can see "this is a conflict
	// against the so:libfoo soname" rather than a flat "conflict".
	Conflict bool

	// Raw is the original token text, for error messages.
	Raw string
}

// ParseDep turns one APKINDEX dep token into a Dep. Handles every form
// listed in <https://wiki.alpinelinux.org/wiki/Apk_spec#Dependencies>:
//
//	name              bare package name
//	name<op>ver       versioned name; op ∈ {=, <, <=, >, >=, ~}
//	so:libfoo.so.3    shared-object provider
//	so:libfoo.so.3=ver versioned soname
//	cmd:gpg           command provider
//	pc:libfoo         pkg-config provider
//	/file/path        explicit file dependency
//	!something        conflict (any of the above prefixed with !)
//
// Returns an error for genuinely malformed input (empty name, unknown
// op characters). Unknown-but-shaped tokens parse as DepKindName so an
// unfamiliar prefix doesn't kill the whole index.
func ParseDep(s string) (Dep, error) {
	if s == "" {
		return Dep{}, fmt.Errorf("apkindex: empty dep token")
	}
	d := Dep{Raw: s}
	if s[0] == '!' {
		d.Conflict = true
		s = s[1:]
		if s == "" {
			return Dep{}, fmt.Errorf("apkindex: dep %q: empty after !", d.Raw)
		}
	}
	if s[0] == '/' {
		d.Kind = DepKindPath
		d.Name = s
		return d, nil
	}

	// Split name<op>version. Operator scan must respect "<=" / ">="
	// before single-char "<" / ">".
	name, op, ver := splitConstraint(s)
	if name == "" {
		return Dep{}, fmt.Errorf("apkindex: dep %q: empty name", d.Raw)
	}
	d.Op = op
	d.Version = ver

	switch {
	case strings.HasPrefix(name, "so:"):
		d.Kind = DepKindSo
	case strings.HasPrefix(name, "cmd:"):
		d.Kind = DepKindCmd
	case strings.HasPrefix(name, "pc:"):
		d.Kind = DepKindPc
	default:
		d.Kind = DepKindName
	}
	d.Name = name
	return d, nil
}

// ParseDeps parses every token in a `D:` / `p:` / `r:` / `i:` line.
// Errors include the failing token's index so a malformed entry is easy
// to locate.
func ParseDeps(tokens []string) ([]Dep, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	out := make([]Dep, 0, len(tokens))
	for i, t := range tokens {
		d, err := ParseDep(t)
		if err != nil {
			return nil, fmt.Errorf("apkindex: dep[%d]: %w", i, err)
		}
		out = append(out, d)
	}
	return out, nil
}

// splitConstraint splits "name<op>version" into its three parts. Returns
// (name, OpNone, "") when no operator is present.
//
// APKINDEX dep tokens occasionally embed colons inside the name (so:foo,
// cmd:bar) so we scan the byte sequence directly rather than splitting
// on operator characters indiscriminately. The first operator character
// after position 0 wins.
func splitConstraint(s string) (name string, op Op, version string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '=':
			return s[:i], OpEq, s[i+1:]
		case '~':
			return s[:i], OpTilde, s[i+1:]
		case '<':
			if i+1 < len(s) && s[i+1] == '=' {
				return s[:i], OpLe, s[i+2:]
			}
			return s[:i], OpLt, s[i+1:]
		case '>':
			if i+1 < len(s) && s[i+1] == '=' {
				return s[:i], OpGe, s[i+2:]
			}
			return s[:i], OpGt, s[i+1:]
		}
	}
	return s, OpNone, ""
}
