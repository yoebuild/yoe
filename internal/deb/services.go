package deb

import (
	"fmt"
	"os"
	"path/filepath"
)

// MaterializeSystemdServiceSymlinks turns the unit's services list into
// the multi-user.target.wants/<svc>.service symlinks systemd uses to
// auto-start a service at boot. Mirrors the apk-side
// internal/artifact:materializeServiceSymlinks pattern: the .deb's
// data.tar carries the symlink as regular package content, so on-target
// `dpkg -i` (or image-time extract) produces a rootfs with the unit
// already enabled — yoe never patches the rootfs after install.
//
// For each svc in services, this creates:
//
//	etc/systemd/system/multi-user.target.wants/<svc>.service ->
//	  /lib/systemd/system/<svc>.service
//
// The target unit file must exist either in destDir (the unit ships
// it) or sysroot (a depended-on unit ships it). Either is sufficient
// for the symlink to resolve at boot. If neither has it, that's a unit
// bug — surface it loudly.
func MaterializeSystemdServiceSymlinks(destDir, sysroot string, services []string) error {
	if len(services) == 0 {
		return nil
	}
	wantsDir := filepath.Join(destDir, "etc", "systemd", "system", "multi-user.target.wants")
	for _, svc := range services {
		unitFile := svc + ".service"
		if !serviceFileAvailable(destDir, sysroot, unitFile) {
			return fmt.Errorf("service %q declared but /lib/systemd/system/%s missing in destdir or sysroot", svc, unitFile)
		}
		linkPath := filepath.Join(wantsDir, unitFile)
		if _, err := os.Lstat(linkPath); err == nil {
			continue
		}
		if err := os.MkdirAll(wantsDir, 0755); err != nil {
			return err
		}
		if err := os.Symlink("/lib/systemd/system/"+unitFile, linkPath); err != nil {
			return err
		}
	}
	return nil
}

// serviceFileAvailable returns true if /lib/systemd/system/<unitFile>
// exists in either destDir or sysroot. systemd searches multiple
// directories at runtime; for build-time enablement we accept either
// the canonical /lib path or its alias under /usr/lib.
func serviceFileAvailable(destDir, sysroot, unitFile string) bool {
	candidates := []string{
		filepath.Join("lib", "systemd", "system", unitFile),
		filepath.Join("usr", "lib", "systemd", "system", unitFile),
		filepath.Join("etc", "systemd", "system", unitFile),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(destDir, c)); err == nil {
			return true
		}
	}
	if sysroot == "" {
		return false
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(sysroot, c)); err == nil {
			return true
		}
	}
	return false
}
