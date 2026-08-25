package starlark

import "testing"

// A task with no distros runs everywhere; a tagged one only where it says.
// This is what keeps an OpenRC script out of a .deb and a systemd unit out of
// an .apk, so the empty case has to stay permissive.
func TestTaskAppliesToDistro(t *testing.T) {
	cases := []struct {
		name    string
		distros []string
		distro  string
		want    bool
	}{
		{"untagged applies everywhere", nil, "alpine", true},
		{"untagged applies with no distro", nil, "", true},
		{"empty list applies everywhere", []string{}, "debian", true},
		{"tagged matches", []string{"alpine"}, "alpine", true},
		{"tagged does not match", []string{"alpine"}, "debian", false},
		{"multi-tagged matches second", []string{"debian", "ubuntu"}, "ubuntu", true},
		{"multi-tagged does not match", []string{"debian", "ubuntu"}, "alpine", false},
		{"tagged excludes empty distro", []string{"alpine"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task := Task{Name: "service", Distros: c.distros}
			if got := task.AppliesToDistro(c.distro); got != c.want {
				t.Errorf("Task{Distros:%v}.AppliesToDistro(%q) = %v, want %v",
					c.distros, c.distro, got, c.want)
			}
		})
	}
}
