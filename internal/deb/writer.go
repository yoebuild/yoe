package deb

import (
	"crypto/md5"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// BuildDeb assembles a .deb from a staged destdir + control metadata.
// It writes DEBIAN/control + DEBIAN/md5sums under destDir, bakes any
// `services=[...]` symlinks (the caller invokes
// MaterializeSystemdServiceSymlinks first), then shells `dpkg-deb
// --build` to produce outputPath.
//
// compression is "xz" (Debian bookworm default), "gzip" (lighter, less
// dense), or "zstd" (faster, requires dpkg >= 1.21.18). Pass "" for
// dpkg-deb's default (currently xz on Debian bookworm).
//
// destDir is mutated: DEBIAN/control, DEBIAN/md5sums, and any service
// symlinks land inside it before the build. Callers that want to keep
// destDir clean should stage into a scratch copy.
func BuildDeb(destDir string, control Control, outputPath, compression string) error {
	debianDir := filepath.Join(destDir, "DEBIAN")
	if err := os.MkdirAll(debianDir, 0755); err != nil {
		return fmt.Errorf("deb: mkdir DEBIAN: %w", err)
	}

	controlPath := filepath.Join(debianDir, "control")
	if _, err := os.Stat(controlPath); err == nil {
		// A unit may ship its own control (rare) — respect it.
	} else {
		// Compute Installed-Size if the caller didn't.
		if control.InstalledSize == 0 {
			size, err := installedSize(destDir)
			if err != nil {
				return fmt.Errorf("deb: installed size: %w", err)
			}
			control.InstalledSize = size
		}
		f, err := os.Create(controlPath)
		if err != nil {
			return fmt.Errorf("deb: create control: %w", err)
		}
		if err := WriteControl(f, control); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("deb: close control: %w", err)
		}
	}

	if err := writeMd5sums(destDir, debianDir); err != nil {
		return fmt.Errorf("deb: write md5sums: %w", err)
	}

	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		return fmt.Errorf("deb: dpkg-deb missing on PATH: %w", err)
	}

	args := []string{"--build", "--root-owner-group"}
	if compression != "" {
		args = append(args, "-Z"+compression)
	}
	args = append(args, destDir, outputPath)
	cmd := exec.Command("dpkg-deb", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deb: dpkg-deb: %w: %s", err, out)
	}
	return nil
}

// installedSize walks destDir and returns the total in kilobytes per
// dpkg's Installed-Size convention.
func installedSize(destDir string) (int, error) {
	var total int64
	err := filepath.WalkDir(destDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if rel, _ := filepath.Rel(destDir, path); strings.HasPrefix(rel, "DEBIAN") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(total / 1024), nil
}

// writeMd5sums walks destDir for regular files outside DEBIAN/ and
// writes DEBIAN/md5sums in the canonical "<hex>  <relative-path>"
// format (two spaces, no leading slash, no DEBIAN/ entries).
func writeMd5sums(destDir, debianDir string) error {
	var paths []string
	err := filepath.WalkDir(destDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(destDir, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "DEBIAN/") || rel == "DEBIAN" {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)

	out, err := os.Create(filepath.Join(debianDir, "md5sums"))
	if err != nil {
		return err
	}
	defer out.Close()

	for _, rel := range paths {
		full := filepath.Join(destDir, rel)
		f, err := os.Open(full)
		if err != nil {
			return err
		}
		h := md5.New()
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return err
		}
		f.Close()
		fmt.Fprintf(out, "%x  %s\n", h.Sum(nil), rel)
	}
	return nil
}
