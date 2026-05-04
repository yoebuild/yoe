package device

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
