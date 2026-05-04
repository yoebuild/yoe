package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yoebuild/yoe/internal/artifact"
)

// cmdKey dispatches `yoe key <subcommand>`.
//
//	yoe key info       — print the current project's key path, fingerprint,
//	                     and whether it exists on disk
//	yoe key generate   — create a fresh keypair if none exists yet (no-op
//	                     when the project's key file is already present)
//
// Both subcommands operate against the same path discovery as the build
// pipeline: PROJECT.star's signing_key wins; if unset, yoe defaults to
// ~/.config/yoe/keys/<project>.rsa.
func cmdKey(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s key <generate|info>\n", os.Args[0])
		os.Exit(1)
	}

	proj := loadProject()

	switch args[0] {
	case "generate":
		signer, err := artifact.LoadOrGenerateSigner(proj.Name, proj.SigningKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Signing key: %s\n", keyPathFor(proj.Name, proj.SigningKey))
		fmt.Printf("Public key:  %s\n", keyPathFor(proj.Name, proj.SigningKey)+".pub")
		fmt.Printf("Key name:    %s\n", signer.KeyName)
		fmt.Printf("Fingerprint: %s\n", fingerprint(signer.PubPEM))

	case "info":
		path := keyPathFor(proj.Name, proj.SigningKey)
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(os.Stderr, "No signing key at %s — run `%s key generate` to create one.\n",
				path, os.Args[0])
			os.Exit(1)
		}
		signer, err := artifact.LoadOrGenerateSigner(proj.Name, proj.SigningKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Signing key: %s\n", path)
		fmt.Printf("Public key:  %s\n", path+".pub")
		fmt.Printf("Key name:    %s\n", signer.KeyName)
		fmt.Printf("Fingerprint: %s\n", fingerprint(signer.PubPEM))

	default:
		fmt.Fprintf(os.Stderr, "Unknown key subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// keyPathFor mirrors artifact.LoadOrGenerateSigner's path discovery: the
// configured signing_key path wins, otherwise ~/.config/yoe/keys/<name>.rsa.
func keyPathFor(projectName, configured string) string {
	if configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "yoe", "keys", projectName+".rsa")
}

// fingerprint returns the SHA-256 of the PEM-encoded public key, formatted
// as the leading bytes in colon-separated hex — enough for a human to
// confirm two systems are talking about the same key without printing the
// whole digest.
func fingerprint(pubPEM []byte) string {
	sum := sha256.Sum256(pubPEM)
	const n = 8
	hex := make([]byte, 0, n*3)
	for i := 0; i < n; i++ {
		if i > 0 {
			hex = append(hex, ':')
		}
		hex = append(hex, hexByte(sum[i])...)
	}
	return string(hex) + "..."
}

func hexByte(b byte) []byte {
	const digits = "0123456789abcdef"
	return []byte{digits[b>>4], digits[b&0xf]}
}
