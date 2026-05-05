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
