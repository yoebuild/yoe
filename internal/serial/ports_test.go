package serial

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseByIDName(t *testing.T) {
	cases := []struct {
		name                  string
		vendor, serial, iface string
	}{
		{"usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0", "FTDI FT232R USB UART", "A50285BI", "if00"},
		{"usb-Silicon_Labs_CP2102N_USB_to_UART_Bridge_Controller_0001-if00", "Silicon Labs CP2102N USB to UART Bridge Controller", "0001", "if00"},
		{"usb-1a86_USB_Serial-if00-port0", "1a86 USB Serial", "", "if00"},
		{"usb-FTDI_Quad_RS232-HS_FT1ABCD-if01-port0", "FTDI Quad RS232-HS", "FT1ABCD", "if01"},
	}
	for _, c := range cases {
		v, s, i := parseByIDName(c.name)
		if v != c.vendor || s != c.serial || i != c.iface {
			t.Errorf("%s: got (%q, %q, %q), want (%q, %q, %q)", c.name, v, s, i, c.vendor, c.serial, c.iface)
		}
	}
}

// fakeDev builds a /dev stand-in: tty nodes as regular files, by-id links
// pointing at them the way udev does (relative, ../../ttyUSB0).
func fakeDev(t *testing.T, nodes []string, links map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range nodes {
		if err := os.WriteFile(filepath.Join(root, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if links != nil {
		byID := filepath.Join(root, "serial", "by-id")
		if err := os.MkdirAll(byID, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, target := range links {
			if err := os.Symlink("../../"+target, filepath.Join(byID, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func TestListUnder(t *testing.T) {
	root := fakeDev(t,
		[]string{"ttyUSB0", "ttyUSB1", "ttyACM0", "ttyS0"},
		map[string]string{
			"usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0": "ttyUSB0",
			"usb-Silicon_Labs_CP2102N_0001-if00":           "ttyUSB1",
		})
	ports, err := listUnder(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 3 {
		t.Fatalf("got %d ports, want 3: %+v", len(ports), ports)
	}
	// by-id entries first, sorted by link; then the bare ACM node.
	if ports[0].Serial != "A50285BI" || ports[0].Path != filepath.Join(root, "ttyUSB0") {
		t.Errorf("ports[0] = %+v", ports[0])
	}
	if ports[0].Key() != ports[0].ByID {
		t.Errorf("Key() should prefer ByID, got %q", ports[0].Key())
	}
	if ports[1].Vendor != "Silicon Labs CP2102N" || ports[1].Serial != "0001" {
		t.Errorf("ports[1] = %+v", ports[1])
	}
	if ports[2].ByID != "" || ports[2].Path != filepath.Join(root, "ttyACM0") {
		t.Errorf("ports[2] = %+v", ports[2])
	}
	if ports[2].Key() != ports[2].Path {
		t.Errorf("Key() without ByID should be Path, got %q", ports[2].Key())
	}
}

func TestListUnder_NoByIDDir(t *testing.T) {
	root := fakeDev(t, []string{"ttyUSB0", "cu.usbserial-A50285BI"}, nil)
	ports, err := listUnder(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 2 {
		t.Fatalf("got %+v, want ttyUSB0 and cu.usbserial", ports)
	}
}

func TestListUnder_Empty(t *testing.T) {
	ports, err := listUnder(t.TempDir())
	if err != nil || len(ports) != 0 {
		t.Fatalf("got %+v, %v; want empty, nil", ports, err)
	}
}

func TestMatch(t *testing.T) {
	ports := []Port{
		{ByID: "/dev/serial/by-id/usb-FTDI_X_A1-if00-port0", Path: "/dev/ttyUSB0"},
		{Path: "/dev/ttyACM0"},
	}
	if p, ok := Match(ports, "/dev/serial/by-id/usb-FTDI_X_A1-if00-port0"); !ok || p.Path != "/dev/ttyUSB0" {
		t.Errorf("by-id match: %+v %v", p, ok)
	}
	if p, ok := Match(ports, "/dev/ttyUSB0"); !ok || p.ByID == "" {
		t.Errorf("path match should find the by-id port: %+v %v", p, ok)
	}
	if p, ok := Match(ports, "/dev/ttyACM0"); !ok || p.Path != "/dev/ttyACM0" {
		t.Errorf("bare path: %+v %v", p, ok)
	}
	if _, ok := Match(ports, "/dev/ttyUSB9"); ok {
		t.Error("absent path should not match")
	}
	if _, ok := Match(ports, ""); ok {
		t.Error("empty mapping should not match")
	}
}
