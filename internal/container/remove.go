package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	archpkg "github.com/yoebuild/yoe/internal/arch"
)

// RemoveDir removes dir and everything under it.
//
// Image builds deliberately leave parts of their output owned by root:
// build/<image>.<scope>/destdir/rootfs has to carry the uid/gid the
// booted system will see, and mkfs.ext4 -d reads it that way. The host
// user then cannot delete those files, so a plain RemoveAll fails with
// EACCES on exactly the trees yoe most often needs to clean.
//
// The recovery is to chown the tree back to the host user from inside a
// container, which does have that privilege, and retry the removal on
// the host. The deletion itself always runs host-side as the ordinary
// user: a container-side `rm -rf` against a path built by concatenating
// strings is a much worse failure when the path is wrong, and the chown
// is the only step that actually needs the privilege.
//
// image names the container to run the chown in. Passing "" picks
// whichever yoe toolchain image is present locally — callers cleaning up
// after a specific unit know its container, while `yoe clean` and the
// TUI have no unit in hand.
func RemoveDir(ctx context.Context, dir, projectDir, image string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		return nil
	}
	if cerr := chownToHost(ctx, dir, projectDir, image); cerr != nil {
		return fmt.Errorf("%w (and ownership recovery failed: %v)", err, cerr)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing after ownership recovery: %w", err)
	}
	return nil
}

// chownToHost runs chown -R uid:gid over dir inside a container, which
// has the privilege to chown root-owned files. No-op if dir is gone.
func chownToHost(ctx context.Context, dir, projectDir, image string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	if image == "" {
		// Any yoe toolchain image can run chown; which one does not
		// matter. Pinning a version here made docker try to pull a
		// tag that only ever exists locally.
		image = LocalToolchainImage(archpkg.Host())
	}
	if image == "" {
		return fmt.Errorf("cannot recover ownership of %s: no local yoe toolchain image found "+
			"(build a target first, or remove the directory manually with sudo)", dir)
	}
	parent := filepath.Dir(dir)
	base := filepath.Base(dir)
	return RunInContainer(ContainerRunConfig{
		Ctx:        ctx,
		Image:      image,
		Command:    fmt.Sprintf("chown -R %d:%d /__yoe_cleanup/%s", os.Getuid(), os.Getgid(), base),
		ProjectDir: projectDir,
		Mounts:     []Mount{{Host: parent, Container: "/__yoe_cleanup"}},
		NoUser:     true,
		Quiet:      true,
	})
}
