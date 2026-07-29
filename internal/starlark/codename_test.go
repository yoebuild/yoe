package starlark

import (
	"strings"
	"testing"
)

// The invariant CodenameForDistro enforces is one *release* per distro,
// not one archive suite. These tests pin the distinction: feeds may fetch
// from different dists/<suite> paths (a vendor overlay repo names its
// channel whatever it likes; a security pocket is a suffixed suite of the
// same release) as long as they agree on the release their packages are
// built against.

func TestCodenameForDistro_AgreeingFeeds(t *testing.T) {
	proj := &Project{
		SyntheticModules: []*SyntheticModule{
			{Name: "debian.main", Distro: "debian", Suite: "trixie", Codename: "trixie"},
			{Name: "debian.contrib", Distro: "debian", Suite: "trixie", Codename: "trixie"},
		},
	}
	got, err := proj.CodenameForDistro("debian")
	if err != nil {
		t.Fatalf("CodenameForDistro: %v", err)
	}
	if got != "trixie" {
		t.Errorf("codename = %q, want %q", got, "trixie")
	}
}

func TestCodenameForDistro_OverlayFeedWithOwnSuite(t *testing.T) {
	// A vendor BSP repo publishes under dists/stable with no relation to
	// Debian's own "stable" alias; it declares the release its packages
	// are ABI-coupled to. That must resolve, not error — it is the case
	// that motivated splitting codename from suite.
	proj := &Project{
		SyntheticModules: []*SyntheticModule{
			{Name: "debian.main", Distro: "debian", Suite: "trixie", Codename: "trixie"},
			{Name: "qcom.arduino", Distro: "debian", Suite: "stable", Codename: "trixie"},
		},
	}
	got, err := proj.CodenameForDistro("debian")
	if err != nil {
		t.Fatalf("CodenameForDistro: %v", err)
	}
	if got != "trixie" {
		t.Errorf("codename = %q, want %q", got, "trixie")
	}
}

func TestCodenameForDistro_SecurityPocketSuite(t *testing.T) {
	// A -security pocket is a different suite of the same release.
	proj := &Project{
		SyntheticModules: []*SyntheticModule{
			{Name: "debian.main", Distro: "debian", Suite: "trixie", Codename: "trixie"},
			{Name: "debian.main-security", Distro: "debian", Suite: "trixie-security", Codename: "trixie"},
		},
	}
	if _, err := proj.CodenameForDistro("debian"); err != nil {
		t.Fatalf("CodenameForDistro: %v", err)
	}
}

func TestCodenameForDistro_MixedReleasesRejected(t *testing.T) {
	// The real hazard the guard exists for: two releases of one distro
	// means two incompatible libc builds in one rootfs.
	proj := &Project{
		SyntheticModules: []*SyntheticModule{
			{Name: "debian.main", Distro: "debian", Suite: "trixie", Codename: "trixie"},
			{Name: "debian.old", Distro: "debian", Suite: "bookworm", Codename: "bookworm"},
		},
	}
	_, err := proj.CodenameForDistro("debian")
	if err == nil {
		t.Fatal("want error for mixed releases, got nil")
	}
	// The message must name the offending feeds, not just the codenames —
	// a project can have many feeds and the author needs to know which.
	for _, want := range []string{"debian.main", "debian.old", "trixie", "bookworm"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestCodenameForDistro_PerDistroIsolation(t *testing.T) {
	// A project carrying both a Debian and an Ubuntu feed resolves each
	// distro's own release; they are not compared against each other.
	proj := &Project{
		SyntheticModules: []*SyntheticModule{
			{Name: "debian.main", Distro: "debian", Suite: "trixie", Codename: "trixie"},
			{Name: "ubuntu.main", Distro: "ubuntu", Suite: "resolute", Codename: "resolute"},
		},
	}
	deb, err := proj.CodenameForDistro("debian")
	if err != nil {
		t.Fatalf("debian: %v", err)
	}
	ubu, err := proj.CodenameForDistro("ubuntu")
	if err != nil {
		t.Fatalf("ubuntu: %v", err)
	}
	if deb != "trixie" || ubu != "resolute" {
		t.Errorf("got (%q, %q), want (trixie, resolute)", deb, ubu)
	}
}

func TestCodenameForDistro_NoFeed(t *testing.T) {
	proj := &Project{SyntheticModules: []*SyntheticModule{
		{Name: "alpine.main", Distro: "alpine", Release: "v3.21"},
	}}
	if _, err := proj.CodenameForDistro("debian"); err == nil {
		t.Fatal("want error when no apt feed declares a codename, got nil")
	}
}

func TestBaseVersionForDistro_IgnoresOverlaySuite(t *testing.T) {
	// os-release must carry the base release, so registration order must
	// not let an overlay feed decide it. The overlay is listed first here
	// precisely to catch a first-match-wins implementation.
	proj := &Project{
		SyntheticModules: []*SyntheticModule{
			{Name: "qcom.arduino", Distro: "debian", Suite: "stable", Codename: "trixie"},
			{Name: "debian.main", Distro: "debian", Suite: "trixie", Codename: "trixie"},
		},
	}
	if got := proj.BaseVersionForDistro("debian"); got != "trixie" {
		t.Errorf("BaseVersionForDistro = %q, want %q", got, "trixie")
	}
}

func TestBaseVersionForDistro_AlpineBranch(t *testing.T) {
	proj := &Project{SyntheticModules: []*SyntheticModule{
		{Name: "alpine.main", Distro: "alpine", Release: "v3.21"},
	}}
	if got := proj.BaseVersionForDistro("alpine"); got != "v3.21" {
		t.Errorf("BaseVersionForDistro = %q, want %q", got, "v3.21")
	}
}
