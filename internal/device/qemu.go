package device

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	yoe "github.com/yoebuild/yoe/internal"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// checkQEMUPortsFree returns a descriptive error if any host-side forward
// port is already bound. A bound forward port almost always means another
// QEMU guest from an earlier `yoe run` is still running — a live guest
// holds its forwards for its whole lifetime — so this turns an opaque
// QEMU "exit status 1" into a message that names the actual problem.
func checkQEMUPortsFree(ports []string) error {
	for _, p := range ports {
		host, _, ok := strings.Cut(p, ":")
		if !ok || host == "" {
			continue
		}
		ln, err := net.Listen("tcp", ":"+host)
		if err != nil {
			return fmt.Errorf("host port %s is already in use — a QEMU guest from an earlier `yoe run` is probably still running. Stop that guest first, or pass --port to forward different host ports", host)
		}
		_ = ln.Close()
	}
	return nil
}

// mergeQEMUPorts combines a machine's declared forwards with CLI `--port`
// entries. A CLI entry whose guest port matches a machine entry replaces
// that machine entry; a CLI entry with a new guest port is appended.
//
// Replacing (rather than appending) is what makes `--port` usable for
// qemu-in-qemu: when `yoe run` executes inside a QEMU guest, the outer
// guest already holds the machine's default host forwards (2222, 8080,
// 8118). `--port 18118:8118` then moves the host side of the 8118 forward
// off the taken port instead of declaring a second, still-colliding
// forward for the same guest port.
func mergeQEMUPorts(machinePorts, cliPorts []string) []string {
	guestPort := func(p string) (string, bool) {
		_, guest, ok := strings.Cut(p, ":")
		return guest, ok && guest != ""
	}
	merged := append([]string(nil), machinePorts...)
	for _, cp := range cliPorts {
		guest, ok := guestPort(cp)
		if !ok {
			merged = append(merged, cp)
			continue
		}
		replaced := false
		for i, mp := range merged {
			if g, ok := guestPort(mp); ok && g == guest {
				merged[i] = cp
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, cp)
		}
	}
	return merged
}

// CheckQEMUPortsAvailable verifies the host forward ports a `yoe run` of
// this machine would bind are free. The CLI and the TUI both call it
// before launching so a guest that is already running is reported
// clearly instead of as an opaque QEMU exit code.
func CheckQEMUPortsAvailable(machine *yoestar.Machine, extraPorts []string) error {
	return checkQEMUPortsFree(mergeQEMUPorts(machine.QEMUPorts(), extraPorts))
}

// qemuStderrTail returns the last non-empty line of captured QEMU stderr,
// prefixed with a newline, or "" when there is nothing useful to add.
func qemuStderrTail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return "\n" + line
		}
	}
	return ""
}

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
	// BootTest turns `yoe run` into a non-interactive smoke test: boot the
	// image headless, wait for the serial console to reach the login
	// prompt, SSH in and run a health command, then power off. The process
	// exits 0 on success and non-zero on any failure (timeout, early QEMU
	// exit, no SSH). Used by CI to catch boot/runtime regressions a build
	// can't. Requires qemu-system on the host (the container fallback can't
	// forward the guest SSH port back to the host).
	BootTest bool
	// BootTestTimeout bounds the whole boot test (boot + SSH). Zero selects
	// defaultBootTestTimeout. Generous by default because a host without
	// /dev/kvm falls back to TCG emulation, which boots several times slower.
	BootTestTimeout time.Duration
}

