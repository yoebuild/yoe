package starlark

import (
	"os"
	"path/filepath"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func TestInstallFile_ReturnsValue(t *testing.T) {
	thread := &starlark.Thread{Name: "test"}
	predeclared := starlark.StringDict{
		"install_file":     starlark.NewBuiltin("install_file", fnInstallFile),
		"install_template": starlark.NewBuiltin("install_template", fnInstallTemplate),
	}
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "t.star", `
f = install_file("sshd", "$DESTDIR/etc/init.d/sshd", mode = 0o755)
t = install_template("inittab.tmpl", "$DESTDIR/etc/inittab")
`, predeclared)
	if err != nil {
		t.Fatalf("ExecFile: %v", err)
	}
	f, ok := globals["f"].(*InstallStepValue)
	if !ok {
		t.Fatalf("f = %T, want *InstallStepValue", globals["f"])
	}
	if f.Kind != "file" || f.Src != "sshd" || f.Dest != "$DESTDIR/etc/init.d/sshd" || f.Mode != 0o755 {
		t.Errorf("f = %+v, unexpected fields", f)
	}
	tt, ok := globals["t"].(*InstallStepValue)
	if !ok {
		t.Fatalf("t = %T, want *InstallStepValue", globals["t"])
	}
	if tt.Kind != "template" || tt.Mode != 0o644 {
		t.Errorf("t = %+v, want default mode 0o644", tt)
	}
}

// TestInstallStep_BaseDirFromCallerFile verifies that BaseDir is captured from
// the .star file containing the install_template() call — not from where the
// resulting value is later used. This is what lets a helper function in
// units/base/base-files.star generate install steps for units registered
// from images/dev-image.star.
func TestInstallStep_BaseDirFromCallerFile(t *testing.T) {
	tmp := t.TempDir()
	helper := filepath.Join(tmp, "helper.star")
	caller := filepath.Join(tmp, "caller.star")
	if err := os.WriteFile(helper, []byte(`
def make_steps():
    return install_template("inittab.tmpl", "$DESTDIR/etc/inittab")
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caller, []byte(`
load("helper.star", "make_steps")
v = make_steps()
`), 0o644); err != nil {
		t.Fatal(err)
	}

	thread := &starlark.Thread{Name: caller}
	thread.Load = func(_ *starlark.Thread, module string) (starlark.StringDict, error) {
		t := &starlark.Thread{Name: filepath.Join(tmp, module)}
		return starlark.ExecFileOptions(&syntax.FileOptions{}, t, filepath.Join(tmp, module), nil, starlark.StringDict{
			"install_template": starlark.NewBuiltin("install_template", fnInstallTemplate),
		})
	}
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, caller, nil, starlark.StringDict{
		"install_template": starlark.NewBuiltin("install_template", fnInstallTemplate),
	})
	if err != nil {
		t.Fatalf("ExecFile: %v", err)
	}
	v, ok := globals["v"].(*InstallStepValue)
	if !ok {
		t.Fatalf("v = %T, want *InstallStepValue", globals["v"])
	}
	want := filepath.Join(tmp, "helper")
	if v.BaseDir != want {
		t.Errorf("BaseDir = %q, want %q (should reflect helper.star, not caller.star)", v.BaseDir, want)
	}
}

func TestInstallStepValue_HashStable(t *testing.T) {
	a := &InstallStepValue{Kind: "file", Src: "a", Dest: "/b", Mode: 0o644}
	b := &InstallStepValue{Kind: "file", Src: "a", Dest: "/b", Mode: 0o644}
	ha, _ := a.Hash()
	hb, _ := b.Hash()
	if ha != hb {
		t.Errorf("equal values hash differently: %d vs %d", ha, hb)
	}
	c := &InstallStepValue{Kind: "file", Src: "a", Dest: "/b", Mode: 0o755}
	hc, _ := c.Hash()
	if ha == hc {
		t.Error("different modes should produce different hashes")
	}
}
