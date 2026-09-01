package module

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yoebuild/yoe/internal/gitutil"
	"github.com/yoebuild/yoe/internal/source"
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

// Sync fetches the given modules. For each module:
// - If Local is set, skip (use the local path directly)
// - Otherwise, git clone/fetch into $YOE_CACHE/modules/<name>/
// Returns a map of module name -> directory path.
func Sync(modules []yoestar.ModuleRef, w io.Writer) (map[string]string, error) {
	cacheDir, err := CacheDir()
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)

	for _, m := range modules {
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
			cmd := gitutil.Command("", "clone", "--depth", "1", "--branch", ref, m.URL, moduleDir)
			cmd.Stdout = w
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return nil, fmt.Errorf("cloning module %s: %w", name, err)
			}
		} else if state := moduleState(moduleDir); source.IsDev(state) {
			// A module the user switched to dev mode is theirs. Bring
			// it up to date the way they would by hand and leave the
			// working tree alone if that is not possible.
			pullDevModule(moduleDir, name, state, w)
		} else {
			// Pin mode: yoe owns this tree, so put it on the declared ref.
			fmt.Fprintf(w, "  %-20s fetching %s...\n", name, ref)
			cmd := gitutil.Command(moduleDir, "fetch", "origin", ref)
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return nil, fmt.Errorf("fetching module %s: %w", name, err)
			}

			cmd = gitutil.Command(moduleDir, "checkout", "FETCH_HEAD")
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return nil, fmt.Errorf("checking out %s in module %s: %w", ref, name, err)
			}
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

// moduleState reports the source state of an existing module clone,
// combining the persisted toggle decision with observed git state.
//
// When the git probe fails, a clone the user has recorded as dev still
// counts as dev: an unclassifiable tree the user has claimed is exactly
// the one to leave alone. Anything else reads as StateEmpty and takes
// the pin path, where yoe-managed trees belong.
func moduleState(moduleDir string) source.State {
	cached := ReadState(moduleDir)
	state, err := source.DetectState(moduleDir, cached)
	if err != nil {
		if source.IsDev(cached) {
			return cached
		}
		return source.StateEmpty
	}
	return state
}

// pullDevModule fast-forwards a dev-mode module clone onto its tracking
// branch, the same `git pull --ff-only` the user would run themselves.
// The clone comes off `git clone --branch <ref>` already on a local
// branch tracking origin/<ref>, so the configured upstream is there to
// pull from — and following the branch the user is actually on, rather
// than the project's declared ref, is the point of dev mode.
//
// A pull that cannot fast-forward is the expected outcome whenever the
// user has commits of their own or edits in the tree. That is not a
// sync failure: leaving the work untouched is the correct result, so
// this reports what happened and returns. Only the pin path, where yoe
// owns the tree, treats a checkout failure as fatal.
func pullDevModule(moduleDir, name string, state source.State, w io.Writer) {
	fmt.Fprintf(w, "  %-20s dev mode — pulling instead of checking out\n", name)
	out, err := gitutil.Run(moduleDir, "pull", "--ff-only")
	if err != nil {
		fmt.Fprintf(w, "  %-20s dev mode — left as-is: %s\n", name, firstLine(err.Error()))
		return
	}
	fmt.Fprintf(w, "  %-20s %s\n", name, firstLine(out))

	// Re-anchor the dev tag on a clone that had no local work, so a
	// module merely following upstream keeps reporting `dev` rather
	// than looking like it carries commits of its own. A clone that
	// was already dev-mod or dev-dirty keeps its original anchor —
	// the commits it counts are still the user's.
	if state == source.StateDev {
		_, _ = gitutil.Run(moduleDir, "tag", "-f", source.PinTag, "HEAD")
	}
}

// firstLine trims git output down to its headline so the sync listing
// stays one line per module. Git's own detail still reaches the user
// through the error text when a pull is refused.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
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
		cmd := gitutil.Command("", "clone", "--depth", "1", "--branch", ref, m.URL, moduleDir)
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
// If Path is set, uses the last component of Path (e.g., "modules/module-core" -> "module-core").
// Otherwise uses the last component of URL (e.g., "github.com/yoe/module-core" -> "module-core").
func ModuleName(m yoestar.ModuleRef) string {
	if m.Path != "" {
		return filepath.Base(m.Path)
	}
	url := strings.TrimSuffix(m.URL, ".git")
	return filepath.Base(url)
}
