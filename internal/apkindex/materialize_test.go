package apkindex

import (
	"strings"
	"testing"
)

// fixtureProviders returns a Providers that resolves through a single
// in-memory table built from a small set of entries.
func fixtureProviders(t *testing.T, entries []Entry) Providers {
	t.Helper()
	return TableProviders{Table: BuildProvidesTable(entries)}
}

func TestMaterializeUnit_BasicPackage(t *testing.T) {
	libs := []Entry{
		{Name: "musl", Version: "1.2.5-r10",
			Provides: []string{"so:libc.musl-x86_64.so.1=1"}},
		{Name: "openssl-libs", Version: "3.5.4-r0",
			Provides: []string{"so:libcrypto.so.3=3.5.4", "so:libssl.so.3=3.5.4"}},
	}
	entry := Entry{
		Name:         "openssh-server",
		Version:      "9.9_p2-r0",
		Description:  "Port of OpenBSD's free SSH release - server",
		License:      "BSD-2-Clause",
		ChecksumText: "Q1wmRLywlDhwD28lS6Qlp6nGlzzIk=",
		Deps: []string{
			"so:libc.musl-x86_64.so.1",
			"so:libcrypto.so.3=3.5.4-r0",
			"so:libssl.so.3=3.5.4-r0",
		},
		Provides: []string{"cmd:sshd"},
	}
	all := append([]Entry{entry}, libs...)
	u, err := MaterializeUnit(entry, fixtureProviders(t, all), "alpine.main")
	if err != nil {
		t.Fatalf("MaterializeUnit: %v", err)
	}

	if u.Name != "openssh-server" {
		t.Errorf("Name: %q", u.Name)
	}
	if u.Version != "9.9_p2" {
		t.Errorf("Version: %q (want 9.9_p2 — release stripped)", u.Version)
	}
	if u.Release != 0 {
		t.Errorf("Release: %d (want 0)", u.Release)
	}
	if u.APKChecksum == "" {
		t.Error("APKChecksum: empty")
	}
	if u.Module != "alpine.main" {
		t.Errorf("Module: %q", u.Module)
	}
	if got, want := u.RuntimeDeps, []string{"musl", "openssl-libs"}; !sameSet(got, want) {
		t.Errorf("RuntimeDeps: got %v, want %v", got, want)
	}
	if len(u.Provides) != 0 {
		t.Errorf("Provides: got %v, want empty (cmd: filtered)", u.Provides)
	}
}

func TestMaterializeUnit_DropsConflictsAndPaths(t *testing.T) {
	libs := []Entry{
		{Name: "musl", Version: "1.0"},
	}
	entry := Entry{
		Name:    "tool",
		Version: "1.0-r0",
		Deps:    []string{"musl", "!busybox", "/etc/passwd"},
	}
	all := append([]Entry{entry}, libs...)
	u, err := MaterializeUnit(entry, fixtureProviders(t, all), "alpine.main")
	if err != nil {
		t.Fatalf("MaterializeUnit: %v", err)
	}
	if got, want := u.RuntimeDeps, []string{"musl"}; !sameSet(got, want) {
		t.Errorf("RuntimeDeps: got %v, want %v", got, want)
	}
}

func TestMaterializeUnit_UnresolvedDep(t *testing.T) {
	entry := Entry{
		Name:    "thing",
		Version: "1.0-r0",
		Deps:    []string{"so:libnope.so.999"},
	}
	_, err := MaterializeUnit(entry, fixtureProviders(t, []Entry{entry}), "alpine.main")
	if err == nil {
		t.Fatal("want error for unresolved dep")
	}
	if !strings.Contains(err.Error(), "so:libnope.so.999") {
		t.Errorf("error: %v (want token in message)", err)
	}
}

func TestMaterializeUnit_DedupsSamePackage(t *testing.T) {
	// musl is referenced twice: once by bare name, once via its
	// soname. Final runtime_deps must list it only once.
	musl := Entry{Name: "musl", Version: "1.0",
		Provides: []string{"so:libc.musl-x86_64.so.1=1"}}
	entry := Entry{
		Name:    "consumer",
		Version: "1.0-r0",
		Deps:    []string{"musl", "so:libc.musl-x86_64.so.1"},
	}
	u, err := MaterializeUnit(entry, fixtureProviders(t, []Entry{musl, entry}), "alpine.main")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.RuntimeDeps, []string{"musl"}; !sameSet(got, want) {
		t.Errorf("RuntimeDeps: got %v, want %v", got, want)
	}
}

func TestMaterializeUnit_SkipsSelfReference(t *testing.T) {
	// A package that lists itself in its own deps (origin-pkg pattern).
	entry := Entry{
		Name:    "openssh-client",
		Version: "9.9_p2-r0",
		Deps:    []string{"openssh-client"},
	}
	u, err := MaterializeUnit(entry, fixtureProviders(t, []Entry{entry}), "alpine.main")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.RuntimeDeps) != 0 {
		t.Errorf("RuntimeDeps: got %v, want empty (self-ref dropped)", u.RuntimeDeps)
	}
}

func TestMaterializeUnit_VersionSplit(t *testing.T) {
	cases := []struct {
		in           string
		wantVer      string
		wantRel      int
	}{
		{"1.2.5-r11", "1.2.5", 11},
		{"9.9_p2-r0", "9.9_p2", 0},
		{"3.5.4", "3.5.4", 0},
		{"1.0-rNotInt", "1.0-rNotInt", 0},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			entry := Entry{Name: "x", Version: c.in}
			u, err := MaterializeUnit(entry, fixtureProviders(t, []Entry{entry}), "m")
			if err != nil {
				t.Fatal(err)
			}
			if u.Version != c.wantVer || u.Release != c.wantRel {
				t.Errorf("split(%q): got (%q, %d), want (%q, %d)",
					c.in, u.Version, u.Release, c.wantVer, c.wantRel)
			}
		})
	}
}

func TestFilterProvides(t *testing.T) {
	in := []string{"abi-versioned=2", "so:libfoo.so.1", "cmd:bar", "plain"}
	got := filterProvides(in)
	want := []string{"abi-versioned", "plain"}
	if !sameSet(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTableProviders_NilTable(t *testing.T) {
	p := TableProviders{}
	if name, ok := p.Resolve("foo"); ok || name != "" {
		t.Errorf("nil table: got (%q, %v), want (\"\", false)", name, ok)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}
