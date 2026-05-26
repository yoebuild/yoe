package apkindex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// makeKeyPair generates a 1024-bit RSA keypair (small for fast tests
// — production keys live in module-alpine's keys/ at 2048+) and
// writes the public key to disk in PEM form. Returns the key, the
// path to the public-key PEM file, and the public-key filename.
func makeKeyPair(t *testing.T, dir, name string) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	pubPath := filepath.Join(dir, name)
	pem := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})
	if err := os.WriteFile(pubPath, pem, 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}
	return key, pubPath
}

// makeSignedTarball constructs an Alpine-style APKINDEX.tar.gz: gzip
// stream 1 holds a tar with .SIGN.RSA.<keyName> carrying the RSA
// signature, gzip stream 2+ holds the signed payload. Returns the
// full file bytes.
func makeSignedTarball(t *testing.T, key *rsa.PrivateKey, keyName string, payload []byte) []byte {
	t.Helper()
	// First produce the payload gzip stream so we can sign its raw bytes.
	payloadGz := gzipTarBytes(t, map[string][]byte{
		"DESCRIPTION": []byte("test description"),
		"APKINDEX":    payload,
	})
	digest := sha1.Sum(payloadGz)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sigGz := gzipTarBytes(t, map[string][]byte{
		".SIGN.RSA." + keyName: sig,
	})
	return append(sigGz, payloadGz...)
}

func gzipTarBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	// Flush tar without writing the EOF marker — apk-tools reads one
	// gzip stream per logical chunk and the trailing zero blocks just
	// waste space. Mirror artifact/sign.go's signatureGzipStream
	// behavior so test fixtures look like the real thing.
	if err := tw.Flush(); err != nil {
		t.Fatalf("tar flush: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestVerifySignature_HappyPath(t *testing.T) {
	dir := t.TempDir()
	key, pubPath := makeKeyPair(t, dir, "test-key.rsa.pub")
	tarball := makeSignedTarball(t, key, "test-key.rsa.pub", []byte("P:musl\nV:1.0\n"))

	if err := VerifySignatureBytes(tarball, []string{pubPath}); err != nil {
		t.Fatalf("VerifySignatureBytes: %v", err)
	}
}

func TestVerifySignature_EmptyTrustList(t *testing.T) {
	dir := t.TempDir()
	key, _ := makeKeyPair(t, dir, "test-key.rsa.pub")
	tarball := makeSignedTarball(t, key, "test-key.rsa.pub", []byte("P:musl\n"))

	err := VerifySignatureBytes(tarball, nil)
	var ue *UntrustedKeyError
	if !errors.As(err, &ue) {
		t.Fatalf("want UntrustedKeyError, got %v", err)
	}
	if ue.KeyName != "test-key.rsa.pub" {
		t.Errorf("KeyName: got %q", ue.KeyName)
	}
}

func TestVerifySignature_KeyMismatch(t *testing.T) {
	dir := t.TempDir()
	// Signed with key A; trust list contains key B.
	keyA, _ := makeKeyPair(t, dir, "key-a.rsa.pub")
	_, pubB := makeKeyPair(t, dir, "key-b.rsa.pub")
	tarball := makeSignedTarball(t, keyA, "key-a.rsa.pub", []byte("P:musl\n"))

	err := VerifySignatureBytes(tarball, []string{pubB})
	var ue *UntrustedKeyError
	if !errors.As(err, &ue) {
		t.Fatalf("want UntrustedKeyError, got %v", err)
	}
}

func TestVerifySignature_NoSignature(t *testing.T) {
	// A "tarball" that's just a single content gzip stream — no
	// signature stream prepended.
	payload := gzipTarBytes(t, map[string][]byte{
		"APKINDEX": []byte("P:musl\n"),
	})
	err := VerifySignatureBytes(payload, nil)
	if !errors.Is(err, ErrNoSignature) {
		t.Errorf("want ErrNoSignature, got %v", err)
	}
}

func TestVerifySignature_SignatureStreamHasNoSignEntry(t *testing.T) {
	// Two gzip streams, but the first one contains a regular file
	// rather than a .SIGN.RSA.* entry.
	junkStream := gzipTarBytes(t, map[string][]byte{"random.txt": []byte("hi")})
	payload := gzipTarBytes(t, map[string][]byte{"APKINDEX": []byte("P:musl\n")})
	tarball := append(junkStream, payload...)
	err := VerifySignatureBytes(tarball, nil)
	if !errors.Is(err, ErrNoSignature) {
		t.Errorf("want ErrNoSignature, got %v", err)
	}
}

func TestVerifySignature_TamperedPayload(t *testing.T) {
	dir := t.TempDir()
	key, pubPath := makeKeyPair(t, dir, "test-key.rsa.pub")
	tarball := makeSignedTarball(t, key, "test-key.rsa.pub", []byte("P:musl\n"))

	// Flip a byte well inside the payload stream (past the signature
	// stream's bounds) so the signature SHA1 no longer matches.
	bounds, err := gzipStreamBoundaries(tarball)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounds) < 2 {
		t.Fatalf("need >= 2 streams to tamper, got %d", len(bounds))
	}
	mutated := append([]byte(nil), tarball...)
	mutated[bounds[1][0]+15] ^= 0x01

	err = VerifySignatureBytes(mutated, []string{pubPath})
	var me *SignatureMismatchError
	if !errors.As(err, &me) {
		t.Fatalf("want SignatureMismatchError, got %v", err)
	}
	if me.KeyName != "test-key.rsa.pub" {
		t.Errorf("KeyName: got %q", me.KeyName)
	}
}

func TestVerifySignature_FromDisk(t *testing.T) {
	dir := t.TempDir()
	key, pubPath := makeKeyPair(t, dir, "test-key.rsa.pub")
	tarball := makeSignedTarball(t, key, "test-key.rsa.pub", []byte("P:musl\n"))
	path := filepath.Join(dir, "APKINDEX.tar.gz")
	if err := os.WriteFile(path, tarball, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifySignature(path, []string{pubPath}); err != nil {
		t.Errorf("VerifySignature(disk): %v", err)
	}
}

func TestVerifySignature_PKCS1RSAPublicKey(t *testing.T) {
	// Some maintainers ship RSA PUBLIC KEY (PKCS#1) instead of
	// PUBLIC KEY (PKIX SubjectPublicKeyInfo); verify accepts both.
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pubPath := filepath.Join(dir, "pkcs1.rsa.pub")
	block := &pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(block), 0o644); err != nil {
		t.Fatal(err)
	}
	tarball := makeSignedTarball(t, key, "pkcs1.rsa.pub", []byte("P:musl\n"))
	if err := VerifySignatureBytes(tarball, []string{pubPath}); err != nil {
		t.Errorf("VerifySignatureBytes (PKCS#1): %v", err)
	}
}
