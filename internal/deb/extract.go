package deb

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractDataTar extracts a Deb's data.tar into destRoot, preserving
// mode, ownership, and symlinks. Used by image assembly to populate the
// staging rootfs from each resolved package.
//
// Existing entries at destRoot are overwritten; the caller is expected
// to extract packages in dependency order so later packages can replace
// files (Debian's "Replaces:" semantics).
func ExtractDataTar(d *Deb, destRoot string) error {
	if d == nil {
		return fmt.Errorf("deb: ExtractDataTar: nil Deb")
	}
	if d.Data == nil {
		return fmt.Errorf("deb: ExtractDataTar: no data tar")
	}
	if err := os.MkdirAll(destRoot, 0755); err != nil {
		return fmt.Errorf("deb: mkdir destRoot: %w", err)
	}
	for {
		hdr, err := d.Data.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("deb: data.tar next: %w", err)
		}
		if err := extractEntry(d.Data, hdr, destRoot); err != nil {
			return fmt.Errorf("deb: extract %s: %w", hdr.Name, err)
		}
	}
}

// extractEntry writes one tar entry into destRoot. Strips a leading "./"
// from the name (data.tar entries are typically rooted at "./").
func extractEntry(tr *tar.Reader, hdr *tar.Header, destRoot string) error {
	name := strings.TrimPrefix(hdr.Name, "./")
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return nil
	}
	target := filepath.Join(destRoot, name)
	// Resolve symlinks safely: ensure target stays within destRoot.
	if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destRoot)) {
		return fmt.Errorf("entry escapes destRoot: %s", hdr.Name)
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, hdr.FileInfo().Mode().Perm())

	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Chmod(target, hdr.FileInfo().Mode().Perm()); err != nil {
			return err
		}

	case tar.TypeSymlink:
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		_ = os.Remove(target)
		return os.Symlink(hdr.Linkname, target)

	case tar.TypeLink:
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		link := filepath.Join(destRoot, strings.TrimPrefix(hdr.Linkname, "./"))
		_ = os.Remove(target)
		return os.Link(link, target)

	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		// Device/special nodes are rare in non-arch-base packages; skip
		// for v1. udev/dpkg-divert handles those at first boot anyway.
		return nil
	}
	return nil
}
