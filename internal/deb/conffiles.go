package deb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteConffiles emits DEBIAN/conffiles from a unit's conffiles list.
// dpkg reads that file at install time and, for each path it names,
// preserves a locally modified copy across upgrades instead of
// overwriting it — prompting or leaving a .dpkg-new alongside.
//
// Called before BuildDeb, alongside MaterializeSystemdServiceSymlinks:
// both turn a declarative unit field into content under destDir that the
// package then carries.
//
// Paths are emitted absolute (dpkg requires it) and sorted, so two builds
// of the same unit produce byte-identical output. A path that names
// nothing in destDir is a unit bug — dpkg would record a conffile the
// package never ships — so it is reported rather than written.
//
// This has no apk-side counterpart by design: apk records a checksum for
// every installed file and writes a .apk-new when the on-disk copy has
// been modified, so config preservation is automatic and needs no
// per-package declaration.
func WriteConffiles(destDir string, conffiles []string) error {
	if len(conffiles) == 0 {
		return nil
	}

	paths := make([]string, 0, len(conffiles))
	for _, p := range conffiles {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if _, err := os.Lstat(filepath.Join(destDir, strings.TrimPrefix(p, "/"))); err != nil {
			return fmt.Errorf("deb: conffiles: %s is declared but not installed by this unit", p)
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)

	debianDir := filepath.Join(destDir, "DEBIAN")
	if err := os.MkdirAll(debianDir, 0755); err != nil {
		return fmt.Errorf("deb: mkdir DEBIAN: %w", err)
	}
	body := strings.Join(paths, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(debianDir, "conffiles"), []byte(body), 0644); err != nil {
		return fmt.Errorf("deb: write conffiles: %w", err)
	}
	return nil
}
