package deb

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
)

// SignInRelease produces a clearsigned InRelease from releaseBytes
// using the GPG secret key keyID in homedir. Shells `gpg --clearsign`;
// the gpg binary ships with the toolchain-glibc container.
//
// homedir is always passed explicitly via --homedir so the caller can't
// accidentally pick up an ambient GNUPGHOME. keyID is the long key id
// or fingerprint to sign with — `--local-user` selects it. Pass empty
// keyID to let gpg pick the default key for the homedir.
func SignInRelease(releaseBytes []byte, homedir, keyID string) ([]byte, error) {
	if _, err := exec.LookPath("gpg"); err != nil {
		return nil, fmt.Errorf("deb sign: gpg missing on PATH: %w", err)
	}
	if homedir == "" {
		return nil, fmt.Errorf("deb sign: homedir required (no ambient GNUPGHOME fallback)")
	}
	if _, err := os.Stat(homedir); err != nil {
		return nil, fmt.Errorf("deb sign: homedir %s: %w", homedir, err)
	}

	args := []string{
		"--batch",
		"--pinentry-mode", "loopback",
		"--homedir", homedir,
		"--armor",
		"--clearsign",
	}
	if keyID != "" {
		args = append(args, "--local-user", keyID)
	}
	cmd := exec.Command("gpg", args...)
	cmd.Stdin = bytes.NewReader(releaseBytes)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("deb sign: gpg --clearsign: %w: %s", err, errBuf.String())
	}
	return out.Bytes(), nil
}
