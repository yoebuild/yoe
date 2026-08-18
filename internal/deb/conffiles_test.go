package deb

import (
	"os"
	"path/filepath"
	"testing"
)

// stageConffile creates destDir/<rel> so WriteConffiles' installed-file
// check passes.
func stageConffile(t *testing.T, destDir, rel string) {
	t.Helper()
	full := filepath.Join(destDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("setting = 1\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestWriteConffiles(t *testing.T) {
	dest := t.TempDir()
	stageConffile(t, dest, "etc/ssh/sshd_config")
	stageConffile(t, dest, "etc/myapp/config.toml")

	// Declared out of order and with one path missing its leading slash;
	// output must be absolute and sorted so rebuilds are byte-identical.
	if err := WriteConffiles(dest, []string{"/etc/ssh/sshd_config", "etc/myapp/config.toml"}); err != nil {
		t.Fatalf("WriteConffiles: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "DEBIAN", "conffiles"))
	if err != nil {
		t.Fatalf("read conffiles: %v", err)
	}
	want := "/etc/myapp/config.toml\n/etc/ssh/sshd_config\n"
	if string(got) != want {
		t.Errorf("conffiles = %q, want %q", got, want)
	}
}

func TestWriteConffiles_Empty(t *testing.T) {
	dest := t.TempDir()
	if err := WriteConffiles(dest, nil); err != nil {
		t.Fatalf("WriteConffiles(nil): %v", err)
	}
	// No conffiles declared means no DEBIAN/ directory is created just to
	// hold an empty file.
	if _, err := os.Stat(filepath.Join(dest, "DEBIAN", "conffiles")); !os.IsNotExist(err) {
		t.Errorf("expected no conffiles file, got err=%v", err)
	}
}

// A declared path the unit doesn't actually install would make dpkg track
// a conffile that isn't in the package. Fail loudly rather than emit it.
func TestWriteConffiles_NotInstalled(t *testing.T) {
	dest := t.TempDir()
	err := WriteConffiles(dest, []string{"/etc/missing.conf"})
	if err == nil {
		t.Fatal("expected an error for a conffile the unit does not install")
	}
}
