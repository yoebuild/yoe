package deb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeSystemdServiceSymlinks(t *testing.T) {
	destDir := t.TempDir()
	// Unit ships its own service file under /lib/systemd/system.
	libDir := filepath.Join(destDir, "lib", "systemd", "system")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "foo.service"), []byte("[Unit]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := MaterializeSystemdServiceSymlinks(destDir, "", []string{"foo"}); err != nil {
		t.Fatalf("MaterializeSystemdServiceSymlinks: %v", err)
	}

	link := filepath.Join(destDir, "etc", "systemd", "system", "multi-user.target.wants", "foo.service")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if want := "/lib/systemd/system/foo.service"; target != want {
		t.Errorf("symlink target: got %q, want %q", target, want)
	}
}

func TestMaterializeSystemdServiceSymlinks_FromSysroot(t *testing.T) {
	destDir := t.TempDir()
	sysroot := t.TempDir()
	libDir := filepath.Join(sysroot, "lib", "systemd", "system")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "bar.service"), []byte("[Unit]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := MaterializeSystemdServiceSymlinks(destDir, sysroot, []string{"bar"}); err != nil {
		t.Fatalf("MaterializeSystemdServiceSymlinks: %v", err)
	}
	link := filepath.Join(destDir, "etc", "systemd", "system", "multi-user.target.wants", "bar.service")
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink missing: %v", err)
	}
}

func TestMaterializeSystemdServiceSymlinks_MissingUnitFile(t *testing.T) {
	destDir := t.TempDir()
	err := MaterializeSystemdServiceSymlinks(destDir, "", []string{"missing"})
	if err == nil {
		t.Fatal("expected error for missing service file")
	}
}

func TestMaterializeSystemdServiceSymlinks_Empty(t *testing.T) {
	destDir := t.TempDir()
	if err := MaterializeSystemdServiceSymlinks(destDir, "", nil); err != nil {
		t.Errorf("empty services: %v", err)
	}
}
