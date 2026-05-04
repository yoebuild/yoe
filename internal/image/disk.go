package image

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	yoe "github.com/yoebuild/yoe/internal"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// toContainerPath converts a host path to a /project-relative container path.
func toContainerPath(projectDir, hostPath string) (string, error) {
	rel, err := filepath.Rel(projectDir, hostPath)
	if err != nil {
		return "", fmt.Errorf("converting to container path: %w", err)
	}
	return filepath.Join("/project", rel), nil
}

// GenerateDiskImage creates a partitioned disk image from a rootfs directory.
// Disk tools (sfdisk, mkfs, dd, etc.) run inside the container via
// RunInContainer; pure file operations stay on the host.
func GenerateDiskImage(rootfs, imgPath string, unit *yoestar.Unit, projectDir, arch string, w io.Writer) error {
	partitions := unit.Partitions
	if len(partitions) == 0 {
		partitions = []yoestar.Partition{
			{Label: "rootfs", Type: "ext4", Size: "512M", Root: true},
		}
	}

	// Calculate sizes. Add 1MB for MBR/partition table overhead.
	totalMB := 1
	for _, p := range partitions {
		size := parseSizeMB(p.Size)
		if size == 0 {
			size = 512
		}
		totalMB += size
	}

	fmt.Fprintf(w, "  Creating %dMB disk image...\n", totalMB)

	// Create sparse image (pure Go — no container needed)
	if err := createSparseImage(imgPath, totalMB); err != nil {
		return fmt.Errorf("creating image: %w", err)
	}

	// Partition with sfdisk via container
	if err := partitionImage(imgPath, partitions, projectDir, w); err != nil {
		return fmt.Errorf("partitioning: %w", err)
	}

	// Create individual partition images and dd them into the disk image.
	// The first 1MB is reserved for the MBR/partition table.
	offsetMB := 1
	for _, p := range partitions {
		sizeMB := parseSizeMB(p.Size)
		if sizeMB == 0 || sizeMB > totalMB-offsetMB {
			sizeMB = totalMB - offsetMB
		}

		fmt.Fprintf(w, "  Creating %s partition (%s, %dMB)...\n", p.Label, p.Type, sizeMB)

		partImg := imgPath + "." + p.Label + ".part"
		defer os.Remove(partImg)

		switch p.Type {
		case "vfat":
			if err := createVfatPartition(partImg, sizeMB, rootfs, p, projectDir, w); err != nil {
				return fmt.Errorf("vfat %s: %w", p.Label, err)
			}
		case "ext4":
			if err := createExt4Partition(partImg, sizeMB, rootfs, p, projectDir, w); err != nil {
				return fmt.Errorf("ext4 %s: %w", p.Label, err)
			}
		}

		// DD the partition image into the disk image at the right offset via container
		if _, err := os.Stat(partImg); err == nil {
			cPartImg, err := toContainerPath(projectDir, partImg)
			if err != nil {
				return err
			}
			cImgPath, err := toContainerPath(projectDir, imgPath)
			if err != nil {
				return err
			}
			ddCmd := fmt.Sprintf("dd if=%s of=%s bs=1M seek=%d conv=notrunc",
				cPartImg, cImgPath, offsetMB)
			if err := yoe.RunInContainer(yoe.ContainerRunConfig{
				Image:      "yoe/toolchain-musl:15",
				Command:    ddCmd,
				ProjectDir: projectDir,
				Stdout:     w,
				Stderr:     w,
			}); err != nil {
				return fmt.Errorf("dd partition %s: %w", p.Label, err)
			}
		}

		offsetMB += sizeMB
	}

	// Install syslinux bootloader (x86 only — arm64/riscv64 use direct kernel boot)
	if arch == "x86_64" {
		if err := installBootloader(imgPath, rootfs, unit, projectDir, w); err != nil {
			fmt.Fprintf(w, "  Warning: could not install bootloader: %v\n", err)
		}
	}

	info, _ := os.Stat(imgPath)
	if info != nil {
		fmt.Fprintf(w, "  Disk image: %s (%dMB)\n", imgPath, info.Size()/(1024*1024))
	}

	return nil
}

func createSparseImage(path string, sizeMB int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(int64(sizeMB) * 1024 * 1024)
}

