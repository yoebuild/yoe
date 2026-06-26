package device

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestMergeQEMUPorts(t *testing.T) {
	machine := []string{"2222:22", "8080:80", "8118:8118"}

	tests := []struct {
		name    string
		machine []string
		cli     []string
		want    []string
	}{
		{
			name:    "no CLI ports keeps machine defaults",
			machine: machine,
			cli:     nil,
			want:    []string{"2222:22", "8080:80", "8118:8118"},
		},
		{
			name:    "matching guest port replaces the machine forward",
			machine: machine,
			cli:     []string{"18118:8118"},
			want:    []string{"2222:22", "8080:80", "18118:8118"},
		},
		{
			name:    "new guest port is appended",
			machine: machine,
			cli:     []string{"9000:9000"},
			want:    []string{"2222:22", "8080:80", "8118:8118", "9000:9000"},
		},
		{
			name:    "qemu-in-qemu: every default forward remapped",
			machine: machine,
			cli:     []string{"12222:22", "18080:80", "18118:8118"},
			want:    []string{"12222:22", "18080:80", "18118:8118"},
		},
		{
			name:    "replace and append mixed",
			machine: machine,
			cli:     []string{"18080:80", "9000:9000"},
			want:    []string{"2222:22", "18080:80", "8118:8118", "9000:9000"},
		},
		{
			name:    "malformed CLI entry is appended untouched",
			machine: machine,
			cli:     []string{"nonsense"},
			want:    []string{"2222:22", "8080:80", "8118:8118", "nonsense"},
		},
		{
			name:    "no machine ports, CLI only",
			machine: nil,
			cli:     []string{"2222:22"},
			want:    []string{"2222:22"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeQEMUPorts(tt.machine, tt.cli)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MergeQEMUPorts(%v, %v) = %v, want %v", tt.machine, tt.cli, got, tt.want)
			}
		})
	}
}

// TestMergeQEMUPortsDoesNotMutateMachine guards against the merge aliasing
// and writing through the machine's declared slice.
func TestMergeQEMUPortsDoesNotMutateMachine(t *testing.T) {
	machine := []string{"2222:22", "8118:8118"}
	_ = MergeQEMUPorts(machine, []string{"18118:8118"})
	if machine[1] != "8118:8118" {
		t.Errorf("machine slice was mutated: %v", machine)
	}
}

// TestCheckQEMUPortsAvailable_OverrideRetargetsBusyPort reproduces the Setup
// → QEMU settings fix end-to-end at the preflight layer: a machine forward on
// a host port that's already bound is moved off it by a local override, and
// the availability check must honor the override (test the remapped port)
// rather than the original machine port. Passing nil overrides — the old TUI
// behavior — must still flag the collision.
func TestCheckQEMUPortsAvailable_OverrideRetargetsBusyPort(t *testing.T) {
	// Bind a port to stand in for "8080 is already taken".
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind busy port: %v", err)
	}
	defer busy.Close()
	busyPort := strconv.Itoa(busy.Addr().(*net.TCPAddr).Port)

	// Grab a second ephemeral port, then release it so it's free to remap onto.
	freeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind free port: %v", err)
	}
	freePort := strconv.Itoa(freeLn.Addr().(*net.TCPAddr).Port)
	freeLn.Close()

	machine := &yoestar.Machine{
		QEMU: &yoestar.QEMUConfig{Ports: []string{busyPort + ":8080"}},
	}

	// No override: the machine forward still points at the busy port → error.
	if err := CheckQEMUPortsAvailable(machine, nil); err == nil {
		t.Fatalf("expected a collision on busy port %s with no override", busyPort)
	}

	// Override remaps guest 8080 onto the free host port → must pass.
	if err := CheckQEMUPortsAvailable(machine, []string{freePort + ":8080"}); err != nil {
		t.Fatalf("override %s:8080 should clear the collision, got: %v", freePort, err)
	}
}

