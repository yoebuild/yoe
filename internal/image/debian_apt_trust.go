package image

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// StageProjectAPTSource stages /etc/apt/keyrings/<project>.gpg and a
// deb822 /etc/apt/sources.list.d/<project>.sources entry inside the
// rootfs after dpkg --configure -a has run.
//
// Per R26, repoURL must be HTTPS — HTTP fails at image-build time.
//
// Per R20, the Signed-By: field scopes the project key's trust to
// this source only, never to /etc/apt/trusted.gpg.d/. Phased-updates
// are disabled so apt-get upgrade doesn't silently skip packages on
// development devices.
func StageProjectAPTSource(rootfsPath, projectName, projKeyPath, repoURL, suite string, components []string, w io.Writer) error {
	if !strings.HasPrefix(repoURL, "https://") {
		return fmt.Errorf("StageProjectAPTSource: HTTPS required (got %q) per R26", repoURL)
	}
	if projectName == "" {
		return fmt.Errorf("StageProjectAPTSource: empty project name")
	}

	// Copy keyring.
	keyringDir := filepath.Join(rootfsPath, "etc", "apt", "keyrings")
	if err := os.MkdirAll(keyringDir, 0o755); err != nil {
		return err
	}
	keyringDst := filepath.Join(keyringDir, projectName+".gpg")
	keyringSrc, err := os.ReadFile(projKeyPath)
	if err != nil {
		return fmt.Errorf("read project pubkey %s: %w", projKeyPath, err)
	}
	if err := os.WriteFile(keyringDst, keyringSrc, 0o644); err != nil {
		return fmt.Errorf("write keyring: %w", err)
	}

	// Compose deb822 .sources file.
	srcDir := filepath.Join(rootfsPath, "etc", "apt", "sources.list.d")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return err
	}
	srcPath := filepath.Join(srcDir, projectName+".sources")
	if len(components) == 0 {
		components = []string{"main"}
	}
	body := strings.Join([]string{
		"Types: deb",
		"URIs: " + strings.TrimSuffix(repoURL, "/"),
		"Suites: " + suite,
		"Components: " + strings.Join(components, " "),
		"Signed-By: /etc/apt/keyrings/" + projectName + ".gpg",
		"",
	}, "\n")
	if err := os.WriteFile(srcPath, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s.sources: %w", projectName, err)
	}

	// Disable phased updates so apt-get upgrade pulls every package
	// the project ships — embedded fleets want deterministic update
	// behavior, not a percentage rollout.
	confDir := filepath.Join(rootfsPath, "etc", "apt", "apt.conf.d")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return err
	}
	const phasedConf = `APT::Get::Never-Include-Phased-Updates "true";
`
	if err := os.WriteFile(filepath.Join(confDir, "99-yoe.conf"), []byte(phasedConf), 0o644); err != nil {
		return fmt.Errorf("write 99-yoe.conf: %w", err)
	}

	fmt.Fprintf(w, "  apt: staged keyring + %s.sources for %s\n", projectName, repoURL)
	return nil
}
