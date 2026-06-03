package dpkg

import (
	"fmt"
	"strings"

	"pault.ag/go/debian/dependency"
)

// Op encodes the version constraint operator. The string values match
// Debian's notation (>>, >=, =, <=, <<).
type Op string

const (
	OpNone Op = ""
	OpLt   Op = "<<"
	OpLe   Op = "<="
	OpEq   Op = "="
	OpGe   Op = ">="
	OpGt   Op = ">>"
)

// Possibility is one alternative in a dependency relation. Given
// "libssl3 (>= 3.0.0) | libssl1.1", each | branch is one Possibility.
type Possibility struct {
	Name    string // bare package or virtual name; lookup key
	Arch    string // arch qualifier from "name:arch" (":any", ":native", "amd64", ...)
	Version string // empty if no constraint
	Op      Op

	// Raw is the original token text, for error messages.
	Raw string
}

// Relation is one comma-separated dependency atom, possibly with
// alternatives. The resolver tries each Possibility until one resolves.
type Relation struct {
	Possibilities []Possibility
}

// Dependency is a parsed Depends / Pre-Depends / Provides / Conflicts /
// Breaks / Replaces line. Empty input parses cleanly to an empty
// Dependency.
type Dependency struct {
	Relations []Relation
}

// ParseDependency parses a dpkg dependency line per Debian Policy 7.1.
// Returns an error only on truly malformed input; empty input yields an
// empty Dependency.
func ParseDependency(s string) (Dependency, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Dependency{}, nil
	}
	parsed, err := dependency.Parse(s)
	if err != nil {
		return Dependency{}, fmt.Errorf("dpkg: parse dependency %q: %w", s, err)
	}
	out := Dependency{Relations: make([]Relation, 0, len(parsed.Relations))}
	for _, rel := range parsed.Relations {
		rOut := Relation{Possibilities: make([]Possibility, 0, len(rel.Possibilities))}
		for _, p := range rel.Possibilities {
			pp := Possibility{
				Name: p.Name,
				Raw:  p.Name,
			}
			if p.Arch != nil {
				pp.Arch = p.Arch.String()
			}
			if p.Version != nil {
				pp.Version = p.Version.Number
				pp.Op = Op(p.Version.Operator)
			}
			rOut.Possibilities = append(rOut.Possibilities, pp)
		}
		out.Relations = append(out.Relations, rOut)
	}
	return out, nil
}

// ParseProvides parses a Provides line. Provides syntax is a subset of
// the dependency grammar — only "=" is allowed and architecture
// qualifiers are forbidden — but apt has historically been lenient, so
// we fall through to ParseDependency.
func ParseProvides(s string) ([]Possibility, error) {
	dep, err := ParseDependency(s)
	if err != nil {
		return nil, err
	}
	if len(dep.Relations) == 0 {
		return nil, nil
	}
	out := make([]Possibility, 0, len(dep.Relations))
	for _, rel := range dep.Relations {
		for _, p := range rel.Possibilities {
			out = append(out, p)
		}
	}
	return out, nil
}

// FlattenNames walks every Relation and returns the bare name of the
// first Possibility in each. yoe resolves by name only — alternative
// resolution (foo | bar) picks the first that the provides table
// satisfies; callers run the lookup themselves so they can resolve to
// the actual provider.
//
// Use this for "what does this package potentially depend on" surveys;
// for resolution, walk Relations and Possibilities directly.
func (d Dependency) FlattenNames() []string {
	if len(d.Relations) == 0 {
		return nil
	}
	out := make([]string, 0, len(d.Relations))
	for _, rel := range d.Relations {
		if len(rel.Possibilities) == 0 {
			continue
		}
		out = append(out, rel.Possibilities[0].Name)
	}
	return out
}
