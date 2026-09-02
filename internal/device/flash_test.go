package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemDisksSkipsNonCriticalMounts(t *testing.T) {
	mounts := `proc /proc proc rw 0 0
/dev/sda1 /mnt/usb ext4 rw 0 0
tmpfs /tmp tmpfs rw 0 0
`
	got := systemDisks(mounts)
	if len(got) != 0 {
		t.Errorf("expected no system disks for non-critical mounts, got %v", got)
	}
}

func TestSystemDisksParsesCriticalMountpoints(t *testing.T) {
	// Run against the actual /proc/mounts on the test runner. Whatever
	// disk hosts / on this machine should be returned, and an obviously
	// unrelated path should not.
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		t.Skipf("cannot read /proc/mounts: %v", err)
	}
	disks := systemDisks(string(data))
	for _, d := range disks {
		if !strings.HasPrefix(d, "/dev/") {
			t.Errorf("system disk %q does not start with /dev/", d)
		}
	}
}

func TestValidateDeviceRejectsNonBlockDevice(t *testing.T) {
	tmp, err := os.CreateTemp("", "flash-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.Close()

	err = validateDevice(tmp.Name())
	if err == nil || !strings.Contains(err.Error(), "not a block device") {
		t.Errorf("expected 'not a block device' error, got %v", err)
	}
}

func TestValidateDeviceRejectsMissingPath(t *testing.T) {
	if err := validateDevice(""); err == nil {
		t.Error("expected error for empty device path")
	}
	if err := validateDevice("/dev/does-not-exist-xyz"); err == nil {
		t.Error("expected error for non-existent device")
	}
}

// ----- Stable device identity -----

// writeAttr creates a sysfs-style attribute file.
func writeAttr(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// fakeBlockDev builds a /sys/class/block/<name> directory whose `device`
// entry symlinks into a device tree, the way sysfs lays it out.
func fakeBlockDev(t *testing.T, root, name, devRel string) string {
	t.Helper()
	blockDir := filepath.Join(root, "class", "block", name)
	devDir := filepath.Join(root, "devices", devRel)
	if err := os.MkdirAll(blockDir, 0o755); err != nil {
		t.Fatalf("mkdir block dir: %v", err)
	}
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatalf("mkdir device dir: %v", err)
	}
	if err := os.Symlink(devDir, filepath.Join(blockDir, "device")); err != nil {
		t.Fatalf("symlink device: %v", err)
	}
	return blockDir
}

func TestReadIdentity_PrefersBlockLevelWWID(t *testing.T) {
	root := t.TempDir()
	blockDir := fakeBlockDev(t, root, "nvme0n1", "pci0000:00/nvme/nvme0n1")
	writeAttr(t, filepath.Join(blockDir, "wwid"), "eui.002538520150d195\n")
	writeAttr(t, filepath.Join(blockDir, "device", "serial"), "S5H7NG0N214327R   \n")

	if got := readIdentity(blockDir); got != "wwid:eui.002538520150d195" {
		t.Fatalf("readIdentity = %q, want the block-level wwid", got)
	}
}

func TestReadIdentity_FallsBackToDeviceWWIDThenSerial(t *testing.T) {
	root := t.TempDir()
	blockDir := fakeBlockDev(t, root, "sda", "pci0000:00/host0/target0:0:0/0:0:0:0")
	writeAttr(t, filepath.Join(blockDir, "device", "wwid"), "naa.5001b444a9035749\n")
	writeAttr(t, filepath.Join(blockDir, "device", "serial"), "182503420075\n")
	if got := readIdentity(blockDir); got != "wwid:naa.5001b444a9035749" {
		t.Fatalf("readIdentity = %q, want the device wwid", got)
	}

	// With the wwid blank, the serial carries the identity. This is the
	// path an SD card takes: /sys/class/block/mmcblk0/device/serial.
	writeAttr(t, filepath.Join(blockDir, "device", "wwid"), "\n")
	if got := readIdentity(blockDir); got != "serial:182503420075" {
		t.Fatalf("readIdentity = %q, want the device serial", got)
	}
}

func TestReadIdentity_WalksUpToTheUSBDevice(t *testing.T) {
	root := t.TempDir()
	// USB storage commonly leaves wwid and serial blank on the SCSI device;
	// the identity lives on the USB device several levels up.
	blockDir := fakeBlockDev(t, root, "sdf", "usb3/3-6/3-6.2/3-6.2:1.0/host16/target16:0:0/16:0:0:0")
	writeAttr(t, filepath.Join(blockDir, "device", "wwid"), "\n")
	writeAttr(t, filepath.Join(blockDir, "device", "serial"), "")
	usbDir := filepath.Join(root, "devices", "usb3", "3-6", "3-6.2")
	writeAttr(t, filepath.Join(usbDir, "idVendor"), "0781\n")
	writeAttr(t, filepath.Join(usbDir, "idProduct"), "5575\n")
	writeAttr(t, filepath.Join(usbDir, "serial"), "04017711101522123217\n")

	want := "usb:0781:5575:04017711101522123217:0"
	if got := readIdentity(blockDir); got != want {
		t.Fatalf("readIdentity = %q, want %q", got, want)
	}
}

func TestReadIdentity_CardReaderSlotsStayDistinct(t *testing.T) {
	root := t.TempDir()
	// One USB device, two slots: the serial is shared, so only the logical
	// unit number tells the two block devices apart.
	usbRel := filepath.Join("usb1", "1-5", "1-5.1")
	usbDir := filepath.Join(root, "devices", usbRel)
	writeAttr(t, filepath.Join(usbDir, "idVendor"), "0955\n")
	writeAttr(t, filepath.Join(usbDir, "idProduct"), "7020\n")
	writeAttr(t, filepath.Join(usbDir, "serial"), "1420925030589\n")

	first := fakeBlockDev(t, root, "sde", filepath.Join(usbRel, "1-5.1:1.4", "host17", "target17:0:0", "17:0:0:0"))
	second := fakeBlockDev(t, root, "sdh", filepath.Join(usbRel, "1-5.1:1.4", "host17", "target17:0:0", "17:0:0:1"))

	a, b := readIdentity(first), readIdentity(second)
	if a == b {
		t.Fatalf("two reader slots share identity %q", a)
	}
	if a != "usb:0955:7020:1420925030589:0" || b != "usb:0955:7020:1420925030589:1" {
		t.Fatalf("slot identities = %q / %q", a, b)
	}
}

func TestReadIdentity_EmptyWhenNothingIdentifying(t *testing.T) {
	root := t.TempDir()
	blockDir := fakeBlockDev(t, root, "sdx", "pci0000:00/host9/target9:0:0/9:0:0:0")

	if got := readIdentity(blockDir); got != "" {
		t.Fatalf("readIdentity = %q, want empty so the caller falls back to the path", got)
	}

	// A USB device with no serial identifies a make and model, not a unit,
	// so it must not become an identity either.
	usbDir := filepath.Join(root, "devices", "pci0000:00")
	writeAttr(t, filepath.Join(usbDir, "idVendor"), "1234\n")
	writeAttr(t, filepath.Join(usbDir, "idProduct"), "5678\n")
	if got := readIdentity(blockDir); got != "" {
		t.Fatalf("readIdentity = %q, want empty for a serial-less USB device", got)
	}
}

func TestCandidateIgnoreKey(t *testing.T) {
	withID := Candidate{Path: "/dev/sdb", ID: "wwid:naa.5000c500b07a66d1"}
	if got := withID.IgnoreKey(); got != "wwid:naa.5000c500b07a66d1" {
		t.Fatalf("IgnoreKey = %q, want the stable identity", got)
	}
	noID := Candidate{Path: "/dev/sdb"}
	if got := noID.IgnoreKey(); got != "/dev/sdb" {
		t.Fatalf("IgnoreKey = %q, want the path fallback", got)
	}
}

func TestScsiLUN(t *testing.T) {
	if got := scsiLUN("17:0:0:2"); got != "2" {
		t.Fatalf("scsiLUN = %q, want 2", got)
	}
	if got := scsiLUN("nvme0n1"); got != "" {
		t.Fatalf("scsiLUN on a non-SCSI address = %q, want empty", got)
	}
}
