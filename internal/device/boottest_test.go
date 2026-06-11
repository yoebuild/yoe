package device

import (
	"strings"
	"testing"
	"time"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestMarkerScannerFindsMarker(t *testing.T) {
	var tee strings.Builder
	s := newMarkerScanner(&tee, "login:")

	s.Write([]byte("Welcome\nbooting...\n"))
	select {
	case <-s.found:
		t.Fatal("marker reported before it was written")
	default:
	}

	s.Write([]byte("qemu-x86_64 login: "))
	select {
	case <-s.found:
	default:
		t.Fatal("marker not detected after it was written")
	}

	if got := tee.String(); !strings.Contains(got, "Welcome") || !strings.Contains(got, "login:") {
		t.Fatalf("console output not tee'd through: %q", got)
	}
}

func TestMarkerScannerMarkerSplitAcrossWrites(t *testing.T) {
	s := newMarkerScanner(&strings.Builder{}, "login:")
	// Split the marker across two writes — the retained tail must bridge them.
	s.Write([]byte("some boot noise lo"))
	select {
	case <-s.found:
		t.Fatal("marker reported on partial write")
	default:
	}
	s.Write([]byte("gin: "))
	select {
	case <-s.found:
	default:
		t.Fatal("marker split across writes was not detected")
	}
}

func TestMarkerScannerClosesOnce(t *testing.T) {
	s := newMarkerScanner(&strings.Builder{}, "login:")
	// Multiple post-marker writes must not panic on a double close.
	s.Write([]byte("login: "))
	s.Write([]byte("login: again"))
	select {
	case <-s.found:
	default:
		t.Fatal("marker not detected")
	}
}

func machineWithPorts(ports []string) *yoestar.Machine {
	return &yoestar.Machine{Name: "qemu-test", QEMU: &yoestar.QEMUConfig{Ports: ports}}
}

func TestSSHHostPort(t *testing.T) {
	tests := []struct {
		name      string
		machine   []string
		cli       []string
		want      int
		wantError bool
	}{
		{name: "machine default", machine: []string{"2222:22", "8080:80"}, want: 2222},
		{name: "cli override replaces guest 22", machine: []string{"2222:22"}, cli: []string{"3333:22"}, want: 3333},
		{name: "cli for a different guest port is ignored", machine: []string{"2222:22"}, cli: []string{"9000:80"}, want: 2222},
		{name: "no forward to guest 22", machine: []string{"8080:80"}, wantError: true},
		{name: "no qemu ports at all", machine: nil, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sshHostPort(machineWithPorts(tt.machine), QEMUOptions{Ports: tt.cli})
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected an error, got port %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got host port %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunBootTestRequiresReachablePort(t *testing.T) {
	// A bogus qemu binary that exits immediately stands in for "QEMU never
	// reaches the login prompt"; the boot test must fail fast rather than
	// hang, and well within the short timeout.
	start := time.Now()
	err := runBootTest("/bin/true", nil, 2222, 3*time.Second, &strings.Builder{})
	if err == nil {
		t.Fatal("expected boot test to fail when QEMU exits immediately")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("boot test took too long to fail: %s", time.Since(start))
	}
}