func partitionImage(imgPath string, partitions []yoestar.Partition, projectDir string, w io.Writer) error {
	script := "label: dos\n"
	for i, p := range partitions {
		size := ""
		sizeMB := parseSizeMB(p.Size)
		if sizeMB > 0 && i < len(partitions)-1 {
			// Specify size only for non-last partitions; last gets remaining space
			size = fmt.Sprintf("size=%dMiB, ", sizeMB)
		}
		ptype := "83" // Linux
		if p.Type == "vfat" {
			ptype = "c" // W95 FAT32 (LBA)
		}
		bootable := ""
		if p.Root {
			bootable = ", bootable"
		}
		script += fmt.Sprintf("%stype=%s%s\n", size, ptype, bootable)
	}

	cImgPath, err := toContainerPath(projectDir, imgPath)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "  Partitioning (MBR)...")
	// Use printf to pipe the sfdisk script via stdin inside the container
	cmd := fmt.Sprintf("printf '%s' | sfdisk --quiet %s", strings.ReplaceAll(script, "'", "'\\''"), cImgPath)
	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Image:      "yoe/toolchain-musl:15",
		Command:    cmd,
		ProjectDir: projectDir,
		Stdout:     w,
		Stderr:     w,
	})
}

// createVfatPartition creates a FAT32 filesystem image and copies boot files.
// Uses mkfs.vfat + mcopy (mtools) via RunInContainer.
func createVfatPartition(partImg string, sizeMB int, rootfs string, p yoestar.Partition, projectDir string, w io.Writer) error {
	// Create the partition image file (pure Go — no container needed)
	if err := createSparseImage(partImg, sizeMB); err != nil {
		return err
	}

	cPartImg, err := toContainerPath(projectDir, partImg)
	if err != nil {
		return err
	}

	// Format as FAT32 via container
	mkfsCmd := fmt.Sprintf("mkfs.vfat -n %s %s", strings.ToUpper(p.Label), cPartImg)
	if err := yoe.RunInContainer(yoe.ContainerRunConfig{
		Image:      "yoe/toolchain-musl:15",
		Command:    mkfsCmd,
		ProjectDir: projectDir,
		Stdout:     w,
		Stderr:     w,
	}); err != nil {
		return fmt.Errorf("mkfs.vfat: %w", err)
	}

	// Copy boot files using mcopy via container
	for _, pattern := range p.Contents {
		// Glob on the host to find matching files
		matches, _ := filepath.Glob(filepath.Join(rootfs, "boot", pattern))
		for _, f := range matches {
			cFile, err := toContainerPath(projectDir, f)
			if err != nil {
				return err
			}
			mcopyCmd := fmt.Sprintf("mcopy -i %s %s ::/%s", cPartImg, cFile, filepath.Base(f))
			if err := yoe.RunInContainer(yoe.ContainerRunConfig{
				Image:      "yoe/toolchain-musl:15",
				Command:    mcopyCmd,
				ProjectDir: projectDir,
				Stdout:     w,
				Stderr:     w,
			}); err != nil {
				fmt.Fprintf(w, "    mcopy %s: %v\n", filepath.Base(f), err)
			} else {
				fmt.Fprintf(w, "    boot: %s\n", filepath.Base(f))
			}
		}
	}

	return nil
}

// createExt4Partition creates an ext4 filesystem image populated from rootfs.
// Uses mkfs.ext4 via RunInContainer.
func createExt4Partition(partImg string, sizeMB int, rootfs string, p yoestar.Partition, projectDir string, w io.Writer) error {
	// Create the partition image file (pure Go — no container needed)
	if err := createSparseImage(partImg, sizeMB); err != nil {
		return err
	}

	cPartImg, err := toContainerPath(projectDir, partImg)
	if err != nil {
		return err
	}

	if !p.Root {
		// Non-root ext4 partition — just format empty
		mkfsCmd := fmt.Sprintf("mkfs.ext4 -q -L %s %s", p.Label, cPartImg)
		if err := yoe.RunInContainer(yoe.ContainerRunConfig{
			Image:      "yoe/toolchain-musl:15",
			Command:    mkfsCmd,
			ProjectDir: projectDir,
			Stdout:     w,
			Stderr:     w,
		}); err != nil {
			return fmt.Errorf("mkfs.ext4: %w", err)
		}
		return nil
	}

	// Root partition — create and populate from rootfs using mkfs.ext4 -d
	cRootfs, err := toContainerPath(projectDir, rootfs)
	if err != nil {
		return err
	}

	// Disable ext4 features that syslinux 6.03 can't read:
	// 64bit, metadata_csum, extent tree. Use classic indirect blocks.
	mkfsCmd := fmt.Sprintf("mkfs.ext4 -q -L %s -O ^64bit,^metadata_csum,^extent -d %s %s", p.Label, cRootfs, cPartImg)
	if err := yoe.RunInContainer(yoe.ContainerRunConfig{
		Image:      "yoe/toolchain-musl:15",
		Command:    mkfsCmd,
		ProjectDir: projectDir,
		Stdout:     w,
		Stderr:     w,
	}); err != nil {
		return fmt.Errorf("mkfs.ext4 -d: %w", err)
	}

	return nil
}

