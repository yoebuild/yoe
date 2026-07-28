package build

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoebuild/yoe/internal/feeds/alpine"
	"github.com/yoebuild/yoe/internal/feeds/apt"
	"github.com/yoebuild/yoe/internal/module"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// compatProject holds one ordinary unit, one buildable alpine image, and
// one image the selected machine can't boot — the shape a mixed-distro
// project has whenever a single-distro machine is selected.
func compatProject() *yoestar.Project {
	proj := &yoestar.Project{
		Name:          "compat",
		DefaultDistro: "alpine",
		UnitsByModule: map[string]map[string]*yoestar.Unit{"": {
			"busybox": {
				Name: "busybox", Version: "1.0", Class: "unit", Distro: "alpine",
				Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "true"}}}},
			},
			"alp-image": {
				Name: "alp-image", Version: "1.0", Class: "image", Distro: "alpine",
				Artifacts: []string{"busybox"},
				Tasks:     []yoestar.Task{{Name: "rootfs", Steps: []yoestar.Step{{Command: "true"}}}},
			},
			"bun-image": {
				Name: "bun-image", Version: "1.0", Class: "image", Distro: "alpine",
				UnbuildableMachine:   "arduino-uno-q",
				MachineKernelDistros: []string{"debian"},
			},
		}},
	}
	return proj
}

// TestBuildUnits_NamedUnbuildableImageErrors: naming an image the
// selected machine can't boot is a hard error. This message replaces the
// evaluation-time crash that used to happen instead, so it has to carry
// the whole diagnosis — machine, image distro, and what the machine does
// support.
func TestBuildUnits_NamedUnbuildableImageErrors(t *testing.T) {
	var buf bytes.Buffer
	opts := Options{DryRun: true, ProjectDir: t.TempDir(), Arch: "arm64", Machine: "arduino-uno-q"}

	err := BuildUnits(compatProject(), []string{"bun-image"}, opts, &buf)
	if err == nil {
		t.Fatal("building an image the machine cannot boot should error")
	}
	for _, want := range []string{`"bun-image"`, `"alpine"`, `"arduino-uno-q"`, "debian"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %s", err, want)
		}
	}
}

