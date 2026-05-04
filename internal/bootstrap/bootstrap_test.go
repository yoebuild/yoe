package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestStatus(t *testing.T) {
	projectDir := t.TempDir()
	os.MkdirAll(filepath.Join(projectDir, "build", "repo"), 0755)

	proj := &yoestar.Project{
		Name: "test",
		Units: map[string]*yoestar.Unit{
			"glibc":   {Name: "glibc", Version: "2.39"},
			"gcc":     {Name: "gcc", Version: "14.1"},
			"busybox": {Name: "busybox", Version: "1.36"},
		},
	}

	var buf bytes.Buffer
	if err := Status(proj, projectDir, &buf); err != nil {
		t.Fatalf("Status: %v", err)
	}

	output := buf.String()

	// Should list all bootstrap units
	if !strings.Contains(output, "glibc") {
		t.Error("should list glibc")
	}
	if !strings.Contains(output, "gcc") {
		t.Error("should list gcc")
	}

	// Units that exist should say "unit found"
	if !strings.Contains(output, "unit found") {
		t.Error("should show 'unit found' for existing units")
	}

	// Missing units should say "missing"
	if !strings.Contains(output, "missing") {
		t.Error("should show 'missing' for missing units")
	}
}

func TestStage0_MissingUnits(t *testing.T) {
	proj := &yoestar.Project{
		Name:    "test",
		Units: map[string]*yoestar.Unit{},
	}

	var buf bytes.Buffer
	err := Stage0(proj, t.TempDir(), &buf)
	if err == nil {
		t.Fatal("expected error for missing bootstrap units")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention missing units: %v", err)
	}
}

func TestStage0Commands(t *testing.T) {
	unit := &yoestar.Unit{
		Name:  "test",
		Class: "autotools",
		Tasks: []yoestar.Task{{
			Name: "build",
			Steps: []yoestar.Step{
				{Command: "./configure --with-glibc"},
				{Command: "make -j$NPROC"},
				{Command: "make DESTDIR=$DESTDIR install"},
			},
		}},
	}

	cmds := stage0Commands(unit)
	if len(cmds) != 3 {
		t.Errorf("expected 3 commands for autotools, got %d", len(cmds))
	}
	if len(cmds) > 0 && !strings.Contains(cmds[0], "--with-glibc") {
		t.Errorf("configure should include args: %s", cmds[0])
	}
}

func TestStage0Commands_ExplicitBuild(t *testing.T) {
	unit := &yoestar.Unit{
		Name: "test",
		Tasks: []yoestar.Task{{
			Name:  "build",
			Steps: []yoestar.Step{{Command: "make all"}, {Command: "make install"}},
		}},
	}

	cmds := stage0Commands(unit)
	if len(cmds) != 2 {
		t.Errorf("expected 2 explicit commands, got %d", len(cmds))
	}
}

func TestVerifyStage0_Missing(t *testing.T) {
	repoDir := t.TempDir()
	err := verifyStage0(repoDir, "x86_64")
	if err == nil {
		t.Fatal("expected error for empty repo")
	}
}
