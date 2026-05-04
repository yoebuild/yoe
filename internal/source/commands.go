package source

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// FetchAll downloads sources for all units (or specific ones).
func FetchAll(projectDir string, unitNames []string, w io.Writer) error {
	proj, err := yoestar.LoadProject(projectDir)
	if err != nil {
		return err
	}

	units := filterUnits(proj, unitNames)
	for _, unit := range units {
		if unit.Source == "" {
			continue
		}
		if _, err := Fetch(unit, w); err != nil {
			return fmt.Errorf("fetching %s: %w", unit.Name, err)
		}
	}

	return nil
}

// ListSources shows cached sources and their status.
func ListSources(projectDir string, w io.Writer) error {
	proj, err := yoestar.LoadProject(projectDir)
	if err != nil {
		return err
	}

	cacheDir, err := CacheDir()
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "%-20s %-10s %s\n", "Unit", "Status", "Source")
	for _, unit := range proj.Units {
		if unit.Source == "" {
			continue
		}

		status := "missing"
		if isCached(cacheDir, unit) {
			status = "cached"
		}

		src := unit.Source
		if len(src) > 60 {
			src = src[:57] + "..."
		}
		fmt.Fprintf(w, "%-20s %-10s %s\n", unit.Name, status, src)
	}

	return nil
}

// VerifyAll checks SHA256 of cached sources.
func VerifyAll(projectDir string, w io.Writer) error {
	proj, err := yoestar.LoadProject(projectDir)
	if err != nil {
		return err
	}

	allOk := true
	for _, unit := range proj.Units {
		if unit.Source == "" || unit.SHA256 == "" {
			continue
		}
		if err := Verify(unit); err != nil {
			fmt.Fprintf(w, "FAIL  %s: %v\n", unit.Name, err)
			allOk = false
		} else {
			fmt.Fprintf(w, "OK    %s\n", unit.Name)
		}
	}

	if !allOk {
		return fmt.Errorf("some sources failed verification")
	}
	return nil
}

// CleanSources removes cached sources.
func CleanSources(w io.Writer) error {
	cacheDir, err := CacheDir()
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(w, "No cached sources")
			return nil
		}
		return err
	}

	count := 0
	for _, e := range entries {
		path := filepath.Join(cacheDir, e.Name())
		os.RemoveAll(path)
		count++
	}

	fmt.Fprintf(w, "Removed %d cached source(s)\n", count)
	return nil
}

func filterUnits(proj *yoestar.Project, names []string) []*yoestar.Unit {
	if len(names) == 0 {
		result := make([]*yoestar.Unit, 0, len(proj.Units))
		for _, r := range proj.Units {
			result = append(result, r)
		}
		return result
	}

	result := make([]*yoestar.Unit, 0, len(names))
	for _, name := range names {
		if r, ok := proj.Units[name]; ok {
			result = append(result, r)
		}
	}
	return result
}

func isCached(cacheDir string, unit *yoestar.Unit) bool {
	urlHash := fmt.Sprintf("%x", sha256.Sum256([]byte(unit.Source)))
	if isGitURL(unit.Source) {
		_, err := os.Stat(filepath.Join(cacheDir, urlHash+".git"))
		return err == nil
	}
	ext := guessExt(unit.Source)
	_, err := os.Stat(filepath.Join(cacheDir, urlHash+ext))
	return err == nil
}
