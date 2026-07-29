package starlark

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Machine/distro compatibility: a machine's kernel `distro_unit` keys
// declare which distros the board can boot. Every image in every loaded
// module is evaluated against the one selected machine, so images for
// other distros must register inert rather than fail the whole project.
//
// These tests drive the real module-core image class. The class file is
// copied into each fixture project's classes/ rather than referenced
// through a module, because pulling module-core in as a module would drag
// its entire unit set (and the feed names those units reach for) into a
// test that only cares about one branch in image().

// compatFixture writes a self-contained project exercising image()'s
// machine-compatibility branch and returns its root.
//
// The supported distro is alpine and the unsupported one is debian —
// deliberately that way round. image() seeds every apt-family closure
// with Debian's Essential set, so making debian the *supported* side
// would force the fixture to define 23 unrelated units to satisfy a
// resolution the test isn't about.
type compatFixture struct {
	// kernel is the kernel(...) argument text for the machine.
	kernel string
	// images maps a file name under images/ to its image() call text.
	images map[string]string
}

func (f compatFixture) write(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	mkdir := func(sub string) string {
		t.Helper()
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The real image class, verbatim.
	classSrc, err := os.ReadFile(filepath.Join("..", "..", "modules", "module-core", "classes", "image.star"))
	if err != nil {
		t.Fatalf("reading module-core image class: %v", err)
	}
	write(filepath.Join(mkdir("classes"), "image.star"), string(classSrc))

	write(filepath.Join(root, "PROJECT.star"), `
project(
    name = "compat-test",
    version = "0.1.0",
    defaults = defaults(
        machine = "board",
        image = "alp-image",
        distro = "alpine",
    ),
)
`)

	write(filepath.Join(mkdir("machines"), "board.star"), `
machine(
    name = "board",
    arch = "arm64",
    kernel = `+f.kernel+`,
    packages = ["board-firmware"],
    distro_packages = {"alpine": ["board-tools"]},
    partitions = [
        partition(label = "boot", type = "vfat", size = "64M"),
        partition(label = "root", type = "ext4", size = "512M", root = True),
    ],
)
`)

	write(filepath.Join(mkdir("units"), "base.star"), `
unit(name = "linux-board", version = "1.0")
unit(name = "board-firmware", version = "1.0")
unit(name = "board-tools", version = "1.0")
unit(name = "busybox", version = "1.0")
unit(name = "toolchain", version = "1.0", unit_class = "container")
`)

	imagesDir := mkdir("images")
	for file, body := range f.images {
		write(filepath.Join(imagesDir, file), "load(\"//classes/image.star\", \"image\")\n"+body+"\n")
	}
	return root
}

// alpineOnlyKernel is a machine that can boot exactly one distro — the
// shape that broke project evaluation outright before this behavior
// existed.
const alpineOnlyKernel = `kernel(
        distro_unit = {"alpine": "linux-board"},
        provides = "linux",
        cmdline = "console=ttyS0",
    )`

const alpImage = `image(name = "alp-image", distro = "alpine", artifacts = ["linux", "busybox"])`

// debImage names an artifact that exists nowhere. On the skip path the
// closure is never walked, so the bogus name is never resolved — which is
// exactly what proves no work happened.
const debImage = `image(name = "deb-image", distro = "debian", artifacts = ["no-such-unit"])`

// TestImage_UnsupportedDistroRegistersInert is the direct regression
// test: selecting a single-distro machine used to fail project evaluation
// on the first image targeting any other distro, before any build began.
func TestImage_UnsupportedDistroRegistersInert(t *testing.T) {
	root := compatFixture{
		kernel: alpineOnlyKernel,
		images: map[string]string{"alp.star": alpImage, "deb.star": debImage},
	}.write(t)

	proj, err := LoadProjectFromRoot(root)
	if err != nil {
		t.Fatalf("project with a debian image on an alpine-only machine should evaluate cleanly: %v", err)
	}

	u := proj.LookupUnit("debian", "deb-image")
	if u == nil {
		t.Fatal("deb-image should still be registered — skipped images are marked, not hidden")
	}
	if !u.NotBuildable() {
		t.Fatal("deb-image should be marked not buildable on this machine")
	}
	if u.UnbuildableMachine != "board" {
		t.Errorf("UnbuildableMachine = %q, want %q", u.UnbuildableMachine, "board")
	}
	if got := strings.Join(u.MachineKernelDistros, ","); got != "alpine" {
		t.Errorf("MachineKernelDistros = %v, want [alpine]", u.MachineKernelDistros)
	}

	// Genuinely inert: nothing here may become a DAG node's build edge or
	// a runnable task.
	if len(u.Artifacts) != 0 || len(u.ArtifactsExplicit) != 0 {
		t.Errorf("artifacts should be empty; got %v / %v", u.Artifacts, u.ArtifactsExplicit)
	}
	if len(u.Deps) != 0 {
		t.Errorf("deps should be empty; got %v", u.Deps)
	}
	if len(u.Tasks) != 0 {
		t.Errorf("tasks should be empty; got %d tasks", len(u.Tasks))
	}
	if u.Container != "" {
		t.Errorf("container should be empty (it becomes a build edge); got %q", u.Container)
	}
	if len(u.Partitions) != 0 {
		t.Errorf("partitions should be empty; got %v", u.Partitions)
	}

	// The message a user gets when they ask for it names every part of
	// the mismatch.
	reason := u.UnbuildableReason()
	for _, want := range []string{`"deb-image"`, `"debian"`, `"board"`, "alpine"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q missing %s", reason, want)
		}
	}
}

