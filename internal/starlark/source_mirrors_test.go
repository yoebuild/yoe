package starlark

import (
	"os"
	"path/filepath"
	"testing"
)

func loadWithProject(t *testing.T, body string) (*Project, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "PROJECT.star"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadProject(dir)
}

const mirrorProject = `project(
    name = "m",
    version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64", image = "i", distro = "alpine"),
    source_mirrors = [
        ("https://ftp.gnu.org/gnu", "https://mirrors.kernel.org/gnu"),
        ("https://ftp.gnu.org/pub/gnu", "https://mirrors.kernel.org/gnu"),
    ],
)
`

func TestProjectParsesSourceMirrors(t *testing.T) {
	proj, err := loadWithProject(t, mirrorProject)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if len(proj.SourceMirrors) != 2 {
		t.Fatalf("got %d rules, want 2: %+v", len(proj.SourceMirrors), proj.SourceMirrors)
	}
	got, ok := proj.SourceMirrors[0].Apply("https://ftp.gnu.org/gnu/bash/bash-5.2.37.tar.gz")
	if !ok {
		t.Fatal("rule did not match its own prefix")
	}
	if want := "https://mirrors.kernel.org/gnu/bash/bash-5.2.37.tar.gz"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, ok := proj.SourceMirrors[0].Apply("https://example.com/bash.tar.gz"); ok {
		t.Error("rule matched an unrelated URL")
	}
}

// The pairs are load-bearing; a bare string or a wrong-length tuple is a
// typo worth reporting rather than silently ignoring.
func TestProjectRejectsMalformedSourceMirrors(t *testing.T) {
	for name, entry := range map[string]string{
		"bare string":   `"https://ftp.gnu.org/gnu"`,
		"single member": `("https://ftp.gnu.org/gnu",)`,
		"non-string":    `("https://ftp.gnu.org/gnu", 3)`,
		"empty prefix":  `("", "https://mirrors.kernel.org/gnu")`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `project(
    name = "m",
    version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64", image = "i", distro = "alpine"),
    source_mirrors = [` + entry + `],
)
`
			if _, err := loadWithProject(t, body); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}
