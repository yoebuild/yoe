package deb

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BuildDeb assembles a .deb from a staged destdir + control metadata.
// It writes DEBIAN/control + DEBIAN/md5sums under destDir, bakes any
// `services=[...]` symlinks (the caller invokes
// MaterializeSystemdServiceSymlinks first), then writes the binary
// package to outputPath.
//
// The package is built entirely in-process — an ar(1) archive of
// debian-binary + control.tar.gz + data.tar.gz, assembled with the Go
// standard library (archive/tar + compress/gzip). This mirrors the
// pure-Go apk writer (internal/artifact.CreateAPK) so packaging needs
// no host or container tooling and runs anywhere yoe runs. dpkg and apt
// read gzip-compressed members natively, so no xz writer is required.
//
// The compression argument is accepted for API symmetry but only gzip
// is emitted today; a non-gzip request is honored as gzip.
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

	// control.tar.gz holds the DEBIAN/ metadata (control, md5sums, any
	// conffiles or maintainer scripts) renamed to the archive root.
	controlTar, err := buildTarGz(debianDir, nil)
	if err != nil {
		return fmt.Errorf("deb: control.tar.gz: %w", err)
	}
	// data.tar.gz holds the installed filesystem tree, everything under
	// destDir except the DEBIAN/ metadata directory.
	dataTar, err := buildTarGz(destDir, func(rel string) bool {
		return rel == "DEBIAN" || strings.HasPrefix(rel, "DEBIAN/")
	})
	if err != nil {
		return fmt.Errorf("deb: data.tar.gz: %w", err)
	}

	return writeDebAr(outputPath, []arMember{
		{name: "debian-binary", data: []byte("2.0\n")},
		{name: "control.tar.gz", data: controlTar},
		{name: "data.tar.gz", data: dataTar},
	})
}

// buildTarGz walks root and returns a gzip-compressed tar of its
// contents, with each entry named relative to root and prefixed "./"
// (dpkg's convention). A leading "./" directory entry is emitted first.
// Ownership is normalized to root:root (dpkg-deb --root-owner-group).
// skip, when non-nil, drops any path whose root-relative slash path it
// reports true for (and prunes directories).
func buildTarGz(root string, skip func(rel string) bool) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Archive root directory entry.
	if err := tw.WriteHeader(&tar.Header{
		Name:     "./",
		Typeflag: tar.TypeDir,
		Mode:     0755,
		Uname:    "root",
		Gname:    "root",
	}); err != nil {
		return nil, err
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil // already emitted as "./"
		}
		slashRel := filepath.ToSlash(rel)
		if skip != nil && skip(slashRel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		var link string
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = "./" + slashRel
		if d.IsDir() {
			hdr.Name += "/"
		}
		// Normalize ownership; build hosts are not the package owner.
		hdr.Uid, hdr.Gid = 0, 0
		hdr.Uname, hdr.Gname = "root", "root"
		// Force GNU tar format. Go's archive/tar otherwise emits a PAX
		// extended header (typeflag 'x') whenever a field overflows
		// USTAR limits — a long path or, for packages like
		// ca-certificates, a long symlink target. dpkg-deb's tar
		// extractor rejects PAX 'x' records ("unsupported PAX tar
		// header type 'x'") and aborts the install; GNU format encodes
		// long names/links with the 'L'/'K' typeflags dpkg understands.
		hdr.Format = tar.FormatGNU
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			if _, err := io.Copy(tw, f); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// arMember is one member of a Debian ar(1) archive.
type arMember struct {
	name string
	data []byte
}

// writeDebAr writes members to outputPath as a Debian-flavored ar(1)
// archive: the "!<arch>\n" magic followed by 60-byte ASCII headers.
// Members are root-owned, mode 100644, with mtime 0 for reproducible
// output; odd-sized members are padded to an even boundary with '\n'.
func writeDebAr(outputPath string, members []arMember) error {
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("deb: create %s: %w", outputPath, err)
	}
	bw := bufio.NewWriter(f)

	if _, err := bw.WriteString("!<arch>\n"); err != nil {
		f.Close()
		return err
	}
	for _, m := range members {
		// name(16) mtime(12) uid(6) gid(6) mode(8) size(10) magic(2) = 60
		hdr := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d`\n",
			m.name, 0, 0, 0, "100644", len(m.data))
		if len(hdr) != 60 {
			f.Close()
			return fmt.Errorf("deb: ar header for %q is %d bytes, want 60", m.name, len(hdr))
		}
		if _, err := bw.WriteString(hdr); err != nil {
			f.Close()
			return err
		}
		if _, err := bw.Write(m.data); err != nil {
			f.Close()
			return err
		}
		if len(m.data)%2 == 1 {
			if err := bw.WriteByte('\n'); err != nil {
				f.Close()
				return err
			}
		}
	}

	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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