// TestImage_SupportedDistroResolvesAsBefore: the matching image is
// untouched by the new branch — it still picks its kernel out of
// distro_unit and still merges the machine's packages and distro_packages.
func TestImage_SupportedDistroResolvesAsBefore(t *testing.T) {
	root := compatFixture{
		kernel: alpineOnlyKernel,
		images: map[string]string{"alp.star": alpImage, "deb.star": debImage},
	}.write(t)

	proj, err := LoadProjectFromRoot(root)
	if err != nil {
		t.Fatalf("LoadProjectFromRoot: %v", err)
	}
	u := proj.LookupUnit("alpine", "alp-image")
	if u == nil {
		t.Fatal("alp-image not registered")
	}
	if u.NotBuildable() {
		t.Fatalf("alp-image is buildable on this machine; got marked: %s", u.UnbuildableReason())
	}
	has := func(name string) bool {
		for _, a := range u.Artifacts {
			if a == name {
				return true
			}
		}
		return false
	}
	// "linux" resolved through distro_unit["alpine"], not left virtual.
	if !has("linux-board") {
		t.Errorf("kernel not resolved via distro_unit; artifacts = %v", u.Artifacts)
	}
	if has("linux") {
		t.Errorf("virtual kernel name leaked into artifacts: %v", u.Artifacts)
	}
	// machine packages + distro_packages both merged.
	if !has("board-firmware") {
		t.Errorf("machine packages not merged; artifacts = %v", u.Artifacts)
	}
	if !has("board-tools") {
		t.Errorf("machine distro_packages not merged; artifacts = %v", u.Artifacts)
	}
	if len(u.Tasks) == 0 {
		t.Error("buildable image should still carry its rootfs/disk tasks")
	}
}

// TestImage_FlatKernelUnitUnchanged: a machine using the flat
// kernel(unit = ...) form asserts its kernel works on every distro, so
// nothing is ever skipped for it — images of any distro resolve.
func TestImage_FlatKernelUnitUnchanged(t *testing.T) {
	root := compatFixture{
		kernel: `kernel(unit = "linux-board", provides = "linux", cmdline = "console=ttyS0")`,
		images: map[string]string{
			"alp.star": alpImage,
			// A distro the machine never named. Under distro_unit this
			// would be skipped; under the flat form it must resolve.
			// ("otherdistro" rather than debian only to stay off the
			// apt-family path, which seeds Debian's Essential set.)
			"other.star": `image(name = "other-image", distro = "otherdistro", artifacts = ["busybox"])`,
		},
	}.write(t)

	proj, err := LoadProjectFromRoot(root)
	if err != nil {
		t.Fatalf("LoadProjectFromRoot: %v", err)
	}
	for _, tc := range []struct{ distro, name string }{{"alpine", "alp-image"}, {"otherdistro", "other-image"}} {
		u := proj.LookupUnit(tc.distro, tc.name)
		if u == nil {
			t.Fatalf("%s not registered", tc.name)
		}
		if u.NotBuildable() {
			t.Errorf("flat-unit machine must not mark any image; %s got: %s", tc.name, u.UnbuildableReason())
		}
		if len(u.Tasks) == 0 {
			t.Errorf("%s should carry build tasks", tc.name)
		}
	}
}

// TestImage_FlatKernelUnitStillErrorsUnresolved: the flat form's claim is
// load-bearing. When the named kernel doesn't exist for the distro being
// resolved, the closure walk must still fail loudly — and point the
// machine author at distro_unit, which is the actual fix.
func TestImage_FlatKernelUnitStillErrorsUnresolved(t *testing.T) {
	root := compatFixture{
		kernel: `kernel(unit = "linux-absent", provides = "linux", cmdline = "console=ttyS0")`,
		images: map[string]string{"alp.star": alpImage},
	}.write(t)

	_, err := LoadProjectFromRoot(root)
	if err == nil {
		t.Fatal("an unresolvable flat kernel unit must still fail loudly")
	}
	msg := err.Error()
	if !strings.Contains(msg, "linux-absent") {
		t.Errorf("error %q should name the unresolved kernel unit", msg)
	}
	if !strings.Contains(msg, "distro_unit") {
		t.Errorf("error %q should point the machine author at distro_unit", msg)
	}
}
