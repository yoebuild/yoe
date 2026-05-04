package bootstrap

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	yoe "github.com/yoebuild/yoe/internal"
	"github.com/yoebuild/yoe/internal/build"
	"github.com/yoebuild/yoe/internal/artifact"
	"github.com/yoebuild/yoe/internal/repo"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// Bootstrap unit names — the minimal set needed for a self-hosting build root.
var bootstrapUnits = []string{
	"linux-headers",
	"glibc",
	"binutils",
	"gcc",
	"busybox",
	"apk-tools",
	"bubblewrap",
}

// Stage0 builds the initial base packages using the host's toolchain (Alpine's
// gcc inside the container). The output is a minimal set of .apk files — enough
// to create a self-hosting Yoe build root.
func Stage0(proj *yoestar.Project, projectDir string, w io.Writer) error {
	fmt.Fprintln(w, "=== Bootstrap Stage 0: Cross-Pollination ===")
	fmt.Fprintln(w, "Building base packages using host toolchain...")
	fmt.Fprintln(w)

	arch := build.Arch()

	// Verify bootstrap units exist
	var missing []string
	for _, name := range bootstrapUnits {
		if _, ok := proj.Units[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("bootstrap units not found: %s\nAdd them to your project or include a module that provides them",
			strings.Join(missing, ", "))
	}

	// Build each bootstrap unit without sandbox isolation (using host tools)
	repoDir := repo.RepoDir(proj, projectDir)

	for _, name := range bootstrapUnits {
		unit := proj.Units[name]
		fmt.Fprintf(w, "\n--- Building %s %s ---\n", unit.Name, unit.Version)

		buildDir := build.UnitBuildDir(projectDir, arch, unit.Name)
		destDir := filepath.Join(buildDir, "destdir")

		// Clean and prepare
		os.RemoveAll(destDir)
		os.MkdirAll(destDir, 0755)

		// Get build commands
		commands := stage0Commands(unit)
		if len(commands) == 0 {
			fmt.Fprintf(w, "  (no build steps for %s)\n", unit.Name)
			continue
		}

		// Build environment — use host tools directly, no sandbox
		env := map[string]string{
			"PREFIX":  "/usr",
			"DESTDIR": destDir,
			"NPROC":   build.NProc(),
			"ARCH":    arch,
			"HOME":    "/tmp",
		}

		for i, cmd := range commands {
			fmt.Fprintf(w, "  [%d/%d] %s\n", i+1, len(commands), cmd)
			cfg := &build.SandboxConfig{
				SrcDir:     buildDir,
				DestDir:    destDir,
				Env:        env,
				ProjectDir: projectDir,
			}
			if err := build.RunSimple(cfg, cmd); err != nil {
				return fmt.Errorf("stage0 %s step %d: %w", unit.Name, i+1, err)
			}
		}

		// Package the output
		apkPath, err := artifact.CreateAPK(unit, destDir, filepath.Join(buildDir, "pkg"), arch, "", nil)
		if err != nil {
			return fmt.Errorf("packaging %s: %w", unit.Name, err)
		}

		// Publish to the local repo
		if err := repo.Publish(apkPath, repoDir, arch, nil); err != nil {
			return fmt.Errorf("publishing %s: %w", unit.Name, err)
		}

		fmt.Fprintf(w, "  → %s\n", filepath.Base(apkPath))
	}

	fmt.Fprintf(w, "\n=== Stage 0 complete: %d packages in %s ===\n", len(bootstrapUnits), repoDir)
	return nil
}

// Stage1 rebuilds the base packages using the Stage 0 packages. After this,
// all packages in the repository were built by Yoe's own toolchain.
func Stage1(proj *yoestar.Project, projectDir string, w io.Writer) error {
	fmt.Fprintln(w, "=== Bootstrap Stage 1: Self-Hosting Rebuild ===")
	fmt.Fprintln(w, "Rebuilding base packages using Yoe's own toolchain...")
	fmt.Fprintln(w)

	arch := build.Arch()
	repoDir := repo.RepoDir(proj, projectDir)

	// Verify Stage 0 packages exist in the repo
	if err := verifyStage0(repoDir, arch); err != nil {
		return fmt.Errorf("stage 0 not complete: %w\nRun 'yoe bootstrap stage0' first", err)
	}

	// Create a Yoe build root from Stage 0 packages
	buildRoot := filepath.Join(projectDir, "build", "bootstrap", "buildroot")
	if err := createBuildRoot(buildRoot, repoDir, projectDir, w); err != nil {
		return fmt.Errorf("creating build root: %w", err)
	}

	// Rebuild each bootstrap unit inside the build root
	for _, name := range bootstrapUnits {
		unit := proj.Units[name]
		fmt.Fprintf(w, "\n--- Rebuilding %s %s (self-hosted) ---\n", unit.Name, unit.Version)

		buildDir := build.UnitBuildDir(projectDir, arch, unit.Name)
		destDir := filepath.Join(buildDir, "destdir")

		os.RemoveAll(destDir)
		os.MkdirAll(destDir, 0755)

		commands := stage0Commands(unit)
		if len(commands) == 0 {
			continue
		}

		// Build inside the Yoe build root using bubblewrap
		env := map[string]string{
			"PREFIX":  "/usr",
			"DESTDIR": "/build/destdir",
			"NPROC":   build.NProc(),
			"ARCH":    arch,
			"HOME":    "/tmp",
		}

		for i, cmd := range commands {
			fmt.Fprintf(w, "  [%d/%d] %s\n", i+1, len(commands), cmd)

			cfg := &build.SandboxConfig{
				BuildRoot:  buildRoot,
				SrcDir:     buildDir,
				DestDir:    destDir,
				Env:        env,
				ProjectDir: projectDir,
			}
			if err := build.RunInSandbox(cfg, cmd); err != nil {
				return fmt.Errorf("stage1 %s step %d: %w", unit.Name, i+1, err)
			}
		}

		// Package and publish (overwriting Stage 0 packages)
		apkPath, err := artifact.CreateAPK(unit, destDir, filepath.Join(buildDir, "pkg"), arch, "", nil)
		if err != nil {
			return fmt.Errorf("packaging %s: %w", unit.Name, err)
		}
		if err := repo.Publish(apkPath, repoDir, arch, nil); err != nil {
			return fmt.Errorf("publishing %s: %w", unit.Name, err)
		}

		fmt.Fprintf(w, "  → %s (self-hosted)\n", filepath.Base(apkPath))
	}

	fmt.Fprintf(w, "\n=== Stage 1 complete: all base packages rebuilt with Yoe toolchain ===\n")
	return nil
}

// Status shows the current bootstrap state.
func Status(proj *yoestar.Project, projectDir string, w io.Writer) error {
	repoDir := repo.RepoDir(proj, projectDir)
	arch := build.Arch()
	archDir := filepath.Join(repoDir, arch)

	fmt.Fprintf(w, "Bootstrap status for %s\n\n", proj.Name)
	fmt.Fprintf(w, "Repository: %s\n", repoDir)
	fmt.Fprintf(w, "Architecture: %s\n\n", arch)

	for _, name := range bootstrapUnits {
		status := "missing"
		if _, ok := proj.Units[name]; ok {
			status = "unit found"
		}

		// Check if package exists in repo's per-arch subdir
		entries, err := os.ReadDir(archDir)
		if err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), name+"-") && strings.HasSuffix(e.Name(), ".apk") {
					status = "built (" + e.Name() + ")"
					break
				}
			}
		}

		fmt.Fprintf(w, "  %-16s %s\n", name, status)
	}

	return nil
}

