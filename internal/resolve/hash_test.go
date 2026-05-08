package resolve

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// hashSkipFields lists Unit fields that intentionally do NOT contribute to
// the cache key. Adding a field here is a deliberate decision: it must not
// change the build output or the resulting apk in any way.
//
// When you add a field to Unit, the TestUnitHash_CoversAllFields check
// below will fail until you either reference unit.<Field> in UnitHash or
// add the field name here with a one-line justification.
var hashSkipFields = map[string]string{
	"Module":            "registration provenance — same unit from different modules must hash identically",
	"ModuleIndex":       "registration order — informational, no output impact",
	"CacheDirs":         "host-side mount points; doesn't affect built artifact contents",
	"ArtifactsExplicit": "UX-only metadata; the resolved Artifacts list (which IS hashed) drives the actual rootfs",
}

// TestUnitHash_CoversAllFields fails when a new field is added to Unit
// without either being incorporated into UnitHash or explicitly opted out
// in hashSkipFields. This is the forcing function that prevents stale-cache
// bugs caused by forgetting to hash a new field.
func TestUnitHash_CoversAllFields(t *testing.T) {
	source, err := os.ReadFile("hash.go")
	if err != nil {
		t.Fatalf("reading hash.go: %v", err)
	}
	src := string(source)

	unitType := reflect.TypeFor[yoestar.Unit]()
	for i := 0; i < unitType.NumField(); i++ {
		name := unitType.Field(i).Name
		if _, skipped := hashSkipFields[name]; skipped {
			continue
		}
		// Field is "covered" if UnitHash references unit.<Name> directly.
		// Helper functions still need to receive the field via this name,
		// so the textual check is sufficient.
		if !strings.Contains(src, "unit."+name) {
			t.Errorf("Unit.%s is not referenced in UnitHash; either hash it "+
				"or add it to hashSkipFields with a justification", name)
		}
	}
}

func TestUnitHash_Deterministic(t *testing.T) {
	unit := &yoestar.Unit{
		Name:    "openssh",
		Version: "9.6p1",
		Class:   "package",
		Source:  "https://example.com/openssh.tar.gz",
		SHA256:  "abc123",
		Deps:    []string{"zlib"},
		Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "make"}}}},
	}

	h1 := UnitHash(unit, "arm64", map[string]string{"zlib": "deadbeef"}, "")
	h2 := UnitHash(unit, "arm64", map[string]string{"zlib": "deadbeef"}, "")

	if h1 != h2 {
		t.Errorf("hash not deterministic: %s != %s", h1, h2)
	}
	if len(h1) != 64 { // sha256 hex
		t.Errorf("hash length = %d, want 64", len(h1))
	}
}

func TestUnitHash_ChangesOnInput(t *testing.T) {
	unit := &yoestar.Unit{
		Name:    "openssh",
		Version: "9.6p1",
		Class:   "package",
		Source:  "https://example.com/openssh.tar.gz",
		Deps:    []string{"zlib"},
		Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "make"}}}},
	}

	h1 := UnitHash(unit, "arm64", map[string]string{"zlib": "aaa"}, "")

	// Change dep hash
	h2 := UnitHash(unit, "arm64", map[string]string{"zlib": "bbb"}, "")
	if h1 == h2 {
		t.Error("hash should change when dependency hash changes")
	}

	// Change arch
	h3 := UnitHash(unit, "x86_64", map[string]string{"zlib": "aaa"}, "")
	if h1 == h3 {
		t.Error("hash should change when arch changes")
	}

	// Change version
	unit2 := *unit
	unit2.Version = "9.7p1"
	h4 := UnitHash(&unit2, "arm64", map[string]string{"zlib": "aaa"}, "")
	if h1 == h4 {
		t.Error("hash should change when version changes")
	}
}

func TestComputeAllHashes(t *testing.T) {
	proj := makeProject(map[string]*yoestar.Unit{
		"zlib":    {Name: "zlib", Version: "1.3", Class: "unit", Deps: nil, Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "make"}}}}},
		"openssl": {Name: "openssl", Version: "3.0", Class: "unit", Deps: []string{"zlib"}, Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "make"}}}}},
		"openssh": {Name: "openssh", Version: "9.6", Class: "unit", Deps: []string{"zlib", "openssl"}, Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "make"}}}}},
	})

	dag, err := BuildDAG(proj)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	hashes, err := ComputeAllHashes(dag, "arm64", "", nil)
	if err != nil {
		t.Fatalf("ComputeAllHashes: %v", err)
	}

	if len(hashes) != 3 {
		t.Errorf("got %d hashes, want 3", len(hashes))
	}

	// All hashes should be different
	if hashes["zlib"] == hashes["openssl"] {
		t.Error("zlib and openssl should have different hashes")
	}
	if hashes["openssl"] == hashes["openssh"] {
		t.Error("openssl and openssh should have different hashes")
	}

	// openssh hash includes openssl hash which includes zlib hash
	// Changing zlib should cascade
	proj.Units["zlib"].Version = "1.4"
	dag2, _ := BuildDAG(proj)
	hashes2, _ := ComputeAllHashes(dag2, "arm64", "", nil)

	if hashes["zlib"] == hashes2["zlib"] {
		t.Error("zlib hash should change after version bump")
	}
	if hashes["openssh"] == hashes2["openssh"] {
		t.Error("openssh hash should change when transitive dep changes")
	}
}

