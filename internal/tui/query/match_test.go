package query

import (
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// fixtureProject builds a tiny synthetic project covering every dimension
// the matcher is expected to filter on.
func fixtureProject() map[string]*yoestar.Unit {
	return map[string]*yoestar.Unit{
		"base-image":      {Name: "base-image", Class: "image", Module: "module-core"},
		"toolchain-musl":  {Name: "toolchain-musl", Class: "container", Module: "module-core"},
		"openssl":         {Name: "openssl", Class: "unit", Module: "module-core"},
		"musl":            {Name: "musl", Class: "unit", Module: "module-alpine"},
		"libcrypto3":      {Name: "libcrypto3", Class: "unit", Module: "module-alpine"},
		"my-app":          {Name: "my-app", Class: "unit", Module: ""}, // project root
	}
}

func mustParse(t *testing.T, s string) Query {
	t.Helper()
	q, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return q
}

func matchAll(q Query, units map[string]*yoestar.Unit, statuses map[string]string, inSet map[string]bool) []string {
	var out []string
	for name, u := range units {
		if q.Matches(name, u, statuses[name], inSet) {
			out = append(out, name)
		}
	}
	return out
}

func has(out []string, want string) bool {
	for _, n := range out {
		if n == want {
			return true
		}
	}
	return false
}

func TestMatches_Empty(t *testing.T) {
	units := fixtureProject()
	out := matchAll(mustParse(t, ""), units, nil, nil)
	if len(out) != len(units) {
		t.Fatalf("empty query: matched %d, want %d", len(out), len(units))
	}
}

func TestMatches_TypeImage(t *testing.T) {
	units := fixtureProject()
	out := matchAll(mustParse(t, "type:image"), units, nil, nil)
	if len(out) != 1 || out[0] != "base-image" {
		t.Fatalf("type:image: got %v", out)
	}
}

func TestMatches_ImagesShortcut(t *testing.T) {
	units := fixtureProject()
	a := matchAll(mustParse(t, "images"), units, nil, nil)
	b := matchAll(mustParse(t, "type:image"), units, nil, nil)
	if !equalSet(a, b) {
		t.Fatalf("`images` should equal `type:image`; got %v vs %v", a, b)
	}
}

func TestMatches_ModuleORWithin(t *testing.T) {
	units := fixtureProject()
	out := matchAll(mustParse(t, "module:module-core module:module-alpine"), units, nil, nil)
	for _, want := range []string{"base-image", "toolchain-musl", "openssl", "musl", "libcrypto3"} {
		if !has(out, want) {
			t.Fatalf("expected %q in %v", want, out)
		}
	}
	if has(out, "my-app") {
		t.Fatalf("project-root unit should not match modules: got %v", out)
	}
}

func TestMatches_ModuleProject(t *testing.T) {
	units := fixtureProject()
	out := matchAll(mustParse(t, "module:project"), units, nil, nil)
	if len(out) != 1 || out[0] != "my-app" {
		t.Fatalf("module:project: got %v", out)
	}
}

func TestMatches_StatusFromMap(t *testing.T) {
	units := fixtureProject()
	statuses := map[string]string{"openssl": "failed", "musl": "cached"}
	out := matchAll(mustParse(t, "status:failed"), units, statuses, nil)
	if len(out) != 1 || out[0] != "openssl" {
		t.Fatalf("status:failed: got %v", out)
	}
}

func TestMatches_BareSubstring(t *testing.T) {
	units := fixtureProject()
	out := matchAll(mustParse(t, "ssl"), units, nil, nil)
	if !has(out, "openssl") || has(out, "musl") {
		t.Fatalf("substring `ssl`: got %v", out)
	}
}

func TestMatches_BareSubstringCaseInsensitive(t *testing.T) {
	units := fixtureProject()
	out := matchAll(mustParse(t, "OPENSSL"), units, nil, nil)
	if len(out) != 1 || out[0] != "openssl" {
		t.Fatalf("OPENSSL substring: got %v", out)
	}
}

func TestMatches_AndAcrossFields(t *testing.T) {
	units := fixtureProject()
	out := matchAll(mustParse(t, "module:module-alpine type:unit"), units, nil, nil)
	for _, want := range []string{"musl", "libcrypto3"} {
		if !has(out, want) {
			t.Fatalf("expected %q in %v", want, out)
		}
	}
	if has(out, "base-image") || has(out, "toolchain-musl") {
		t.Fatalf("module+type AND failed: %v", out)
	}
}

func TestMatches_InClosure(t *testing.T) {
	units := fixtureProject()
	inSet := map[string]bool{"openssl": true, "musl": true}
	out := matchAll(mustParse(t, "in:openssl"), units, nil, inSet)
	for _, want := range []string{"openssl", "musl"} {
		if !has(out, want) {
			t.Fatalf("expected %q in %v", want, out)
		}
	}
	if has(out, "base-image") || has(out, "my-app") {
		t.Fatalf("in: closure leaked: %v", out)
	}
}

func TestMatches_InClosureNilDoesNotMatch(t *testing.T) {
	// Caller forgot to compute the closure: matcher should reject every unit
	// rather than silently match all. This makes the bug obvious if it
	// happens — better than masking it as "no results".
	units := fixtureProject()
	out := matchAll(mustParse(t, "in:openssl"), units, nil, nil)
	if len(out) != 0 {
		t.Fatalf("nil inSet should match nothing, got %v", out)
	}
}

func TestMatches_UnknownTypeValue(t *testing.T) {
	// type:gizmo is a syntactically valid query that simply matches no
	// unit, per spec.
	units := fixtureProject()
	out := matchAll(mustParse(t, "type:gizmo"), units, nil, nil)
	if len(out) != 0 {
		t.Fatalf("type:gizmo should match nothing, got %v", out)
	}
}

func equalSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if !m[x] {
			return false
		}
	}
	return true
}
