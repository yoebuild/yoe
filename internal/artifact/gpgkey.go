package artifact

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GPGKey identifies a project's GPG signing key. Homedir is the
// isolated --homedir gpg should always use (never falling back to
// $GNUPGHOME); KeyID is the long key id or fingerprint to pass to
// --local-user. PubKeyPath is the binary-dearmored public key staged
// on the device under /etc/apt/keyrings/<project>.gpg.
type GPGKey struct {
	Homedir    string
	KeyID      string
	PubKeyPath string
}

// EnsureProjectGPGKey generates (or loads) the project's GPG signing
// key. The key bootstrap runs once per project per workstation:
//   - homedir lives at ~/.config/yoe/keys/<project>-gpg/
//   - public key (binary dearmored) lands at ~/.config/yoe/keys/<project>.gpg
//
// Both paths can be overridden when configuredKeyDir is set on the
// Project (parallel to the apk signing_key field).
//
// The homedir is explicit so every gpg shell-out can pass --homedir
// and never accidentally pick up the developer's ambient GNUPGHOME.
// Subsequent runs detect the homedir and reuse the key.
func EnsureProjectGPGKey(projectName, configuredKeyDir string) (*GPGKey, error) {
	if projectName == "" {
		return nil, fmt.Errorf("EnsureProjectGPGKey: project name is empty")
	}
	keyDir := configuredKeyDir
	if keyDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving GPG key path: %w", err)
		}
		keyDir = filepath.Join(home, ".config", "yoe", "keys")
	}
	homedir := filepath.Join(keyDir, projectName+"-gpg")
	pubKey := filepath.Join(keyDir, projectName+".gpg")

	if _, err := exec.LookPath("gpg"); err != nil {
		return nil, fmt.Errorf("EnsureProjectGPGKey: gpg missing on PATH: %w", err)
	}

	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", keyDir, err)
	}
	if err := os.MkdirAll(homedir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", homedir, err)
	}

	keyID, err := findExistingKey(homedir)
	if err != nil {
		return nil, err
	}
	if keyID == "" {
		keyID, err = generateKey(homedir, projectName)
		if err != nil {
			return nil, err
		}
	}

	if _, err := os.Stat(pubKey); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat pubkey: %w", err)
		}
		if err := exportPublicKey(homedir, keyID, pubKey); err != nil {
			return nil, fmt.Errorf("exporting public key: %w", err)
		}
	}

	return &GPGKey{Homedir: homedir, KeyID: keyID, PubKeyPath: pubKey}, nil
}

// findExistingKey returns the first long-fingerprint key found in
// homedir. Empty string means "no key yet".
func findExistingKey(homedir string) (string, error) {
	cmd := exec.Command("gpg", "--batch", "--homedir", homedir, "--list-secret-keys", "--with-colons")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// Empty homedir produces an empty output and a 2 exit; treat as
		// "no key" rather than failure.
		if out.Len() == 0 {
			return "", nil
		}
		return "", fmt.Errorf("gpg --list-secret-keys: %w: %s", err, errBuf.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "fpr:") {
			fields := strings.Split(line, ":")
			if len(fields) >= 10 && fields[9] != "" {
				return fields[9], nil
			}
		}
	}
	return "", nil
}

func generateKey(homedir, projectName string) (string, error) {
	uid := fmt.Sprintf("Yoe Project %s <yoe-%s@invalid>", projectName, projectName)
	cmd := exec.Command("gpg",
		"--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--homedir", homedir,
		"--quick-generate-key", uid, "rsa4096", "sign", "0",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("gpg --quick-generate-key: %w: %s", err, out)
	}
	return findExistingKey(homedir)
}

func exportPublicKey(homedir, keyID, dst string) error {
	cmd := exec.Command("gpg", "--batch", "--homedir", homedir, "--output", dst, "--export", keyID)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gpg --export %s: %w: %s", keyID, err, out)
	}
	return nil
}
