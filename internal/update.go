package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

const (
	githubAPIURL = "https://api.github.com/repos/yoebuild/yoe/releases/latest"
	githubRelURL = "https://github.com/yoebuild/yoe/releases/latest/download"
)

// GitHubRelease represents the GitHub API release response
type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
}

// Update checks for and downloads the latest version of yoe
func Update(currentVersion string) error {
	fmt.Println("Checking for updates...")

	// Get latest release info from GitHub API
	latestVersion, err := getLatestVersion()
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	// Normalize versions for comparison (remove 'v' prefix)
	current := strings.TrimPrefix(currentVersion, "v")
	latest := strings.TrimPrefix(latestVersion, "v")

	if current == latest {
		fmt.Printf("Already running the latest version (%s)\n", currentVersion)
		return nil
	}

	if current == "dev" {
		fmt.Printf("Running development version. Latest release is %s\n", latestVersion)
		fmt.Println("Proceeding with update...")
	} else {
		fmt.Printf("Updating from %s to %s\n", currentVersion, latestVersion)
	}

	// Download and install the latest version
	if err := downloadAndInstall(); err != nil {
		return fmt.Errorf("failed to update: %w", err)
	}

	fmt.Printf("Successfully updated to version %s\n", latestVersion)
	fmt.Println()
	fmt.Println("Note: Yoe is in heavy development. We recommend cleaning your")
	fmt.Println("build directory and re-creating projects (yoe init) with each new release")
	return nil
}

// getLatestVersion fetches the latest release version from GitHub
func getLatestVersion() (string, error) {
	resp, err := http.Get(githubAPIURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	return release.TagName, nil
}

// downloadAndInstall downloads and installs the latest version
func downloadAndInstall() error {
	binaryName := getBinaryName()
	downloadURL := fmt.Sprintf("%s/%s", githubRelURL, binaryName)

	fmt.Printf("Downloading %s...\n", downloadURL)

	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Get the current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	// Create a temporary file in the same directory
	tmpFile, err := os.CreateTemp(filepath.Dir(execPath), "yoe-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Write the downloaded binary to the temp file
	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write binary: %w", err)
	}
	tmpFile.Close()

	// Make the temp file executable
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("failed to make binary executable: %w", err)
	}

	// Replace the current binary
	if err := os.Rename(tmpPath, execPath); err != nil {
		return fmt.Errorf("failed to install new binary: %w", err)
	}

	fmt.Println("Binary updated successfully")
	return nil
}

// getBinaryName returns the binary name for the current platform,
// matching the goreleaser naming convention: yoe-{Os}-{arch}
func getBinaryName() string {
	osName := cases.Title(language.English).String(runtime.GOOS)

	archName := runtime.GOARCH
	if archName == "amd64" {
		archName = "x86_64"
	}

	return fmt.Sprintf("yoe-%s-%s", osName, archName)
}