// TestBuildUnits_SweptUnbuildableImageSkipped: an unnamed build sweeps
// the whole per-distro graph. Erroring there would make full builds
// impossible in any mixed-distro project — the original problem one level
// up — so the image is skipped, loudly, and everything else still builds.
func TestBuildUnits_SweptUnbuildableImageSkipped(t *testing.T) {
	var buf bytes.Buffer
	opts := Options{DryRun: true, ProjectDir: t.TempDir(), Arch: "arm64", Machine: "arduino-uno-q"}

	if err := BuildUnits(compatProject(), nil, opts, &buf); err != nil {
		t.Fatalf("a full build must not error on an image the machine cannot boot: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "note: skipping") {
		t.Errorf("skip must be announced, not silent; output:\n%s", out)
	}
	for _, want := range []string{`"bun-image"`, `"arduino-uno-q"`, "debian"} {
		if !strings.Contains(out, want) {
			t.Errorf("skip notice missing %s; output:\n%s", want, out)
		}
	}
	// Skipped, not built: the dry run's build order must not list it.
	if strings.Contains(out, "bun-image      ") || strings.Contains(out, "bun-image [image]") {
		t.Errorf("skipped image should not appear in the build order; output:\n%s", out)
	}
	// The rest of the project still builds.
	for _, want := range []string{"alp-image", "busybox"} {
		if !strings.Contains(out, want) {
			t.Errorf("build order should still contain %s; output:\n%s", want, out)
		}
	}
}

// TestBuildUnits_NamedBuildableTargetIsQuiet: naming an ordinary unit in
// a project that also holds marked images must not spray notices about
// images this invocation was never going to build.
func TestBuildUnits_NamedBuildableTargetIsQuiet(t *testing.T) {
	var buf bytes.Buffer
	opts := Options{DryRun: true, ProjectDir: t.TempDir(), Arch: "arm64", Machine: "arduino-uno-q"}

	if err := BuildUnits(compatProject(), []string{"busybox"}, opts, &buf); err != nil {
		t.Fatalf("BuildUnits: %v", err)
	}
	if out := buf.String(); strings.Contains(out, "note: skipping") {
		t.Errorf("a targeted build should not report images outside its scope; output:\n%s", out)
	}
}

// loadE2EWithMachine loads the shared e2e project against a specific
// machine and distro. Skips when the project or the module isn't present.
func loadE2EWithMachine(t *testing.T, machine, distro string) *yoestar.Project {
	t.Helper()
	projectDir := filepath.Join("..", "..", "testdata", "e2e-project")
	if _, err := os.Stat(filepath.Join(projectDir, "PROJECT.star")); os.IsNotExist(err) {
		t.Skip("e2e test project not found")
	}
	abs, _ := filepath.Abs(projectDir)
	t.Setenv("YOE_CACHE", filepath.Join(abs, "cache"))
	if _, err := os.Stat(filepath.Join(abs, "cache", "modules", "module-qcom")); os.IsNotExist(err) {
		t.Skip("module-qcom not synced into the e2e cache")
	}

	opts := []yoestar.LoadOption{
		yoestar.WithModuleSync(module.SyncIfNeeded),
		yoestar.WithAllowDuplicateProvides(true),
		yoestar.WithBuiltin("alpine_feed", alpine.Builtin),
		yoestar.WithBuiltin("apt_feed", apt.Builtin),
		yoestar.WithMachine(machine),
	}
	if distro != "" {
		opts = append(opts, yoestar.WithDistroOverride(distro))
	}
	proj, err := yoestar.LoadProject(projectDir, opts...)
	if err != nil {
		t.Fatalf("LoadProject(machine=%s, distro=%s): %v", machine, distro, err)
	}
	return proj
}

// TestE2E_SingleDistroMachineEvaluates is the regression test for the
// reported failure: selecting arduino-uno-q — whose kernel exists only in
// a Debian-format vendor feed — used to break project evaluation on the
// first Alpine image in any loaded module, before any build started. The
// e2e project loads @module-alpine, so this load exercises exactly that.
func TestE2E_SingleDistroMachineEvaluates(t *testing.T) {
	proj := loadE2EWithMachine(t, "arduino-uno-q", "")

	if _, ok := proj.Machines["arduino-uno-q"]; !ok {
		t.Skip("arduino-uno-q machine not in the synced module set")
	}

	// @module-alpine's own images are the ones that used to break the
	// load — nobody asked to build them for a Qualcomm board, but every
	// image is evaluated against the selected machine. They must now be
	// registered and marked rather than fatal.
	markedAlpine := 0
	for _, u := range proj.AllUnits() {
		if u.Distro == "alpine" && u.NotBuildable() {
			markedAlpine++
			if u.UnbuildableMachine != "arduino-uno-q" {
				t.Errorf("%s: UnbuildableMachine = %q, want arduino-uno-q", u.Name, u.UnbuildableMachine)
			}
			if len(u.Artifacts) != 0 || len(u.Tasks) != 0 {
				t.Errorf("%s should be inert; got %d artifacts, %d tasks", u.Name, len(u.Artifacts), len(u.Tasks))
			}
		}
	}
	if markedAlpine == 0 {
		t.Error("expected the alpine images to be marked not-buildable on a debian-only machine")
	}

	// The project's own effective distro (defaults.distro, or a
	// developer's local.star override) is likewise one this machine can't
	// boot, so its default image is marked — and naming it is where it
	// gets loud. This is the same footgun a user hits by omitting
	// --distro debian on this machine.
	effective := proj.DefaultDistroOverride
	if effective == "" {
		effective = proj.DefaultDistro
	}
	if effective == "debian" {
		t.Skip("local.star selects debian, the one distro this machine boots")
	}
	img := proj.LookupUnit(effective, "base-image")
	if img == nil {
		t.Fatalf("base-image should still be registered for distro %q", effective)
	}
	if !img.NotBuildable() {
		t.Fatalf("%s base-image should be marked on a debian-only machine; artifacts = %v", effective, img.Artifacts)
	}

	var buf bytes.Buffer
	err := BuildUnits(proj, []string{"base-image"}, Options{
		DryRun: true, ProjectDir: t.TempDir(), Arch: "arm64", Machine: "arduino-uno-q",
		EffectiveDistro: effective,
	}, &buf)
	if err == nil {
		t.Fatal("naming a marked image should refuse, not build")
	}
	if !strings.Contains(err.Error(), "arduino-uno-q") {
		t.Errorf("refusal %q should name the machine", err)
	}
}

// TestE2E_SingleDistroMachineResolvesItsOwnDistro: the flip side — with
// the distro the machine does boot, the same image resolves a real
// closure containing the vendor feed's kernel and board packages.
func TestE2E_SingleDistroMachineResolvesItsOwnDistro(t *testing.T) {
	proj := loadE2EWithMachine(t, "arduino-uno-q", "debian")

	m, ok := proj.Machines["arduino-uno-q"]
	if !ok {
		t.Skip("arduino-uno-q machine not in the synced module set")
	}
	img := proj.LookupUnit("debian", "base-image")
	if img == nil {
		t.Fatal("debian base-image not resolvable")
	}
	if img.NotBuildable() {
		t.Fatalf("debian base-image should build on this machine; got: %s", img.UnbuildableReason())
	}
	has := func(name string) bool {
		for _, a := range img.Artifacts {
			if a == name {
				return true
			}
		}
		return false
	}
	if kernel := m.Kernel.DistroUnit["debian"]; kernel == "" {
		t.Error("arduino-uno-q should declare a debian kernel")
	} else if !has(kernel) {
		t.Errorf("closure missing the machine kernel %q; artifacts = %v", kernel, img.Artifacts)
	}
	for _, pkg := range m.DistroPackages["debian"] {
		if !has(pkg) {
			t.Errorf("closure missing board package %q", pkg)
		}
	}
}