// installBootloader writes syslinux MBR code and runs extlinux --install
// on the root partition to set up the VBR and ldlinux.sys.
// MBR byte writing is pure Go (host); losetup/mount/extlinux run in the container.
func installBootloader(imgPath, rootfs string, unit *yoestar.Unit, projectDir string, w io.Writer) error {
	// Write MBR boot code (pure Go — reads mbr.bin from rootfs on host)
	mbrBin := filepath.Join(rootfs, "usr", "share", "syslinux", "mbr.bin")
	if _, err := os.Stat(mbrBin); os.IsNotExist(err) {
		// mbr.bin not in rootfs — skip (container's copy not accessible from host)
		return fmt.Errorf("syslinux mbr.bin not found in rootfs")
	}

	mbrData, err := os.ReadFile(mbrBin)
	if err != nil {
		return err
	}
	if len(mbrData) > 440 {
		mbrData = mbrData[:440]
	}

	img, err := os.OpenFile(imgPath, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := img.WriteAt(mbrData, 0); err != nil {
		img.Close()
		return fmt.Errorf("writing MBR: %w", err)
	}
	img.Close()
	fmt.Fprintln(w, "  Installed syslinux MBR boot code")

	// Run extlinux --install on the root partition using a loop device
	// inside the container (needs losetup/mount/extlinux + --privileged).
	cImgPath, err := toContainerPath(projectDir, imgPath)
	if err != nil {
		return err
	}

	// Find the root partition offset and size
	offsetBytes := int64(1024 * 1024) // 1MB default (after MBR)
	var rootSizeMB int
	for _, p := range unit.Partitions {
		if p.Root {
			rootSizeMB = parseSizeMB(p.Size)
			if rootSizeMB == 0 {
				rootSizeMB = 512
			}
			break
		}
		sizeMB := parseSizeMB(p.Size)
		if sizeMB > 0 {
			offsetBytes += int64(sizeMB) * 1024 * 1024
		}
	}

	// Run losetup + mount + extlinux as a single shell script inside the container.
	// NoUser: true because losetup/mount require root.
	// Docker's --privileged does not populate /dev/loop*, so pre-create the
	// device nodes before losetup --find (see classes/image.star for details).
	extlinuxScript := fmt.Sprintf(`set -e
for i in $(seq 0 31); do
    [ -b /dev/loop$i ] || mknod /dev/loop$i b 7 $i
done
LOOP=$(losetup --find --show --offset %d --sizelimit %d %s)
trap 'umount /mnt/extlinux 2>/dev/null; losetup -d $LOOP 2>/dev/null' EXIT
mkdir -p /mnt/extlinux
mount -t ext4 $LOOP /mnt/extlinux
extlinux --install /mnt/extlinux/boot/extlinux`,
		offsetBytes, int64(rootSizeMB)*1024*1024, cImgPath)

	if err := yoe.RunInContainer(yoe.ContainerRunConfig{
		Image:      "yoe/toolchain-musl:15",
		Command:    extlinuxScript,
		ProjectDir: projectDir,
		NoUser:     true,
		Stdout:     w,
		Stderr:     w,
	}); err != nil {
		fmt.Fprintf(w, "  extlinux install failed: %v\n", err)
		return nil // non-fatal — image still has the files, just no VBR
	}

	fmt.Fprintln(w, "  Installed extlinux bootloader")
	return nil
}

func parseSizeMB(size string) int {
	if size == "fill" || size == "" {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(size, "%dM", &n); err == nil {
		return n
	}
	if _, err := fmt.Sscanf(size, "%dG", &n); err == nil {
		return n * 1024
	}
	return 0
}
