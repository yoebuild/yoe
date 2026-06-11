package internal

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
				if err := RemoveDirAnyOwner(dir, projectDir); err != nil {
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
			if err := RemoveDirAnyOwner(dir, projectDir); err != nil {
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
		if err := RemoveDirAnyOwner(buildDir, projectDir); err != nil {
			return fmt.Errorf("removing %s: %w", buildDir, err)
		}
		fmt.Println("Cleaned build intermediates (packages preserved)")
	}

	return nil
}

// RemoveDirAnyOwner removes dir, falling back to a container-side `rm -rf`
// when host-side os.RemoveAll hits EACCES on root- or service-user-owned
// files left by image-class builds. The container runs as uid 0 (NoUser:
// true) and has the privilege to remove them.
//
// dir must be under projectDir (we bind-mount projectDir into the container at
// /project and translate). The host user cannot rm those files without sudo,
// and yoe deliberately leaves them owned correctly so that
// build/<image>.<arch>/destdir/rootfs inspects with the same uid/gid the
// booted system will see — see docs/security.md and docs/comparisons.md.
func RemoveDirAnyOwner(dir, projectDir string) error {
	if err := os.RemoveAll(dir); err == nil {
		return nil
	}
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		return nil
	}
	rel, err := filepath.Rel(projectDir, dir)
	if err != nil {
		return fmt.Errorf("computing container path for %s: %w", dir, err)
	}
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("refusing to container-rm a path outside the project tree: %s", dir)
	}
	cPath := "/project/" + filepath.ToSlash(rel)
	image := fmt.Sprintf("yoe/toolchain-musl:15-%s", HostArch())
	return RunInContainer(ContainerRunConfig{
		Image:      image,
		Command:    "rm -rf " + cPath,
		ProjectDir: projectDir,
		NoUser:     true,
		Quiet:      true,
	})
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
