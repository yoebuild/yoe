// Package fsutil holds filesystem helpers shared across yoe's packages.
//
// Everything here writes atomically: content goes to a temporary file in
// the destination directory, is flushed, and is renamed into place. The
// rename is what matters — yoe's repository trees are written by parallel
// unit builds and read by index generators and package managers walking
// the same directories, so a reader must never observe a half-written
// package or index. A plain create-and-write leaves exactly that window
// open, and the resulting failures are intermittent and look like
// corruption rather than a race.
package fsutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// tmpPattern names the temporary files. The leading dot keeps them out of
// the globs that scan these directories for packages and indices, so a
// scan that runs mid-write sees nothing rather than a partial entry.
const tmpPattern = ".yoe-tmp-*"

// WriteFileAtomic writes data to path, replacing any existing file only
// once the content is completely on disk.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeAtomic(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// CopyFileAtomic copies src to dst, replacing any existing dst only once
// the copy is complete. The source file's contents are read in full; its
// mode is not carried over, since every caller is publishing into a tree
// with its own conventions.
func CopyFileAtomic(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeAtomic(dst, perm, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
}

func writeAtomic(path string, perm os.FileMode, fill func(io.Writer) error) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			f.Close()
			os.Remove(tmp)
		}
	}()

	if err := fill(f); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Flush before the rename: the rename is what publishes the file,
	// and a rename that lands ahead of the data leaves a valid-looking
	// name pointing at nothing after a crash.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync %s: %w", path, err)
	}
	if err := f.Chmod(perm); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename into %s: %w", path, err)
	}
	committed = true
	return nil
}
