package artifact_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoebuild/yoe/internal/artifact"
	"github.com/yoebuild/yoe/internal/repo"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// TestAPKRoundTripWithUpstreamApk exercises yoe-built apks against the real
// apk-tools 2.14.x in an Alpine container. It builds a small package and
// runs `apk add` from upstream apk against it. Skipped if Docker isn't
// available.
//
// This is the gating test for Phase 1 of the apk-compat plan
// (docs/superpowers/plans/2026-04-29-apk-compat.md). It records the gaps
// upstream apk reports in yoe's output. As format fixes land, the list
// of acceptable warnings shrinks; eventually it should be empty (modulo
// the untrusted-signature warning, gated by phase 3).
func TestAPKRoundTripWithUpstreamApk(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	tmp := t.TempDir()
	destDir := filepath.Join(tmp, "destdir")
	if err := os.MkdirAll(filepath.Join(destDir, "usr/bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "usr/bin/hello"),
		[]byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(tmp, "out")
	unit := &yoestar.Unit{
		Name:        "hello",
		Version:     "1.0.0",
		License:     "MIT",
		Description: "test package for apk compat",
	}
	apkPath, err := artifact.CreateAPK(unit, destDir, out, "x86_64", "", nil)
	if err != nil {
		t.Fatalf("CreateAPK: %v", err)
	}

	// Direct install: hand the .apk to apk add and let it validate the
	// format. No index, no deps — purely a format check.
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(apkPath, filepath.Join(work, filepath.Base(apkPath))); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("docker", "run", "--rm",
		"-v", work+":/work:ro",
		"alpine:3.21",
		"sh", "-c",
		"apk add --allow-untrusted --root /tmp/test --initdb "+
			"/work/"+filepath.Base(apkPath)+" 2>&1; echo EXIT=$?")
	output, _ := cmd.CombinedOutput()
	t.Logf("upstream apk output:\n%s", string(output))

	report := categorizeApkOutput(string(output))
	if report.exitCode != 0 {
		t.Errorf("apk add exited with %d", report.exitCode)
	}
	for _, e := range report.errors {
		t.Errorf("upstream apk ERROR: %s", e)
	}
	for _, w := range report.unexpectedWarnings {
		t.Errorf("upstream apk WARNING: %s", w)
	}
}

// TestAPKRepoInstallWithUpstreamApk exercises the index path: build a
// yoe-style repo (Alpine layout, with APKINDEX) and ask upstream apk to
// install via `--repository`. This validates that the APKINDEX C: hash
// (control-stream SHA-1) matches what apk computes itself — i.e. that yoe
// and apk agree on package identity.
func TestAPKRepoInstallWithUpstreamApk(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	tmp := t.TempDir()
	destDir := filepath.Join(tmp, "destdir")
	if err := os.MkdirAll(filepath.Join(destDir, "usr/bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "usr/bin/hello"),
		[]byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Build the apk into Alpine repo layout: <repo>/<arch>/<pkg>.apk.
	repoDir := filepath.Join(tmp, "repo", "x86_64")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "out")
	unit := &yoestar.Unit{
		Name:        "hello",
		Version:     "1.0.0",
		License:     "MIT",
		Description: "test package for apk repo compat",
	}
	apkPath, err := artifact.CreateAPK(unit, destDir, out, "x86_64", "", nil)
	if err != nil {
		t.Fatalf("CreateAPK: %v", err)
	}
	dst := filepath.Join(repoDir, filepath.Base(apkPath))
	if err := copyFile(apkPath, dst); err != nil {
		t.Fatal(err)
	}

	// Generate APKINDEX with yoe's index code.
	if err := repo.GenerateIndex(repoDir, nil); err != nil {
		t.Fatalf("GenerateIndex: %v", err)
	}

	cmd := exec.Command("docker", "run", "--rm",
		"-v", filepath.Join(tmp, "repo")+":/repo:ro",
		"alpine:3.21",
		"sh", "-c",
		"apk add --allow-untrusted --root /tmp/test --initdb "+
			"--repository /repo --no-network hello 2>&1; echo EXIT=$?")
	output, _ := cmd.CombinedOutput()
	t.Logf("upstream apk output:\n%s", string(output))

	report := categorizeApkOutput(string(output))
	if report.exitCode != 0 {
		t.Errorf("apk add via repo exited with %d", report.exitCode)
	}
	for _, e := range report.errors {
		t.Errorf("upstream apk ERROR: %s", e)
	}
	for _, w := range report.unexpectedWarnings {
		t.Errorf("upstream apk WARNING: %s", w)
	}
}

// TestAPKSignedRepoInstallWithUpstreamApk exercises the signed-repo path:
// build an apk and an APKINDEX both signed with a yoe-generated key, and
// install via stock apk-tools WITHOUT `--allow-untrusted`. apk add must
// verify the signatures against the public key we drop into
// /etc/apk/keys/. This closes Phase 3.2/3.3 verification — proves the
// signature format yoe writes is byte-for-byte compatible with apk-tools'
// verification path.
func TestAPKSignedRepoInstallWithUpstreamApk(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	tmp := t.TempDir()

	// Generate a signing key in a temp dir so the test doesn't touch
	// ~/.config/yoe/keys/.
	keyPath := filepath.Join(tmp, "test-signing.rsa")
	signer, err := artifact.LoadOrGenerateSigner("test", keyPath)
	if err != nil {
		t.Fatalf("LoadOrGenerateSigner: %v", err)
	}

	destDir := filepath.Join(tmp, "destdir")
	if err := os.MkdirAll(filepath.Join(destDir, "usr/bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "usr/bin/hello"),
		[]byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Build the apk and the APKINDEX, both signed.
	repoDir := filepath.Join(tmp, "repo", "x86_64")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "out")
	unit := &yoestar.Unit{
		Name:        "hello",
		Version:     "1.0.0",
		License:     "MIT",
		Description: "test package for signed apk compat",
	}
	apkPath, err := artifact.CreateAPK(unit, destDir, out, "x86_64", "", signer)
	if err != nil {
		t.Fatalf("CreateAPK: %v", err)
	}
	if err := copyFile(apkPath, filepath.Join(repoDir, filepath.Base(apkPath))); err != nil {
		t.Fatal(err)
	}
	if err := repo.GenerateIndex(repoDir, signer); err != nil {
		t.Fatalf("GenerateIndex: %v", err)
	}

	// Drop the public key where apk will look for it: <root>/etc/apk/keys/
	// inside the container. apk reads that directory by default when
	// --root is set, and that's also where base-files installs the key
	// in real builds (see image.star and base-files.star).
	keysHostDir := filepath.Join(tmp, "keys")
	if err := os.MkdirAll(keysHostDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keysHostDir, signer.KeyName), signer.PubPEM, 0644); err != nil {
		t.Fatal(err)
	}

	// No --allow-untrusted, no --keys-dir. We pre-stage the key into
	// /tmp/test/etc/apk/keys/ before apk add runs — same flow as
	// image.star.
	cmd := exec.Command("docker", "run", "--rm",
		"-v", filepath.Join(tmp, "repo")+":/repo:ro",
		"-v", keysHostDir+":/keys:ro",
		"alpine:3.21",
		"sh", "-c",
		"mkdir -p /tmp/test/etc/apk/keys && "+
			"cp /keys/* /tmp/test/etc/apk/keys/ && "+
			"apk add --root /tmp/test --initdb "+
			"--repository /repo --no-network hello 2>&1; echo EXIT=$?")
	output, _ := cmd.CombinedOutput()
	t.Logf("upstream apk output:\n%s", string(output))

	report := categorizeApkOutput(string(output))
	if report.exitCode != 0 {
		t.Errorf("apk add (signed) exited with %d", report.exitCode)
	}
	for _, e := range report.errors {
		t.Errorf("upstream apk ERROR: %s", e)
	}
	// With signing in place, untrusted-signature warnings would be a
	// regression — we want zero warnings here.
	for _, w := range report.expectedWarnings {
		t.Errorf("unexpected (would-be-expected) apk WARNING in signed flow: %s", w)
	}
	for _, w := range report.unexpectedWarnings {
		t.Errorf("upstream apk WARNING: %s", w)
	}
}

type apkReport struct {
	exitCode           int
	errors             []string
	unexpectedWarnings []string
	expectedWarnings   []string
}

func categorizeApkOutput(out string) apkReport {
	r := apkReport{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "EXIT=") {
			fmtSscan(line[5:], &r.exitCode)
			continue
		}
		if strings.HasPrefix(line, "ERROR:") {
			r.errors = append(r.errors, line)
			continue
		}
		if strings.HasPrefix(line, "WARNING:") {
			if isExpectedApkWarning(line) {
				r.expectedWarnings = append(r.expectedWarnings, line)
			} else {
				r.unexpectedWarnings = append(r.unexpectedWarnings, line)
			}
		}
	}
	return r
}

func isExpectedApkWarning(line string) bool {
	// Until phase 3 (signing) lands, untrusted-signature warnings are OK.
	return strings.Contains(line, "untrusted") ||
		strings.Contains(line, "no valid signatures")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func fmtSscan(s string, v *int) {
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		*v = (*v)*10 + int(c-'0')
	}
}
