// Package serial captures a board's serial console for the TUI, the CLI,
// and any agent that wants to read or type at the board.
//
// A machine declares its console speed (`serial = serial_config(baud = …)`);
// the developer maps the machine to a host adapter in local.star by its
// /dev/serial/by-id/ path. One yoe process opens the adapter and becomes the
// owner of a Session, which logs the console and serves the raw stream on a
// Unix socket so other yoe processes can attach without a second open.
package serial

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Port is one serial adapter on the host.
type Port struct {
	// ByID is the /dev/serial/by-id/ link for the adapter, or "" when the
	// kernel exposed none (some bridges report no serial number).
	ByID string
	// Path is the /dev/tty* node ByID resolves to this boot, or the node
	// itself when there is no ByID.
	Path string
	// Vendor is the vendor and product text parsed from the ByID name,
	// e.g. "FTDI FT232R USB UART". Empty without a ByID.
	Vendor string
	// Serial is the serial number parsed from the ByID name, when the
	// name carries one.
	Serial string
	// Interface is the USB interface tag from the ByID name ("if00",
	// "if01" on multi-port bridges). Empty without a ByID.
	Interface string
}

// Key returns the value stored in local.star for this port: the stable
// ByID link when there is one, else the /dev/tty* path.
func (p Port) Key() string {
	if p.ByID != "" {
		return p.ByID
	}
	return p.Path
}

// devRoot is the directory List enumerates. Tests point it at a temp dir.
var devRoot = "/dev"

// List returns the serial adapters on the host: every /dev/serial/by-id/
// entry, then any /dev/ttyUSB* or /dev/ttyACM* node no entry points at.
// Hosts without a by-id directory (macOS) get the /dev/cu.* nodes. A host
// with no adapters returns an empty list, not an error.
func List() ([]Port, error) {
	return listUnder(devRoot)
}

func listUnder(root string) ([]Port, error) {
	var ports []Port
	covered := map[string]bool{}

	byIDDir := filepath.Join(root, "serial", "by-id")
	entries, err := os.ReadDir(byIDDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		link := filepath.Join(byIDDir, e.Name())
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			// A dangling link is an adapter that just went away; udev
			// removes it shortly. Skip rather than fail the listing.
			continue
		}
		p := Port{ByID: link, Path: target}
		p.Vendor, p.Serial, p.Interface = parseByIDName(e.Name())
		ports = append(ports, p)
		covered[target] = true
	}

	patterns := []string{"ttyUSB*", "ttyACM*"}
	if errors.Is(err, os.ErrNotExist) {
		patterns = append(patterns, "cu.*")
	}
	for _, pat := range patterns {
		matches, _ := filepath.Glob(filepath.Join(root, pat))
		for _, m := range matches {
			if covered[m] {
				continue
			}
			ports = append(ports, Port{Path: m})
			covered[m] = true
		}
	}

	sort.Slice(ports, func(i, j int) bool {
		if (ports[i].ByID == "") != (ports[j].ByID == "") {
			return ports[i].ByID != ""
		}
		return ports[i].Key() < ports[j].Key()
	})
	return ports, nil
}

var byIDInterface = regexp.MustCompile(`-(if\d+)(-port\d+)?$`)

// parseByIDName splits a udev by-id name such as
// "usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0" into its vendor/product
// text, its serial number, and its interface tag. The last underscore
// token is taken as the serial number when it contains a digit; a name
// like "usb-1a86_USB_Serial-if00-port0" (a CH340 with no serial) yields
// no serial and keeps every token as vendor text.
func parseByIDName(name string) (vendor, serial, iface string) {
	base := name
	if m := byIDInterface.FindStringSubmatchIndex(base); m != nil {
		iface = base[m[2]:m[3]]
		base = base[:m[0]]
	}
	base = strings.TrimPrefix(base, "usb-")
	tokens := strings.Split(base, "_")
	if len(tokens) > 1 && strings.ContainsAny(tokens[len(tokens)-1], "0123456789") {
		serial = tokens[len(tokens)-1]
		tokens = tokens[:len(tokens)-1]
	}
	vendor = strings.Join(tokens, " ")
	return vendor, serial, iface
}

// Match finds the listed port a local.star mapping refers to. The mapping
// is compared against each port's Key first, then against its current
// path, so a hand-written "/dev/ttyUSB0" still matches the adapter that
// sits there now.
func Match(ports []Port, mapped string) (Port, bool) {
	if mapped == "" {
		return Port{}, false
	}
	for _, p := range ports {
		if p.Key() == mapped {
			return p, true
		}
	}
	for _, p := range ports {
		if p.Path == mapped {
			return p, true
		}
	}
	return Port{}, false
}
