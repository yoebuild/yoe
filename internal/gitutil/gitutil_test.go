package gitutil

import "testing"

func TestHTTPSToSSH(t *testing.T) {
	cases := []struct {
		in         string
		want       string
		wantOK     bool
		wantSSHFmt bool
	}{
		{"https://github.com/foo/bar.git", "git@github.com:foo/bar.git", true, true},
		{"https://gitlab.com/foo/bar.git", "git@gitlab.com:foo/bar.git", true, true},
		{"https://example.com/path/to/repo.git", "git@example.com:path/to/repo.git", true, true},
		{"https://foo.example.com/x.git", "git@foo.example.com:x.git", true, true},
		{"git@github.com:foo/bar.git", "git@github.com:foo/bar.git", false, true},          // already SSH, no rewrite
		{"git://git.kernel.org/linux.git", "git://git.kernel.org/linux.git", false, false}, // git:// not handled
	}
	for _, c := range cases {
		got, ok := HTTPSToSSH(c.in)
		if got != c.want {
			t.Errorf("HTTPSToSSH(%q) = %q, want %q", c.in, got, c.want)
		}
		if ok != c.wantOK {
			t.Errorf("HTTPSToSSH(%q) ok=%v, want %v", c.in, ok, c.wantOK)
		}
	}
}
