package starlark

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestStar(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRewriteUnitField_BareUnit(t *testing.T) {
	dir := t.TempDir()
	path := writeTestStar(t, dir, "foo.star", `unit(
    name = "foo",
    version = "1.0.0",
    tag = "v1.0.0",
    source = "https://example.com/foo.git",
)
`)
	if err := RewriteUnitField(path, "foo", "tag", "v1.1.0"); err != nil {
		t.Fatalf("RewriteUnitField: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `tag = "v1.1.0"`) {
		t.Errorf("expected new tag, got:\n%s", got)
	}
	if strings.Contains(string(got), `tag = "v1.0.0"`) {
		t.Errorf("old tag still present:\n%s", got)
	}
}

func TestRewriteUnitField_AutotoolsCall(t *testing.T) {
	dir := t.TempDir()
	path := writeTestStar(t, dir, "openssl.star", `load("//classes/autotools.star", "autotools")

autotools(
    name = "openssl",
    version = "3.4.1",
    tag = "openssl-3.4.1",
    source = "https://github.com/openssl/openssl.git",
)
`)
	if err := RewriteUnitField(path, "openssl", "tag", "openssl-3.5.0"); err != nil {
		t.Fatalf("RewriteUnitField: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `tag = "openssl-3.5.0"`) {
		t.Errorf("expected new tag in autotools call, got:\n%s", got)
	}
}

func TestRewriteUnitField_PreservesTrailingComment(t *testing.T) {
	dir := t.TempDir()
	path := writeTestStar(t, dir, "foo.star", `unit(
    name = "foo",
    tag = "v1.0",  # bumped 2026-04-01
)
`)
	if err := RewriteUnitField(path, "foo", "tag", "v1.1"); err != nil {
		t.Fatalf("RewriteUnitField: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := `tag = "v1.1",  # bumped 2026-04-01`
	if !strings.Contains(string(got), want) {
		t.Errorf("comment not preserved:\nwant substr: %s\ngot:\n%s", want, got)
	}
}

func TestRewriteUnitField_PreservesIndent(t *testing.T) {
	dir := t.TempDir()
	// 8-space indent (some yoe files use it for nested calls).
	path := writeTestStar(t, dir, "foo.star", `unit(
        name = "foo",
        tag = "v1.0",
)
`)
	if err := RewriteUnitField(path, "foo", "tag", "v1.1"); err != nil {
		t.Fatalf("RewriteUnitField: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `        tag = "v1.1",`) {
		t.Errorf("indent not preserved:\n%s", got)
	}
}

func TestRewriteUnitField_InsertsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	// Unit defines branch but not tag — we promote to tag.
	path := writeTestStar(t, dir, "foo.star", `unit(
    name = "foo",
    branch = "main",
    source = "https://example.com/foo.git",
)
`)
	if err := RewriteUnitField(path, "foo", "tag", "v2.0"); err != nil {
		t.Fatalf("RewriteUnitField: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `tag = "v2.0"`) {
		t.Errorf("expected inserted tag, got:\n%s", got)
	}
	// Insertion should come right after name line.
	if !strings.Contains(string(got), "name = \"foo\",\n    tag = \"v2.0\",") {
		t.Errorf("tag not inserted right after name:\n%s", got)
	}
}

func TestRewriteUnitField_MultipleUnitsPicksRight(t *testing.T) {
	dir := t.TempDir()
	path := writeTestStar(t, dir, "multi.star", `unit(
    name = "first",
    tag = "v1.0",
)

unit(
    name = "second",
    tag = "v2.0",
)
`)
	if err := RewriteUnitField(path, "second", "tag", "v2.1"); err != nil {
		t.Fatalf("RewriteUnitField: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `tag = "v1.0"`) {
		t.Errorf("first unit's tag should be untouched:\n%s", got)
	}
	if !strings.Contains(string(got), `tag = "v2.1"`) {
		t.Errorf("second unit's tag should be updated:\n%s", got)
	}
	if strings.Contains(string(got), `tag = "v2.0"`) {
		t.Errorf("old second-unit tag still present:\n%s", got)
	}
}

func TestRewriteUnitField_UnitNotFound(t *testing.T) {
	dir := t.TempDir()
	path := writeTestStar(t, dir, "foo.star", `unit(name = "foo", tag = "v1")`)
	err := RewriteUnitField(path, "bar", "tag", "v2")
	if err == nil {
		t.Fatal("expected error for missing unit")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found: %v", err)
	}
	// File untouched.
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `tag = "v1"`) {
		t.Errorf("file modified despite error:\n%s", got)
	}
}

func TestRemoveUnitField_DropsLine(t *testing.T) {
	dir := t.TempDir()
	path := writeTestStar(t, dir, "foo.star", `unit(
    name = "foo",
    tag = "v1.0",
    branch = "main",
    source = "https://example.com/foo.git",
)
`)
	if err := RemoveUnitField(path, "foo", "branch"); err != nil {
		t.Fatalf("RemoveUnitField: %v", err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "branch") {
		t.Errorf("branch line should be removed:\n%s", got)
	}
	if !strings.Contains(string(got), `tag = "v1.0"`) {
		t.Errorf("tag line should be untouched:\n%s", got)
	}
	// No double blank line where branch used to be.
	if strings.Contains(string(got), "\n\n    source") {
		t.Errorf("double blank line left behind:\n%s", got)
	}
}

func TestRemoveUnitField_NoOpWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := writeTestStar(t, dir, "foo.star", `unit(
    name = "foo",
    tag = "v1.0",
)
`)
	original, _ := os.ReadFile(path)
	if err := RemoveUnitField(path, "foo", "branch"); err != nil {
		t.Fatalf("RemoveUnitField on missing field: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Errorf("file should be untouched:\n%s", got)
	}
}

func TestRewriteUnitField_AtomicOnError(t *testing.T) {
	dir := t.TempDir()
	path := writeTestStar(t, dir, "foo.star", `unit(name = "foo")`)
	// Trigger error: unit name doesn't match.
	_ = RewriteUnitField(path, "bar", "tag", "v1")
	// No leftover tmp files in the dir.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".yoe-edit-") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

// TestRewriteUnitField_ParsesAfterRewrite confirms that the rewriter
// produces a Starlark-loadable file. Round-trips through the engine.
func TestRewriteUnitField_ParsesAfterRewrite(t *testing.T) {
	dir := t.TempDir()
	path := writeTestStar(t, dir, "foo.star", `unit(
    name = "foo",
    version = "1.0.0",
    tag = "v1.0.0",
)
`)
	if err := RewriteUnitField(path, "foo", "tag", "v1.1.0"); err != nil {
		t.Fatalf("RewriteUnitField: %v", err)
	}
	eng := NewEngine()
	src, _ := os.ReadFile(path)
	if err := eng.ExecString(path, string(src)); err != nil {
		t.Fatalf("rewritten .star failed to parse: %v\n%s", err, src)
	}
	r := eng.Units()["foo"]
	if r == nil {
		t.Fatal("unit 'foo' not registered after parse")
	}
	if r.Tag != "v1.1.0" {
		t.Errorf("Tag = %q, want %q", r.Tag, "v1.1.0")
	}
}
