package build

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/yoebuild/yoe/internal/module"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestE2E_DryRun(t *testing.T) {
	projectDir := filepath.Join("..", "..", "testdata", "e2e-project")
	if _, err := os.Stat(filepath.Join(projectDir, "PROJECT.star")); os.IsNotExist(err) {
		t.Skip("e2e test project not found")
	}

	abs, _ := filepath.Abs(projectDir)
	// Point at the checked-in cache snapshot under testdata/e2e-project/cache/
	// so the test loads the full module-alpine tree (units/main + units/community).
	// build/cache/ is a build-output subset and may drift; the source-of-truth
	// fixture lives at cache/.
	t.Setenv("YOE_CACHE", filepath.Join(abs, "cache"))

	proj, err := yoestar.LoadProject(projectDir,
		yoestar.WithModuleSync(module.SyncIfNeeded),
		yoestar.WithAllowDuplicateProvides(true),
	)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	// Should have machine from module-core module
	if _, ok := proj.Machines["qemu-x86_64"]; !ok {
		t.Error("expected qemu-x86_64 machine from module-core module")
	}

	// Should have units from module-core module
	if _, ok := proj.Units["busybox"]; !ok {
		t.Error("expected busybox unit from module-core module")
	}
	if _, ok := proj.Units["linux"]; !ok {
		t.Error("expected linux unit from module-core module")
	}
	if _, ok := proj.Units["base-image"]; !ok {
		t.Error("expected base-image from module-core module")
	}
	if _, ok := proj.Units["zlib"]; !ok {
		t.Error("expected zlib unit from module-core module")
	}

	// zlib should have been loaded via a class (autotools or similar).
	// The .star files haven't been migrated to tasks yet (Task 2),
	// so skip assertions about build steps for now.
	if r := proj.Units["zlib"]; r != nil {
		t.Logf("zlib class=%s tasks=%d", r.Class, len(r.Tasks))
	}

	// Dry run should work
	var buf bytes.Buffer
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
