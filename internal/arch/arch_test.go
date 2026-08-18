package arch

import (
	"slices"
	"testing"
)

// These values appear in repo directory names, PKGINFO, APKINDEX A:
// fields, .deb Architecture fields, and multiarch library paths. Getting
// one wrong publishes a package where the target's package manager never
// looks, so pin every mapping rather than spot-checking.
func TestTokens(t *testing.T) {
	cases := []struct {
		yoe, apk, deb, multiarch string
	}{
		{"x86_64", "x86_64", "amd64", "x86_64-linux-gnu"},
		{"arm64", "aarch64", "arm64", "aarch64-linux-gnu"},
		{"riscv64", "riscv64", "riscv64", "riscv64-linux-gnu"},
	}
	for _, c := range cases {
		if got := Apk(c.yoe); got != c.apk {
			t.Errorf("Apk(%q) = %q, want %q", c.yoe, got, c.apk)
		}
		if got := Deb(c.yoe); got != c.deb {
			t.Errorf("Deb(%q) = %q, want %q", c.yoe, got, c.deb)
		}
		if got := Multiarch(c.yoe); got != c.multiarch {
			t.Errorf("Multiarch(%q) = %q, want %q", c.yoe, got, c.multiarch)
		}
	}
}

// Translation passes an unknown token through; rejecting one is
// Validate's job. Multiarch is the exception — it returns "" so a caller
// joining it into a search path gets a collapsed duplicate rather than a
// plausible-looking directory that does not exist.
func TestUnknownArch(t *testing.T) {
	if got := Apk("sparc64"); got != "sparc64" {
		t.Errorf("Apk passthrough = %q", got)
	}
	if got := Deb("sparc64"); got != "sparc64" {
		t.Errorf("Deb passthrough = %q", got)
	}
	if got := Multiarch("sparc64"); got != "" {
		t.Errorf("Multiarch(unknown) = %q, want empty", got)
	}
	if err := Validate("sparc64"); err == nil {
		t.Error("Validate(\"sparc64\") = nil, want an error")
	}
	if err := Validate("arm64"); err != nil {
		t.Errorf("Validate(\"arm64\") = %v", err)
	}
}

func TestSupported(t *testing.T) {
	got := Supported()
	want := []string{"arm64", "riscv64", "x86_64"}
	if !slices.Equal(got, want) {
		t.Errorf("Supported() = %v, want %v", got, want)
	}
	if !IsSupported("arm64") || IsSupported("sparc64") {
		t.Error("IsSupported disagrees with Supported")
	}
}

func TestBinfmt(t *testing.T) {
	cases := map[string]string{
		"arm64":   "qemu-aarch64",
		"riscv64": "qemu-riscv64",
		"x86_64":  "qemu-x86_64",
	}
	for in, want := range cases {
		if got := Binfmt(in); got != want {
			t.Errorf("Binfmt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHost(t *testing.T) {
	// Whatever the host is, it must be a token yoe recognizes, and the
	// memoized second call must agree with the first.
	got := Host()
	if !IsSupported(got) {
		t.Errorf("Host() = %q, which is not a supported arch", got)
	}
	if again := Host(); again != got {
		t.Errorf("Host() returned %q then %q", got, again)
	}
}
