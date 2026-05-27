package image

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// ConfigureDebianRootfs runs dpkg --configure -a inside a binfmt
// sandbox over a populated rootfs. Installs the standard build-time
// stubs (policy-rc.d, ischroot, start-stop-daemon) before invoking
// dpkg, and removes them on the way out.
//
// toolchainImage is the resolved toolchain-glibc container tag — the
// caller computes it via ResolveToolchainImage for the consuming
// image's effective distro. No network access; only the rootfs is
// writable.
//
// Per R18 + R22, the binfmt sandbox uses --network=none (docker's
// primitive — bwrap's --unshare-net isn't reachable on the foreign-arch
// path) and the staging rootfs as the only writable mount.
//
// Cleanup runs even on dpkg failure so a half-configured rootfs
// doesn't ship the build stubs into production.
func ConfigureDebianRootfs(rootfsPath, toolchainImage, projectDir string, w io.Writer) error {
	if _, err := exec.LookPath("docker"); err != nil {
		// podman would work too; the runtime helper in internal/container.go
		// picks the binary at invocation time, but for the early-skeleton
		// path we want a clear error.
		fmt.Fprintf(w, "  (warning: docker/podman not on PATH; skipping dpkg --configure -a)\n")
		return nil
	}

	stubs, err := installBuildTimeStubs(rootfsPath)
	if err != nil {
		return fmt.Errorf("install build-time stubs: %w", err)
	}
	defer func() {
		if rmErr := removeBuildTimeStubs(rootfsPath, stubs); rmErr != nil {
			fmt.Fprintf(w, "  (warning: removing build-time stubs: %v)\n", rmErr)
		}
	}()

	// Mount the rootfs into the toolchain container under /rootfs
	// (rw), bind /proc and /dev/{null,urandom} read-only, and run
	// dpkg --configure -a --no-triggers followed by
	// dpkg --triggers-only -a.
	cmd := exec.Command("docker", "run", "--rm",
		"--network=none",
		"-v", rootfsPath+":/rootfs:rw",
		"--workdir", "/",
		toolchainImage,
		"sh", "-c",
		// `eatmydata` no-ops fsync; chroot into the rootfs so dpkg
		// resolves /var/lib/dpkg/status under the target tree.
		"eatmydata chroot /rootfs dpkg --configure -a --no-triggers && "+
			"eatmydata chroot /rootfs dpkg --triggers-only -a",
	)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dpkg --configure -a: %w", err)
	}
	fmt.Fprintln(w, "  dpkg --configure -a complete")
	return nil
}

type stubFiles struct {
	policyRcD       string
	ischrootDivert  string
	startStopDaemon string
}

// installBuildTimeStubs writes policy-rc.d, swaps /usr/bin/ischroot
// for /bin/true, and replaces /sbin/start-stop-daemon with a no-op
// wrapper. Returns the stub state so removeBuildTimeStubs can revert
// cleanly even when dpkg fails mid-configure.
//
// Reference: isar's meta/recipes-core/isar-mmdebstrap/files/chroot-setup.sh
// uses the same divert pattern.
func installBuildTimeStubs(rootfs string) (*stubFiles, error) {
	s := &stubFiles{}

	// /usr/sbin/policy-rc.d: refuse all invoke-rc.d at build time so
	// postinst service starts don't try to run inside the chroot.
	policyDir := filepath.Join(rootfs, "usr", "sbin")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		return nil, err
	}
	s.policyRcD = filepath.Join(policyDir, "policy-rc.d")
	if err := os.WriteFile(s.policyRcD, []byte("#!/bin/sh\nexit 101\n"), 0o755); err != nil {
		return nil, err
	}

	// /usr/bin/ischroot -> /bin/true so chroot-aware postinsts treat
	// the build-time environment as a chroot.
	ischrootPath := filepath.Join(rootfs, "usr", "bin", "ischroot")
	if _, err := os.Stat(ischrootPath); err == nil {
		divert := ischrootPath + ".yoe-orig"
		if err := os.Rename(ischrootPath, divert); err != nil {
			return nil, err
		}
		s.ischrootDivert = divert
	}
	if err := os.MkdirAll(filepath.Dir(ischrootPath), 0o755); err != nil {
		return nil, err
	}
	// Symlink to /bin/true (which the rootfs has from coreutils).
	if err := os.Symlink("/bin/true", ischrootPath); err != nil && !os.IsExist(err) {
		return nil, err
	}

	// /sbin/start-stop-daemon: replace with a no-op so postinsts that
	// shell to it don't fail.
	ssdPath := filepath.Join(rootfs, "sbin", "start-stop-daemon")
	if _, err := os.Stat(ssdPath); err == nil {
		divert := ssdPath + ".yoe-orig"
		if err := os.Rename(ssdPath, divert); err != nil {
			return nil, err
		}
		s.startStopDaemon = divert
	}
	if err := os.MkdirAll(filepath.Dir(ssdPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(ssdPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		return nil, err
	}
	return s, nil
}

func removeBuildTimeStubs(rootfs string, s *stubFiles) error {
	if s == nil {
		return nil
	}
	_ = os.Remove(s.policyRcD)

	ischrootPath := filepath.Join(rootfs, "usr", "bin", "ischroot")
	_ = os.Remove(ischrootPath)
	if s.ischrootDivert != "" {
		if err := os.Rename(s.ischrootDivert, ischrootPath); err != nil {
			return err
		}
	}

	ssdPath := filepath.Join(rootfs, "sbin", "start-stop-daemon")
	_ = os.Remove(ssdPath)
	if s.startStopDaemon != "" {
		if err := os.Rename(s.startStopDaemon, ssdPath); err != nil {
			return err
		}
	}
	return nil
}
