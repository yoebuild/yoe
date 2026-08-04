package build

import (
	"strings"
	"testing"
)

func TestBwrapCommand(t *testing.T) {
	cfg := &SandboxConfig{
		SrcDir:  "/tmp/src",
		DestDir: "/tmp/dest",
		Sysroot: "/tmp/sysroot",
		Env: map[string]string{
			"PREFIX": "/usr",
			"NPROC":  "4",
		},
	}

	cmd := bwrapCommand(cfg, "make -j4")

	if !strings.HasPrefix(cmd, "bwrap ") {
		t.Errorf("command should start with 'bwrap ': %s", cmd)
	}
	if !strings.Contains(cmd, "--bind / /") {
		t.Errorf("should bind root: %s", cmd)
	}
	if !strings.Contains(cmd, "--bind /build/src /build/src") {
		t.Errorf("should bind src: %s", cmd)
	}
	if !strings.Contains(cmd, "--bind /build/destdir /build/destdir") {
		t.Errorf("should bind dest: %s", cmd)
	}
	if !strings.Contains(cmd, "--ro-bind /build/sysroot /build/sysroot") {
		t.Errorf("should ro-bind sysroot: %s", cmd)
	}
	if !strings.Contains(cmd, "make -j4") {
		t.Errorf("should contain build command: %s", cmd)
	}
	if !strings.Contains(cmd, "export PREFIX=") {
		t.Errorf("should export PREFIX: %s", cmd)
	}
}

func TestBwrapCommand_InnerCommandQuoted(t *testing.T) {
	cfg := &SandboxConfig{
		SrcDir:  "/tmp/src",
		DestDir: "/tmp/dest",
		Env: map[string]string{
			"PREFIX": "/usr",
		},
	}

	cmd := bwrapCommand(cfg, "make -j4")

	// The inner sh -c argument must be single-quoted so semicolons in env
	// exports don't get interpreted by the outer shell.
	if !strings.Contains(cmd, "-- sh -c '") {
		t.Errorf("inner command should be single-quoted: %s", cmd)
	}
	// Should end with closing quote
	if !strings.HasSuffix(cmd, "'") {
		t.Errorf("command should end with closing single quote: %s", cmd)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"simple", "'simple'"},
		{"has space", "'has space'"},
		{"has;semicolon", "'has;semicolon'"},
		{"it's quoted", "'it'\\''s quoted'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.in)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestContainerMountsForBuild(t *testing.T) {
	cfg := &SandboxConfig{
		SrcDir:  "/home/user/project/build/zlib/src",
		DestDir: "/home/user/project/build/zlib/destdir",
		Sysroot: "/home/user/project/build/sysroot",
	}

	mounts := containerMountsForBuild(cfg)

	if len(mounts) != 3 {
		t.Fatalf("expected 3 mounts, got %d", len(mounts))
	}

	for _, m := range mounts {
		if m.Container == "/build/sysroot" && !m.ReadOnly {
			t.Error("sysroot mount should be read-only")
		}
	}
}
