package build

import (
	"os"
	"path/filepath"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestBuildTemplateContext_MergesFields(t *testing.T) {
	u := &yoestar.Unit{
		Name:    "base-files",
		Version: "1.0.0",
		Release: 2,
		Extra: map[string]any{
			"port":      int64(8080),
			"log_level": "info",
		},
	}
	ctx := BuildTemplateContext(u, "x86_64", "qemu-x86_64", "ttyS0", "e2e-project")

	want := map[string]any{
		"name":      "base-files",
		"version":   "1.0.0",
		"release":   int64(2),
		"arch":      "x86_64",
		"machine":   "qemu-x86_64",
		"console":   "ttyS0",
		"project":   "e2e-project",
		"port":      int64(8080),
		"log_level": "info",
	}
	if len(ctx) != len(want) {
		t.Errorf("len(ctx) = %d, want %d; ctx = %v", len(ctx), len(want), ctx)
	}
	for k, v := range want {
		if ctx[k] != v {
			t.Errorf("ctx[%q] = %v (%T), want %v (%T)", k, ctx[k], ctx[k], v, v)
		}
	}
}

func TestBuildTemplateContext_ExtraOverridesAuto(t *testing.T) {
	u := &yoestar.Unit{
		Name:    "my-app",
		Version: "1.0.0",
		Extra: map[string]any{
			"machine": "override", // should win over auto-populated "qemu-x86_64"
		},
	}
	ctx := BuildTemplateContext(u, "x86_64", "qemu-x86_64", "ttyS0", "e2e-project")
	if ctx["machine"] != "override" {
		t.Errorf("ctx[machine] = %v, want \"override\" (Extra should override auto)", ctx["machine"])
	}
}

func TestDoInstallStep_CopiesFileVerbatim(t *testing.T) {
	tmp := t.TempDir()
	unitDir := filepath.Join(tmp, "unit-src", "my-unit")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\necho hello\n")
	if err := os.WriteFile(filepath.Join(unitDir, "script.sh"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(tmp, "destdir")
	u := &yoestar.Unit{Name: "my-unit", DefinedIn: filepath.Join(tmp, "unit-src")}
	step := &yoestar.InstallStep{
		Kind: "file",
		Src:  "script.sh",
		Dest: "$DESTDIR/usr/bin/script.sh",
		Mode: 0o755,
	}
	env := map[string]string{"DESTDIR": destDir}
	if err := doInstallStep(u, step, nil, env); err != nil {
		t.Fatalf("doInstallStep: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "usr/bin/script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch: got %q want %q", got, content)
	}
	info, _ := os.Stat(filepath.Join(destDir, "usr/bin/script.sh"))
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0o755", info.Mode().Perm())
	}
}

func TestDoInstallStep_RendersTemplateWithData(t *testing.T) {
	tmp := t.TempDir()
	unitDir := filepath.Join(tmp, "unit-src", "base-files")
	_ = os.MkdirAll(unitDir, 0o755)
	_ = os.WriteFile(filepath.Join(unitDir, "info.tmpl"),
		[]byte("machine={{.machine}}\nconsole={{.console}}\n"), 0o644)

	destDir := filepath.Join(tmp, "destdir")
	u := &yoestar.Unit{Name: "base-files", DefinedIn: filepath.Join(tmp, "unit-src")}
	step := &yoestar.InstallStep{
		Kind: "template",
		Src:  "info.tmpl",
		Dest: "$DESTDIR/etc/info",
		Mode: 0o644,
	}
	env := map[string]string{"DESTDIR": destDir}
	data := map[string]any{"machine": "qemu-x86_64", "console": "ttyS0"}
	if err := doInstallStep(u, step, data, env); err != nil {
		t.Fatalf("doInstallStep: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(destDir, "etc/info"))
	want := "machine=qemu-x86_64\nconsole=ttyS0\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDoInstallStep_MissingKeyIsError(t *testing.T) {
	tmp := t.TempDir()
	unitDir := filepath.Join(tmp, "unit-src", "u")
	_ = os.MkdirAll(unitDir, 0o755)
	_ = os.WriteFile(filepath.Join(unitDir, "x.tmpl"), []byte(`{{.missing}}`), 0o644)

	u := &yoestar.Unit{Name: "u", DefinedIn: filepath.Join(tmp, "unit-src")}
	step := &yoestar.InstallStep{Kind: "template", Src: "x.tmpl", Dest: "$DESTDIR/out", Mode: 0o644}
	env := map[string]string{"DESTDIR": filepath.Join(tmp, "dd")}
	if err := doInstallStep(u, step, map[string]any{}, env); err == nil {
		t.Fatal("expected error on missing key, got nil")
	}
}

func TestDoInstallStep_PathEscapeRejected(t *testing.T) {
	tmp := t.TempDir()
	u := &yoestar.Unit{Name: "u", DefinedIn: filepath.Join(tmp, "unit-src")}
	step := &yoestar.InstallStep{Kind: "file", Src: "../../etc/passwd", Dest: "$DESTDIR/p", Mode: 0o644}
	if err := doInstallStep(u, step, nil, map[string]string{"DESTDIR": tmp}); err == nil {
		t.Fatal("expected escape error, got nil")
	}
}

// TestDoInstallStep_BaseDirOverridesUnitDir verifies that when BaseDir is set
// (the normal case for install_template/install_file calls in real units),
// the source file is resolved relative to BaseDir — not to the unit's
// DefinedIn/Name. This is what makes helper functions work: the helper's
// templates are located via the helper's source file, regardless of which
// unit ends up holding the install step.
func TestDoInstallStep_BaseDirOverridesUnitDir(t *testing.T) {
	tmp := t.TempDir()
	helperDir := filepath.Join(tmp, "helper-templates")
	_ = os.MkdirAll(helperDir, 0o755)
	_ = os.WriteFile(filepath.Join(helperDir, "config"),
		[]byte("from helper\n"), 0o644)

	destDir := filepath.Join(tmp, "destdir")
	// Unit looks like it was registered from a totally unrelated directory,
	// matching the helper-from-image scenario in real builds.
	u := &yoestar.Unit{Name: "renamed-unit", DefinedIn: filepath.Join(tmp, "image-dir")}
	step := &yoestar.InstallStep{
		Kind:    "file",
		Src:     "config",
		Dest:    "$DESTDIR/etc/config",
		Mode:    0o644,
		BaseDir: helperDir,
	}
	env := map[string]string{"DESTDIR": destDir}
	if err := doInstallStep(u, step, nil, env); err != nil {
		t.Fatalf("doInstallStep: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "etc/config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from helper\n" {
		t.Errorf("got %q, want %q", got, "from helper\n")
	}
}