// stage0Commands extracts shell commands from a unit's tasks for bootstrap builds.
func stage0Commands(unit *yoestar.Unit) []string {
	var cmds []string
	for _, t := range unit.Tasks {
		for _, s := range t.Steps {
			if s.Command != "" {
				cmds = append(cmds, s.Command)
			}
		}
	}
	return cmds
}

func verifyStage0(repoDir, arch string) error {
	archDir := filepath.Join(repoDir, arch)
	entries, err := os.ReadDir(archDir)
	if err != nil {
		return fmt.Errorf("repo arch dir not found at %s", archDir)
	}

	for _, name := range bootstrapUnits {
		found := false
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), name+"-") && strings.HasSuffix(e.Name(), ".apk") {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("package %s not found in repo (arch=%s)", name, arch)
		}
	}

	return nil
}

func createBuildRoot(buildRoot, repoDir, projectDir string, w io.Writer) error {
	fmt.Fprintf(w, "Creating build root at %s...\n", buildRoot)

	os.RemoveAll(buildRoot)
	os.MkdirAll(buildRoot, 0755)

	// Use apk inside the container to install base packages. The repo is
	// in Alpine layout (`/build/repo/<arch>/APKINDEX.tar.gz`), so passing
	// the parent path as a repository works directly. --allow-untrusted is
	// required until we add signing in phase 3 of the apk-compat plan.
	args := []string{
		"apk",
		"--root", "/build/buildroot",
		"--initdb",
		"--no-scripts",
		"--no-cache",
		"--allow-untrusted",
		"--repository", "/build/repo",
		"add",
	}
	args = append(args, bootstrapUnits...)
	cmd := strings.Join(args, " ")

	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Image:      "yoe/toolchain-musl:15",
		Command:    cmd,
		ProjectDir: projectDir,
		Mounts: []yoe.Mount{
			{Host: buildRoot, Container: "/build/buildroot"},
			{Host: repoDir, Container: "/build/repo", ReadOnly: true},
		},
	})
}