// TestBaseQEMUArgsDisplay covers the headless-vs-graphical fork of the
// QEMU command line. `--display` (Display=true) must open a window AND
// keep serial on host stdio so the user can see kernel logs alongside
// the framebuffer; `--no-display` (Display=false) keeps the legacy
// `-nographic` behavior so SSH sessions and the existing TUI workflow
// stay unchanged.
func TestBaseQEMUArgsDisplay(t *testing.T) {
	machine := &yoestar.Machine{Arch: "x86_64"}

	headless := baseQEMUArgs(machine, QEMUOptions{})
	if !slices.Contains(headless, "-nographic") {
		t.Errorf("Display=false: expected -nographic, got %v", headless)
	}
	if slices.Contains(headless, "virtio-vga") {
		t.Errorf("Display=false: expected no virtio-vga, got %v", headless)
	}

	graphical := baseQEMUArgs(machine, QEMUOptions{Display: true})
	if slices.Contains(graphical, "-nographic") {
		t.Errorf("Display=true: expected no -nographic, got %v", graphical)
	}
	// virtio-vga is passed with xres/yres preferred-mode hints, so look
	// for any arg that starts with "virtio-vga".
	hasVirtioVga := false
	for _, a := range graphical {
		if strings.HasPrefix(a, "virtio-vga") {
			hasVirtioVga = true
			break
		}
	}
	if !hasVirtioVga {
		t.Errorf("Display=true: expected virtio-vga device, got %v", graphical)
	}
	if !containsPair(graphical, "-serial", "mon:stdio") {
		t.Errorf("Display=true: expected -serial mon:stdio, got %v", graphical)
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// writeBoot creates <dir>/rootfs/boot populated with the named files and
// returns the image path (<dir>/img.img) findBootKernel resolves against.
func writeBoot(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	bootDir := filepath.Join(dir, "rootfs", "boot")
	if err := os.MkdirAll(bootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(bootDir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "img.img")
}

func TestFindBootKernel(t *testing.T) {
	t.Run("alpine: unversioned vmlinuz, no initrd", func(t *testing.T) {
		img := writeBoot(t, "vmlinuz")
		kernel, initrd := findBootKernel(img)
		if filepath.Base(kernel) != "vmlinuz" {
			t.Errorf("kernel = %q, want .../vmlinuz", kernel)
		}
		if initrd != "" {
			t.Errorf("initrd = %q, want empty (Alpine boots without one)", initrd)
		}
	})

	t.Run("debian: versioned vmlinuz + initrd", func(t *testing.T) {
		img := writeBoot(t,
			"vmlinuz-6.12.86+deb13-arm64",
			"initrd.img-6.12.86+deb13-arm64",
			"config-6.12.86+deb13-arm64",
			"System.map-6.12.86+deb13-arm64",
		)
		kernel, initrd := findBootKernel(img)
		if filepath.Base(kernel) != "vmlinuz-6.12.86+deb13-arm64" {
			t.Errorf("kernel = %q, want versioned vmlinuz", kernel)
		}
		if filepath.Base(initrd) != "initrd.img-6.12.86+deb13-arm64" {
			t.Errorf("initrd = %q, want versioned initrd.img", initrd)
		}
	})

	t.Run("ubuntu: dangling initrd.img symlink is skipped", func(t *testing.T) {
		// Ubuntu's kernel only Recommends an initramfs generator, so no real
		// initrd.img-<ver> is ever written — but the maintainer scripts still
		// leave /boot/initrd.img and /boot/vmlinuz symlinks. The vmlinuz one
		// resolves; the initrd one dangles. The launcher must follow the
		// kernel symlink and drop the broken initrd so QEMU boots through the
		// kernel's built-in drivers instead of aborting on -initrd.
		img := writeBoot(t,
			"vmlinuz-7.0.0-14-generic",
			"config-7.0.0-14-generic",
		)
		bootDir := filepath.Join(filepath.Dir(img), "rootfs", "boot")
		if err := os.Symlink("vmlinuz-7.0.0-14-generic", filepath.Join(bootDir, "vmlinuz")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("initrd.img-7.0.0-14-generic", filepath.Join(bootDir, "initrd.img")); err != nil {
			t.Fatal(err)
		}
		kernel, initrd := findBootKernel(img)
		if filepath.Base(kernel) != "vmlinuz" {
			t.Errorf("kernel = %q, want the resolvable .../vmlinuz symlink", kernel)
		}
		if initrd != "" {
			t.Errorf("initrd = %q, want empty (dangling symlink must be skipped)", initrd)
		}
	})

	t.Run("multiple kernels: newest-sorting wins", func(t *testing.T) {
		img := writeBoot(t, "vmlinuz-6.1.0-47-arm64", "vmlinuz-6.12.86+deb13-arm64")
		kernel, _ := findBootKernel(img)
		if filepath.Base(kernel) != "vmlinuz-6.12.86+deb13-arm64" {
			t.Errorf("kernel = %q, want the newest-sorting version", kernel)
		}
	})

	t.Run("no kernel: both empty", func(t *testing.T) {
		img := writeBoot(t) // empty /boot
		kernel, initrd := findBootKernel(img)
		if kernel != "" || initrd != "" {
			t.Errorf("expected empty results, got kernel=%q initrd=%q", kernel, initrd)
		}
	})
}

func TestBuildQEMUArgsDirectBoot(t *testing.T) {
	// arm64 virt with no firmware → direct kernel boot. Debian also needs
	// -initrd; Alpine passes an empty initrd and must omit the flag.
	machine := &yoestar.Machine{
		Arch:   "arm64",
		Kernel: yoestar.KernelConfig{Unit: "linux", Cmdline: "console=ttyAMA0 root=/dev/vda1 rw"},
		QEMU:   &yoestar.QEMUConfig{Machine: "virt"},
	}

	withInitrd := BuildQEMUArgs(machine, QEMUOptions{}, "/img.img", "/boot/vmlinuz-x", "/boot/initrd.img-x")
	if !containsPair(withInitrd, "-kernel", "/boot/vmlinuz-x") {
		t.Errorf("expected -kernel, got %v", withInitrd)
	}
	if !containsPair(withInitrd, "-initrd", "/boot/initrd.img-x") {
		t.Errorf("expected -initrd, got %v", withInitrd)
	}
	if !containsPair(withInitrd, "-append", "console=ttyAMA0 root=/dev/vda1 rw") {
		t.Errorf("expected -append cmdline, got %v", withInitrd)
	}

	noInitrd := BuildQEMUArgs(machine, QEMUOptions{}, "/img.img", "/boot/vmlinuz", "")
	if slices.Contains(noInitrd, "-initrd") {
		t.Errorf("expected no -initrd when initrd path is empty, got %v", noInitrd)
	}

	// No kernel resolved (e.g. image not built) → no -kernel at all.
	none := BuildQEMUArgs(machine, QEMUOptions{}, "/img.img", "", "")
	if slices.Contains(none, "-kernel") {
		t.Errorf("expected no -kernel with empty kernel path, got %v", none)
	}
}

// TestBuildQEMUArgsDirectBootDistroUnit guards the per-distro machine kernel:
// a machine declaring its kernel via distro_unit (so Kernel.Unit is empty) must
// still take the direct-kernel-boot path. Gating on Unit != "" instead of
// HasKernel() silently dropped -kernel and broke `yoe run` on qemu machines.
func TestBuildQEMUArgsDirectBootDistroUnit(t *testing.T) {
	machine := &yoestar.Machine{
		Arch: "arm64",
		Kernel: yoestar.KernelConfig{
			DistroUnit: map[string]string{"alpine": "linux", "debian": "linux-image-arm64"},
			Cmdline:    "console=ttyAMA0 root=/dev/vda1 rw",
		},
		QEMU: &yoestar.QEMUConfig{Machine: "virt"},
	}
	args := BuildQEMUArgs(machine, QEMUOptions{}, "/img.img", "/boot/vmlinuz-x", "/boot/initrd.img-x")
	if !containsPair(args, "-kernel", "/boot/vmlinuz-x") {
		t.Errorf("distro_unit machine should still direct-boot a kernel, got %v", args)
	}
	if !containsPair(args, "-initrd", "/boot/initrd.img-x") {
		t.Errorf("expected -initrd, got %v", args)
	}
}

// writeKernelFile creates a fake arm64 kernel: a bare Image carries the "ARMd"
// magic at offset 56; an EFI-only (zboot) image is a PE stub without it.
func writeKernelFile(t *testing.T, bareImage bool) string {
	t.Helper()
	buf := make([]byte, 64)
	copy(buf[0:2], "MZ")
	if bareImage {
		copy(buf[56:60], "ARMd")
	}
	p := filepath.Join(t.TempDir(), "vmlinuz")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArm64BareImage(t *testing.T) {
	if !arm64BareImage(writeKernelFile(t, true)) {
		t.Error("bare Image (ARMd magic) should be reported direct-bootable")
	}
	if arm64BareImage(writeKernelFile(t, false)) {
		t.Error("EFI-only kernel (no ARMd magic) should not be direct-bootable")
	}
	if !arm64BareImage(filepath.Join(t.TempDir(), "missing")) {
		t.Error("unreadable path should default to true (preserve direct boot)")
	}
}

func TestBuildQEMUArgsZbootUEFI(t *testing.T) {
	machine := &yoestar.Machine{
		Arch:   "arm64",
		Kernel: yoestar.KernelConfig{Unit: "linux", Cmdline: "console=ttyAMA0 root=/dev/vda1 rw"},
		QEMU:   &yoestar.QEMUConfig{Machine: "virt"},
	}

	// A bare Image direct-boots: never gets -bios, regardless of host firmware.
	bare := BuildQEMUArgs(machine, QEMUOptions{}, "/img.img", writeKernelFile(t, true), "")
	if slices.Contains(bare, "-bios") {
		t.Errorf("bare Image must not add -bios, got %v", bare)
	}

	// An EFI-only (zboot) kernel boots via UEFI: -bios is added iff edk2/AAVMF
	// firmware is installed on this host. The -kernel handoff stays either way.
	zboot := BuildQEMUArgs(machine, QEMUOptions{}, "/img.img", writeKernelFile(t, false), "")
	if !slices.Contains(zboot, "-kernel") {
		t.Errorf("expected -kernel for zboot kernel, got %v", zboot)
	}
	wantBios := aarch64UEFIFirmware() != ""
	if got := slices.Contains(zboot, "-bios"); got != wantBios {
		t.Errorf("zboot -bios = %v, want %v (firmware=%q)", got, wantBios, aarch64UEFIFirmware())
	}
}
