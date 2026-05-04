package image

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestAssemble(t *testing.T) {
	t.Skip("image assembly moved to Starlark (classes/image.star); Go Assemble() is unused")
	projectDir := t.TempDir()
	outputDir := filepath.Join(projectDir, "build", "output")

	// Create a fake local repo with minimal .apk files (flat, scope in filename).
	// RepoDir scopes by project name, so use repo/<project-name>/.
	repoDir := filepath.Join(projectDir, "repo", "test")
	os.MkdirAll(repoDir, 0755)
	for _, pkg := range []string{"openssh-9.0-r0.x86_64.apk", "myapp-1.0-r0.x86_64.apk"} {
		createFakeAPK(t, repoDir, pkg)
	}

	// Create an overlay
	overlayDir := filepath.Join(projectDir, "overlays", "etc", "myapp")
	os.MkdirAll(overlayDir, 0755)
	os.WriteFile(filepath.Join(overlayDir, "config.toml"), []byte("key = \"value\"\n"), 0644)

	unit := &yoestar.Unit{
		Name:     "test-image",
		Version:  "1.0.0",
		Class:    "image",
		Artifacts: []string{"openssh", "myapp"},
		Hostname: "yoe-test",
		Timezone: "UTC",
		Locale:   "en_US.UTF-8",
		Services: []string{"sshd", "myapp"},
	}

	proj := &yoestar.Project{
		Name: "test",
	}

	var buf bytes.Buffer
	if err := Assemble(unit, proj, projectDir, outputDir, "x86_64", "qemu-x86_64", &buf); err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	output := buf.String()

	// Check hostname was set
	hostname, _ := os.ReadFile(filepath.Join(outputDir, "rootfs", "etc", "hostname"))
	if strings.TrimSpace(string(hostname)) != "yoe-test" {
		t.Errorf("hostname = %q, want %q", string(hostname), "yoe-test")
	}

	// Check timezone symlink
	localtime := filepath.Join(outputDir, "rootfs", "etc", "localtime")
	link, err := os.Readlink(localtime)
	if err != nil {
		t.Errorf("localtime symlink: %v", err)
	} else if link != "/usr/share/zoneinfo/UTC" {
		t.Errorf("localtime = %q, want UTC", link)
	}

	// Check services enabled
	sshLink := filepath.Join(outputDir, "rootfs", "etc", "systemd", "system",
		"multi-user.target.wants", "sshd.service")
	if _, err := os.Lstat(sshLink); os.IsNotExist(err) {
		t.Error("sshd service not enabled")
	}

	// Check overlay was applied
	overlayFile := filepath.Join(outputDir, "rootfs", "etc", "myapp", "config.toml")
	if _, err := os.Stat(overlayFile); os.IsNotExist(err) {
		t.Error("overlay file not applied")
	}

	// Check disk image was generated
	imgPath := filepath.Join(outputDir, "test-image.img")
	if _, err := os.Stat(imgPath); os.IsNotExist(err) {
		t.Error("disk image not generated")
	}

	// Check output messages
	if !strings.Contains(output, "hostname") {
		t.Error("output should mention hostname")
	}
	if !strings.Contains(output, "sshd") {
		t.Error("output should mention sshd service")
	}
}

// createFakeAPK creates a minimal .apk file (gzip'd tar) in repoDir.
func createFakeAPK(t *testing.T, repoDir, name string) {
	t.Helper()
	apkPath := filepath.Join(repoDir, name)
	// Create a tar.gz with a single .PKGINFO file
	cmd := exec.Command("sh", "-c",
		"echo 'pkgname=test' | tar czf "+apkPath+" --files-from=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("creating fake apk %s: %v\n%s", name, err, out)
	}
}

func TestApplyConfig_Empty(t *testing.T) {
	rootfs := filepath.Join(t.TempDir(), "rootfs")
	os.MkdirAll(rootfs, 0755)

	unit := &yoestar.Unit{Name: "empty"}
	var buf bytes.Buffer
	if err := applyConfig(rootfs, unit, &buf); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	// Should succeed with no config to apply
}
