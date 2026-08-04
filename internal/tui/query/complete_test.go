package query

import (
	"reflect"
	"testing"
)

func ctxFixture() Context {
	return Context{
		Modules: []string{"module-core", "module-alpine", "module-rpi"},
		Units:   []string{"openssl", "openssh", "musl", "base-image"},
	}
}

func TestComplete_FieldName(t *testing.T) {
	start, end, got := Complete("mo", ctxFixture())
	if start != 0 || end != 2 {
		t.Fatalf("span: got [%d,%d)", start, end)
	}
	if !reflect.DeepEqual(got, []string{"module:"}) {
		t.Fatalf("candidates: %v", got)
	}
}

func TestComplete_ViewShortcut(t *testing.T) {
	_, _, got := Complete("im", ctxFixture())
	// Only "images" matches — "in:" doesn't share the "im" prefix.
	want := []string{"images"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplete_EmptyInput(t *testing.T) {
	// Tab on an empty bar suggests every field name and shortcut.
	// LCP across them is "", so the TUI splice path is a no-op — but
	// the candidates are still returned so a future ghost-line UI can
	// render them.
	_, _, got := Complete("", ctxFixture())
	want := []string{"building", "containers", "failed", "images", "in:", "module:", "status:", "type:"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplete_TypeValue(t *testing.T) {
	_, _, got := Complete("type:i", ctxFixture())
	if !reflect.DeepEqual(got, []string{"image"}) {
		t.Fatalf("got %v", got)
	}
}

func TestComplete_StatusValueAll(t *testing.T) {
	_, _, got := Complete("status:", ctxFixture())
	want := []string{"building", "cached", "failed", "pending"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplete_ModuleValue(t *testing.T) {
	_, _, got := Complete("module:module-r", ctxFixture())
	if !reflect.DeepEqual(got, []string{"module-rpi"}) {
		t.Fatalf("got %v", got)
	}
}

func TestComplete_InValuePrefix(t *testing.T) {
	_, _, got := Complete("in:open", ctxFixture())
	want := []string{"openssh", "openssl"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplete_BareTerm(t *testing.T) {
	// Bare term completes from unit names whose name CONTAINS the
	// partial token (substring), matching the bare-term filter's
	// semantics.
	_, _, got := Complete("ss", ctxFixture())
	want := []string{"openssh", "openssl"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplete_SecondToken(t *testing.T) {
	// Cursor at the end of the second token; first token is unaffected.
	input := "in:base-image type:i"
	start, end, got := Complete(input, ctxFixture())
	if start != 14 || end != len(input) {
		t.Fatalf("span: [%d,%d) want [14,%d)", start, end, len(input))
	}
	if !reflect.DeepEqual(got, []string{"image"}) {
		t.Fatalf("got %v", got)
	}
}

func TestComplete_NoCandidates(t *testing.T) {
	_, _, got := Complete("xyzzy", ctxFixture())
	if got != nil {
		t.Fatalf("expected no candidates, got %v", got)
	}
}
