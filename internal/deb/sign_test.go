package deb

import (
	"bytes"
	"os/exec"
	"testing"

	"github.com/yoebuild/yoe/internal/dpkg"
)

func requireGPG(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not on PATH")
	}
}

func TestSignInRelease_RoundTrip(t *testing.T) {
	requireGPG(t)

	homedir := t.TempDir()
	// Set up an isolated homedir with a single signing key.
	mkKey := exec.Command("gpg",
		"--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--homedir", homedir,
		"--quick-generate-key", "deb-sign test", "rsa2048", "sign", "0",
	)
	if out, err := mkKey.CombinedOutput(); err != nil {
		t.Fatalf("key gen: %v: %s", err, out)
	}

	exportKey := exec.Command("gpg", "--homedir", homedir, "--export")
	var pub bytes.Buffer
	exportKey.Stdout = &pub
	if err := exportKey.Run(); err != nil {
		t.Fatalf("export: %v", err)
	}

	release := []byte(`Suite: stable
Codename: bookworm
Date: Sat, 27 May 2026 00:00:00 UTC
Valid-Until: Mon, 28 May 2099 00:00:00 UTC
Components: main
Architectures: amd64
SHA256:
 abcd 12 main/binary-amd64/Packages
`)
	signed, err := SignInRelease(release, homedir, "")
	if err != nil {
		t.Fatalf("SignInRelease: %v", err)
	}
	if !bytes.Contains(signed, []byte("BEGIN PGP SIGNED MESSAGE")) {
		t.Fatalf("signed output missing clearsign header: %s", signed)
	}

	// VerifyInRelease must accept the result against the public key.
	if _, err := dpkg.VerifyInRelease(signed, pub.Bytes()); err != nil {
		t.Errorf("VerifyInRelease against own signature: %v", err)
	}
}

func TestSignInRelease_MissingHomedir(t *testing.T) {
	requireGPG(t)
	_, err := SignInRelease([]byte("hello"), "", "")
	if err == nil {
		t.Fatal("expected error for empty homedir")
	}
}
