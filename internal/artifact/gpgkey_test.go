package artifact

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestEnsureProjectGPGKey_FirstRunGenerates(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not on PATH")
	}
	tmp := t.TempDir()
	k, err := EnsureProjectGPGKey("demo", tmp)
	if err != nil {
		t.Fatalf("EnsureProjectGPGKey: %v", err)
	}
	if k.Homedir == "" || k.KeyID == "" || k.PubKeyPath == "" {
		t.Errorf("nil-ish key: %+v", k)
	}
}

func TestEnsureProjectGPGKey_SecondRunReuses(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not on PATH")
	}
	tmp := t.TempDir()
	first, err := EnsureProjectGPGKey("demo", tmp)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := EnsureProjectGPGKey("demo", tmp)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.KeyID != second.KeyID {
		t.Errorf("key changed: %q -> %q", first.KeyID, second.KeyID)
	}
}

func TestEnsureProjectGPGKey_HomedirIsolation(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not on PATH")
	}
	tmp := t.TempDir()
	k, err := EnsureProjectGPGKey("iso", tmp)
	if err != nil {
		t.Fatalf("EnsureProjectGPGKey: %v", err)
	}

	// Confirm the homedir holds at least one secret key.
	cmd := exec.Command("gpg", "--batch", "--homedir", k.Homedir, "--list-secret-keys", "--with-colons")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("gpg --list-secret-keys: %v", err)
	}
	if !strings.Contains(out.String(), "fpr:") {
		t.Errorf("isolated homedir has no fingerprint output: %q", out.String())
	}
}
