package dpkg

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func TestVerifyInRelease_Valid(t *testing.T) {
	release := mustRead(t, "testdata/InRelease.valid")
	keyring := mustRead(t, "testdata/keyring.gpg")
	body, err := VerifyInRelease(release, keyring)
	if err != nil {
		t.Fatalf("VerifyInRelease: %v", err)
	}
	if !strings.Contains(string(body), "Codename: bookworm") {
		t.Errorf("body missing expected field; got %q", string(body))
	}
}

func TestVerifyInRelease_UntrustedKey(t *testing.T) {
	release := mustRead(t, "testdata/InRelease.valid")
	keyring := mustRead(t, "testdata/keyring.untrusted.gpg")
	_, err := VerifyInRelease(release, keyring)
	if err == nil {
		t.Fatal("VerifyInRelease: expected error for untrusted key")
	}
	var ute *UntrustedKeyError
	if !errors.As(err, &ute) {
		t.Errorf("expected *UntrustedKeyError, got %T: %v", err, err)
	}
}

func TestVerifyInRelease_ValidUntilMissing(t *testing.T) {
	// Debian stable/oldstable main InRelease omits Valid-Until; the
	// signature is the trust anchor, so verification must succeed and
	// return the body rather than rejecting on the missing field.
	release := mustRead(t, "testdata/InRelease.no-valid-until")
	keyring := mustRead(t, "testdata/keyring.gpg")
	body, err := VerifyInRelease(release, keyring)
	if err != nil {
		t.Fatalf("missing Valid-Until should verify on signature alone, got %v", err)
	}
	if len(body) == 0 {
		t.Error("expected a non-empty Release body")
	}
}

func TestVerifyInRelease_ValidUntilExpired(t *testing.T) {
	release := mustRead(t, "testdata/InRelease.expired")
	keyring := mustRead(t, "testdata/keyring.gpg")
	_, err := VerifyInRelease(release, keyring)
	if err == nil {
		t.Fatal("expected expired error")
	}
	var vue *ValidUntilExpiredError
	if !errors.As(err, &vue) {
		t.Errorf("expected *ValidUntilExpiredError, got %T: %v", err, err)
	}
}

func TestVerifyInRelease_NoSignature(t *testing.T) {
	keyring := mustRead(t, "testdata/keyring.gpg")
	_, err := VerifyInRelease([]byte("Suite: stable\n"), keyring)
	if !errors.Is(err, ErrNoSignature) {
		t.Errorf("expected ErrNoSignature, got %v", err)
	}
}
