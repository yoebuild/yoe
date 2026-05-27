package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoebuild/yoe/internal/deb"
)

// stagedHelloDeb writes a minimal hello_1.0_amd64.deb into pool and
// returns its path. Skips when dpkg-deb is unavailable on the host.
func stagedHelloDeb(t *testing.T, repoDir, pkg, version, arch string) string {
	t.Helper()
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("dpkg-deb not on PATH")
	}
	staging := t.TempDir()
	binDir := filepath.Join(staging, "usr", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, pkg), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), pkg+".deb")
	c := deb.Control{
		Package:      pkg,
		Version:      version,
		Architecture: arch,
		Maintainer:   "Yoe <yoe@example.com>",
		Description:  "test " + pkg,
	}
	if err := deb.BuildDeb(staging, c, out, ""); err != nil {
		t.Fatalf("BuildDeb: %v", err)
	}

	// Place the deb into the pool layout.
	pool := filepath.Join(repoDir, "pool", "main", string(pkg[0]), pkg)
	if err := os.MkdirAll(pool, 0755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(pool, pkg+"_"+version+"_"+arch+".deb")
	if err := copyFile(out, dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestGenerateDebianIndex_OneDeb(t *testing.T) {
	repoDir := t.TempDir()
	stagedHelloDeb(t, repoDir, "hello", "1.0", "amd64")

	opts := DebRepoOptions{
		RepoDir:        repoDir,
		Suite:          "bookworm",
		Components:     []string{"main"},
		Arches:         []string{"amd64", "arm64"},
		ValidUntilDays: 30,
	}
	if err := GenerateDebianIndex(opts); err != nil {
		t.Fatalf("GenerateDebianIndex: %v", err)
	}

	for _, arch := range opts.Arches {
		pPath := filepath.Join(repoDir, "dists", "bookworm", "main", "binary-"+arch, "Packages")
		body, err := os.ReadFile(pPath)
		if err != nil {
			t.Errorf("read %s: %v", pPath, err)
			continue
		}
		if arch == "amd64" {
			if !strings.Contains(string(body), "Package: hello") {
				t.Errorf("amd64 Packages missing hello: %s", body)
			}
		} else {
			// arm64 has no hello deb, so Packages should be empty.
			if strings.TrimSpace(string(body)) != "" {
				t.Errorf("arm64 Packages non-empty (only amd64 hello exists): %q", body)
			}
		}
	}

	relPath := filepath.Join(repoDir, "dists", "bookworm", "Release")
	rel, err := os.ReadFile(relPath)
	if err != nil {
		t.Fatalf("read Release: %v", err)
	}
	if !strings.Contains(string(rel), "Suite: bookworm") {
		t.Errorf("Release missing Suite: %s", rel)
	}
	if !strings.Contains(string(rel), "Valid-Until:") {
		t.Errorf("Release missing Valid-Until: %s", rel)
	}
	if !strings.Contains(string(rel), "SHA256:") {
		t.Errorf("Release missing SHA256 block")
	}
}

func TestVerifyMirrorSHA256_Match(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("dpkg-deb not on PATH")
	}
	repoDir := t.TempDir()
	debPath := stagedHelloDeb(t, repoDir, "hello", "1.0", "amd64")

	// Compute SHA256 ourselves
	raw, err := os.ReadFile(debPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256Hex(raw)
	if err := VerifyMirrorSHA256(debPath, sum); err != nil {
		t.Errorf("VerifyMirrorSHA256 match: %v", err)
	}
}

func TestVerifyMirrorSHA256_Mismatch(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("dpkg-deb not on PATH")
	}
	repoDir := t.TempDir()
	debPath := stagedHelloDeb(t, repoDir, "hello", "1.0", "amd64")
	if err := VerifyMirrorSHA256(debPath, strings.Repeat("0", 64)); err == nil {
		t.Error("expected mismatch error")
	}
}
