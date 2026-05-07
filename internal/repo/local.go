package repo

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yoebuild/yoe/internal/artifact"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// RepoDir returns the local package repository path for a project.
// Repos are scoped per project: repo/<project-name>/.
// This prevents stale packages from one project contaminating another's APKINDEX.
func RepoDir(proj *yoestar.Project, projectDir string) string {
	if proj != nil && proj.Name != "" {
		return filepath.Join(projectDir, "repo", proj.Name)
	}
	return filepath.Join(projectDir, "repo")
}

// Publish copies an .apk file into the per-arch subdirectory of the local
// repository and regenerates the APKINDEX for that arch. The on-disk layout
// matches Alpine's convention so `apk add -X <repoDir>` Just Works:
//
//	<repoDir>/<archDir>/<pkg>-<ver>-r<N>.apk
//	<repoDir>/<archDir>/APKINDEX.tar.gz
//	<repoDir>/keys/<keyname>.rsa.pub  (when signer is non-nil)
//
// archDir is the package's arch ("x86_64", "aarch64", ...) or the literal
// "noarch" for portable packages. The public key sits in <repoDir>/keys/
// so apk add can verify the repo via `--keys-dir <repoDir>/keys` without
// any further configuration.
func Publish(apkPath, repoDir, archDir string, signer *artifact.Signer) error {
	archPath := filepath.Join(repoDir, archDir)
	if err := os.MkdirAll(archPath, 0755); err != nil {
		return err
	}

	name := filepath.Base(apkPath)
	dst := filepath.Join(archPath, name)

	src, err := os.Open(apkPath)
	if err != nil {
		return err
	}
	defer src.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, src); err != nil {
		return err
	}

	if signer != nil {
		if err := WritePublicKey(repoDir, signer); err != nil {
			return fmt.Errorf("publishing public key: %w", err)
		}
	}

	if err := GenerateIndex(archPath, signer); err != nil {
		return err
	}
	// A noarch publish has to refresh every per-arch APKINDEX too —
	// each one includes noarch entries via GenerateIndex's sibling
	// scan, so adding a noarch apk silently invalidates them all.
	if archDir == "noarch" {
		archDirs, err := ArchDirs(repoDir)
		if err != nil {
			return err
		}
		for _, ad := range archDirs {
			if ad == "noarch" {
				continue
			}
			if err := GenerateIndex(filepath.Join(repoDir, ad), signer); err != nil {
				return fmt.Errorf("regenerating %s APKINDEX after noarch publish: %w", ad, err)
			}
		}
	}
	return nil
}

// WritePublicKey drops the project's public key into <repoDir>/keys/ so
// image-time apk add can find it via --keys-dir, and so anyone consuming
// the repo can install signature verification with a single file copy.
func WritePublicKey(repoDir string, signer *artifact.Signer) error {
	keysDir := filepath.Join(repoDir, "keys")
	if err := os.MkdirAll(keysDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(keysDir, signer.KeyName), signer.PubPEM, 0644)
}

// KeysDir returns the directory under repoDir that holds published public
// keys. apk's --keys-dir flag points at this path during image assembly.
func KeysDir(repoDir string) string {
	return filepath.Join(repoDir, "keys")
}

// ArchDirs returns the per-arch subdirectories that hold .apk files in repoDir.
// Useful for callers that need to walk every arch's contents (list, info,
// remove, and the image-rootfs assembler that searches multiple arch dirs).
func ArchDirs(repoDir string) ([]string, error) {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// List prints all packages in the local repository, grouped by arch.
func List(repoDir string, w io.Writer) error {
	archDirs, err := ArchDirs(repoDir)
	if err != nil {
		return err
	}
	if len(archDirs) == 0 {
		fmt.Fprintln(w, "No packages in repository")
		return nil
	}

	fmt.Fprintf(w, "Repository: %s\n", repoDir)
	total := 0
	for _, ad := range archDirs {
		archPath := filepath.Join(repoDir, ad)
		entries, err := os.ReadDir(archPath)
		if err != nil {
			return err
		}
		var apks []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".apk") {
				apks = append(apks, e.Name())
			}
		}
		if len(apks) == 0 {
			continue
		}
		sort.Strings(apks)
		fmt.Fprintf(w, "\n[%s]\n", ad)
		for _, name := range apks {
			info, _ := os.Stat(filepath.Join(archPath, name))
			size := ""
			if info != nil {
				size = formatSize(info.Size())
			}
			fmt.Fprintf(w, "  %-40s %s\n", name, size)
		}
		total += len(apks)
	}
	if total == 0 {
		fmt.Fprintln(w, "No packages in repository")
		return nil
	}
	fmt.Fprintf(w, "\n%d package(s)\n", total)
	return nil
}

