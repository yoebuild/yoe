package apkindex

import (
	"strings"
	"testing"
)

func TestParseDep_Forms(t *testing.T) {
	cases := []struct {
		in       string
		kind     DepKind
		name     string
		op       Op
		version  string
		conflict bool
	}{
		{"musl", DepKindName, "musl", OpNone, "", false},
		{"musl>=1.2", DepKindName, "musl", OpGe, "1.2", false},
		{"musl<2", DepKindName, "musl", OpLt, "2", false},
		{"musl<=1.2", DepKindName, "musl", OpLe, "1.2", false},
		{"musl>1", DepKindName, "musl", OpGt, "1", false},
		{"musl=1.2.3-r0", DepKindName, "musl", OpEq, "1.2.3-r0", false},
		{"musl~1.2", DepKindName, "musl", OpTilde, "1.2", false},
		{"so:libcrypto.so.3", DepKindSo, "so:libcrypto.so.3", OpNone, "", false},
		{"so:libcrypto.so.3=3.5.4-r0", DepKindSo, "so:libcrypto.so.3", OpEq, "3.5.4-r0", false},
		{"cmd:gpg", DepKindCmd, "cmd:gpg", OpNone, "", false},
		{"cmd:gpg=2.0", DepKindCmd, "cmd:gpg", OpEq, "2.0", false},
		{"pc:libfoo", DepKindPc, "pc:libfoo", OpNone, "", false},
		{"/etc/passwd", DepKindPath, "/etc/passwd", OpNone, "", false},
		{"!busybox", DepKindName, "busybox", OpNone, "", true},
		{"!so:libold.so.1", DepKindSo, "so:libold.so.1", OpNone, "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			d, err := ParseDep(c.in)
			if err != nil {
				t.Fatalf("ParseDep(%q): %v", c.in, err)
			}
			if d.Kind != c.kind {
				t.Errorf("Kind: got %d, want %d", d.Kind, c.kind)
			}
			if d.Name != c.name {
				t.Errorf("Name: got %q, want %q", d.Name, c.name)
			}
			if d.Op != c.op {
				t.Errorf("Op: got %d, want %d", d.Op, c.op)
			}
			if d.Version != c.version {
				t.Errorf("Version: got %q, want %q", d.Version, c.version)
			}
			if d.Conflict != c.conflict {
				t.Errorf("Conflict: got %v, want %v", d.Conflict, c.conflict)
			}
			if d.Raw != c.in {
				t.Errorf("Raw: got %q, want %q", d.Raw, c.in)
			}
		})
	}
}

func TestParseDep_Errors(t *testing.T) {
	cases := []string{"", "!", "<1.0"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := ParseDep(c); err == nil {
				t.Errorf("ParseDep(%q): want error", c)
			}
		})
	}
}

func TestParseDeps_Mixed(t *testing.T) {
	in := []string{"musl>=1.2", "so:libcrypto.so.3=3.5.4-r0", "cmd:gpg", "/etc/passwd"}
	deps, err := ParseDeps(in)
	if err != nil {
		t.Fatalf("ParseDeps: %v", err)
	}
	if len(deps) != 4 {
		t.Fatalf("len: got %d, want 4", len(deps))
	}
	if deps[0].Kind != DepKindName || deps[0].Name != "musl" || deps[0].Op != OpGe {
		t.Errorf("deps[0]: %+v", deps[0])
	}
	if deps[1].Kind != DepKindSo || deps[1].Name != "so:libcrypto.so.3" {
		t.Errorf("deps[1]: %+v", deps[1])
	}
	if deps[2].Kind != DepKindCmd || deps[2].Name != "cmd:gpg" {
		t.Errorf("deps[2]: %+v", deps[2])
	}
	if deps[3].Kind != DepKindPath {
		t.Errorf("deps[3]: %+v", deps[3])
	}
}

func TestParseDeps_ErrorIndex(t *testing.T) {
	in := []string{"musl", "", "openssh"}
	_, err := ParseDeps(in)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "dep[1]") {
		t.Errorf("error: got %v, want index hint", err)
	}
}
