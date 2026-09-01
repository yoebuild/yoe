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

func TestSSHToHTTPS(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"git@github.com:foo/bar.git", "https://github.com/foo/bar.git", true},
		{"git@gitlab.com:group/sub/proj.git", "https://gitlab.com/group/sub/proj.git", true},
		{"ssh://git@github.com/foo/bar.git", "https://github.com/foo/bar.git", true},
		{"ssh://github.com/foo/bar.git", "https://github.com/foo/bar.git", true},
		// Already https, or no path to move — left alone.
		{"https://github.com/foo/bar.git", "https://github.com/foo/bar.git", false},
		{"git@github.com:", "git@github.com:", false},
		{"/local/path/repo.git", "/local/path/repo.git", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := SSHToHTTPS(c.in)
		if ok != c.wantOK || got != c.want {
			t.Errorf("SSHToHTTPS(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// Round-tripping an https URL through both rewrites returns the
// original, so the prompt can offer either scheme from either start.
func TestHTTPSToSSHRoundTrip(t *testing.T) {
	for _, in := range []string{
		"https://github.com/foo/bar.git",
		"https://git.example.com/deep/group/proj.git",
	} {
		ssh, ok := HTTPSToSSH(in)
		if !ok {
			t.Fatalf("HTTPSToSSH(%q) failed", in)
		}
		back, ok := SSHToHTTPS(ssh)
		if !ok || back != in {
			t.Errorf("round trip %q -> %q -> (%q, %v), want %q", in, ssh, back, ok, in)
		}
	}
}
