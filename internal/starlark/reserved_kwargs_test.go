package starlark

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every kwarg that unitFields reads into a typed field must also be
// reserved, or it is captured into Unit.Extra as well — and Extra is
// hashed, so a purely presentational field silently becomes part of the
// content-addressed cache key. That drift happened once with
// artifacts_explicit, which every image sets.
//
// Deriving reservedUnitKwargs from unitFields is what makes this hold;
// this test pins that the derivation is complete, including the kwargs
// registerUnit reads outside the table.
func TestReservedUnitKwargs_CoversUnitFields(t *testing.T) {
	for _, f := range unitFields {
		if !reservedUnitKwargs[f.kwarg] {
			t.Errorf("unitFields reads %q into a typed field but it is not reserved", f.kwarg)
		}
	}
	for _, kw := range []string{"name", "unit_class"} {
		if !reservedUnitKwargs[kw] {
			t.Errorf("registerUnit reads %q directly but it is not reserved", kw)
		}
	}
}

// registerUnit must not grow a kwarg read outside unitFields without
// that name also being reserved — the table is the intended place to add
// one, and reading a kwarg directly bypasses the derivation above.
func TestRegisterUnit_ReadsOnlyReservedKwargs(t *testing.T) {
	src, err := os.ReadFile("builtins.go")
	if err != nil {
		t.Fatalf("read builtins.go: %v", err)
	}
	body := funcBody(t, string(src), "func (e *Engine) registerUnit(")

	re := regexp.MustCompile(`kw\w*\(kwargs, "([a-z0-9_]+)"\)`)
	var missing []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if !reservedUnitKwargs[m[1]] {
			missing = append(missing, m[1])
		}
	}
	if len(missing) > 0 {
		t.Errorf("registerUnit reads %v directly; add them to unitFields so they are reserved", missing)
	}
}

// Round-trip a unit through the builtin: a kwarg the table knows lands
// in its field and stays out of Extra, and an unknown one lands in Extra
// (which is what makes it available to templates, and hashed).
func TestUnitFields_TypedFieldsStayOutOfExtra(t *testing.T) {
	eng := NewEngine()
	err := eng.ExecString("units/hello.star", `
unit(
    name = "hello",
    version = "1.0",
    artifacts_explicit = ["base-files"],
    services = ["helloq"],
    my_own_thing = "for the template",
)
`)
	if err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	u := eng.Units()["hello"]
	if u == nil {
		t.Fatal("hello was not registered")
	}
	if len(u.ArtifactsExplicit) != 1 || u.ArtifactsExplicit[0] != "base-files" {
		t.Errorf("ArtifactsExplicit = %v", u.ArtifactsExplicit)
	}
	for _, kw := range []string{"artifacts_explicit", "services", "version"} {
		if _, ok := u.Extra[kw]; ok {
			t.Errorf("%q was captured into Extra as well as its typed field", kw)
		}
	}
	if got := u.Extra["my_own_thing"]; got != "for the template" {
		t.Errorf("Extra[my_own_thing] = %v, want the declared string", got)
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
