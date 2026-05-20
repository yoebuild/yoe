package internal

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func RunClean(projectDir, arch string, all bool, force bool, units []string) error {
	buildDir := filepath.Join(projectDir, "build")

	if len(units) > 0 {
		for _, r := range units {
			dir := filepath.Join(buildDir, arch, r)
			if err := removeDirAnyOwner(dir, projectDir); err != nil {
				return fmt.Errorf("removing %s: %w", dir, err)
			}
			fmt.Printf("Cleaned %s\n", r)
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
			if err := removeDirAnyOwner(dir, projectDir); err != nil {
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
		if err := removeDirAnyOwner(buildDir, projectDir); err != nil {
			return fmt.Errorf("removing %s: %w", buildDir, err)
		}
		fmt.Println("Cleaned build intermediates (packages preserved)")
	}

	return nil
}

// removeDirAnyOwner removes dir, falling back to a container-side `rm -rf` when
// host-side os.RemoveAll hits EACCES on root- or service-user-owned files left
// by image-class builds. The container runs as uid 0 (NoUser: true) and has
// the privilege to remove them.
//
// dir must be under projectDir (we bind-mount projectDir into the container at
// /project and translate). The host user cannot rm those files without sudo,
// and yoe deliberately leaves them owned correctly so that
// build/<image>.<arch>/destdir/rootfs inspects with the same uid/gid the
// booted system will see — see docs/security.md and docs/comparisons.md.
func removeDirAnyOwner(dir, projectDir string) error {
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

func CleanLocks(projectDir, arch string) error {
	buildDir := filepath.Join(projectDir, "build", arch)
	entries, err := os.ReadDir(buildDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No build directory")
			return nil
		}
		return err
	}

	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		lockPath := filepath.Join(buildDir, e.Name(), ".lock")
		if _, err := os.Stat(lockPath); err == nil {
			os.Remove(lockPath)
			fmt.Printf("Removed lock: %s\n", e.Name())
			count++
		}
	}
	if count == 0 {
		fmt.Println("No stale locks found")
	} else {
		fmt.Printf("Removed %d lock(s)\n", count)
	}
	return nil
}

func confirmYes() bool {
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(scanner.Text()), "y")
}
