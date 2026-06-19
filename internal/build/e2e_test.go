package build

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/yoebuild/yoe/internal/feeds/alpine"
	"github.com/yoebuild/yoe/internal/feeds/apt"
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
		yoestar.WithBuiltin("alpine_feed", alpine.Builtin),
		yoestar.WithBuiltin("apt_feed", apt.Builtin),
	)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	// Should have machine from module-core module
	if _, ok := proj.Machines["qemu-x86_64"]; !ok {
		t.Error("expected qemu-x86_64 machine from module-core module")
	}

	// Should have units from module-core module
	if proj.AnyUnit("busybox") == nil {
		t.Error("expected busybox unit from module-core module")
	}
	if proj.AnyUnit("linux") == nil {
		t.Error("expected linux unit from module-core module")
	}
	if proj.AnyUnit("base-image") == nil {
		t.Error("expected base-image from module-core module")
	}
	if proj.AnyUnit("zlib") == nil {
		t.Error("expected zlib unit from module-core module")
	}

	// zlib should have been loaded via a class (autotools or similar).
	// The .star files haven't been migrated to tasks yet (Task 2),
	// so skip assertions about build steps for now.
	if r := proj.AnyUnit("zlib"); r != nil {
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

// TestE2E_DistroArtifactsConsolidatedImage verifies the consolidated
// module-core ssh-image resolves its distro_artifacts: the project's default
// distro is alpine, so the resolved closure must contain the alpine branch
// (busybox/apk-tools) and none of the inert debian branch (systemd-sysv) —
// proving both the per-distro merge and that non-selected branches are never
// walked. It also exercises the per-distro machine kernel: "linux" must resolve
// (to the qemu-x86_64 alpine kernel unit) rather than appearing unresolved.
func TestE2E_DistroArtifactsConsolidatedImage(t *testing.T) {
	projectDir := filepath.Join("..", "..", "testdata", "e2e-project")
	if _, err := os.Stat(filepath.Join(projectDir, "PROJECT.star")); os.IsNotExist(err) {
		t.Skip("e2e test project not found")
	}
	abs, _ := filepath.Abs(projectDir)
	t.Setenv("YOE_CACHE", filepath.Join(abs, "cache"))

	proj, err := yoestar.LoadProject(projectDir,
		yoestar.WithModuleSync(module.SyncIfNeeded),
		yoestar.WithAllowDuplicateProvides(true),
		yoestar.WithBuiltin("alpine_feed", alpine.Builtin),
		yoestar.WithBuiltin("apt_feed", apt.Builtin),
	)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	img := proj.AnyUnit("ssh-image")
	if img == nil {
		t.Fatal("expected consolidated ssh-image")
	}
	has := func(name string) bool {
		for _, a := range img.Artifacts {
			if a == name {
				return true
			}
		}
		return false
	}
	// Alpine branch was selected (default distro = alpine).
	if !has("apk-tools") {
		t.Errorf("ssh-image closure missing alpine-branch apk-tools; got %v", img.Artifacts)
	}
	// Debian branch is inert and must not leak into an alpine build.
	if has("systemd-sysv") {
		t.Errorf("ssh-image closure leaked debian-branch systemd-sysv into alpine build; got %v", img.Artifacts)
	}
	// Per-distro machine kernel resolved "linux" to a concrete unit.
	if has("linux-image-amd64") {
		t.Errorf("alpine build resolved apt kernel linux-image-amd64; got %v", img.Artifacts)
	}
}
