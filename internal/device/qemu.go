package device

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	yoe "github.com/yoebuild/yoe/internal"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// QEMUOptions configures a QEMU run.
type QEMUOptions struct {
	Memory  string
	Ports   []string // host:guest port mappings
	Display bool
	Daemon  bool
	// DiskSize, if non-empty, is the size to grow the QEMU-side image
	// copy to before launch. Format is the usual K/M/G suffix (e.g. "8G").
	// The built disk.img is left untouched so `yoe flash` keeps writing
	// only the partition-sized image; the grown copy lives alongside as
	// disk.run.img and is reused across runs. Enables on-target
	// grow-rootfs to actually have free space to extend into.
	// Empty string disables the copy (yoe run uses disk.img directly).
	DiskSize string
}

// RunQEMU launches an image in QEMU.
func RunQEMU(proj *yoestar.Project, unitName, machineName, projectDir string, opts QEMUOptions, w io.Writer) error {
	// Find the image unit
	unit, ok := proj.Units[unitName]
	if !ok {
		return fmt.Errorf("unit %q not found", unitName)
	}
	if unit.Class != "image" {
		return fmt.Errorf("unit %q is not an image", unitName)
	}

	// Find the machine
	if machineName == "" {
		machineName = proj.Defaults.Machine
	}
	machine, ok := proj.Machines[machineName]
	if !ok {
		return fmt.Errorf("machine %q not found", machineName)
	}

	// Find the built image
	imgPath := findImage(projectDir, machine.Name, unitName)
	if imgPath == "" {
		return fmt.Errorf("no built image for %q — run yoe build %s first", unitName, unitName)
	}

	// Optionally grow a side-by-side copy of the image so grow-rootfs on
	// the guest has free space to extend the partition into. Leaves the
	// original disk.img untouched.
	if opts.DiskSize != "" {
		grown, err := ensureGrownQEMUImage(imgPath, opts.DiskSize)
		if err != nil {
			return fmt.Errorf("growing QEMU image: %w", err)
		}
		imgPath = grown
	}

	qemuBin := qemuBinary(machine.Arch)

	// Build common QEMU args (without image path — that differs host vs container)
	buildArgs := func(imgFile string) []string {
		a := baseQEMUArgs(machine, opts)
		a = append(a, "-drive", fmt.Sprintf("file=%s,format=raw,if=virtio", imgFile))

		// Merge machine-defined ports with CLI ports (CLI takes precedence)
		ports := machine.QEMUPorts()
		ports = append(ports, opts.Ports...)

		netdev := "user,id=net0"
		for _, port := range ports {
			// port format is "host:guest", QEMU wants "hostfwd=tcp::host-:guest"
			netdev += fmt.Sprintf(",hostfwd=tcp::%s", strings.Replace(port, ":", "-:", 1))
		}
		a = append(a, "-netdev", netdev)
		a = append(a, "-device", "virtio-net-pci,netdev=net0")

		// Direct kernel boot: when no firmware is configured, pass
		// -kernel and -append for architectures that need it (arm64, riscv64).
		needsDirectBoot := machine.QEMU == nil || machine.QEMU.Firmware == ""
		if needsDirectBoot && machine.Kernel.Unit != "" {
			kernelPath := findKernelImage(projectDir, machine.Arch, machine.Kernel.Unit)
			if kernelPath != "" {
				a = append(a, "-kernel", kernelPath)
				if machine.Kernel.Cmdline != "" {
					a = append(a, "-append", machine.Kernel.Cmdline)
				}
			}
		}

		return a
	}

	// Try host QEMU first
	if _, err := exec.LookPath(qemuBin); err == nil {
		fmt.Fprintf(w, "Starting QEMU (host): %s %s\n", qemuBin, machine.Arch)
		args := buildArgs(imgPath)
		cmd := exec.Command(qemuBin, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if opts.Daemon {
			cmd.Stdin = nil
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Start(); err != nil {
				return fmt.Errorf("starting QEMU: %w", err)
			}
			fmt.Fprintf(w, "QEMU running in background (PID %d)\n", cmd.Process.Pid)
			return nil
		}
		return cmd.Run()
	}

	// Fall back to container
	fmt.Fprintf(w, "Starting QEMU (container): %s %s\n", qemuBin, machine.Arch)
	rel, err := filepath.Rel(projectDir, imgPath)
	if err != nil {
		return fmt.Errorf("image path not under project: %w", err)
	}
	containerImgPath := filepath.Join("/project", rel)
	args := buildArgs(containerImgPath)
	fullCmd := qemuBin + " " + strings.Join(args, " ")

	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Image:       yoe.DefaultContainerImage(proj.Units),
		Command:     fullCmd,
		ProjectDir:  projectDir,
		Interactive: !opts.Daemon,
		NoUser:      true,
	})
}