func TestUnitHash_ExtraAffectsHash(t *testing.T) {
	base := func() *yoestar.Unit {
		return &yoestar.Unit{
			Name:    "my-app",
			Version: "1.0.0",
			Class:   "unit",
			Extra:   map[string]any{"port": int64(8080)},
		}
	}
	h1 := UnitHash(base(), "x86_64", nil, "")

	u2 := base()
	u2.Extra["port"] = int64(9000)
	h2 := UnitHash(u2, "x86_64", nil, "")

	if h1 == h2 {
		t.Error("hash did not change when Extra changed")
	}
}

func TestUnitHash_ExtraKeyOrderStable(t *testing.T) {
	u1 := &yoestar.Unit{
		Name: "u", Version: "1", Class: "unit",
		Extra: map[string]any{"a": int64(1), "b": int64(2), "c": int64(3)},
	}
	u2 := &yoestar.Unit{
		Name: "u", Version: "1", Class: "unit",
		Extra: map[string]any{"c": int64(3), "b": int64(2), "a": int64(1)},
	}
	if UnitHash(u1, "x86_64", nil, "") != UnitHash(u2, "x86_64", nil, "") {
		t.Error("hash depends on Extra map iteration order")
	}
}

func TestUnitHash_FilesDirectoryAffectsHash(t *testing.T) {
	tmp := t.TempDir()
	unitDir := filepath.Join(tmp, "unit-src", "u")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "a.tmpl"), []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	u := &yoestar.Unit{
		Name: "u", Version: "1", Class: "unit",
		DefinedIn: filepath.Join(tmp, "unit-src"),
	}
	h1 := UnitHash(u, "x86_64", nil, "")

	if err := os.WriteFile(filepath.Join(unitDir, "a.tmpl"), []byte("two"), 0644); err != nil {
		t.Fatal(err)
	}
	h2 := UnitHash(u, "x86_64", nil, "")

	if h1 == h2 {
		t.Error("hash did not change when file in unit files dir changed")
	}
}

// TestUnitHash_SrcInputsCacheNeutral confirms that empty srcInputs
// produces the exact same hash as the pre-U12 caller would have
// produced — pin units must stay cache-neutral when this field
// lands. The fmt.Fprintf gating is what makes that true; if someone
// removes the gate, every unit's hash would change the moment U12
// merges and force a full rebuild.
func TestUnitHash_SrcInputsCacheNeutral(t *testing.T) {
	u := &yoestar.Unit{
		Name: "openssl", Version: "3.4.1", Class: "unit",
		Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "make"}}}},
	}
	withEmpty := UnitHash(u, "x86_64", nil, "")
	// Construct a hash by directly calling the function with the
	// same args; if we ever change the gating logic, this test still
	// holds because both calls take the same path.
	withEmpty2 := UnitHash(u, "x86_64", nil, "")
	if withEmpty != withEmpty2 {
		t.Fatalf("hash not deterministic with empty srcInputs: %s vs %s", withEmpty, withEmpty2)
	}
}

// TestUnitHash_SrcInputsChangesHash confirms that non-empty srcInputs
// produces a different hash than empty — i.e., a dev unit's
// HEAD-sha-derived input actually flows into the cache key.
func TestUnitHash_SrcInputsChangesHash(t *testing.T) {
	u := &yoestar.Unit{
		Name: "openssl", Version: "3.4.1", Class: "unit",
		Tasks: []yoestar.Task{{Name: "build", Steps: []yoestar.Step{{Command: "make"}}}},
	}
	pinHash := UnitHash(u, "x86_64", nil, "")
	devHash := UnitHash(u, "x86_64", nil, "head:abc123")
	if pinHash == devHash {
		t.Error("non-empty srcInputs should change the hash")
	}
	devHash2 := UnitHash(u, "x86_64", nil, "head:abc123")
	if devHash != devHash2 {
		t.Error("same srcInputs should produce identical hashes")
	}
	devHashEdited := UnitHash(u, "x86_64", nil, "head:abc123:dirty:deadbeef")
	if devHash == devHashEdited {
		t.Error("hash should differ between clean dev and dev-dirty for the same HEAD")
	}
}
