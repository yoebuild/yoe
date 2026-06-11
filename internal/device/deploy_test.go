package device

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDeployAlpineScriptInstallsUnit(t *testing.T) {
	sshRecs, ssh := newSSHRecorder("", nil)
	out := &bytes.Buffer{}
	err := Deploy(context.Background(), DeployInput{
		Target:  SSHTarget{Host: "dev-pi", User: "root"},
		Unit:    "myapp",
		Distro:  "alpine",
		FeedURL: "http://laptop.local:8765/myproj",
		Out:     out,
		SSH:     ssh,
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(*sshRecs) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(*sshRecs))
	}
	script := (*sshRecs)[0].script
	for _, want := range []string{
		"touch /etc/apk/repositories",
		"# >>> yoe-dev",
		// Deploy appends the distro segment to the feed root.
		"http://laptop.local:8765/myproj/alpine",
		"# <<< yoe-dev",
		"apk update",
		"apk del --no-scripts myapp",
		"apk add myapp",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
}

func TestDeployDebianScriptInstallsUnit(t *testing.T) {
	sshRecs, ssh := newSSHRecorder("", nil)
	err := Deploy(context.Background(), DeployInput{
		Target:  SSHTarget{Host: "dev-pi", User: "root"},
		Unit:    "myapp",
		Distro:  "debian",
		Suite:   "bookworm",
		FeedURL: "http://laptop.local:8765/myproj",
		Out:     &bytes.Buffer{},
		SSH:     ssh,
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(*sshRecs) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(*sshRecs))
	}
	script := (*sshRecs)[0].script
	for _, want := range []string{
		"/etc/apt/sources.list.d/yoe-dev.list",
		// trusted=yes because the dev feed's InRelease is unsigned.
		"deb [trusted=yes] http://laptop.local:8765/myproj/debian bookworm main",
		// Pin origin is the feed hostname so the dev feed wins downgrades.
		`Pin: origin "laptop.local"`,
		"Pin-Priority: 1001",
		"apt-get update",
		"apt-get install -y --reinstall --allow-downgrades myapp",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	// The alpine package manager must never run on a debian target.
	// (The comment prose mentions apk by name, so match command forms.)
	for _, forbidden := range []string{"apk add", "apk del", "apk update"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("debian script unexpectedly invokes %q:\n%s", forbidden, script)
		}
	}
}

func TestDeployDebianRequiresSuite(t *testing.T) {
	_, ssh := newSSHRecorder("", nil)
	err := Deploy(context.Background(), DeployInput{
		Target:  SSHTarget{Host: "dev-pi"},
		Unit:    "myapp",
		Distro:  "debian",
		FeedURL: "http://laptop.local:8765/p",
		Out:     &bytes.Buffer{},
		SSH:     ssh,
	})
	if err == nil {
		t.Fatal("expected error for missing suite on debian deploy")
	}
}

func TestDeployRejectsUnknownDistro(t *testing.T) {
	_, ssh := newSSHRecorder("", nil)
	err := Deploy(context.Background(), DeployInput{
		Target:  SSHTarget{Host: "dev-pi"},
		Unit:    "myapp",
		Distro:  "gentoo",
		FeedURL: "http://laptop.local:8765/p",
		Out:     &bytes.Buffer{},
		SSH:     ssh,
	})
	if err == nil {
		t.Fatal("expected error for unsupported distro")
	}
}

func TestDeployPropagatesInstallError(t *testing.T) {
	_, ssh := newSSHRecorder("", errors.New("apk: package not found"))
	err := Deploy(context.Background(), DeployInput{
		Target:  SSHTarget{Host: "dev-pi"},
		Unit:    "ghost",
		Distro:  "alpine",
		FeedURL: "http://laptop.local:8765/p",
		Out:     &bytes.Buffer{},
		SSH:     ssh,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDeployRequiresUnitURLAndDistro(t *testing.T) {
	if err := Deploy(context.Background(), DeployInput{Unit: "x", Distro: "alpine"}); err == nil {
		t.Error("expected error for empty FeedURL")
	}
	if err := Deploy(context.Background(), DeployInput{FeedURL: "http://x", Distro: "alpine"}); err == nil {
		t.Error("expected error for empty Unit")
	}
	if err := Deploy(context.Background(), DeployInput{Unit: "x", FeedURL: "http://x"}); err == nil {
		t.Error("expected error for empty Distro")
	}
}
