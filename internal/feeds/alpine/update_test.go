package alpine

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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testKeyPair returns a small RSA key, the public-key PEM bytes, and
// the basename used for the .SIGN.RSA.<name> entry.
func testKeyPair(t *testing.T, dir, name string) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	body := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return key, path
}

// buildTarball returns a valid APKINDEX.tar.gz signed by key: gzip
// stream 1 carries .SIGN.RSA.<keyName>, gzip stream 2 carries
// DESCRIPTION + APKINDEX.
func buildTarball(t *testing.T, key *rsa.PrivateKey, keyName string, apkindex []byte) []byte {
	t.Helper()
	payload := gzippedTar(t, map[string][]byte{
		"DESCRIPTION": []byte("test repo"),
		"APKINDEX":    apkindex,
	})
	digest := sha1.Sum(payload)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sigStream := gzippedTar(t, map[string][]byte{".SIGN.RSA." + keyName: sig})
	return append(sigStream, payload...)
}

func gzippedTar(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fixtureModuleDir lays out a MODULE.star with one alpine_feed call
// plus the declared keys file. Returns the module directory path.
func fixtureModuleDir(t *testing.T, dir, mirrorURL, keyName string) string {
	t.Helper()
	mod := filepath.Join(dir, "module-alpine")
	if err := os.MkdirAll(filepath.Join(mod, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	starContent := fmt.Sprintf(`module_info(name = "alpine")
alpine_feed(
    name = "main",
    url = %q,
    branch = "v3.21",
    section = "main",
    index = "feeds/main",
    keys = ["keys/%s"],
)
`, mirrorURL, keyName)
	if err := os.WriteFile(filepath.Join(mod, "MODULE.star"), []byte(starContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return mod
}

func TestUpdateFeeds_HappyPath(t *testing.T) {
	dir := t.TempDir()
	keyName := "test-mirror.rsa.pub"
	key, pubPath := testKeyPair(t, dir, keyName)
	indexBytes := []byte("C:Q1wmRLywlDhwD28lS6Qlp6nGlzzIk=\nP:musl\nV:1.2.5-r10\n")
	tarball := buildTarball(t, key, keyName, indexBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Honor every /v3.21/main/<arch>/APKINDEX.tar.gz request.
		if !strings.HasSuffix(r.URL.Path, "/APKINDEX.tar.gz") {
			http.NotFound(w, r)
			return
		}
		w.Write(tarball)
	}))
	defer srv.Close()

	mod := fixtureModuleDir(t, dir, srv.URL, keyName)
	// Stage the key file inside the module's keys/ dir so peek can
	// resolve the alpine_feed(keys=[...]) path.
	if err := os.Rename(pubPath, filepath.Join(mod, "keys", keyName)); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := UpdateFeeds(UpdateOptions{
		ModuleDir: mod,
		Arches:    []string{"x86_64"},
		Out:       &out,
	})
	if err != nil {
		t.Fatalf("UpdateFeeds: %v\n%s", err, out.String())
	}

	dst := filepath.Join(mod, "feeds/main/x86_64/APKINDEX")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !bytes.Equal(got, indexBytes) {
		t.Errorf("written APKINDEX differs from source\ngot:  %q\nwant: %q", got, indexBytes)
	}
	if !strings.Contains(out.String(), "wrote") {
		t.Errorf("expected progress output, got: %q", out.String())
	}
}

func TestUpdateFeeds_BadSignature(t *testing.T) {
	dir := t.TempDir()
	keyName := "trusted.rsa.pub"
	_, trustedPubPath := testKeyPair(t, dir, keyName)
	// Tarball is signed by a different key the trust list doesn't carry.
	wrongKey, _ := testKeyPair(t, dir, "wrong-key.rsa.pub")
	indexBytes := []byte("P:musl\n")
	tarball := buildTarball(t, wrongKey, "wrong-key.rsa.pub", indexBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball)
	}))
	defer srv.Close()

	mod := fixtureModuleDir(t, dir, srv.URL, keyName)
	if err := os.Rename(trustedPubPath, filepath.Join(mod, "keys", keyName)); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := UpdateFeeds(UpdateOptions{ModuleDir: mod, Arches: []string{"x86_64"}, Out: &out})
	if err == nil {
		t.Fatal("want error for untrusted signing key")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("err = %v, want 'signature' in message", err)
	}
	// Atomic write should not have left a partial file.
	dst := filepath.Join(mod, "feeds/main/x86_64/APKINDEX")
	if _, err := os.Stat(dst); err == nil {
		t.Errorf("APKINDEX written despite signature failure: %s", dst)
	}
}

func TestUpdateFeeds_MissingKeyFile(t *testing.T) {
	dir := t.TempDir()
	mod := fixtureModuleDir(t, dir, "http://nowhere.example", "nonexistent.rsa.pub")
	err := UpdateFeeds(UpdateOptions{ModuleDir: mod, Arches: []string{"x86_64"}, Out: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("want error for missing key file")
	}
	if !strings.Contains(err.Error(), "key file") {
		t.Errorf("err = %v, want 'key file' in message", err)
	}
}

func TestUpdateFeeds_NoFeedsInModule(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(dir, "module-alpine")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "MODULE.star"),
		[]byte(`module_info(name = "alpine")`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := UpdateFeeds(UpdateOptions{ModuleDir: mod, Out: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("want error for module with no alpine_feed() calls")
	}
}

func TestUpdateFeeds_HTTPNotFound(t *testing.T) {
	dir := t.TempDir()
	keyName := "k.rsa.pub"
	_, pubPath := testKeyPair(t, dir, keyName)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	mod := fixtureModuleDir(t, dir, srv.URL, keyName)
	if err := os.Rename(pubPath, filepath.Join(mod, "keys", keyName)); err != nil {
		t.Fatal(err)
	}
	err := UpdateFeeds(UpdateOptions{ModuleDir: mod, Arches: []string{"x86_64"}, Out: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("want error on HTTP 404")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("err = %v, want 'HTTP 404' in message", err)
	}
}

func TestPeekFeedDecls_DetectsCalls(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MODULE.star"),
		[]byte(`module_info(name = "alpine")
alpine_feed(name = "main", url = "http://x", branch = "v3.21", section = "main", index = "feeds/main", keys = ["keys/k.rsa.pub"])
alpine_feed(name = "community", url = "http://x", branch = "v3.21", section = "community", index = "feeds/community", keys = ["keys/k.rsa.pub"])
`), 0o644); err != nil {
		t.Fatal(err)
	}
	decls, err := PeekFeedDecls(dir)
	if err != nil {
		t.Fatalf("PeekFeedDecls: %v", err)
	}
	if len(decls) != 2 {
		t.Fatalf("len: %d (want 2)", len(decls))
	}
	names := []string{decls[0].Name, decls[1].Name}
	want := []string{"community", "main"}
	for i := range names {
		if names[i] != want[i] {
			t.Errorf("decls[%d].Name = %q, want %q (sorted order)", i, names[i], want[i])
		}
	}
}

func TestPeekFeedDecls_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MODULE.star"),
		[]byte(`module_info(name = "alpine")
alpine_feed(name = "main", url = "http://x", branch = "v3.21", section = "main", index = "a", keys = ["k"])
alpine_feed(name = "main", url = "http://y", branch = "v3.21", section = "main", index = "b", keys = ["k"])
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PeekFeedDecls(dir)
	if err == nil {
		t.Fatal("want error for duplicate name")
	}
}
