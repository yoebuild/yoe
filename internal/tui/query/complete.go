package query

import (
	"sort"
	"strings"
)

// Context carries the live data needed for value-side completion.
type Context struct {
	Modules []string
	Units   []string
}

// fieldCompletions and shortcutCompletions are the candidates suggested
// when the token has no `:`. They overlap intentionally: typing "im"
// completes "images" (shortcut), typing "mo" completes "module:".
var fieldCompletions = []string{"in:", "module:", "status:", "type:"}
var shortcutCompletions = []string{"building", "containers", "failed", "images"}

var typeValues = []string{"container", "image", "unit"}
var statusValues = []string{"building", "cached", "failed", "pending", "stale"}

// Complete returns candidate completions for the token under `cursor`.
// start/end describe the byte range of the token to be replaced.
// Candidates are sorted deterministically. Empty result when there is
// nothing to suggest.
func Complete(input string, cursor int, ctx Context) (start, end int, candidates []string) {
	start, end = tokenSpan(input, cursor)
	tok := strings.ToLower(input[start:end])

	if i := strings.IndexByte(tok, ':'); i >= 0 {
		field := tok[:i]
		val := tok[i+1:]
		switch field {
		case "type":
			return start, end, prefixMatch(typeValues, val)
		case "status":
			return start, end, prefixMatch(statusValues, val)
		case "module":
			return start, end, prefixMatch(ctx.Modules, val)
		case "in":
			return start, end, prefixMatch(ctx.Units, val)
		}
		return start, end, nil
	}

	// No colon — could be a field name, a view shortcut, or a bare
	// term. Try field+shortcut prefix-matches first; if either yields,
	// return them. Otherwise fall back to substring match against unit
	// names (the bare-term semantics).
	var out []string
	for _, c := range fieldCompletions {
		if strings.HasPrefix(c, tok) {
			out = append(out, c)
		}
	}
	for _, c := range shortcutCompletions {
		if strings.HasPrefix(c, tok) {
			out = append(out, c)
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		return start, end, out
	}
	return start, end, substringMatch(ctx.Units, tok)
}

func tokenSpan(input string, cursor int) (int, int) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	start := cursor
	for start > 0 && input[start-1] != ' ' {
		start--
	}
	end := cursor
	for end < len(input) && input[end] != ' ' {
		end++
	}
	return start, end
}

func prefixMatch(pool []string, prefix string) []string {
	if prefix == "" {
		out := append([]string(nil), pool...)
		sort.Strings(out)
		return out
	}
	var out []string
	for _, s := range pool {
		if strings.HasPrefix(strings.ToLower(s), prefix) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func substringMatch(pool []string, needle string) []string {
	if needle == "" {
		return nil
	}
	var out []string
	for _, s := range pool {
		if strings.Contains(strings.ToLower(s), needle) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
