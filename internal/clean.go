package internal

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yoebuild/yoe/internal/container"
)

func RunClean(projectDir, _ string, all bool, force bool, units []string) error {
	buildDir := filepath.Join(projectDir, "build")

	if len(units) > 0 {
		for _, r := range units {
			// Per-R14a build layout: build/<distro>/<name>.<scopeDir>/.
			// Glob across every distro and scope so `yoe clean <unit>`
			// removes the unit's destdirs regardless of which images
			// have built it. (Pre-fix this constructed
			// build/<arch>/<unit>, which never matched the actual
			// layout — per-unit clean was a no-op.)
			matches, err := filepath.Glob(filepath.Join(buildDir, "*", r+".*"))
			if err != nil {
				return fmt.Errorf("globbing %s: %w", r, err)
			}
			for _, dir := range matches {
				if err := container.RemoveDir(context.Background(), dir, projectDir, ""); err != nil {
					return fmt.Errorf("removing %s: %w", dir, err)
				}
			}
			if len(matches) == 0 {
				fmt.Printf("Cleaned %s (no on-disk build dirs)\n", r)
			} else {
				fmt.Printf("Cleaned %s (%d build dirs)\n", r, len(matches))
			}
		}
		return nil
	}

	if all {
		if !force {
			fmt.Print("Remove all build artifacts and packages? [y/N] ")
			if !confirmYes() {
				fmt.Println("Aborted")
				return nil
			}
		}
		dirs := []string{buildDir, filepath.Join(projectDir, "repo")}
		for _, dir := range dirs {
			if err := container.RemoveDir(context.Background(), dir, projectDir, ""); err != nil {
				return fmt.Errorf("removing %s: %w", dir, err)
			}
		}
		fmt.Println("Cleaned all build artifacts, packages, and sources")
	} else {
		if !force {
			fmt.Print("Remove all build intermediates? [y/N] ")
			if !confirmYes() {
				fmt.Println("Aborted")
				return nil
			}
		}
		if err := container.RemoveDir(context.Background(), buildDir, projectDir, ""); err != nil {
			return fmt.Errorf("removing %s: %w", buildDir, err)
		}
		fmt.Println("Cleaned build intermediates (packages preserved)")
	}

	return nil
}

func CleanLocks(projectDir, _ string) error {
	// Per-R14a build layout: build/<distro>/<unit>.<scope>/.lock. Walk
	// every distro subtree rather than a single per-arch tree.
	lockPaths, err := filepath.Glob(filepath.Join(projectDir, "build", "*", "*.*/.lock"))
	if err != nil {
		return err
	}
	if len(lockPaths) == 0 {
		// Surface "no build dir" vs "build dir with no locks" so the
		// user knows whether a typo or a clean slate is to blame.
		if _, err := os.Stat(filepath.Join(projectDir, "build")); os.IsNotExist(err) {
			fmt.Println("No build directory")
			return nil
		}
		fmt.Println("No stale locks found")
		return nil
	}
	for _, lockPath := range lockPaths {
		os.Remove(lockPath)
		// Surface the unit name plus its enclosing distro so the user
		// sees which build was holding the lock.
		rel, _ := filepath.Rel(filepath.Join(projectDir, "build"), filepath.Dir(lockPath))
		fmt.Printf("Removed lock: %s\n", rel)
	}
	fmt.Printf("Removed %d lock(s)\n", len(lockPaths))
	return nil
}

func confirmYes() bool {
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(scanner.Text()), "y")
}
