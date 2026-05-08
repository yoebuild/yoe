package module

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yoebuild/yoe/internal/source"
)

func TestModuleState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteState(dir, source.StateDev); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	got := ReadState(dir)
	if got != source.StateDev {
		t.Errorf("got %q, want %q", got, source.StateDev)
	}
}

func TestModuleState_MissingFile(t *testing.T) {
	dir := t.TempDir()
	got := ReadState(dir)
	if got != source.StateEmpty {
		t.Errorf("got %q, want %q (missing file should return empty)", got, source.StateEmpty)
	}
}

func TestModuleState_ModuleDirGone(t *testing.T) {
	got := ReadState(filepath.Join(t.TempDir(), "does-not-exist"))
	if got != source.StateEmpty {
		t.Errorf("got %q, want %q", got, source.StateEmpty)
	}
}

func TestModuleState_Corrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(StatePath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadState(dir)
	if got != source.StateEmpty {
		t.Errorf("got %q, want %q (corrupt file should return empty, not panic)", got, source.StateEmpty)
	}
}

// TestModuleState_WriteEmptyRemovesFile confirms that writing
// StateEmpty deletes the state file rather than leaving an
// empty-state placeholder.
func TestModuleState_WriteEmptyRemovesFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteState(dir, source.StateDev); err != nil {
		t.Fatalf("WriteState dev: %v", err)
	}
	if _, err := os.Stat(StatePath(dir)); err != nil {
		t.Fatalf("state file should exist after dev write: %v", err)
	}
	if err := WriteState(dir, source.StateEmpty); err != nil {
		t.Fatalf("WriteState empty: %v", err)
	}
	if _, err := os.Stat(StatePath(dir)); !os.IsNotExist(err) {
		t.Errorf("state file should be removed after empty write, got err=%v", err)
	}
}

// TestModuleState_WriteEmptyOnEmptyDir is a no-op success case.
func TestModuleState_WriteEmptyOnEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := WriteState(dir, source.StateEmpty); err != nil {
		t.Errorf("WriteState empty on empty dir should succeed, got: %v", err)
	}
}
