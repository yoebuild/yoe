package build

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildMeta_SourceStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := &BuildMeta{
		Status:      "complete",
		Hash:        "abc123",
		SourceState: "dev",
	}
	if err := WriteMeta(dir, in); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	out := ReadMeta(dir)
	if out == nil {
		t.Fatal("ReadMeta returned nil")
	}
	if out.SourceState != "dev" {
		t.Errorf("SourceState = %q, want %q", out.SourceState, "dev")
	}
}

// TestBuildMeta_OmitsEmptySourceState verifies the json tag's `omitempty`
// keeps existing meta files round-trippable — units that never touched
// dev mode shouldn't grow a "source_state": "" line in their build.json.
func TestBuildMeta_OmitsEmptySourceState(t *testing.T) {
	dir := t.TempDir()
	in := &BuildMeta{Status: "complete", Hash: "abc"}
	if err := WriteMeta(dir, in); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "build.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); contains(got, "source_state") {
		t.Errorf("build.json contains source_state field when empty: %s", got)
	}
}

// TestBuildMeta_ReadsLegacyFile verifies a build.json without the new
// field still parses cleanly with empty SourceState.
func TestBuildMeta_ReadsLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"status":"complete","hash":"abc","installed_bytes":42}`
	if err := os.WriteFile(filepath.Join(dir, "build.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	out := ReadMeta(dir)
	if out == nil {
		t.Fatal("ReadMeta returned nil for legacy file")
	}
	if out.Hash != "abc" || out.InstalledBytes != 42 {
		t.Errorf("legacy fields not parsed: %+v", out)
	}
	if out.SourceState != "" {
		t.Errorf("SourceState = %q, want empty", out.SourceState)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
