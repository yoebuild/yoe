package query

import (
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func depProject() *yoestar.Project {
	return &yoestar.Project{
		Units: map[string]*yoestar.Unit{
			"toolchain-musl": {Name: "toolchain-musl", Class: "container"},
			"zlib":           {Name: "zlib", Class: "unit", Deps: []string{"toolchain-musl"}},
			"openssl":        {Name: "openssl", Class: "unit", Deps: []string{"zlib", "toolchain-musl"}, RuntimeDeps: []string{"zlib"}},
			"apk-tools":      {Name: "apk-tools", Class: "unit", Deps: []string{"openssl", "zlib", "toolchain-musl"}, RuntimeDeps: []string{"openssl", "zlib"}},
			"base-image":     {Name: "base-image", Class: "image", Artifacts: []string{"openssl", "apk-tools"}},
		},
		Provides: map[string]string{},
	}
}

func TestClosure_Leaf(t *testing.T) {
	proj := depProject()
	got := BuildInClosure(proj, "toolchain-musl")
	if !got["toolchain-musl"] || len(got) != 1 {
		t.Fatalf("toolchain-musl closure: %v", got)
	}
}

func TestClosure_BuildDeps(t *testing.T) {
	proj := depProject()
	got := BuildInClosure(proj, "openssl")
	for _, want := range []string{"openssl", "zlib", "toolchain-musl"} {
		if !got[want] {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	if got["apk-tools"] {
		t.Fatalf("closure leaked upward: %v", got)
	}
}

func TestClosure_RuntimeDepsViaProvides(t *testing.T) {
	proj := depProject()
	proj.Units["libcrypto3"] = &yoestar.Unit{Name: "libcrypto3"}
	proj.Units["consumer"] = &yoestar.Unit{Name: "consumer", RuntimeDeps: []string{"libcrypto3"}}
	proj.Provides["libcrypto3"] = "openssl"
	got := BuildInClosure(proj, "consumer")
	if !got["openssl"] {
		t.Fatalf("expected libcrypto3 → openssl via Provides, got %v", got)
	}
	if got["libcrypto3"] {
		t.Fatalf("Provides routing should redirect, not include the virtual: %v", got)
	}
}

func TestClosure_ImageArtifacts(t *testing.T) {
	proj := depProject()
	got := BuildInClosure(proj, "base-image")
	for _, want := range []string{"base-image", "openssl", "apk-tools", "zlib", "toolchain-musl"} {
		if !got[want] {
			t.Fatalf("base-image closure missing %q in %v", want, got)
		}
	}
}

func TestClosure_Cycle(t *testing.T) {
	proj := &yoestar.Project{Units: map[string]*yoestar.Unit{
		"a": {Name: "a", Deps: []string{"b"}},
		"b": {Name: "b", Deps: []string{"a"}},
	}}
	got := BuildInClosure(proj, "a")
	if !got["a"] || !got["b"] || len(got) != 2 {
		t.Fatalf("cycle: %v", got)
	}
}

func TestClosure_MissingRoot(t *testing.T) {
	proj := depProject()
	if got := BuildInClosure(proj, "nonexistent"); got != nil {
		t.Fatalf("missing root: expected nil, got %v", got)
	}
}
