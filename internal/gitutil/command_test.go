package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Command must actually put the stall configuration in front of the
// subcommand. Asserting on the argv rather than on behavior keeps this cheap
// and does not need a network peer that misbehaves on demand.
func TestCommandCarriesStallGuard(t *testing.T) {
	cmd := Command("/some/dir", "clone", "--bare", "https://example.invalid/r.git")

	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"http.lowSpeedLimit=1000", "http.lowSpeedTime=60"} {
		if !strings.Contains(args, want) {
			t.Errorf("argv missing %s: %v", want, cmd.Args)
		}
	}
	// git only accepts -c before the subcommand.
	if i, j := indexOf(cmd.Args, "-c"), indexOf(cmd.Args, "clone"); i > j {
		t.Errorf("-c appears after the subcommand: %v", cmd.Args)
	}
	if cmd.Dir != "/some/dir" {
		t.Errorf("Dir = %q, want /some/dir", cmd.Dir)
	}
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// git must accept the configuration this package injects. A typo in a config
// key is silently ignored by git, so asserting on argv alone would not catch
// it — read the values back out of a real git process.
func TestGitAcceptsStallGuard(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if out, err := Command(dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	for _, tc := range []struct{ key, want string }{
		{"http.lowSpeedLimit", "1000"},
		{"http.lowSpeedTime", "60"},
	} {
		out, err := Command(dir, "config", "--get", tc.key).Output()
		if err != nil {
			t.Errorf("git did not accept %s: %v", tc.key, err)
			continue
		}
		if got := strings.TrimSpace(string(out)); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// Every git invocation in the tree must be built here. A raw exec.Command
// silently opts out of the stall guard, and the resulting hang only shows up
// against a remote that has gone silent — which is not something the rest of
// the suite can reproduce. Catching it structurally is the cheap option.
//
// The guard is inert for local commands, so there is no category of git call
// that legitimately needs to bypass it.
func TestNoRawGitInvocations(t *testing.T) {
	root := "../.."
	var offenders []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "testdata", "build", "cache", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Test helpers drive local temp repositories and never reach the
		// network, so the guard buys them nothing.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// This package is where the wrapper itself lives.
		if filepath.Clean(filepath.Dir(path)) == filepath.Clean(filepath.Join(root, "internal/gitutil")) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, `exec.Command("git"`) ||
				strings.Contains(line, `exec.CommandContext(ctx, "git"`) {
				offenders = append(offenders, filepath_line(path, i+1, line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("git invoked without the stall guard in %d place(s); use gitutil.Command:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func filepath_line(path string, n int, line string) string {
	return path + ":" + itoa(n) + ": " + strings.TrimSpace(line)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
