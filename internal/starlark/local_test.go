package starlark

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalOverrides_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := LocalOverrides{
		Machine:    "qemu-x86_64",
		Image:      "dev-image",
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

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o644) }
