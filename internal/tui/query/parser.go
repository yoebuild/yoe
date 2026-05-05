package query

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
	// insensitive) for the unit to match.
	bareTerms []string
}

// Parse compiles a query string. Returns an error for unknown field names
// or malformed input. The empty string parses to the empty query, which
// matches every unit.
func Parse(input string) (Query, error) {
	return Query{}, nil
}

// IsEmpty reports whether q is the empty query (matches everything).
func (q Query) IsEmpty() bool {
	return len(q.fields) == 0 && len(q.bareTerms) == 0
}

// String returns the canonical text form of q. Field filters first
// (sorted by field name, values in declaration order), bare terms last.
// Round-trips through Parse.
func (q Query) String() string {
	return ""
}
