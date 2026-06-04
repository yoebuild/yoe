package device

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
			got := mergeQEMUPorts(tt.machine, tt.cli)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mergeQEMUPorts(%v, %v) = %v, want %v", tt.machine, tt.cli, got, tt.want)
			}
		})
	}
}

// TestMergeQEMUPortsDoesNotMutateMachine guards against the merge aliasing
// and writing through the machine's declared slice.
func TestMergeQEMUPortsDoesNotMutateMachine(t *testing.T) {
	machine := []string{"2222:22", "8118:8118"}
	_ = mergeQEMUPorts(machine, []string{"18118:8118"})
	if machine[1] != "8118:8118" {
		t.Errorf("machine slice was mutated: %v", machine)
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
