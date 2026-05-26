package alpine

import (
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
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// TestAlpineFeed_CrossFeedProvidesResolution exercises the
// project-wide provides table that multiFeedProviders builds at
// Lookup time. A package in alpine.community depends on a soname
// provided only by alpine.main; without cross-feed lookup the
// dep resolution errors with "no provider for so:libcrypto.so.3".
//
// The canonical example from the plan: community's openssh-server
// declares D: so:libcrypto.so.3, which lives in main's openssl-libs.
func TestAlpineFeed_CrossFeedProvidesResolution(t *testing.T) {
	dir := t.TempDir()

	mainIndex := []byte(`C:Q1wmRLywlDhwD28lS6Qlp6nGlzzIk=
P:openssl-libs
V:3.5.4-r0
A:x86_64
T:OpenSSL libraries
L:Apache-2.0
o:openssl
p:so:libcrypto.so.3=3.5.4 so:libssl.so.3=3.5.4

C:Q1gccWqxnp4T7mk08WsE7/XtS4YI4=
P:musl
V:1.2.5-r10
A:x86_64
T:musl libc
L:MIT
o:musl
p:so:libc.musl-x86_64.so.1=1
`)
	// openssh-server in community depends on a soname from main's openssl-libs
	// (so:libcrypto.so.3) and on musl. Cross-feed resolution must succeed.
	communityIndex := []byte(`C:Q1wmRLywlDhwD28lS6Qlp6nGlzzIk=
P:openssh-server
V:9.9_p2-r0
A:x86_64
T:OpenBSD SSH server
L:BSD-2-Clause
o:openssh
D:so:libcrypto.so.3=3.5.4-r0 so:libc.musl-x86_64.so.1
`)

	keyName, pubPath, key := setupTestKey(t, dir)
	mainServer, _ := setupMirrorServer(t, key, keyName, mainIndex, communityIndex)
	defer mainServer.Close()

	mod := setupTestModule(t, dir, mainServer.URL, keyName, pubPath)

	// Load with both feeds registered.
	proj, err := yoestar.LoadProject(filepath.Dir(mod),
		yoestar.WithBuiltin("alpine_feed", Builtin),
	)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	// Two synthetic modules registered.
	if len(proj.SyntheticModules) != 2 {
		t.Fatalf("SyntheticModules: got %d, want 2", len(proj.SyntheticModules))
	}

	// Find the community feed and look up openssh-server.
	var community *yoestar.SyntheticModule
	for _, sm := range proj.SyntheticModules {
		if sm.Name == "alpine.community" {
			community = sm
			break
		}
	}
	if community == nil {
		t.Fatal("alpine.community not registered")
	}

	u, err := community.Lookup("openssh-server")
	if err != nil {
		t.Fatalf("Lookup openssh-server: %v (cross-feed dep resolution should succeed)", err)
	}
	if u == nil {
		t.Fatal("openssh-server: nil unit")
	}

	// The resolved RuntimeDeps should include openssl-libs (from main)
	// and musl (from main).
	wantDeps := map[string]bool{"openssl-libs": false, "musl": false}
	for _, d := range u.RuntimeDeps {
		if _, ok := wantDeps[d]; ok {
			wantDeps[d] = true
		}
	}
	for d, ok := range wantDeps {
		if !ok {
			t.Errorf("RuntimeDeps missing %q (got %v)", d, u.RuntimeDeps)
		}
	}
}

// --- fixture helpers ---

func setupTestKey(t *testing.T, dir string) (string, string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keyName := "test-mirror.rsa.pub"
	pubPath := filepath.Join(dir, keyName)
	body := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if err := os.WriteFile(pubPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return keyName, pubPath, key
}

// setupMirrorServer serves signed tarballs for main + community at
// /v3.21/main/x86_64/APKINDEX.tar.gz and /v3.21/community/...
func setupMirrorServer(t *testing.T, key *rsa.PrivateKey, keyName string, mainIndex, communityIndex []byte) (*httptest.Server, func()) {
	t.Helper()
	mainTarball := signTarball(t, key, keyName, mainIndex)
	communityTarball := signTarball(t, key, keyName, communityIndex)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3.21/main/x86_64/APKINDEX.tar.gz":
			w.Write(mainTarball)
		case "/v3.21/community/x86_64/APKINDEX.tar.gz":
			w.Write(communityTarball)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, func() { srv.Close() }
}

func signTarball(t *testing.T, key *rsa.PrivateKey, keyName string, apkindex []byte) []byte {
	t.Helper()
	payload := gzippedTar(t, map[string][]byte{
		"DESCRIPTION": []byte("test"),
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

// setupTestModule writes a project tree: PROJECT.star declaring a
// local module, plus the module's MODULE.star with two alpine_feed
// calls. Returns the module dir path.
func setupTestModule(t *testing.T, dir, mirrorURL, keyName, pubKeyPath string) string {
	t.Helper()
	projDir := filepath.Join(dir, "project")
	modDir := filepath.Join(projDir, "modules", "alpine")
	for _, d := range []string{projDir, filepath.Join(projDir, "machines"), filepath.Join(modDir, "keys")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(projDir, "PROJECT.star"),
		[]byte(`project(name = "p", version = "0.1.0",
    defaults = defaults(machine = "qemu-x86_64"),
    modules = [
        module("https://example.com/alpine.git", local = "modules/alpine"),
    ],
)`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "machines", "q.star"),
		[]byte(`machine(name = "qemu-x86_64", arch = "x86_64")`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stage the trusted key inside the module's keys/ dir.
	keyData, err := os.ReadFile(pubKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "keys", keyName), keyData, 0o644); err != nil {
		t.Fatal(err)
	}

	mod := fmt.Sprintf(`module_info(name = "alpine")
alpine_feed(name = "main", url = %q, branch = "v3.21", section = "main",
            index = "feeds/main", keys = ["keys/%s"])
alpine_feed(name = "community", url = %q, branch = "v3.21", section = "community",
            index = "feeds/community", keys = ["keys/%s"])
`, mirrorURL, keyName, mirrorURL, keyName)
	if err := os.WriteFile(filepath.Join(modDir, "MODULE.star"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fetch the APKINDEX files so the synthetic Lookup can resolve.
	if err := UpdateFeeds(UpdateOptions{ModuleDir: modDir, Arches: []string{"x86_64"}}); err != nil {
		t.Fatalf("UpdateFeeds: %v", err)
	}
	return modDir
}