// Info shows details about the first matching package across every arch
// subdirectory in the repository.
func Info(repoDir, pkgName string, w io.Writer) error {
	archDirs, err := ArchDirs(repoDir)
	if err != nil {
		return err
	}

	for _, ad := range archDirs {
		archPath := filepath.Join(repoDir, ad)
		entries, err := os.ReadDir(archPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), pkgName) && strings.HasSuffix(e.Name(), ".apk") {
				apkPath := filepath.Join(archPath, e.Name())
				hash, err := artifact.APKHash(apkPath)
				if err != nil {
					return err
				}
				info, _ := os.Stat(apkPath)
				fmt.Fprintf(w, "Package:  %s\n", e.Name())
				fmt.Fprintf(w, "Arch:     %s\n", ad)
				fmt.Fprintf(w, "SHA256:   %s\n", hash)
				if info != nil {
					fmt.Fprintf(w, "Size:     %s\n", formatSize(info.Size()))
				}
				return nil
			}
		}
	}
	return fmt.Errorf("package %q not found in repository", pkgName)
}

// Clean drops .apk files in repoDir that are not produced by any current
// project unit, then regenerates APKINDEX for every arch we touched. The
// "expected" filename for a unit is `<name>-<version>-r<release>.apk`,
// matching the convention CreateAPK uses on publish — see internal/artifact
// /apk.go. Anything else is treated as stale (unit removed, version bumped,
// build flags changed in a way that altered the release suffix) and
// removed.
//
// Without this, the local repo accumulates old .apk files indefinitely,
// and image-time `apk add` happily picks the highest-versioned candidate
// — which can be a deleted unit's leftover (e.g. a yoe-built apk-tools
// from before switching to the module-alpine prebuilt). The APKINDEX is
// always rebuilt from the surviving files via GenerateIndex, so apk
// resolves only what's actually on disk.
func Clean(proj *yoestar.Project, repoDir string, signer *artifact.Signer, w io.Writer) error {
	if proj == nil {
		return fmt.Errorf("Clean requires a loaded project")
	}

	keep := make(map[string]struct{}, len(proj.Units))
	for _, u := range proj.Units {
		if u.Version == "" {
			continue
		}
		keep[fmt.Sprintf("%s-%s-r%d.apk", u.Name, u.Version, u.Release)] = struct{}{}
	}

	archDirs, err := ArchDirs(repoDir)
	if err != nil {
		return err
	}

	removed := 0
	dirtyArches := map[string]struct{}{}
	for _, ad := range archDirs {
		archPath := filepath.Join(repoDir, ad)
		entries, err := os.ReadDir(archPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".apk") {
				continue
			}
			if _, ok := keep[name]; ok {
				continue
			}
			if err := os.Remove(filepath.Join(archPath, name)); err != nil {
				return fmt.Errorf("removing %s/%s: %w", ad, name, err)
			}
			fmt.Fprintf(w, "Removed %s/%s\n", ad, name)
			removed++
			dirtyArches[ad] = struct{}{}
		}
	}

	for ad := range dirtyArches {
		if err := GenerateIndex(filepath.Join(repoDir, ad), signer); err != nil {
			return fmt.Errorf("regenerating APKINDEX for %s: %w", ad, err)
		}
	}

	if removed == 0 {
		fmt.Fprintln(w, "Repository already clean")
	} else {
		fmt.Fprintf(w, "Removed %d stale .apk file(s)\n", removed)
	}
	return nil
}

// Remove deletes every matching package from the local repository, walking
// each arch subdirectory. The APKINDEX is regenerated for every arch we
// touch — using `signer` to keep the regenerated index signed and verifiable
// by apk-tools, or unsigned when nil.
func Remove(repoDir, pkgName string, signer *artifact.Signer, w io.Writer) error {
	archDirs, err := ArchDirs(repoDir)
	if err != nil {
		return err
	}

	removed := 0
	dirtyArches := map[string]struct{}{}
	for _, ad := range archDirs {
		archPath := filepath.Join(repoDir, ad)
		entries, err := os.ReadDir(archPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), pkgName) && strings.HasSuffix(e.Name(), ".apk") {
				path := filepath.Join(archPath, e.Name())
				if err := os.Remove(path); err != nil {
					return fmt.Errorf("removing %s: %w", e.Name(), err)
				}
				fmt.Fprintf(w, "Removed %s/%s\n", ad, e.Name())
				removed++
				dirtyArches[ad] = struct{}{}
			}
		}
	}

	if removed == 0 {
		return fmt.Errorf("package %q not found in repository", pkgName)
	}

	// Regenerate APKINDEX for every arch we touched so the index doesn't
	// reference deleted files.
	for ad := range dirtyArches {
		if err := GenerateIndex(filepath.Join(repoDir, ad), signer); err != nil {
			return fmt.Errorf("regenerating APKINDEX for %s: %w", ad, err)
		}
	}
	return nil
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fK", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fM", float64(bytes)/(1024*1024))
}
