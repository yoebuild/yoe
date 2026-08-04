package starlark

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// reservedUnitKwargs is a hand-maintained list that has to stay in
// agreement with the kwargs registerUnit reads into typed Unit fields. A
// kwarg read into a field but missing from the list is captured into
// Unit.Extra as well — and Extra is hashed, so a purely presentational
// field silently becomes part of the content-addressed cache key. That
// exact drift happened once with artifacts_explicit, which every image
// sets, quietly folding UX metadata into every image's hash.
//
// This scans registerUnit's own source for the kwarg names it reads, the
// same way TestUnitHash_CoversAllFields scans hash.go, so the next field
// added without a matching list entry fails here instead of in a cache
// key nobody thinks to inspect.
func TestReservedUnitKwargs_CoversRegisterUnit(t *testing.T) {
	src, err := os.ReadFile("builtins.go")
	if err != nil {
		t.Fatalf("read builtins.go: %v", err)
	}

	body := funcBody(t, string(src), "func (e *Engine) registerUnit(")

	// kwString(kwargs, "name"), kwStringList(kwargs, "deps"), …
	re := regexp.MustCompile(`kw\w*\(kwargs, "([a-z0-9_]+)"\)`)
	var missing []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		kw := m[1]
		if !reservedUnitKwargs[kw] {
			missing = append(missing, kw)
		}
	}
	if len(missing) > 0 {
		t.Errorf("registerUnit reads %v into typed fields, but they are missing from "+
			"reservedUnitKwargs — they will also be captured into Unit.Extra, which is hashed",
			missing)
	}
}

// funcBody returns the source text of the function whose declaration
// starts with prefix, up to the closing brace in column 0.
func funcBody(t *testing.T, src, prefix string) string {
	t.Helper()
	i := strings.Index(src, prefix)
	if i < 0 {
		t.Fatalf("could not find %q in source", prefix)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}\n"); j >= 0 {
		return rest[:j]
	}
	return rest
}
