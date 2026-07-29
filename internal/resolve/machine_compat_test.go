package resolve

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// writeCompatProject writes a project whose machine can boot alpine only,
// plus one alpine image and however many unsupported-distro images the
// caller asks for. Those extra images are skipped during evaluation, and
// the point of the fixture is to prove that skipping them changes nothing
// about the image that isn't skipped.
//
// Uses the real module-core image class, copied in rather than pulled
// through a module so the fixture stays hermetic.
func writeCompatProject(t *testing.T, skipped []string) string {
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

	classSrc, err := os.ReadFile(filepath.Join("..", "..", "modules", "module-core", "classes", "image.star"))
	if err != nil {
		t.Fatalf("reading module-core image class: %v", err)
	}
	write(filepath.Join(mkdir("classes"), "image.star"), string(classSrc))

	write(filepath.Join(root, "PROJECT.star"), `
project(
    name = "compat-hash-test",
    version = "0.1.0",
    defaults = defaults(machine = "board", image = "alp-image", distro = "alpine"),
)
`)
	write(filepath.Join(mkdir("machines"), "board.star"), `
machine(
    name = "board",
    arch = "arm64",
    kernel = kernel(
        distro_unit = {"alpine": "linux-board"},
        provides = "linux",
        cmdline = "console=ttyS0",
    ),
    packages = ["board-firmware"],
    partitions = [partition(label = "root", type = "ext4", size = "512M", root = True)],
)
`)
	write(filepath.Join(mkdir("units"), "base.star"), `
unit(name = "linux-board", version = "1.0")
unit(name = "board-firmware", version = "1.0")
unit(name = "busybox", version = "1.0", runtime_deps = ["musl"])
unit(name = "musl", version = "1.0")
unit(name = "toolchain", version = "1.0", unit_class = "container")
`)

	imagesDir := mkdir("images")
	write(filepath.Join(imagesDir, "alp.star"),
		"load(\"//classes/image.star\", \"image\")\n"+
			`image(name = "alp-image", distro = "alpine", artifacts = ["linux", "busybox"])`+"\n")
	for i, name := range skipped {
		write(filepath.Join(imagesDir, "skipped-"+string(rune('a'+i))+".star"),
			"load(\"//classes/image.star\", \"image\")\n"+
				`image(name = "`+name+`", distro = "otherdistro", artifacts = ["no-such-unit"])`+"\n")
	}
	return root
}

// resolveAlpImage loads a compat project and returns the surviving alpine
// image plus its input hash.
func resolveAlpImage(t *testing.T, skipped []string) (*yoestar.Unit, string) {
	t.Helper()
	proj, err := yoestar.LoadProjectFromRoot(writeCompatProject(t, skipped))
	if err != nil {
		t.Fatalf("LoadProjectFromRoot: %v", err)
	}
	u := proj.LookupUnit("alpine", "alp-image")
	if u == nil {
		t.Fatal("alp-image not registered")
	}
	dag, err := BuildDAG(proj, "alpine")
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	hashes, err := ComputeAllHashes(dag, "arm64", "board", nil, "alpine")
	if err != nil {
		t.Fatalf("ComputeAllHashes: %v", err)
	}
	// A skipped image must contribute no build edges — it can never
	// build, so an edge from it would schedule work for nothing.
	for _, name := range skipped {
		node, ok := dag.Nodes[name]
		if !ok {
			continue // registered under another distro's view; fine
		}
		if len(node.Deps) != 0 {
			t.Errorf("skipped image %s has build edges %v; it must be inert", name, node.Deps)
		}
	}
	return u, hashes["alp-image"]
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestSkippedImagesDoNotPerturbResolution: skipping an image changes the
// set of images that resolve at all, and resolution registers what it
// touches into a catalog shared by every later walk. If that reordering
// leaked, a surviving image could resolve differently — and silently, since
// nothing would error. Assert it doesn't: same artifacts, same input hash,
// with and without the skipped images present.
func TestSkippedImagesDoNotPerturbResolution(t *testing.T) {
	withSkipped, hashWith := resolveAlpImage(t, []string{"other-image-1", "other-image-2"})
	alone, hashAlone := resolveAlpImage(t, nil)

	// Compared as sets: the closure walker's topological pass seeds
	// itself from a Go map, so the emitted order already varies run to
	// run regardless of this change. The membership is the property, and
	// the hash (which sorts the list) is the thing that must not move.
	if !reflect.DeepEqual(sortedCopy(withSkipped.Artifacts), sortedCopy(alone.Artifacts)) {
		t.Errorf("artifact set moved when skipped images were present:\n with: %v\n without: %v",
			withSkipped.Artifacts, alone.Artifacts)
	}
	if hashWith != hashAlone {
		t.Errorf("input hash moved when skipped images were present: %s vs %s", hashWith, hashAlone)
	}
	if hashWith == "" {
		t.Fatal("no hash computed for alp-image")
	}
}

// TestUnbuildableMarkerIsCacheNeutral: the marker fields must not reach
// UnitHash. An ungated hash line here would invalidate every unit's cache
// the moment it landed and force a full rebuild.
func TestUnbuildableMarkerIsCacheNeutral(t *testing.T) {
	base := &yoestar.Unit{
		Name:    "some-image",
		Version: "1.0",
		Class:   "image",
		Tasks:   []yoestar.Task{{Name: "rootfs", Steps: []yoestar.Step{{Command: "true"}}}},
	}
	before := UnitHash(base, "arm64", nil, "", "alpine")

	marked := *base
	marked.UnbuildableMachine = "arduino-uno-q"
	marked.MachineKernelDistros = []string{"debian"}
	after := UnitHash(&marked, "arm64", nil, "", "alpine")

	if before != after {
		t.Errorf("machine-compat marker changed the input hash (%s -> %s); it must stay out of the cache key",
			before, after)
	}
}

// TestDescribe_UnbuildableImage: `yoe desc` on a marked image must say
// why and qualify the hash. An unqualified hash on a unit whose artifact
// list was never resolved invites comparison against a real one.
func TestDescribe_UnbuildableImage(t *testing.T) {
	proj := &yoestar.Project{
		Name: "compat",
		UnitsByModule: map[string]map[string]*yoestar.Unit{"": {
			"bun-image": {
				Name: "bun-image", Version: "1.0", Class: "image", Distro: "alpine",
				UnbuildableMachine:   "arduino-uno-q",
				MachineKernelDistros: []string{"debian"},
			},
		}},
	}

	var buf strings.Builder
	if err := Describe(&buf, proj, "bun-image", "arm64"); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Buildable:    no",
		`machine "arduino-uno-q"`,
		"kernel supports: debian",
		"not a build key",
		"not resolved for machine",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("desc output missing %q:\n%s", want, out)
		}
	}
}