// RunQEMU launches an image in QEMU.
func RunQEMU(proj *yoestar.Project, unitName, machineName, projectDir string, opts QEMUOptions, w io.Writer) error {
	// Find the image unit. AnyUnit suffices to read class + name;
	// the actual built artifact lives at a per-machine path.
	unit := proj.AnyUnit(unitName)
	if unit == nil {
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
	distro, err := proj.EffectiveDistroForImage(unitName)
	if err != nil {
		return fmt.Errorf("resolving distro for %q: %w", unitName, err)
	}
	imgPath := findImage(projectDir, machine.Name, unitName, distro)
	if imgPath == "" {
		return fmt.Errorf("no built image for %q — run yoe build %s first", unitName, unitName)
	}

	// Fail fast (before the disk grow) if a guest is already holding the
	// host forward ports — the common "an image is already running" case.
	if err := checkQEMUPortsFree(mergeQEMUPorts(machine.QEMUPorts(), opts.Ports)); err != nil {
		return err
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
		kernelPath := ""
		needsDirectBoot := machine.QEMU == nil || machine.QEMU.Firmware == ""
		if needsDirectBoot && machine.Kernel.Unit != "" {
			kernelPath = findKernelImage(projectDir, machine.Arch, machine.Kernel.Unit)
		}
		return BuildQEMUArgs(machine, opts, imgFile, kernelPath)
	}

	// Try host QEMU first
	if _, err := exec.LookPath(qemuBin); err == nil {
		if machine.Arch == detectHostArch() && !kvmAvailable() {
			fmt.Fprintf(w, "  /dev/kvm not available — using TCG software emulation (slower)\n")
		}
		args := buildArgs(imgPath)
		if opts.BootTest {
			sshPort, err := sshHostPort(machine, opts)
			if err != nil {
				return err
			}
			return runBootTest(qemuBin, args, sshPort, opts.BootTestTimeout, w)
		}
		fmt.Fprintf(w, "Starting QEMU (host): %s %s\n", qemuBin, machine.Arch)
		cmd := exec.Command(qemuBin, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		if opts.Daemon {
			cmd.Stdin = nil
			cmd.Stdout = nil
			if err := cmd.Start(); err != nil {
				return fmt.Errorf("starting QEMU: %w", err)
			}
			fmt.Fprintf(w, "QEMU running in background (PID %d)\n", cmd.Process.Pid)
			return nil
		}
		// Tee stderr so a QEMU launch failure can be reported with the
		// reason QEMU printed, not just a bare exit code.
		var errBuf strings.Builder
		cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("QEMU exited with an error: %w%s", err, qemuStderrTail(errBuf.String()))
		}
		return nil
	}

	// Fall back to container
	if opts.BootTest {
		// The boot test SSHes into the guest over a host-side port forward.
		// Inside the build container QEMU's user-net forwards land on the
		// container's loopback, not the host's, so the SSH probe can't reach
		// the guest — and that's exactly the port-mapping tangle host QEMU
		// avoids. Require qemu-system on the host rather than emulate it.
		return fmt.Errorf("boot-test requires %s on the host PATH (the container fallback can't forward the guest SSH port back to the host); install qemu-system for %s", qemuBin, machine.Arch)
	}
	fmt.Fprintf(w, "Starting QEMU (container): %s %s\n", qemuBin, machine.Arch)
	rel, err := filepath.Rel(projectDir, imgPath)
	if err != nil {
		return fmt.Errorf("image path not under project: %w", err)
	}
	containerImgPath := filepath.Join("/project", rel)
	args := buildArgs(containerImgPath)
	fullCmd := qemuBin + " " + strings.Join(args, " ")

	return yoe.RunInContainer(yoe.ContainerRunConfig{
		Image:       yoe.DefaultContainerImage(proj),
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

// QEMUBinary returns the qemu-system-* executable that yoe would launch for
// the given target arch. Exported so callers outside this package (the TUI
// command preview, in particular) can render the full invocation.
func QEMUBinary(arch string) string { return qemuBinary(arch) }

// BuildQEMUArgs assembles the qemu-system-* argv (excluding the binary
// itself) that yoe would pass for the given machine + options + concrete
// on-disk paths. Both `RunQEMU` and the TUI's "equivalent command line"
// preview go through here so the two stay in lock-step.
//
// imgPath is required; kernelPath may be empty (no `-kernel` argument is
// emitted) or a placeholder string when the image hasn't been built yet
// — the caller picks the placeholder.
func BuildQEMUArgs(machine *yoestar.Machine, opts QEMUOptions, imgPath, kernelPath string) []string {
	a := baseQEMUArgs(machine, opts)
	a = append(a, "-drive", fmt.Sprintf("file=%s,format=raw,if=virtio", imgPath))

	// Merge machine-defined ports with extra ports. An extra entry whose
	// guest port matches a machine forward replaces it (qemu-in-qemu).
	ports := mergeQEMUPorts(machine.QEMUPorts(), opts.Ports)
	netdev := "user,id=net0"
	for _, port := range ports {
		// port format is "host:guest", QEMU wants "hostfwd=tcp::host-:guest"
		netdev += fmt.Sprintf(",hostfwd=tcp::%s", strings.Replace(port, ":", "-:", 1))
	}
	a = append(a, "-netdev", netdev)
	a = append(a, "-device", "virtio-net-pci,netdev=net0")

	// Direct kernel boot: when no firmware is configured, pass -kernel and
	// -append for architectures that need it (arm64, riscv64). Skipped if
	// the caller couldn't resolve a kernel path.
	needsDirectBoot := machine.QEMU == nil || machine.QEMU.Firmware == ""
	if needsDirectBoot && machine.Kernel.Unit != "" && kernelPath != "" {
		a = append(a, "-kernel", kernelPath)
		if machine.Kernel.Cmdline != "" {
			a = append(a, "-append", machine.Kernel.Cmdline)
		}
	}
	return a
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

// kvmAvailable reports whether /dev/kvm exists — the prerequisite for KVM
// acceleration. Inside a QEMU guest it is absent unless the outer guest
// was started with nested virtualization, so qemu-in-qemu runs fall back
// to TCG emulation.
func kvmAvailable() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

// qemuCPU resolves the -cpu model. `host` (and the implicit default) only
// works under an accelerator; without KVM it becomes `max`, the broadest
// TCG-emulatable model. Any other explicit model is passed through.
func qemuCPU(configured string, useKVM bool) string {
	if useKVM {
		return configured // "" lets QEMU pick its default
	}
	if configured == "" || configured == "host" {
		return "max"
	}
	return configured
}

func baseQEMUArgs(machine *yoestar.Machine, opts QEMUOptions) []string {
	var args []string

	// KVM needs a same-arch host with an accessible /dev/kvm. The latter
	// is absent in qemu-in-qemu unless the outer guest was given nested
	// virtualization, so fall back to TCG emulation rather than failing.
	useKVM := machine.Arch == detectHostArch() && kvmAvailable()

	qemu := machine.QEMU
	if qemu != nil {
		if qemu.Machine != "" {
			args = append(args, "-machine", qemu.Machine)
		}
		if cpu := qemuCPU(qemu.CPU, useKVM); cpu != "" {
			args = append(args, "-cpu", cpu)
		}
	} else {
		switch machine.Arch {
		case "arm64", "riscv64":
			args = append(args, "-machine", "virt")
		default:
			args = append(args, "-machine", "q35")
		}
		args = append(args, "-cpu", qemuCPU("host", useKVM))
	}

	if useKVM {
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
	//
	// Default (`-nographic`): no QEMU window; the guest's serial console
	// is multiplexed onto host stdio so `yoe run` is a plain terminal
	// session. This matches the embedded-board workflow where the image's
	// console is `ttyS0`.
	//
	// With `--display`: open a QEMU window so the guest's framebuffer is
	// visible — what the qt-image and other graphical demos need. Add a
	// virtio-vga adapter (DRM-driven virtio-gpu plus VGA for early boot)
	// so the kernel's framebuffer console renders into that window.
	// Leaving the `-display` backend unspecified lets QEMU pick GTK on
	// Linux, Cocoa on macOS, SDL otherwise — robust across hosts and
	// honoring DISPLAY/Wayland the same way every other QEMU invocation
	// would. `-serial mon:stdio` keeps the serial console attached to
	// host stdio (the default without `-nographic` is the in-window
	// virtual console, which would hide kernel logs from the terminal).
	if opts.Display {
		// xres/yres set virtio-vga's *preferred* mode in the EDID it
		// advertises to the guest. Without them the kernel's virtio_gpu
		// driver picks the first connector mode (640×480 on QEMU 9.x),
		// scans out only that region of an otherwise-1280×800
		// framebuffer, and any UI that centres in the framebuffer
		// (e.g. the qt-image demo) lands at the bottom-right corner of
		// the visible area. 1280×800 fits a 16:10 demo nicely and stays
		// well inside virtio-vga's default 16 MiB of vgamem.
		args = append(args, "-device", "virtio-vga,xres=1280,yres=800")
		args = append(args, "-serial", "mon:stdio")
	} else {
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
