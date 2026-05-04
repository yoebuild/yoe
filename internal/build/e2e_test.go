package build

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestE2E_DryRun(t *testing.T) {
	projectDir := filepath.Join("..", "..", "testdata", "e2e-project")
	if _, err := os.Stat(filepath.Join(projectDir, "PROJECT.star")); os.IsNotExist(err) {
		t.Skip("e2e test project not found")
	}

	proj, err := yoestar.LoadProject(projectDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	// Should have machine from units-core module
	if _, ok := proj.Machines["qemu-x86_64"]; !ok {
		t.Error("expected qemu-x86_64 machine from units-core module")
	}

	// Should have units from units-core module
	if _, ok := proj.Units["busybox"]; !ok {
		t.Error("expected busybox unit from units-core module")
	}
	if _, ok := proj.Units["linux"]; !ok {
		t.Error("expected linux unit from units-core module")
	}
	if _, ok := proj.Units["base-image"]; !ok {
		t.Error("expected base-image from units-core module")
	}
	if _, ok := proj.Units["zlib"]; !ok {
		t.Error("expected zlib unit from units-core module")
	}

	// zlib should have been loaded via a class (autotools or similar).
	// The .star files haven't been migrated to tasks yet (Task 2),
	// so skip assertions about build steps for now.
	if r := proj.Units["zlib"]; r != nil {
		t.Logf("zlib class=%s tasks=%d", r.Class, len(r.Tasks))
	}

	// Dry run should work
	var buf bytes.Buffer
	abs, _ := filepath.Abs(projectDir)
	opts := Options{
		DryRun:     true,
		ProjectDir: abs,
		Arch:       "x86_64",
	}

	if err := BuildUnits(proj, nil, opts, &buf); err != nil {
		t.Fatalf("BuildUnits dry run: %v", err)
	}

	output := buf.String()
	t.Logf("Dry run output:\n%s", output)

	if len(output) == 0 {
		t.Error("dry run produced no output")
	}
}
