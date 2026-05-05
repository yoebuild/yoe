# TUI unit query language — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the TUI's substring-only `/` search with a sndtool-style query
language so a 3000-unit project remains navigable. Default scope is the closure
of the active image (`in:base-image`), with field filters (`type:`, `module:`,
`status:`, `in:`), bare-term substring, view shortcuts (`images`, `containers`,
`failed`, `building`), live filtering, tab completion, snap-back (`\`), and
save-to-local.star (`S`).

**Architecture:**

- A new `internal/tui/query` package owns the parser, match function, closure
  operator, and tab-completion candidates. It has no dependency on bubbletea —
  it operates on `*yoestar.Project` plus a `map[string]unitStatus` and produces
  a filter predicate plus completion candidates.
- `internal/starlark/local.go` gains a `Query` field on `LocalOverrides` and
  round-trips it through `local.star`.
- `internal/tui/app.go` swaps its `searchText`/`filtered` fields for a
  `query.Query` plus visible-index slice; rendering reads from the same query in
  both search-mode and normal mode (search mode is just "the bar is focused for
  editing"). The TUI also gains a header line (`Query: …  Units: N/M`), `\`
  snap-back, and capital `S` save.

**Tech Stack:** Go 1.21+, bubbletea/lipgloss (already in use), no new
dependencies.

**Spec:** `docs/superpowers/specs/2026-05-04-tui-unit-query-design.md`. Read it
before starting Task 1.

---

## File Structure

**New files:**

- `internal/tui/query/parser.go` — `Query` type, `Parse(string) (Query, error)`,
  `Query.IsEmpty()`, `Query.String()`, and the `Query.InRoot()` /
  `Query.BareTerms()` accessors used by the TUI integration tasks. (Trait of the
  type, not the matcher, so it lives next to the type definition.)
- `internal/tui/query/parser_test.go` — grammar coverage.
- `internal/tui/query/match.go` — `Query.Matches(name, unit, status, inSet)`.
- `internal/tui/query/match_test.go`
- `internal/tui/query/closure.go` — `BuildInClosure(proj, root)` returning the
  unit-name set.
- `internal/tui/query/closure_test.go`
- `internal/tui/query/complete.go` — `Complete(input, cursor, ctx)` returning
  candidate list and replacement span.
- `internal/tui/query/complete_test.go`

**Modified files:**

- `internal/starlark/local.go` — add `Query` field, parse `query =` kwarg, write
  it.
- `internal/starlark/local_test.go` — round-trip test (file may not exist yet;
  create it as part of Task 5).
- `internal/tui/app.go` — replace `searchText`/`filtered` with the query state,
  add header line, key bindings, completion overlay, status-bar plumbing.

**Touched but not redesigned:** `internal/resolve/runtime.go` (consumed only)
and `internal/resolve/dag.go` (consumed only).

---

### Task 1: Define the `Query` type and a placeholder parser

**Files:**

- Create: `internal/tui/query/parser.go`
- Create: `internal/tui/query/parser_test.go`

This task establishes the type surface and the empty-string base case so
subsequent tasks have something to call. Real grammar lands in Task 2.

- [ ] **Step 1: Write the failing test**

```go
// internal/tui/query/parser_test.go
package query

import "testing"

func TestParse_Empty(t *testing.T) {
	q, err := Parse("")
	if err != nil {
		t.Fatalf("Parse(\"\"): %v", err)
	}
	if !q.IsEmpty() {
		t.Fatalf("expected IsEmpty for empty string")
	}
	if q.String() != "" {
		t.Fatalf("expected canonical empty string, got %q", q.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected: build
failure — package doesn't exist yet.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/tui/query/parser.go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected:
`ok  github.com/yoebuild/yoe/internal/tui/query`.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/query/parser.go internal/tui/query/parser_test.go
git commit -m "tui/query: stub Query type and empty-string parser"
```

---

### Task 2: Full grammar — parser, error cases, canonical String()

**Files:**

- Modify: `internal/tui/query/parser.go`
- Modify: `internal/tui/query/parser_test.go`

The grammar is:

```
query        := term (WS+ term)*
term         := field-filter | view-shortcut | bare-term
field-filter := IDENT ":" VALUE
view-shortcut := "images" | "containers" | "failed" | "building"
bare-term    := WORD
```

Whitespace separates terms. Field names and values are case-insensitive. View
shortcuts are recognized **before** bare-term substring matching (so `images`
filters by type, not by name-contains-"images"). Unknown field names return an
error from Parse. Unknown values for known fields parse cleanly and simply match
nothing at filter time.

Recognized field names (v1): `type`, `module`, `status`, `in`. Anything else is
an error.

- [ ] **Step 1: Write the failing tests**

```go
// internal/tui/query/parser_test.go (replace the file)
package query

import (
	"strings"
	"testing"
)

func TestParse_Empty(t *testing.T) {
	q, err := Parse("")
	if err != nil {
		t.Fatalf("Parse(\"\"): %v", err)
	}
	if !q.IsEmpty() {
		t.Fatalf("expected IsEmpty for empty string")
	}
	if q.String() != "" {
		t.Fatalf("canonical: got %q, want empty", q.String())
	}
}

func TestParse_BareTerm(t *testing.T) {
	q, err := Parse("openssl")
	if err != nil {
		t.Fatal(err)
	}
	if q.IsEmpty() {
		t.Fatal("expected non-empty")
	}
	if q.String() != "openssl" {
		t.Fatalf("canonical: got %q", q.String())
	}
}

func TestParse_FieldFilter(t *testing.T) {
	q, err := Parse("type:image")
	if err != nil {
		t.Fatal(err)
	}
	if q.String() != "type:image" {
		t.Fatalf("canonical: got %q", q.String())
	}
}

func TestParse_ViewShortcuts(t *testing.T) {
	cases := map[string]string{
		"images":     "type:image",
		"containers": "type:container",
		"failed":     "status:failed",
		"building":   "status:building",
	}
	for in, want := range cases {
		q, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if q.String() != want {
			t.Fatalf("Parse(%q): canonical %q, want %q", in, q.String(), want)
		}
	}
}

func TestParse_MultipleTerms(t *testing.T) {
	q, err := Parse("in:base-image status:failed openssl")
	if err != nil {
		t.Fatal(err)
	}
	// Canonical order: fields sorted by name (in, status), bare terms
	// last in source order.
	if q.String() != "in:base-image status:failed openssl" {
		t.Fatalf("canonical: got %q", q.String())
	}
}

func TestParse_RepeatedFieldOR(t *testing.T) {
	q, err := Parse("module:units-core module:units-rpi")
	if err != nil {
		t.Fatal(err)
	}
	if q.String() != "module:units-core module:units-rpi" {
		t.Fatalf("canonical: got %q", q.String())
	}
}

func TestParse_CaseInsensitive(t *testing.T) {
	q, err := Parse("TYPE:Image OpenSSL")
	if err != nil {
		t.Fatal(err)
	}
	// Field name + value lowercased; bare term lowercased so matching is
	// uniform. The canonical form preserves the lowered casing.
	if q.String() != "type:image openssl" {
		t.Fatalf("canonical: got %q", q.String())
	}
}

func TestParse_UnknownField(t *testing.T) {
	_, err := Parse("fizz:foo")
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "fizz") {
		t.Fatalf("error should mention the bad field, got %v", err)
	}
}

func TestParse_TrailingColon(t *testing.T) {
	if _, err := Parse("type:"); err == nil {
		t.Fatal("expected error for trailing colon")
	}
}

func TestParse_EmptyFieldName(t *testing.T) {
	if _, err := Parse(":foo"); err == nil {
		t.Fatal("expected error for empty field name")
	}
}

func TestParse_RoundTrip(t *testing.T) {
	inputs := []string{
		"",
		"openssl",
		"type:image",
		"in:base-image",
		"in:base-image status:failed",
		"module:units-core module:units-rpi linux-firmware",
	}
	for _, in := range inputs {
		q, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		canonical := q.String()
		q2, err := Parse(canonical)
		if err != nil {
			t.Fatalf("re-parse(%q): %v", canonical, err)
		}
		if q2.String() != canonical {
			t.Fatalf("round-trip drift: %q -> %q -> %q", in, canonical, q2.String())
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected: most
tests fail (Parse returns empty query for everything).

- [ ] **Step 3: Implement the parser**

Replace `internal/tui/query/parser.go` with:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected: PASS
for all `TestParse_*`.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/query/parser.go internal/tui/query/parser_test.go
git commit -m "tui/query: parser, view shortcuts, canonical round-trip"
```

---

### Task 3: `Query.Matches` — apply filters to a unit

**Files:**

- Create: `internal/tui/query/match.go`
- Create: `internal/tui/query/match_test.go`

`Matches` answers the per-row predicate. Inputs:

- `name string` — the unit's name.
- `unit *yoestar.Unit` — for `Class` (→ `type:`) and `Module`.
- `status string` — the per-unit TUI status string (`""`, `"cached"`,
  `"building"`, `"failed"`, …). The TUI passes a string so the matcher doesn't
  depend on `tui.unitStatus`.
- `inSet map[string]bool` — pre-computed `in:` closure, or nil when no `in:`
  filter is active. Computed once per query change in Task 4 and reused for
  every row.

The match rule:

- For each field used in `q.fields`, the unit must match at least one of the
  listed values (OR within field).
- All bare terms must appear in `name` (AND across bare terms).
- Empty query matches every unit.

Special-case: `module:project` matches `unit.Module == ""` (project root has
empty Module). This is the spec's convention.

- [ ] **Step 1: Write the failing tests**

```go
// internal/tui/query/match_test.go
package query

import (
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// fixtureProject builds a tiny synthetic project covering every dimension
// the matcher is expected to filter on.
func fixtureProject() map[string]*yoestar.Unit {
	return map[string]*yoestar.Unit{
		"base-image":      {Name: "base-image", Class: "image", Module: "units-core"},
		"toolchain-musl":  {Name: "toolchain-musl", Class: "container", Module: "units-core"},
		"openssl":         {Name: "openssl", Class: "unit", Module: "units-core"},
		"musl":            {Name: "musl", Class: "unit", Module: "units-alpine"},
		"libcrypto3":      {Name: "libcrypto3", Class: "unit", Module: "units-alpine"},
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
	out := matchAll(mustParse(t, "module:units-core module:units-alpine"), units, nil, nil)
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
	out := matchAll(mustParse(t, "module:units-alpine type:unit"), units, nil, nil)
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected: build
failure — `Matches` undefined.

- [ ] **Step 3: Implement Matches**

```go
// internal/tui/query/match.go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected: all
`TestMatches_*` PASS, and earlier `TestParse_*` still PASS.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/query/match.go internal/tui/query/match_test.go
git commit -m "tui/query: Matches predicate with field, bare, and in: handling"
```

---

### Task 4: `BuildInClosure` — closure operator for `in:X`

**Files:**

- Create: `internal/tui/query/closure.go`
- Create: `internal/tui/query/closure_test.go`

`BuildInClosure(proj, root)` returns the set
`{root} ∪ build-deps*(root) ∪ runtime-deps*(root)`. Build-deps are walked via
`proj.Units[name].Deps`. For image units, `Deps` already includes `Artifacts`
because `resolve.BuildDAG` does that promotion — but we are not calling BuildDAG
here; we read the raw fields on the unit. So image artifacts must be walked
explicitly.

Runtime deps are walked via `RuntimeClosure` from `internal/resolve` (already
exists, already routes through `proj.Provides`).

Returns `nil` (not an empty map) when `root` is not in `proj.Units`. Callers
treat nil as "matches nothing", same as the parser's `in:nonexistent` semantics.

- [ ] **Step 1: Write the failing tests**

```go
// internal/tui/query/closure_test.go
package query

import (
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func depProject() *yoestar.Project {
	return &yoestar.Project{
		Units: map[string]*yoestar.Unit{
			"toolchain-musl": {Name: "toolchain-musl", Class: "container"},
			"zlib":           {Name: "zlib", Class: "unit", Deps: []string{"toolchain-musl"}},
			"openssl":        {Name: "openssl", Class: "unit", Deps: []string{"zlib", "toolchain-musl"}, RuntimeDeps: []string{"zlib"}},
			"apk-tools":      {Name: "apk-tools", Class: "unit", Deps: []string{"openssl", "zlib", "toolchain-musl"}, RuntimeDeps: []string{"openssl", "zlib"}},
			"base-image":     {Name: "base-image", Class: "image", Artifacts: []string{"openssl", "apk-tools"}},
		},
		Provides: map[string]string{},
	}
}

func TestClosure_Leaf(t *testing.T) {
	proj := depProject()
	got := BuildInClosure(proj, "toolchain-musl")
	if !got["toolchain-musl"] || len(got) != 1 {
		t.Fatalf("toolchain-musl closure: %v", got)
	}
}

func TestClosure_BuildDeps(t *testing.T) {
	proj := depProject()
	got := BuildInClosure(proj, "openssl")
	for _, want := range []string{"openssl", "zlib", "toolchain-musl"} {
		if !got[want] {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	if got["apk-tools"] {
		t.Fatalf("closure leaked upward: %v", got)
	}
}

func TestClosure_RuntimeDepsViaProvides(t *testing.T) {
	proj := depProject()
	proj.Units["libcrypto3"] = &yoestar.Unit{Name: "libcrypto3"}
	proj.Units["consumer"] = &yoestar.Unit{Name: "consumer", RuntimeDeps: []string{"libcrypto3"}}
	proj.Provides["libcrypto3"] = "openssl"
	got := BuildInClosure(proj, "consumer")
	if !got["openssl"] {
		t.Fatalf("expected libcrypto3 → openssl via Provides, got %v", got)
	}
	if got["libcrypto3"] {
		t.Fatalf("Provides routing should redirect, not include the virtual: %v", got)
	}
}

func TestClosure_ImageArtifacts(t *testing.T) {
	proj := depProject()
	got := BuildInClosure(proj, "base-image")
	for _, want := range []string{"base-image", "openssl", "apk-tools", "zlib", "toolchain-musl"} {
		if !got[want] {
			t.Fatalf("base-image closure missing %q in %v", want, got)
		}
	}
}

func TestClosure_Cycle(t *testing.T) {
	proj := &yoestar.Project{Units: map[string]*yoestar.Unit{
		"a": {Name: "a", Deps: []string{"b"}},
		"b": {Name: "b", Deps: []string{"a"}},
	}}
	got := BuildInClosure(proj, "a")
	if !got["a"] || !got["b"] || len(got) != 2 {
		t.Fatalf("cycle: %v", got)
	}
}

func TestClosure_MissingRoot(t *testing.T) {
	proj := depProject()
	if got := BuildInClosure(proj, "nonexistent"); got != nil {
		t.Fatalf("missing root: expected nil, got %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected: build
failure — `BuildInClosure` undefined.

- [ ] **Step 3: Implement BuildInClosure**

```go
// internal/tui/query/closure.go
package query

import (
	"github.com/yoebuild/yoe/internal/resolve"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// BuildInClosure returns the set of unit names reachable from root by
// walking build-time deps (Unit.Deps), runtime deps (Unit.RuntimeDeps,
// routed through proj.Provides), and — for image units — the artifact
// list. The root is included.
//
// Returns nil if root is not a unit in proj. Callers treat nil as "match
// nothing", which matches the spec's `in:nonexistent` failure mode.
//
// Cycles and missing dependency names are tolerated silently: the walker
// never recurses into a name it has already visited, and missing names
// are skipped (the build planner is responsible for flagging them).
func BuildInClosure(proj *yoestar.Project, root string) map[string]bool {
	if proj == nil || proj.Units == nil {
		return nil
	}
	if _, ok := proj.Units[root]; !ok {
		return nil
	}

	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if real, ok := proj.Provides[name]; ok {
			name = real
		}
		u, ok := proj.Units[name]
		if !ok || seen[name] {
			return
		}
		seen[name] = true
		for _, dep := range u.Deps {
			walk(dep)
		}
		// image units carry their package list in Artifacts; treat those
		// as deps for closure purposes (resolve.BuildDAG does the same
		// promotion when constructing the build graph).
		if u.Class == "image" {
			for _, a := range u.Artifacts {
				walk(a)
			}
		}
	}
	walk(root)

	// Union runtime-dep closure rooted at the same unit. RuntimeClosure
	// already routes through proj.Provides.
	for _, name := range resolve.RuntimeClosure(proj, []string{root}) {
		seen[name] = true
	}
	return seen
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected: all
closure tests PASS, earlier tests still PASS.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/query/closure.go internal/tui/query/closure_test.go
git commit -m "tui/query: BuildInClosure for in: operator"
```

---

### Task 5: `local.star` — round-trip the `query` field

**Files:**

- Modify: `internal/starlark/local.go`
- Create: `internal/starlark/local_test.go`

Current `LocalOverrides` has `Machine` and `DeployHost`. Add `Query`. The writer
currently has two branches (with/without DeployHost) producing distinct shapes;
replace it with a single multi-line emission that always uses
`local(\n    machine = ...,\n    deploy_host = ...,\n    query = ...,\n)` and
skips empty fields. This avoids combinatorial growth as new optional fields
land.

Care: existing TUI calls to `WriteLocalOverrides` sometimes pass a
partially-populated struct (e.g. the machine-switch handler at `app.go:865`
writes `{Machine: picked}` and would otherwise wipe `DeployHost`). The TUI side
gets fixed in Task 6; this task only needs the writer to be correct given
whatever struct it's handed.

- [ ] **Step 1: Write the failing tests**

```go
// internal/starlark/local_test.go
package starlark

import (
	"path/filepath"
	"testing"
)

func TestLocalOverrides_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := LocalOverrides{
		Machine:    "qemu-x86_64",
		DeployHost: "localhost:2222",
		Query:      "in:base-image",
	}
	if err := WriteLocalOverrides(dir, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadLocalOverrides(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != in {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}

func TestLocalOverrides_OnlyMachine(t *testing.T) {
	dir := t.TempDir()
	in := LocalOverrides{Machine: "qemu-x86_64"}
	if err := WriteLocalOverrides(dir, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadLocalOverrides(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != in {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}

func TestLocalOverrides_NoFile(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadLocalOverrides(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if (got != LocalOverrides{}) {
		t.Fatalf("expected zero overrides for missing file, got %+v", got)
	}
}

func TestLocalOverrides_BackCompatNoQuery(t *testing.T) {
	// A local.star written by an older yoe (no query field) must still
	// load cleanly.
	dir := t.TempDir()
	path := filepath.Join(dir, "local.star")
	content := "local(machine = \"qemu-arm64\", deploy_host = \"pi.local\")\n"
	if err := writeFile(path, content); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := LoadLocalOverrides(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := LocalOverrides{Machine: "qemu-arm64", DeployHost: "pi.local"}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// writeFile is a tiny helper since we don't import os in the test.
// (The file is part of the package; pull os in if you prefer.)
```

`writeFile` is just `os.WriteFile`; replace with the real call when implementing
— leaving the helper avoids importing os twice in the test file:

```go
import "os"

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o644) }
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /scratch4/yoe/yoe && go test ./internal/starlark/...` Expected: build
failure — `Query` field doesn't exist on `LocalOverrides`.

- [ ] **Step 3: Update local.go**

Replace `internal/starlark/local.go` with:

```go
package starlark

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.starlark.net/starlark"
)

const localStarFile = "local.star"

// LocalOverrides holds values loaded from <project-dir>/local.star.
// Empty fields mean the file did not specify that value (or did not exist).
type LocalOverrides struct {
	Machine    string
	DeployHost string // last-used target for `yoe deploy` from the TUI
	Query      string // last-saved TUI search query (in:base-image, etc.)
}

// LoadLocalOverrides reads <projectDir>/local.star if it exists and
// returns any overrides declared via local(...). Returns a zero-value
// struct (and nil error) when the file is absent.
func LoadLocalOverrides(projectDir string) (LocalOverrides, error) {
	path := filepath.Join(projectDir, localStarFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return LocalOverrides{}, nil
	} else if err != nil {
		return LocalOverrides{}, fmt.Errorf("stat %s: %w", path, err)
	}

	var captured LocalOverrides
	localFn := starlark.NewBuiltin("local", func(thread *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		for _, kv := range kwargs {
			key, ok := kv[0].(starlark.String)
			if !ok {
				continue
			}
			v, ok := kv[1].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("local: %s must be a string", string(key))
			}
			switch string(key) {
			case "machine":
				captured.Machine = string(v)
			case "deploy_host":
				captured.DeployHost = string(v)
			case "query":
				captured.Query = string(v)
			default:
				return nil, fmt.Errorf("local: unknown keyword %q", string(key))
			}
		}
		return starlark.None, nil
	})

	thread := &starlark.Thread{Name: "local"}
	predeclared := starlark.StringDict{"local": localFn}
	if _, err := starlark.ExecFile(thread, path, nil, predeclared); err != nil {
		return LocalOverrides{}, fmt.Errorf("evaluate %s: %w", path, err)
	}
	return captured, nil
}

// WriteLocalOverrides writes the given overrides to <projectDir>/local.star,
// overwriting the file. Always emits the standard auto-generated header.
// Empty fields are omitted so the file stays small and explicit.
func WriteLocalOverrides(projectDir string, ov LocalOverrides) error {
	path := filepath.Join(projectDir, localStarFile)
	var b strings.Builder
	b.WriteString("# local.star — generated by yoe; safe to delete or hand-edit.\n")
	b.WriteString("# Per-developer overrides for this project. Not checked in.\n\n")
	b.WriteString("local(\n")
	if ov.Machine != "" {
		fmt.Fprintf(&b, "    machine = %q,\n", ov.Machine)
	}
	if ov.DeployHost != "" {
		fmt.Fprintf(&b, "    deploy_host = %q,\n", ov.DeployHost)
	}
	if ov.Query != "" {
		fmt.Fprintf(&b, "    query = %q,\n", ov.Query)
	}
	b.WriteString(")\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /scratch4/yoe/yoe && go test ./internal/starlark/...` Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/starlark/local.go internal/starlark/local_test.go
git commit -m "local.star: add Query field, simplify writer"
```

---

### Task 6: TUI state migration — replace `searchText`/`filtered` with the query model

**Files:**

- Modify: `internal/tui/app.go`

The TUI keeps the visible-list rendering identical for now (Task 7 wires the
query bar to actually update the query). This task is purely a refactor: state
moves from `searchText string` + `filtered []int` + `searching bool` to
`query query.Query` + `queryInput string` + `queryEditing bool` +
`inSet map[string]bool` + `visible []int` + `queryError string`. It also fixes
the existing bug at `app.go:865` where switching machines wipes `DeployHost` by
load+merge+writing instead of constructing a fresh struct.

Behavior at the end of this task: list works exactly as before but driven by
`query.Query.Matches`. Nothing user-visible changes.

- [ ] **Step 1: Add the new state fields and import the query package**

In `internal/tui/app.go`:

1. Add the import:

```go
import (
    // ... existing imports ...
    "github.com/yoebuild/yoe/internal/tui/query"
)
```

2. In `type model struct`, replace the search-related block:

```go
// REMOVE:
//   searching  bool
//   searchText string
//   filtered   []int

// ADD (group near the other list-related state):
query        query.Query  // active query, applied to m.units to produce visible
queryInput   string       // text in the query bar; live-parsed every keystroke
queryEditing bool         // true while the user is typing in the query bar
queryError   string       // last parse error; rendered next to the query bar
inSet        map[string]bool // pre-computed in:X closure for the active query, nil if no in: filter
visible      []int        // indexes into m.units after applying m.query
```

3. Below the new fields, add the state status `unitStatus → string` helper
   somewhere convenient (e.g. just after `type unitStatus int`):

```go
// statusKey maps the TUI's enum to the lowercase strings the query
// language exposes via `status:`.
func statusKey(s unitStatus) string {
    switch s {
    case statusCached:
        return "cached"
    case statusBuilding:
        return "building"
    case statusWaiting:
        return "pending"
    case statusFailed:
        return "failed"
    default:
        return ""
    }
}
```

- [ ] **Step 2: Add a single helper that recomputes `inSet` and `visible` from
      the current query**

In `internal/tui/app.go`, add a method on `*model`:

```go
// applyQuery refreshes m.inSet and m.visible after m.query changes.
// The cursor is moved to the first visible row when it falls outside
// the new visible set; otherwise it is left alone (so live filtering
// while typing doesn't yank the cursor).
func (m *model) applyQuery() {
	m.inSet = nil
	if root := m.query.InRoot(); root != "" {
		m.inSet = query.BuildInClosure(m.proj, root)
	}
	m.visible = m.visible[:0]
	for i, name := range m.units {
		u := m.proj.Units[name]
		if m.query.Matches(name, u, statusKey(m.statuses[name]), m.inSet) {
			m.visible = append(m.visible, i)
		}
	}
	// Keep cursor on a visible row if at all possible.
	if len(m.visible) > 0 {
		stillVisible := false
		for _, i := range m.visible {
			if i == m.cursor {
				stillVisible = true
				break
			}
		}
		if !stillVisible {
			m.cursor = m.visible[0]
		}
	}
	m.listOffset = 0
	m.adjustListOffset()
}
```

Add `InRoot()` to `internal/tui/query/parser.go` (and a tiny test in
`parser_test.go`):

```go
// InRoot returns the value of the in: filter, or "" when no in: filter
// is set or multiple in: filters were provided (which the matcher
// already treats as "no match"; in that case the closure is irrelevant).
func (q Query) InRoot() string {
	vs := q.fields["in"]
	if len(vs) != 1 {
		return ""
	}
	return vs[0]
}
```

```go
// internal/tui/query/parser_test.go — append:
func TestQuery_InRoot(t *testing.T) {
	q := mustParse(t, "in:base-image foo")
	if q.InRoot() != "base-image" {
		t.Fatalf("InRoot: got %q", q.InRoot())
	}
	q2 := mustParse(t, "module:units-core")
	if q2.InRoot() != "" {
		t.Fatalf("expected empty InRoot")
	}
}
```

- [ ] **Step 3: Replace every read of `m.searchText` / `m.filtered` /
      `m.searching` / `m.applyFilter()`**

Search-replace every occurrence in `app.go`. Mapping:

| Old                                    | New                                                                             |
| -------------------------------------- | ------------------------------------------------------------------------------- |
| `m.searching`                          | `m.queryEditing`                                                                |
| `m.searching = true`                   | `m.queryEditing = true`                                                         |
| `m.searching = false`                  | `m.queryEditing = false`                                                        |
| `m.searchText` (read)                  | `m.queryInput`                                                                  |
| `m.searchText = ""`                    | `m.queryInput = ""; m.queryError = ""; m.query = query.Query{}; m.applyQuery()` |
| `m.searchText += key`                  | (handled in Task 7)                                                             |
| `m.filtered != nil` (truthiness check) | always true now — drop the check                                                |
| `m.filtered` (the index slice)         | `m.visible`                                                                     |
| `m.applyFilter()`                      | `m.applyQuery()`                                                                |
| `m.visibleIndices()`                   | replace body to `return m.visible`                                              |

Delete the now-unused `applyFilter` method. Delete `visibleIndices`'s old body
and replace with `return m.visible`. Update `viewUnits`'s "visible := …" loop to
use `m.visible` directly (no more rebuild).

After the edits, in `Run` at the bottom of model construction, append:

```go
m.applyQuery()
```

so the initial `m.visible` is populated before the first render.

- [ ] **Step 4: Fix the machine-switch bug while we're here**

At `app.go:865`-ish, the current code constructs a fresh
`LocalOverrides{Machine: picked}` and writes it, wiping any existing
`DeployHost` (and now `Query`). Replace with:

```go
ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
ov.Machine = picked
if err := yoestar.WriteLocalOverrides(m.projectDir, ov); err != nil {
    m.message = fmt.Sprintf("Machine set to %s (warning: failed to save local.star: %v)", picked, err)
} else {
    m.message = fmt.Sprintf("Machine set to %s (saved to local.star)", picked)
}
```

- [ ] **Step 5: Run the build and existing tests**

Run:
`cd /scratch4/yoe/yoe && go build ./... && go test ./cmd/... ./internal/...`
Expected: build clean. Tests PASS, including a still-untouched manual TUI
behavior (no automated coverage exists today).

- [ ] **Step 6: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/app.go internal/tui/query/parser.go internal/tui/query/parser_test.go
git commit -m "tui: route list filtering through query.Query (no behavior change)"
```

---

### Task 7: Live filtering in the query bar

**Files:**

- Modify: `internal/tui/app.go`

Wire the query bar to live-parse on every keystroke. Bootstrap from
`local.star.query` (preferred) or `in:<defaults.image>` (fallback) at TUI start.
`Esc` reverts to whatever was active before opening the bar; `Enter` closes the
bar keeping the typed query active. Parse errors don't change the visible list —
the last valid query stays in effect.

- [ ] **Step 1: Add bootstrap-query computation in `Run`**

After `m := model{...}` and before `m.applyQuery()` (added in Task 6), insert:

```go
// Bootstrap query: prefer local.star, fall back to in:<defaults.image>.
ov, _ := yoestar.LoadLocalOverrides(projectDir)
bootstrap := ov.Query
if bootstrap == "" && proj.Defaults.Image != "" {
    bootstrap = "in:" + proj.Defaults.Image
}
if q, err := query.Parse(bootstrap); err == nil {
    m.query = q
    m.queryInput = q.String()
}
m.savedQuery = m.query.String() // see Step 3 — add field too
```

Add the field on the model struct:

```go
savedQuery string // canonical form of the last user-saved query (or bootstrap)
```

- [ ] **Step 2: Replace `updateSearch`**

```go
// internal/tui/app.go
func (m model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Revert to whatever query was active when the bar opened.
		// queryEditing is set to false; the prior query is what we
		// snapshot in `/`'s handler (see step 4).
		m.queryEditing = false
		m.query = m.queryRevertTo
		m.queryInput = m.query.String()
		m.queryError = ""
		m.applyQuery()
		return m, nil

	case "enter":
		m.queryEditing = false
		// Keep current query active. If parse error, fall back to last
		// valid (already in m.query); clear the error.
		m.queryError = ""
		return m, nil

	case "backspace":
		if len(m.queryInput) > 0 {
			m.queryInput = m.queryInput[:len(m.queryInput)-1]
			m.reparse()
		}
		return m, nil

	default:
		key := msg.String()
		if len(key) == 1 && key[0] >= 32 && key[0] <= 126 {
			m.queryInput += key
			m.reparse()
		}
		return m, nil
	}
}

// reparse re-parses m.queryInput; on success updates m.query and
// re-applies. On failure keeps m.query and m.visible at their last-valid
// values and stores the error message for the bar.
func (m *model) reparse() {
	q, err := query.Parse(m.queryInput)
	if err != nil {
		m.queryError = err.Error()
		return
	}
	m.queryError = ""
	m.query = q
	m.applyQuery()
}
```

Add the field on the model struct so Esc has something to revert to:

```go
queryRevertTo query.Query // snapshot taken when the user opens `/`
```

- [ ] **Step 3: Update the `/` handler**

```go
case "/":
    m.queryEditing = true
    m.queryRevertTo = m.query
    m.queryInput = m.query.String() // start the bar prefilled with the active query
    return m, nil
```

(Replaces the previous
`m.searching = true; m.searchText = ""; m.filtered = nil`.)

- [ ] **Step 4: Manual smoke test**

Run: `cd /scratch4/yoe/yoe && CGO_ENABLED=0 go build -o yoe ./cmd/yoe` Then in
`testdata/e2e-project/`:

```bash
cd testdata/e2e-project && /scratch4/yoe/yoe/yoe --allow-duplicate-provides
```

Expected: TUI opens, header still has the original Machine/Image lines, list is
filtered to `in:base-image`. Press `/`, type `type:image`, list shrinks live to
image units. `Esc` reverts. `Enter` keeps it. Press `q` to quit.

Document the behavior briefly in this step's notes if anything looks off, then
move on.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/app.go
git commit -m "tui: live query parsing in the search bar with bootstrap from local.star"
```

---

### Task 8: Header line — `Query: …  Units: N/M`

**Files:**

- Modify: `internal/tui/app.go`

Add a dedicated header row that shows the active query and the visible/total
count. Dim when the active query equals the saved default; bright otherwise.
While editing, the header still shows the parsed-active query (the bar shows
what's being typed); when the parse fails, the bar shows the error in red and
the header stays at last-valid.

- [ ] **Step 1: Add styles if not already declared**

In `internal/tui/app.go` near the other lipgloss styles:

```go
var (
    queryDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
    queryActiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
    queryErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)
```

(Reuse existing styles if equivalent ones already exist.)

- [ ] **Step 2: Render the header row inside `viewUnits`**

After the existing Machine/Image header line (and the warning/notification/feed
banners), and before the column header, add:

```go
// Query header
qStr := m.query.String()
qLabel := "Query: "
qBody := qStr
if qBody == "" {
    qBody = "(empty — showing all)"
}
style := queryDimStyle
if qStr != m.savedQuery {
    style = queryActiveStyle
}
counter := fmt.Sprintf("Units: %d/%d", len(m.visible), len(m.units))
b.WriteString(fmt.Sprintf("  %s%s    %s\n",
    queryDimStyle.Render(qLabel),
    style.Render(qBody),
    queryDimStyle.Render(counter)))
```

- [ ] **Step 3: Render the bar with live error styling**

Replace the existing search-bar block:

```go
if m.queryEditing {
    if m.queryError != "" {
        b.WriteString(fmt.Sprintf("  /%s    %s",
            m.queryInput,
            queryErrorStyle.Render(m.queryError)))
    } else {
        b.WriteString(fmt.Sprintf("  /%s▌", m.queryInput))
    }
} else {
    // existing help bar — unchanged
    ...
}
```

- [ ] **Step 4: Run go build + manual smoke**

Run: `cd /scratch4/yoe/yoe && CGO_ENABLED=0 go build -o yoe ./cmd/yoe` Visual
check in `testdata/e2e-project`: header now shows
`Query: in:base-image    Units: 87/3104` (or similar). Type `type:image` —
counter updates each keystroke. Type `fizz:foo` — bar shows red error, list
freezes. Backspace until valid — bar clears red, list updates.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/app.go
git commit -m "tui: header line with active query and Units N/M counter"
```

---

### Task 9: Tab completion — fields, values, bare-term suggestions

**Files:**

- Create: `internal/tui/query/complete.go`
- Create: `internal/tui/query/complete_test.go`
- Modify: `internal/tui/app.go`

The completion API is a single function:

```go
// Complete returns candidate completions for the token under `cursor`
// in `input`. start/end describe the byte range of the token to be
// replaced. The returned slice is empty when there is nothing to
// complete. Candidates are returned in deterministic order (sorted by
// string).
func Complete(input string, cursor int, ctx Context) (start, end int, candidates []string)
```

`Context` carries the live data the completer needs:

```go
type Context struct {
    Modules []string // every loaded module name (for module:)
    Units   []string // every unit name (for in: and bare-term)
}
```

Static field-name and value lists are baked into `complete.go`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/tui/query/complete_test.go
package query

import (
	"reflect"
	"testing"
)

func ctxFixture() Context {
	return Context{
		Modules: []string{"units-core", "units-alpine", "units-rpi"},
		Units:   []string{"openssl", "openssh", "musl", "base-image"},
	}
}

func TestComplete_FieldName(t *testing.T) {
	start, end, got := Complete("mo", 2, ctxFixture())
	if start != 0 || end != 2 {
		t.Fatalf("span: got [%d,%d)", start, end)
	}
	if !reflect.DeepEqual(got, []string{"module:"}) {
		t.Fatalf("candidates: %v", got)
	}
}

func TestComplete_ViewShortcut(t *testing.T) {
	_, _, got := Complete("im", 2, ctxFixture())
	// "images" is a view shortcut, "in" is a field name. Both completable.
	want := []string{"images"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplete_TypeValue(t *testing.T) {
	_, _, got := Complete("type:i", 6, ctxFixture())
	if !reflect.DeepEqual(got, []string{"image"}) {
		t.Fatalf("got %v", got)
	}
}

func TestComplete_StatusValueAll(t *testing.T) {
	_, _, got := Complete("status:", 7, ctxFixture())
	want := []string{"building", "cached", "failed", "pending", "stale"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplete_ModuleValue(t *testing.T) {
	_, _, got := Complete("module:units-r", 14, ctxFixture())
	if !reflect.DeepEqual(got, []string{"units-rpi"}) {
		t.Fatalf("got %v", got)
	}
}

func TestComplete_InValuePrefix(t *testing.T) {
	_, _, got := Complete("in:open", 7, ctxFixture())
	want := []string{"openssh", "openssl"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplete_BareTerm(t *testing.T) {
	// Bare term completes from unit names whose name CONTAINS the
	// partial token (substring), matching the bare-term filter's
	// semantics.
	_, _, got := Complete("ss", 2, ctxFixture())
	want := []string{"openssh", "openssl"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplete_SecondToken(t *testing.T) {
	// Cursor at the end of the second token; first token is unaffected.
	input := "in:base-image type:i"
	start, end, got := Complete(input, len(input), ctxFixture())
	if start != 14 || end != len(input) {
		t.Fatalf("span: [%d,%d) want [14,%d)", start, end, len(input))
	}
	if !reflect.DeepEqual(got, []string{"image"}) {
		t.Fatalf("got %v", got)
	}
}

func TestComplete_NoCandidates(t *testing.T) {
	_, _, got := Complete("xyzzy", 5, ctxFixture())
	if got != nil {
		t.Fatalf("expected no candidates, got %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected: build
failure (`Complete` undefined).

- [ ] **Step 3: Implement `Complete`**

```go
// internal/tui/query/complete.go
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
		// Replace just the value portion, not the whole token, so the
		// caller can splice in a single value.
		valueStart := start + i + 1
		switch field {
		case "type":
			return valueStart, end, prefixMatch(typeValues, val)
		case "status":
			return valueStart, end, prefixMatch(statusValues, val)
		case "module":
			return valueStart, end, prefixMatch(ctx.Modules, val)
		case "in":
			return valueStart, end, prefixMatch(ctx.Units, val)
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /scratch4/yoe/yoe && go test ./internal/tui/query/...` Expected: all
complete tests PASS.

- [ ] **Step 5: Wire Tab into the bar (TUI side)**

In `internal/tui/app.go` `updateSearch`, add:

```go
case "tab":
    ctx := query.Context{
        Modules: m.moduleNames(),
        Units:   m.units, // already sorted
    }
    start, end, cands := query.Complete(m.queryInput, len(m.queryInput), ctx)
    switch len(cands) {
    case 0:
        // nothing to do
    case 1:
        // splice in the single candidate
        m.queryInput = m.queryInput[:start] + cands[0] + m.queryInput[end:]
        m.reparse()
    default:
        // longest common prefix
        lcp := longestCommonPrefix(cands)
        if lcp != "" && lcp != m.queryInput[start:end] {
            m.queryInput = m.queryInput[:start] + lcp + m.queryInput[end:]
            m.reparse()
        }
        // else: leave as-is. The "second tab shows ghost line" is
        // deferred to a follow-up; v1 ships a single-tab completion,
        // which already does the heavy lifting.
    }
    return m, nil
```

Helper additions:

```go
// moduleNames returns the sorted set of module names in the project,
// plus the synthetic "project" name used for project-root units.
func (m model) moduleNames() []string {
    set := map[string]bool{"project": true}
    for _, u := range m.proj.Units {
        if u.Module != "" {
            set[u.Module] = true
        }
    }
    out := make([]string, 0, len(set))
    for k := range set {
        out = append(out, k)
    }
    sort.Strings(out)
    return out
}

func longestCommonPrefix(ss []string) string {
    if len(ss) == 0 {
        return ""
    }
    p := ss[0]
    for _, s := range ss[1:] {
        for !strings.HasPrefix(s, p) {
            if p == "" {
                return ""
            }
            p = p[:len(p)-1]
        }
    }
    return p
}
```

(`sort` may need importing; `strings` already is.)

The deferred "second-Tab shows ghost line" UX is a polish item — call it out in
the project-level CHANGELOG entry only; do not block v1 on it. Single-Tab
completion (the case where there is exactly one candidate, or a shared prefix
exists) is already the high-leverage half of the feature.

- [ ] **Step 6: Run build + tests**

Run:
`cd /scratch4/yoe/yoe && go build ./... && go test ./cmd/... ./internal/...`
Expected: PASS.

- [ ] **Step 7: Manual smoke**

```bash
cd /scratch4/yoe/yoe && CGO_ENABLED=0 go build -o yoe ./cmd/yoe
cd testdata/e2e-project && /scratch4/yoe/yoe/yoe --allow-duplicate-provides
```

Expected: in the TUI, press `/`, type `mo`, press `Tab` → bar reads `module:`.
Type `unit`, press `Tab` → completes longest common prefix `units-`. Type `c`
then `Tab` → `units-core`. List filters live.

- [ ] **Step 8: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/query/complete.go internal/tui/query/complete_test.go internal/tui/app.go
git commit -m "tui/query: tab completion for fields, values, and bare terms"
```

---

### Task 10: Snap-back (`\`) and save-query (`S`) keys

**Files:**

- Modify: `internal/tui/app.go`

Both keys only fire when the bar is NOT open (normal navigation mode).

- [ ] **Step 1: Add the `\` and `S` cases in `updateUnits`**

In the `switch msg.String()` of the unit-list view's keymap:

```go
case "\\":
    // Snap-back: revert active query to whatever is saved as the default.
    bootstrap, _ := query.Parse(m.savedQuery) // savedQuery is canonical, parse must succeed
    m.query = bootstrap
    m.queryInput = m.query.String()
    m.queryError = ""
    m.applyQuery()
    return m, nil

case "S":
    // Save the current active query to local.star as the new default.
    ov, _ := yoestar.LoadLocalOverrides(m.projectDir)
    ov.Query = m.query.String()
    if err := yoestar.WriteLocalOverrides(m.projectDir, ov); err != nil {
        m.message = fmt.Sprintf("save query failed: %v", err)
        return m, nil
    }
    m.savedQuery = m.query.String()
    if m.query.IsEmpty() {
        m.message = "saved empty query (will show all units next session)"
    } else {
        m.message = fmt.Sprintf("saved query: %s", m.query.String())
    }
    return m, nil
```

- [ ] **Step 2: Update the help bar text in `viewUnits`**

Find the existing help string near the bottom of `viewUnits` and add
`\\ snap-back  S save query`. Example:

```go
help := "  b build  D deploy  x cancel  e edit  d diagnose  l log  c clean  s setup  / search  \\ home  S save  q quit"
```

(Pick wording that fits the available width; if too long, dropping `c clean` and
`d diagnose` is cheaper than dropping the new keys.)

- [ ] **Step 3: Run build + tests**

Run:
`cd /scratch4/yoe/yoe && go build ./... && go test ./cmd/... ./internal/...`
Expected: PASS.

- [ ] **Step 4: Manual smoke**

In the TUI: type `/type:image Enter`. Header should say `Query: type:image`,
bright. Press `\` — header reverts to `Query: in:base-image`, dim. Press
`/openssl Enter`, then `S` — message bar shows `saved query: openssl`. Quit, run
again — TUI bootstraps to `Query: openssl`.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/app.go
git commit -m "tui: snap-back (\\) and save-query (S) key bindings"
```

---

### Task 11: Empty-result rendering and substring highlighting

**Files:**

- Modify: `internal/tui/app.go`

Two small UX polish items the spec calls out: render `no units match` when the
visible set is empty, and highlight bare-term substrings in the unit name
column.

- [ ] **Step 1: Add the empty-list line in `viewUnits`**

After the loop that emits each row, before the help bar:

```go
if len(m.visible) == 0 {
    b.WriteString(dimStyle.Render("  no units match\n"))
}
```

- [ ] **Step 2: Highlight bare-term matches in the name column**

In `viewUnits`, where each row's `paddedName` is rendered, replace:

```go
nameStyle.Render(paddedName)
```

with:

```go
m.renderName(paddedName, name, nameStyle)
```

Add the helper on `model`:

```go
// matchHighlightStyle draws the matched substring on top of whatever
// the row's existing color is.
var matchHighlightStyle = lipgloss.NewStyle().Underline(true).Bold(true)

// renderName styles `padded` with `base` and underlines/bolds any
// substring that matches a bare term in the active query.
func (m model) renderName(padded, raw string, base lipgloss.Style) string {
    terms := m.query.BareTerms()
    if len(terms) == 0 {
        return base.Render(padded)
    }
    // Lowercase a parallel string for case-insensitive matching.
    lower := strings.ToLower(padded)
    var b strings.Builder
    i := 0
    for i < len(padded) {
        // Find the earliest match of any term starting at i.
        nextEnd := -1
        for _, t := range terms {
            if t == "" {
                continue
            }
            if idx := strings.Index(lower[i:], t); idx >= 0 {
                end := i + idx + len(t)
                start := i + idx
                if nextEnd == -1 || start < nextEnd-len(t) {
                    // Prefer earliest start.
                    b.WriteString(base.Render(padded[i:start]))
                    b.WriteString(base.Inherit(matchHighlightStyle).Render(padded[start:end]))
                    i = end
                    nextEnd = end
                    break
                }
            }
        }
        if nextEnd == -1 {
            b.WriteString(base.Render(padded[i:]))
            break
        }
    }
    return b.String()
}
```

Add `BareTerms` on `Query`:

```go
// internal/tui/query/parser.go — append:
// BareTerms returns the parsed bare substring terms. Each is already
// lowercased by Parse; callers should compare against a lowercased
// haystack.
func (q Query) BareTerms() []string {
    return q.bareTerms
}
```

And a small test:

```go
// internal/tui/query/parser_test.go — append:
func TestQuery_BareTerms(t *testing.T) {
    q := mustParse(t, "type:image foo bar")
    got := q.BareTerms()
    want := []string{"foo", "bar"}
    if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
        t.Fatalf("BareTerms: got %v want %v", got, want)
    }
}
```

- [ ] **Step 3: Run build + tests**

Run:
`cd /scratch4/yoe/yoe && go build ./... && go test ./cmd/... ./internal/...`
Expected: PASS.

- [ ] **Step 4: Manual smoke**

Type `/type:gizmo Enter` → list shows `no units match`. Type `/openssl` →
`openssl` and any other names containing "ssl" stay; the matched substring is
underlined+bold.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add internal/tui/app.go internal/tui/query/parser.go internal/tui/query/parser_test.go
git commit -m "tui: empty-result placeholder and bare-term highlighting"
```

---

### Task 12: CHANGELOG and end-to-end verification

**Files:**

- Modify: `CHANGELOG.md`

Document the user-visible behavior change. Per CLAUDE.md, write for users of
`yoe` — what they see, what they can now do — and lead with the benefit.

- [ ] **Step 1: Add the CHANGELOG entry**

Insert at the top of the `## [Unreleased]` block in
`/scratch4/yoe/yoe/CHANGELOG.md`:

```markdown
- **TUI search is now a query language; defaults to your image's working set.**
  Press `/` to filter by `type:`, `module:`, `status:`, or `in:` (closure of any
  unit), in addition to plain substring search. `Tab` completes field names and
  values. The TUI starts filtered to `in:<your-default-image>`, so a project
  with thousands of units shows just what your image needs. Press `S` to save
  the current query to `local.star` as the new default; press `\` to snap back
  to it. `Units: N/M` in the header tells you how many of the project's units
  the current query is showing.
```

- [ ] **Step 2: Run prettier**

```bash
cd /scratch4/yoe/yoe && (command -v prettier >/dev/null && prettier --write CHANGELOG.md || true)
```

- [ ] **Step 3: Final test sweep**

Run:

```bash
cd /scratch4/yoe/yoe && go test ./cmd/... ./internal/...
```

Expected: all PASS.

- [ ] **Step 4: Manual end-to-end check**

```bash
cd /scratch4/yoe/yoe/testdata/e2e-project && /scratch4/yoe/yoe/yoe --allow-duplicate-provides
```

Walk through the spec's worked-examples table:

- Default view shows units in `base-image`'s closure (header dim).
- `/in:openssl Enter` → openssl + zlib + toolchain-musl.
- `/module:units-alpine Enter` → ~3000 entries; counter says e.g.
  `Units: 2987/3104`.
- `/type:image Enter` → just images.
- `/fizz:foo` → bar turns red, list freezes.
- `/type:gizmo Enter` → `no units match`.
- `/openssl Enter` → "ssl" highlighted in matching rows.
- `\` → reverts to default. `S` → saves new default. Quit, restart, default is
  the saved one.

- [ ] **Step 5: Commit**

```bash
cd /scratch4/yoe/yoe && git add CHANGELOG.md
git commit -m "changelog: TUI query language and image-scoped default view"
```

---

## Self-Review

Walked the spec section by section against the plan:

| Spec section                                                                                                      | Covered by                                                              |
| ----------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| Grammar (terms, AND/OR rules, case-insensitivity, view shortcuts, error rules)                                    | Tasks 1-2 (parser + tests)                                              |
| v1 fields (`type`, `module`, `status`, `in`)                                                                      | Tasks 2 (parser), 3 (matcher), 4 (closure)                              |
| `in:` closure semantics (build-deps + runtime-deps via Provides + image artifacts)                                | Task 4                                                                  |
| `local.star.query` schema + bootstrap order                                                                       | Tasks 5, 7                                                              |
| Header (Query: + Units: N/M)                                                                                      | Task 8                                                                  |
| Live filtering                                                                                                    | Task 7                                                                  |
| Tab completion                                                                                                    | Task 9                                                                  |
| Snap-back `\`                                                                                                     | Task 10                                                                 |
| Save-query `S`                                                                                                    | Task 10                                                                 |
| Persistence triggers (only on `S` and machine/deploy_host change)                                                 | Tasks 6 (machine bug fix), 10 (S handler), pre-existing for deploy_host |
| Failure modes (parse error, unknown field, unknown value, in:nonexistent, empty result, missing-unit saved query) | Task 7 (parse error), Task 11 (empty result), parser+matcher (the rest) |
| Highlighting on bare terms                                                                                        | Task 11                                                                 |
| Tests listed in spec (parser, match, closure, complete, local)                                                    | Tasks 1-5, 9                                                            |

Placeholder scan: every step contains exact code or commands. No "implement
appropriately" / "handle edge cases" entries.

Type consistency check: `Query` and `Context` types defined in Tasks 1/9 match
every consumer (`Matches` in Task 3, `Complete` in Task 9, TUI integration in
Tasks 6-11). `LocalOverrides.Query string` declared in Task 5 matches the read
in Task 7's bootstrap and the write in Task 10's save handler.
`statusKey(unitStatus) string` declared in Task 6 and consumed in Task 6's
`applyQuery` and Task 7 indirectly via `applyQuery`. `query.BuildInClosure`
signature in Task 4 matches the `applyQuery` call site in Task 6.

Deferred (per spec) but reserved by name: `dep-of:`, `class:`, `arch:`,
`dirty:`, `since:`. Not implemented; no task references them.
