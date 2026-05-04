package module

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// CacheDir returns the module cache directory.
// Defaults to cache/modules/ in the current working directory.
func CacheDir() (string, error) {
	dir := os.Getenv("YOE_CACHE")
	if dir == "" {
		dir = "cache"
	}
	dir = filepath.Join(dir, "modules")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// Sync fetches all modules declared in the project. For each module:
// - If Local is set, skip (use the local path directly)
// - Otherwise, git clone/fetch into $YOE_CACHE/modules/<name>/
// Returns a map of module name -> directory path.
func Sync(proj *yoestar.Project, w io.Writer) (map[string]string, error) {
	cacheDir, err := CacheDir()
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)

	for _, m := range proj.Modules {
		name := ModuleName(m)

		if m.Local != "" {
			fmt.Fprintf(w, "  %-20s (local: %s)\n", name, m.Local)
			result[name] = m.Local
			continue
		}

		moduleDir := filepath.Join(cacheDir, name)
		ref := m.Ref
		if ref == "" {
			ref = "main"
		}

		if _, err := os.Stat(filepath.Join(moduleDir, ".git")); os.IsNotExist(err) {
			// Clone
			fmt.Fprintf(w, "  %-20s cloning %s (ref: %s)...\n", name, m.URL, ref)
			cmd := exec.Command("git", "clone", "--depth", "1", "--branch", ref, m.URL, moduleDir)
			cmd.Stdout = w
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return nil, fmt.Errorf("cloning module %s: %w", name, err)
			}
		} else {
			// Fetch and checkout the right ref
			fmt.Fprintf(w, "  %-20s fetching %s...\n", name, ref)
			cmd := exec.Command("git", "fetch", "origin", ref)
			cmd.Dir = moduleDir
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return nil, fmt.Errorf("fetching module %s: %w", name, err)
			}

			cmd = exec.Command("git", "checkout", "FETCH_HEAD")
			cmd.Dir = moduleDir
			cmd.Stderr = os.Stderr
			cmd.Run() // best effort
		}

		// If module specifies a subdirectory path, use that
		moduleRoot := moduleDir
		if m.Path != "" {
			moduleRoot = filepath.Join(moduleDir, m.Path)
		}

		result[name] = moduleRoot
		fmt.Fprintf(w, "  %-20s → %s\n", name, moduleRoot)
	}

	return result, nil
}

// SyncIfNeeded clones any modules that are not already cached. Unlike Sync,
// it does not fetch/update modules that already exist — keeping it fast enough
// to call on every build without adding latency.
func SyncIfNeeded(modules []yoestar.ModuleRef, w io.Writer) error {
	cacheDir, err := CacheDir()
	if err != nil {
		return err
	}

	for _, m := range modules {
		if m.Local != "" {
			continue
		}

		name := ModuleName(m)
		moduleDir := filepath.Join(cacheDir, name)

		if _, err := os.Stat(filepath.Join(moduleDir, ".git")); err == nil {
			continue // already cloned
		}

		ref := m.Ref
		if ref == "" {
			ref = "main"
		}

		fmt.Fprintf(w, "[yoe] cloning module %s (ref: %s)...\n", name, ref)
		cmd := exec.Command("git", "clone", "--depth", "1", "--branch", ref, m.URL, moduleDir)
		cmd.Stdout = w
		cmd.Stderr = w
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("cloning module %s: %w", name, err)
		}
	}

	return nil
}

// ResolveModulePaths returns the module name -> directory mapping for a project.
// Uses local overrides when set, otherwise checks the cache.
func ResolveModulePaths(proj *yoestar.Project, projectRoot string) (map[string]string, error) {
	cacheDir, err := CacheDir()
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)

	for _, m := range proj.Modules {
		name := ModuleName(m)

		if m.Local != "" {
			path := m.Local
			if !filepath.IsAbs(path) {
				path = filepath.Join(projectRoot, path)
			}
			result[name] = path
			continue
		}

		// Check cache
		moduleDir := filepath.Join(cacheDir, name)
		if _, err := os.Stat(moduleDir); err == nil {
			moduleRoot := moduleDir
			if m.Path != "" {
				moduleRoot = filepath.Join(moduleDir, m.Path)
			}
			result[name] = moduleRoot
		}
		// If not cached, it will be missing — yoe module sync is needed
	}

	return result, nil
}

// ModuleName derives the module name from a ModuleRef.
// If Path is set, uses the last component of Path (e.g., "modules/units-core" -> "units-core").
// Otherwise uses the last component of URL (e.g., "github.com/yoe/units-core" -> "units-core").
func ModuleName(m yoestar.ModuleRef) string {
	if m.Path != "" {
		return filepath.Base(m.Path)
	}
	url := strings.TrimSuffix(m.URL, ".git")
	return filepath.Base(url)
}
