package build

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestInitBuildMeta_PreservesDevState is a regression test for a bug
// where the executor wrote a fresh BuildMeta on every build start,
// clobbering the SourceState the dev-mode toggle had written
// out-of-band. Once the field was empty, source.Prepare's dev guard
// no longer fired, and the next build wiped the user's dev-dirty src
// tree on top of itself.
func TestInitBuildMeta_PreservesDevState(t *testing.T) {
	dir := t.TempDir()
	// The toggle wrote dev state earlier (no Status, no Hash — just
	// the source-mode fields).
	if err := WriteMeta(dir, &BuildMeta{
		SourceState:    "dev-dirty",
		SourceDescribe: "v1.0-3-gabc1234-dirty",
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	// Executor starts a new build; initBuildMeta should carry the
	// source fields forward into the fresh "building" meta.
	got := initBuildMeta(dir, "newhash", time.Now())
	if got.Status != "building" {
		t.Errorf("Status = %q, want building", got.Status)
	}
	if got.Hash != "newhash" {
		t.Errorf("Hash = %q, want newhash", got.Hash)
	}
	if got.SourceState != "dev-dirty" {
		t.Errorf("SourceState lost across build start: got %q, want dev-dirty",
			got.SourceState)
	}
	if got.SourceDescribe != "v1.0-3-gabc1234-dirty" {
		t.Errorf("SourceDescribe lost: %q", got.SourceDescribe)
	}
}

// TestInitBuildMeta_NoPriorMeta returns a clean fresh meta when the
// unit has never been built before (the typical case).
func TestInitBuildMeta_NoPriorMeta(t *testing.T) {
	dir := t.TempDir()
	got := initBuildMeta(dir, "h", time.Now())
	if got.SourceState != "" {
		t.Errorf("SourceState = %q on never-built unit, want empty", got.SourceState)
	}
	if got.Hash != "h" || got.Status != "building" {
		t.Errorf("unexpected fresh meta: %+v", got)
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
