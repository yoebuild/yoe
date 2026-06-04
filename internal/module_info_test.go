package internal

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoebuild/yoe/internal/module"
	"github.com/yoebuild/yoe/internal/source"
)

func TestListModuleInfo_NoModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "PROJECT.star"), `project(name = "p", version = "0.1.0")`)

	var out bytes.Buffer
	if err := ListModuleInfo(dir, &out); err != nil {
		t.Fatalf("ListModuleInfo: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "No modules declared in PROJECT.star" {
		t.Fatalf("output = %q", got)
	}
}

func TestListModuleInfo_ShowsDeclaredAndCachedState(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	t.Setenv("YOE_CACHE", cache)

	writeFile(t, filepath.Join(dir, "PROJECT.star"), `
project(
    name = "p",
    version = "0.1.0",
    modules = [
        module("https://example.com/remote.git", ref = "v1.2.3"),
        module("https://example.com/missing.git"),
        module("https://example.com/local.git", local = "modules/local"),
    ],
)
`)
	remoteDir := filepath.Join(cache, "modules", "remote")
	writeFile(t, filepath.Join(remoteDir, "MODULE.star"), `module_info(name = "remote-name")`)
	lastSync := time.Date(2026, 6, 4, 10, 30, 0, 0, time.UTC)
	if err := module.WriteSyncInfo(remoteDir, lastSync); err != nil {
		t.Fatalf("WriteSyncInfo: %v", err)
	}
	writeFile(t, filepath.Join(dir, "modules", "local", "MODULE.star"), `module_info(name = "local-name")`)

	var out bytes.Buffer
	if err := ListModuleInfo(dir, &out); err != nil {
		t.Fatalf("ListModuleInfo: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"NAME",
		"URL/PATH",
		"VERSION/REF",
		"STATUS",
		"LAST SYNC",
		"remote-name",
		"https://example.com/remote.git",
		"v1.2.3",
		"synced",
		"2026-06-04",
		"missing",
		"main",
		"not synced",
		"never",
		"local-name",
		"modules/local",
		"local",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestListModuleInfo_ShowsDevStatus(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	t.Setenv("YOE_CACHE", cache)

	writeFile(t, filepath.Join(dir, "PROJECT.star"), `
project(
    name = "p",
    version = "0.1.0",
    modules = [module("https://example.com/devmod.git", ref = "main")],
)
`)
	moduleDir := filepath.Join(cache, "modules", "devmod")
	makeCleanGitRepo(t, moduleDir)
	writeFile(t, filepath.Join(moduleDir, "MODULE.star"), `module_info(name = "devmod")`)
	if err := module.WriteState(moduleDir, source.StateDev); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	var out bytes.Buffer
	if err := ListModuleInfo(dir, &out); err != nil {
		t.Fatalf("ListModuleInfo: %v", err)
	}
	got := out.String()
	found := false
	for _, field := range strings.Fields(got) {
		if field == "dev" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("output missing dev status:\n%s", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeCleanGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
}
