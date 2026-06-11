package apkindex

import (
	"sort"
	"testing"
)

func TestBuildProvidesTable_Basic(t *testing.T) {
	entries := []Entry{
		{Name: "openssl-libs", Version: "3.5.4-r0", Provides: []string{"so:libcrypto.so.3=3.5.4", "so:libssl.so.3=3.5.4"}},
		{Name: "openssh-server", Version: "9.9_p2-r0", Provides: []string{"cmd:sshd"}},
		{Name: "musl", Version: "1.2.5-r10", Provides: []string{"so:libc.musl-x86_64.so.1=1"}},
	}
	tbl := BuildProvidesTable(entries)

	cases := []struct {
		lookup, wantName string
	}{
		{"openssl-libs", "openssl-libs"},   // bare name self-provides
		{"so:libcrypto.so.3", "openssl-libs"},
		{"so:libssl.so.3", "openssl-libs"},
		{"cmd:sshd", "openssh-server"},
		{"so:libc.musl-x86_64.so.1", "musl"},
	}
	for _, c := range cases {
		e := tbl.Lookup(c.lookup)
		if e == nil {
			t.Errorf("Lookup(%q): got nil, want %q", c.lookup, c.wantName)
			continue
		}
		if e.Name != c.wantName {
			t.Errorf("Lookup(%q): got %q, want %q", c.lookup, e.Name, c.wantName)
		}
	}

	if got := tbl.Lookup("nope"); got != nil {
		t.Errorf("Lookup(nope): got %+v, want nil", got)
	}
}

func TestBuildProvidesTable_VersionTiebreaker(t *testing.T) {
	// Two providers of cmd:sendmail; newer version wins.
	entries := []Entry{
		{Name: "sendmail", Version: "8.17.1-r0", Provides: []string{"cmd:sendmail"}},
		{Name: "exim", Version: "4.97-r2", Provides: []string{"cmd:sendmail"}},
		{Name: "postfix", Version: "3.9.1-r0", Provides: []string{"cmd:sendmail"}},
	}
	tbl := BuildProvidesTable(entries)
	got := tbl.Lookup("cmd:sendmail")
	if got == nil {
		t.Fatal("Lookup: nil")
	}
	// 8.17.1 > 4.97 > 3.9.1 → sendmail wins.
	if got.Name != "sendmail" {
		t.Errorf("winner: got %q, want sendmail (newest version)", got.Name)
	}
}

func TestBuildProvidesTable_StripsVersionFromProvider(t *testing.T) {
	// `p:` tokens may carry `=version`; the lookup key is the bare name.
	entries := []Entry{
		{Name: "libfoo", Version: "2.0", Provides: []string{"libfoo-abi=2"}},
	}
	tbl := BuildProvidesTable(entries)
	if e := tbl.Lookup("libfoo-abi"); e == nil || e.Name != "libfoo" {
		t.Errorf("Lookup(libfoo-abi): got %+v", e)
	}
	// The "=2" suffix is not the lookup key.
	if e := tbl.Lookup("libfoo-abi=2"); e != nil {
		t.Errorf("Lookup with version: got %+v, want nil", e)
	}
}

func TestBuildProvidesTable_Names(t *testing.T) {
	entries := []Entry{
		{Name: "a", Provides: []string{"so:liba"}},
		{Name: "b", Provides: []string{"cmd:b"}},
	}
	tbl := BuildProvidesTable(entries)
	names := tbl.Names()
	sort.Strings(names)
	want := []string{"a", "b", "cmd:b", "so:liba"}
	if !equalSlice(names, want) {
		t.Errorf("Names: got %v, want %v", names, want)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0", "1.1", -1},
		{"1.10", "1.2", +1},
		{"1.0-r0", "1.0-r1", -1},
		{"1.0-r2", "1.0-r1", +1},
		{"2.0", "1.99", +1},
		{"1.0_pre1", "1.0", -1},
		{"1.0_rc2", "1.0", -1},
		{"1.0_rc1", "1.0_rc2", -1},
		{"3.5.4", "3.5.4", 0},
		{"3.5.4-r0", "3.5.4", 0},
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		// Normalize sign for comparison.
		gotSign := 0
		switch {
		case got < 0:
			gotSign = -1
		case got > 0:
			gotSign = +1
		}
		if gotSign != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
