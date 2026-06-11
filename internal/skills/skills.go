// Package skills installs the Claude Code skills baked into the yoe binary
// into a project's own .claude/skills directory.
//
// yoe ships its skills embedded rather than through Claude Code's plugin
// marketplace: `yoe skills install` writes editable copies straight into the
// workspace, so a user owns the files and there is no managed plugin cache to
// fight when they want to tweak one. `yoe skills update` refreshes the
// yoe-managed skills back to the versions in the running binary.
package skills

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	embedded "github.com/yoebuild/yoe"
)

// embedRoot is the path of the skill tree inside embedded.SkillsFS. embed.FS
// always uses forward slashes, independent of the host OS.
const embedRoot = ".claude/skills"

// Names returns the sorted skill names baked into the binary.
func Names() ([]string, error) {
	entries, err := fs.ReadDir(embedded.SkillsFS, embedRoot)
	if err != nil {
		return nil, fmt.Errorf("reading embedded skills: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Install materializes the embedded skills into <root>/.claude/skills.
//
// When overwrite is false (the `install` verb), a skill whose target directory
// already exists is left untouched and reported as skipped, so local edits and
// any same-named skill the user authored survive. When overwrite is true (the
// `update` verb), each yoe-managed skill directory is removed and re-extracted
// so it matches the binary exactly — including files dropped upstream.
//
// Either way, only the names yoe ships are touched; unrelated skills already in
// the project's .claude/skills are never read or modified.
func Install(root string, overwrite bool, out io.Writer) error {
	names, err := Names()
	if err != nil {
		return err
	}

	destBase := filepath.Join(root, ".claude", "skills")
	if err := os.MkdirAll(destBase, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", destBase, err)
	}

	var installed, updated, skipped []string
	for _, name := range names {
		dest := filepath.Join(destBase, name)
		_, statErr := os.Stat(dest)
		exists := statErr == nil

		if exists && !overwrite {
			skipped = append(skipped, name)
			continue
		}
		if exists {
			// Refresh: drop the managed directory first so files removed
			// upstream don't linger, then re-extract from the binary.
			if err := os.RemoveAll(dest); err != nil {
				return fmt.Errorf("refreshing skill %s: %w", name, err)
			}
		}
		if err := extractSkill(name, dest); err != nil {
			return err
		}
		if exists {
			updated = append(updated, name)
		} else {
			installed = append(installed, name)
		}
	}

	report(out, destBase, installed, updated, skipped)
	return nil
}

// extractSkill copies the embedded subtree for one skill into dest.
func extractSkill(name, dest string) error {
	srcRoot := path.Join(embedRoot, name)
	return fs.WalkDir(embedded.SkillsFS, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, srcRoot), "/")
		target := dest
		if rel != "" {
			target = filepath.Join(dest, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := embedded.SkillsFS.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func report(out io.Writer, destBase string, installed, updated, skipped []string) {
	fmt.Fprintf(out, "Skills → %s\n", destBase)
	for _, n := range installed {
		fmt.Fprintf(out, "  installed  %s\n", n)
	}
	for _, n := range updated {
		fmt.Fprintf(out, "  updated    %s\n", n)
	}
	for _, n := range skipped {
		fmt.Fprintf(out, "  skipped    %s (already present; run 'yoe skills update' to refresh)\n", n)
	}
	if len(installed)+len(updated)+len(skipped) == 0 {
		fmt.Fprintln(out, "  (no skills embedded in this binary)")
	}
}
