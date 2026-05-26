package apkindex

import (
	"strings"
	"testing"
)

// realisticFixture is two APKINDEX entries copied from Alpine v3.21
// (manually trimmed). Two entries make the blank-line splitter
// observable in tests.
const realisticFixture = `C:Q1wmRLywlDhwD28lS6Qlp6nGlzzIk=
P:openssh-server
V:9.9_p2-r0
A:x86_64
S:482001
I:1138688
T:Port of OpenBSD's free SSH release - server
U:https://www.openssh.com/portable.html
L:BSD-2-Clause
o:openssh
m:Natanael Copa <ncopa@alpinelinux.org>
t:1736362800
c:abc123def456
D:openssh-keygen=9.9_p2-r0 so:libc.musl-x86_64.so.1 so:libcrypto.so.3
p:cmd:sshd
i:openssh
F:etc/init.d/sshd

C:Q1gccWqxnp4T7mk08WsE7/XtS4YI4=
P:openssh
V:9.9_p2-r0
A:x86_64
S:1024
I:4096
T:Port of OpenBSD's free SSH release
U:https://www.openssh.com/portable.html
L:BSD-2-Clause
o:openssh
D:openssh-client=9.9_p2-r0 openssh-server=9.9_p2-r0
`

func TestParseIndex_Realistic(t *testing.T) {
	entries, err := ParseIndex(strings.NewReader(realisticFixture))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(entries))
	}

	srv := entries[0]
	if srv.Name != "openssh-server" {
		t.Errorf("Name: got %q, want %q", srv.Name, "openssh-server")
	}
	if srv.Version != "9.9_p2-r0" {
		t.Errorf("Version: got %q", srv.Version)
	}
	if srv.Arch != "x86_64" {
		t.Errorf("Arch: got %q", srv.Arch)
	}
	if srv.Size != 482001 {
		t.Errorf("Size: got %d", srv.Size)
	}
	if srv.InstalledSize != 1138688 {
		t.Errorf("InstalledSize: got %d", srv.InstalledSize)
	}
	if srv.Origin != "openssh" {
		t.Errorf("Origin: got %q", srv.Origin)
	}
	if srv.Commit != "abc123def456" {
		t.Errorf("Commit: got %q", srv.Commit)
	}
	if srv.BuildTime != 1736362800 {
		t.Errorf("BuildTime: got %d", srv.BuildTime)
	}
	if srv.ChecksumText != "Q1wmRLywlDhwD28lS6Qlp6nGlzzIk=" {
		t.Errorf("ChecksumText: got %q", srv.ChecksumText)
	}
	if len(srv.Checksum) != 20 {
		t.Errorf("Checksum: want 20 bytes, got %d", len(srv.Checksum))
	}
	if got, want := len(srv.Deps), 3; got != want {
		t.Errorf("Deps len: got %d, want %d", got, want)
	}
	if got := srv.Deps[1]; got != "so:libc.musl-x86_64.so.1" {
		t.Errorf("Deps[1]: got %q", got)
	}
	if got, want := srv.Provides, []string{"cmd:sshd"}; !equalSlice(got, want) {
		t.Errorf("Provides: got %v, want %v", got, want)
	}
	if got, want := srv.InstallIf, []string{"openssh"}; !equalSlice(got, want) {
		t.Errorf("InstallIf: got %v, want %v", got, want)
	}
}

func TestParseIndex_MissingP(t *testing.T) {
	// A block with no P: line is malformed — surface it.
	input := "V:1.0\nA:x86_64\n"
	_, err := ParseIndex(strings.NewReader(input))
	if err == nil {
		t.Fatal("ParseIndex: want error for block with no P:, got nil")
	}
	if !strings.Contains(err.Error(), "no P:") {
		t.Errorf("error: got %v, want message naming missing P:", err)
	}
}

func TestParseIndex_BadChecksum(t *testing.T) {
	// Q1 prefix but wrong base64 length.
	input := "P:foo\nV:1.0\nC:Q1abc=\n"
	_, err := ParseIndex(strings.NewReader(input))
	if err == nil {
		t.Fatal("want error for bad checksum")
	}
}

func TestParseIndex_UnknownKeyIgnored(t *testing.T) {
	// Future Alpine adds a new field "X:..."; we keep parsing.
	input := "P:foo\nV:1.0\nX:future\n"
	entries, err := ParseIndex(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "foo" {
		t.Errorf("entries: got %+v", entries)
	}
}

func TestParseIndex_Empty(t *testing.T) {
	entries, err := ParseIndex(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries: got %d, want 0", len(entries))
	}
}

func TestParseIndex_BadSize(t *testing.T) {
	input := "P:foo\nV:1.0\nS:not-a-number\n"
	_, err := ParseIndex(strings.NewReader(input))
	if err == nil {
		t.Fatal("want error for bad size")
	}
	if !strings.Contains(err.Error(), "S:") {
		t.Errorf("error: got %v, want S: context", err)
	}
}

func TestParseIndex_TrailingBlankLines(t *testing.T) {
	// Real APKINDEX files end with a final blank line; verify the
	// flush logic doesn't drop the last entry.
	input := "P:foo\nV:1.0\n\n\nP:bar\nV:2.0\n\n"
	entries, err := ParseIndex(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(entries))
	}
	if entries[0].Name != "foo" || entries[1].Name != "bar" {
		t.Errorf("names: got %q, %q", entries[0].Name, entries[1].Name)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
