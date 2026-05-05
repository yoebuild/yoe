package query

import (
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// Matches reports whether the given unit satisfies every term in q.
//
// `name` is the unit's name (matched case-insensitively against bare
// terms). `unit` supplies the type (Class) and module fields. `status` is
// the per-unit TUI status string ("cached"/"building"/"failed"/"" for
// none); the matcher takes a string instead of the TUI's enum so this
// package stays free of tui imports. `inSet` is the pre-computed closure
// of `in:X` for whichever X (if any) is in the query — nil when no in:
// filter is active.
//
// An in: filter with a nil inSet matches NO units (caller bug rather
// than "matches everything"). An empty query matches every unit.
func (q Query) Matches(name string, unit *yoestar.Unit, status string, inSet map[string]bool) bool {
	if q.IsEmpty() {
		return true
	}
	lname := strings.ToLower(name)
	for _, term := range q.bareTerms {
		if !strings.Contains(lname, term) {
			return false
		}
	}
	for field, values := range q.fields {
		var got string
		switch field {
		case "type":
			if unit != nil {
				got = strings.ToLower(unit.Class)
			}
		case "module":
			if unit != nil {
				got = strings.ToLower(unit.Module)
				if got == "" {
					got = "project"
				}
			} else {
				got = "project"
			}
		case "status":
			got = strings.ToLower(status)
		case "in":
			// in: only ever takes a single value (one closure root). If
			// the parser ever lets multiple `in:` slip through, we OR
			// over inSets is the wrong shape — reject any result here
			// and let tests catch it.
			if inSet == nil {
				return false
			}
			if !inSet[name] {
				return false
			}
			continue
		}
		if !anyEquals(got, values) {
			return false
		}
	}
	return true
}

func anyEquals(got string, vals []string) bool {
	for _, v := range vals {
		if got == v {
			return true
		}
	}
	return false
}
