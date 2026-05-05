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
