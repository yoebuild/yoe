package query

import (
	"fmt"
	"sort"
	"strings"
)

// Query is a parsed TUI search expression. Apply with (Query).Matches.
//
// Field filters AND across distinct fields and OR within the same field.
// Bare terms AND with everything else (substring match against the unit
// name). The empty Query matches every unit.
type Query struct {
	// Field-keyed filter values. Each slice is OR'd internally; entries
	// across keys are AND'd. Values are lowercased at parse time.
	fields map[string][]string

	// Bare substring terms. Each must appear in the unit name (case-
	// insensitive) for the unit to match. Lowercased at parse time.
	bareTerms []string
}

// knownFields enumerates the field names accepted by the parser. Anything
// else is an error; this is what makes typos visible instead of silent.
var knownFields = map[string]bool{
	"type":   true,
	"module": true,
	"status": true,
	"in":     true,
}

// viewShortcuts desugar a single bare token into a field filter. Recognized
// before falling back to bare-term substring matching, so typing "images"
// filters by type rather than by units whose names contain "images".
var viewShortcuts = map[string]struct{ field, value string }{
	"images":     {"type", "image"},
	"containers": {"type", "container"},
	"failed":     {"status", "failed"},
	"building":   {"status", "building"},
}

// Parse compiles a query string. Returns an error for unknown field names
// or malformed input. The empty string parses to the empty query, which
// matches every unit.
func Parse(input string) (Query, error) {
	q := Query{}
	tokens := strings.Fields(input)
	for _, raw := range tokens {
		tok := strings.ToLower(raw)

		if vs, ok := viewShortcuts[tok]; ok {
			q.fields = appendField(q.fields, vs.field, vs.value)
			continue
		}

		if i := strings.IndexByte(tok, ':'); i >= 0 {
			field := tok[:i]
			value := tok[i+1:]
			if field == "" {
				return Query{}, fmt.Errorf("query: empty field name in %q", raw)
			}
			if !knownFields[field] {
				return Query{}, fmt.Errorf("query: unknown field %q", field)
			}
			if value == "" {
				return Query{}, fmt.Errorf("query: %s: needs a value", field)
			}
			q.fields = appendField(q.fields, field, value)
			continue
		}

		q.bareTerms = append(q.bareTerms, tok)
	}
	return q, nil
}

func appendField(fields map[string][]string, k, v string) map[string][]string {
	if fields == nil {
		fields = map[string][]string{}
	}
	fields[k] = append(fields[k], v)
	return fields
}

// IsEmpty reports whether q is the empty query (matches everything).
func (q Query) IsEmpty() bool {
	return len(q.fields) == 0 && len(q.bareTerms) == 0
}

// String returns the canonical text form of q. Field filters first
// (sorted by field name, values in declaration order), bare terms last.
// Round-trips through Parse.
func (q Query) String() string {
	if q.IsEmpty() {
		return ""
	}
	keys := make([]string, 0, len(q.fields))
	for k := range q.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(q.fields)+len(q.bareTerms))
	for _, k := range keys {
		for _, v := range q.fields[k] {
			parts = append(parts, k+":"+v)
		}
	}
	parts = append(parts, q.bareTerms...)
	return strings.Join(parts, " ")
}
