package device

import (
	"reflect"
	"testing"
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
