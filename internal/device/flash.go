package device

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// Flash writes an image unit's built artifact to a block device.
func Flash(proj *yoestar.Project, unitName, devicePath, projectDir string, dryRun, assumeYes bool, w io.Writer) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("flash currently supports Linux only")
	}

	unit, ok := proj.Units[unitName]
	if !ok {
		return fmt.Errorf("unit %q not found", unitName)
	}
	if unit.Class != "image" {
		return fmt.Errorf("unit %q is not an image (class=%q)", unitName, unit.Class)
	}

	machine, ok := proj.Machines[proj.Defaults.Machine]
	if !ok {
		return fmt.Errorf("default machine %q not found", proj.Defaults.Machine)
	}

	imgPath := findImage(projectDir, machine.Name, unitName)
	if imgPath == "" {
		return fmt.Errorf("no built image found for %q on machine %q — run yoe build %s first", unitName, machine.Name, unitName)
	}

	if err := validateDevice(devicePath); err != nil {
		return err
	}

	imgInfo, err := os.Stat(imgPath)
	if err != nil {
		return fmt.Errorf("stat image: %w", err)
	}

	// Identify the whole-disk path for mount checks.
	resolved, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", devicePath, err)
	}
	disk := parentDisk(resolved)

	if mounts, err := MountedPartitionsFor(disk); err == nil && len(mounts) > 0 {
		fmt.Fprintf(w, "%s has mounted partitions:\n", disk)
		for _, m := range mounts {
			fmt.Fprintf(w, "  %s → %s\n", m.Source, m.Mountpoint)
		}
		fmt.Fprintln(w, "Unmount before flashing, e.g.:")
		for _, m := range mounts {
			fmt.Fprintf(w, "  udisksctl unmount -b %s\n", m.Source)
		}
		return fmt.Errorf("%s has mounted partitions", disk)
	}

	if dryRun {
		fmt.Fprintf(w, "Would flash %s (%s) → %s\n", filepath.Base(imgPath), FormatSize(imgInfo.Size()), devicePath)
		return nil
	}

	if !assumeYes {
		fmt.Fprintf(w, "Flash %s (%s) → %s?\n", filepath.Base(imgPath), FormatSize(imgInfo.Size()), devicePath)
		fmt.Fprintf(w, "This will erase all data on %s. Continue? [y/N] ", devicePath)
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
			fmt.Fprintln(w, "Aborted")
			return nil
		}
	}

	progress := newCLIProgress(w)
	err = Write(imgPath, devicePath, progress)
	if errors.Is(err, ErrPermission) {
		if err := offerChown(devicePath, w); err != nil {
			return err
		}
		err = Write(imgPath, devicePath, progress)
	}
	if errors.Is(err, ErrBusy) {
		return fmt.Errorf("%s has mounted partitions; unmount and retry", disk)
	}
	if err != nil {
		return err
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flash complete")
	return nil
}

// offerChown prompts the user to run sudo chown on the device. If
// accepted, invokes sudo (which prompts for the password directly).
// The username is resolved in yoe's process and passed as a literal
// argv element to avoid shell expansion of $USER under sudo.
func offerChown(devicePath string, w io.Writer) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}
	fmt.Fprintf(w, "Permission denied writing %s.\n", devicePath)
	fmt.Fprintf(w, "Run sudo chown %s %s? [y/N] ", u.Username, devicePath)
	var answer string
	fmt.Scanln(&answer)
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return fmt.Errorf("no write permission on %s — run: sudo chown %s %s", devicePath, u.Username, devicePath)
	}
	cmd := exec.Command("sudo", "chown", u.Username, devicePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo chown failed: %w", err)
	}
	return nil
}

// newCLIProgress returns a progress callback that overprints a single
// line on the given writer with rate and percent.
func newCLIProgress(w io.Writer) func(written, total int64) {
	start := time.Now()
	return func(written, total int64) {
		elapsed := time.Since(start).Seconds()
		var rate float64
		if elapsed > 0 {
			rate = float64(written) / elapsed
		}
		var pct float64
		if total > 0 {
			pct = float64(written) / float64(total) * 100
		}
		fmt.Fprintf(w, "\rwritten %s / %s (%.0f%%) — %s/s   ",
			FormatSize(written), FormatSize(total), pct, FormatSize(int64(rate)))
	}
}

func findImage(projectDir, scopeDir, unitName string) string {
	dir := filepath.Join(projectDir, "build", unitName+"."+scopeDir, "destdir")
	imgPath := filepath.Join(dir, unitName+".img")
	if _, err := os.Stat(imgPath); err == nil {
		return imgPath
	}
	return ""
}

func validateDevice(devicePath string) error {
	if devicePath == "" {
		return fmt.Errorf("device path required")
	}

	info, err := os.Stat(devicePath)
	if err != nil {
		return fmt.Errorf("device %s: %w", devicePath, err)
	}

	if info.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("%s is not a block device", devicePath)
	}

	resolved, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", devicePath, err)
	}
	targetDisk := parentDisk(resolved)

	mountsData, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return fmt.Errorf("read /proc/mounts: %w", err)
	}
	for _, sysDisk := range systemDisks(string(mountsData)) {
		if sysDisk == targetDisk {
			return fmt.Errorf("refusing to write to %s: hosts the running system", devicePath)
		}
	}

	return nil
}

// parentDisk returns the whole-disk device for a partition (e.g. /dev/sda1
// → /dev/sda, /dev/nvme0n1p2 → /dev/nvme0n1). If devicePath is not a
// partition, or its sysfs entry can't be read, returns devicePath unchanged.
func parentDisk(devicePath string) string {
	name := filepath.Base(devicePath)
	sysPath := "/sys/class/block/" + name
	if _, err := os.Stat(filepath.Join(sysPath, "partition")); err != nil {
		return devicePath
	}
	target, err := os.Readlink(sysPath)
	if err != nil {
		return devicePath
	}
	return "/dev/" + filepath.Base(filepath.Dir(target))
}

// systemDisks returns the set of whole-disk devices that host critical
// system mountpoints. Walks /sys/class/block/<name>/slaves to resolve
// dm-crypt, LVM, and md devices to their underlying physical disks.
func systemDisks(mountsContent string) []string {
	critical := map[string]bool{
		"/":         true,
		"/boot":     true,
		"/boot/efi": true,
		"/usr":      true,
	}
	seen := map[string]bool{}
	var disks []string
	for _, line := range strings.Split(mountsContent, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !critical[fields[1]] {
			continue
		}
		src := fields[0]
		if !strings.HasPrefix(src, "/dev/") {
			continue
		}
		resolved, err := filepath.EvalSymlinks(src)
		if err != nil {
			continue
		}
		for _, base := range underlyingDevices(resolved) {
			disk := parentDisk(base)
			if !seen[disk] {
				seen[disk] = true
				disks = append(disks, disk)
			}
		}
	}
	return disks
}

// underlyingDevices recurses through /sys/class/block/<name>/slaves to find
// the physical block devices backing a dm-/md device. For a leaf device
// with no slaves, returns devicePath unchanged.
func underlyingDevices(devicePath string) []string {
	slavesDir := filepath.Join("/sys/class/block", filepath.Base(devicePath), "slaves")
	entries, err := os.ReadDir(slavesDir)
	if err != nil || len(entries) == 0 {
		return []string{devicePath}
	}
	var out []string
	for _, e := range entries {
		out = append(out, underlyingDevices("/dev/"+e.Name())...)
	}
	return out
}
