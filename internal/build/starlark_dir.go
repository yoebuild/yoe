package build

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.starlark.net/starlark"
)

// fnDirSizeMB implements the dir_size_mb(subpath) Starlark builtin.
//
// Returns the total size in MiB (rounded up) of regular files under
// $DESTDIR/<subpath>, walked natively on the host. Used at build time to
// preflight whether contents will fit in a partition before mkfs runs and
// fails with a cryptic "Could not allocate block" mid-populate.
//
// Symlinks and directory entries are not counted; this is a content-size
// approximation, not on-disk footprint after filesystem metadata.
// Returns 0 if the path doesn't exist — callers preflighting before
// populate want "what's there now" and absence is a real "0 MB" answer.
//
// Subpath is always interpreted relative to the build's destdir; absolute
// paths and parent-traversal (..) are rejected so a typo can't accidentally
// walk an unrelated host tree.
func fnDirSizeMB(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var subpath starlark.String
	if err := starlark.UnpackPositionalArgs("dir_size_mb", args, kwargs, 1, &subpath); err != nil {
		return nil, err
	}

	cfgVal := thread.Local(sandboxKey)
	if cfgVal == nil {
		return nil, fmt.Errorf("dir_size_mb() can only be called at build time")
	}
	cfg := cfgVal.(*SandboxConfig)
	if cfg.DestDir == "" {
		return nil, fmt.Errorf("dir_size_mb: build has no destdir configured")
	}

	clean := filepath.Clean(string(subpath))
	if filepath.IsAbs(clean) {
		return nil, fmt.Errorf("dir_size_mb: subpath %q must be relative to $DESTDIR", string(subpath))
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, fmt.Errorf("dir_size_mb: subpath %q escapes $DESTDIR", string(subpath))
	}

	hostPath := filepath.Join(cfg.DestDir, clean)

	if _, err := os.Stat(hostPath); err != nil {
		if os.IsNotExist(err) {
			return starlark.MakeInt(0), nil
		}
		return nil, fmt.Errorf("dir_size_mb: stat %s: %w", hostPath, err)
	}

	// The rootfs we're sizing is assembled with per-file ownership from
	// each apk's tar headers — mode-700 dirs owned by root or by service
	// users (navidrome, postgres, …) exist and the build user can't enter
	// them. Fail-soft on EACCES: skip what we can't read, sum what we
	// can. The result is a slight underestimate of contents that fit-
	// preflights against the partition size; the preflight's headroom
	// margin absorbs the gap, and mkfs.ext4 -d running as root in the
	// container is the authoritative fit check downstream.
	var total int64
	walkErr := filepath.Walk(hostPath, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil // skip dirs/files we cannot read; downstream mkfs sees them
			}
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("dir_size_mb: walk %s: %w", hostPath, walkErr)
	}

	const mib = 1024 * 1024
	mb := (total + mib - 1) / mib
	return starlark.MakeInt64(mb), nil
}