// findKernelImage locates the kernel image (vmlinuz) from a kernel unit's
// build output.
func findKernelImage(projectDir, scopeDir, kernelUnit string) string {
	// Check the unit's destdir for /boot/vmlinuz
	// Uses the new flat layout: build/<name>.<scope>/destdir/
	destDir := filepath.Join(projectDir, "build", kernelUnit+"."+scopeDir, "destdir")
	vmlinuz := filepath.Join(destDir, "boot", "vmlinuz")
	if _, err := os.Stat(vmlinuz); err == nil {
		return vmlinuz
	}
	return ""
}

// ensureGrownQEMUImage copies src to a side file (src with `.run` inserted
// before the extension) and grows it sparsely to targetSize. If the side
// file already exists and is at least targetSize bytes AND newer than src,
// it's reused. Returns the path to the side file.
func ensureGrownQEMUImage(src, targetSize string) (string, error) {
	targetBytes, err := parseSizeBytes(targetSize)
	if err != nil {
		return "", fmt.Errorf("parsing disk size %q: %w", targetSize, err)
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("stat src image: %w", err)
	}
	// Source already meets or exceeds target — no copy needed.
	if srcInfo.Size() >= targetBytes {
		return src, nil
	}

	ext := filepath.Ext(src)
	dst := strings.TrimSuffix(src, ext) + ".run" + ext

	// Reuse an existing side file when it's at least target size AND not
	// stale relative to the source. A rebuild updates src's mtime, which
	// invalidates the cached copy.
	if dstInfo, err := os.Stat(dst); err == nil {
		if dstInfo.Size() >= targetBytes && dstInfo.ModTime().After(srcInfo.ModTime()) {
			return dst, nil
		}
		_ = os.Remove(dst)
	}

	// Sparse copy: read src, write only non-zero blocks. Then truncate
	// up to targetBytes. os.O_CREATE | os.O_TRUNC + io.Copy is plenty;
	// we don't need to detect holes in the source because the source is
	// already a sparse file and io.Copy preserves zero runs into a fresh
	// destination via the same byte writes — but for an explicit sparse
	// result we use cp --sparse=always when available, falling back to
	// plain Go copy + truncate.
	if err := copySparse(src, dst); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	if err := os.Truncate(dst, targetBytes); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("truncate %s to %s: %w", dst, targetSize, err)
	}
	return dst, nil
}

// copySparse copies src to dst. Tries `cp --sparse=always` first so zero
// runs in the source stay holes in the destination; falls back to a plain
// byte copy if cp isn't available or rejects the flag (BusyBox cp).
func copySparse(src, dst string) error {
	if cp, err := exec.LookPath("cp"); err == nil {
		// --sparse=always is a GNU coreutils extension; ignore failure
		// and fall back rather than producing a non-sparse copy with no
		// warning.
		c := exec.Command(cp, "--sparse=always", src, dst)
		if err := c.Run(); err == nil {
			return nil
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// parseSizeBytes turns "8G" / "512M" / "1024K" into bytes. Plain digits
// are bytes.
func parseSizeBytes(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K', 'k':
		mult = 1024
		s = s[:len(s)-1]
	case 'M', 'm':
		mult = 1024 * 1024
		s = s[:len(s)-1]
	case 'G', 'g':
		mult = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case 'T', 't':
		mult = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

func qemuBinary(arch string) string {
	switch arch {
	case "arm64":
		return "qemu-system-aarch64"
	case "riscv64":
		return "qemu-system-riscv64"
	default:
		return "qemu-system-x86_64"
	}
}

func detectHostArch() string {
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		return "x86_64"
	}
	arch := strings.TrimSpace(string(out))
	switch arch {
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}

func baseQEMUArgs(machine *yoestar.Machine, opts QEMUOptions) []string {
	var args []string

	hostArch := detectHostArch()
	crossArch := machine.Arch != hostArch

	qemu := machine.QEMU
	if qemu != nil {
		if qemu.Machine != "" {
			args = append(args, "-machine", qemu.Machine)
		}
		if crossArch {
			args = append(args, "-cpu", "max")
		} else if qemu.CPU != "" {
			args = append(args, "-cpu", qemu.CPU)
		}
	} else {
		switch machine.Arch {
		case "arm64":
			args = append(args, "-machine", "virt")
		case "riscv64":
			args = append(args, "-machine", "virt")
		default:
			args = append(args, "-machine", "q35")
		}
		if crossArch {
			args = append(args, "-cpu", "max")
		} else {
			args = append(args, "-cpu", "host")
		}
	}

	// Enable KVM only for same-arch
	if !crossArch {
		args = append(args, "-enable-kvm")
	}

	// Memory
	mem := opts.Memory
	if mem == "" {
		if qemu != nil && qemu.Memory != "" {
			mem = qemu.Memory
		} else {
			mem = "1G"
		}
	}
	args = append(args, "-m", mem)

	// Display
	if !opts.Display {
		args = append(args, "-nographic")
	}

	// Firmware
	if qemu != nil && qemu.Firmware != "" {
		switch qemu.Firmware {
		case "ovmf":
			args = append(args, "-bios", "/usr/share/OVMF/OVMF.fd")
		case "aavmf":
			args = append(args, "-bios", "/usr/share/AAVMF/AAVMF.fd")
		}
	}

	return args
}
