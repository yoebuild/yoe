// Package feedcore holds the helpers the Alpine and apt feed backends
// genuinely share.
//
// The two backends look parallel — same file names, same function names,
// same overall shape — but measured function by function, only about 60
// of 400-odd lines are actually the same code. The rest differ by
// specification: APKINDEX and Packages are different formats, the
// dependency grammars are different (apk's so:/cmd:/pc: virtuals versus
// deb's alternatives and architecture qualifiers), and the transport,
// signature scheme, and kwargs all differ. Abstracting over that with a
// backend interface and type parameters would add machinery to hide
// differences that are real.
//
// So this package deliberately holds only what is byte-identical and
// type-independent. It exists so the next such helper has an obvious
// home instead of becoming a third copy.
package feedcore

import (
	"fmt"
	"path/filepath"

	"go.starlark.net/starlark"
)

// HumanBytes formats a size as "N B", "N.N KiB", "N.N MiB", "N.NN GiB".
// Base-2 because that is what apk-tools, apt, and du -h show, so a feed
// update's summary line matches what the user sees elsewhere.
func HumanBytes(n int64) string {
	const (
		KiB = 1024
		MiB = 1024 * 1024
		GiB = 1024 * 1024 * 1024
	)
	switch {
	case n < KiB:
		return fmt.Sprintf("%d B", n)
	case n < MiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/KiB)
	case n < GiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/MiB)
	default:
		return fmt.Sprintf("%.2f GiB", float64(n)/GiB)
	}
}

// RelTo returns path relative to base for progress output, falling back
// to the absolute path when the two share no common root (different
// mount points). Progress output is not worth an error return.
func RelTo(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

// StringList converts a Starlark list of strings to []string, skipping
// any element that is not a string. Feed declarations reach this after
// Starlark has already accepted the call, so a non-string element is a
// malformed declaration rather than something to report per element.
func StringList(list *starlark.List) []string {
	if list == nil {
		return nil
	}
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
