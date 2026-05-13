package device

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDeployScriptInstallsUnit(t *testing.T) {
	sshRecs, ssh := newSSHRecorder("", nil)
	out := &bytes.Buffer{}
	err := Deploy(context.Background(), DeployInput{
		Target:  SSHTarget{Host: "dev-pi", User: "root"},
		Unit:    "myapp",
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
		"http://laptop.local:8765/myproj",
		"# <<< yoe-dev",
		"apk --no-cache update",
		"apk del --no-scripts myapp",
		"apk add myapp",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
}

func TestDeployPropagatesAPKError(t *testing.T) {
	_, ssh := newSSHRecorder("", errors.New("apk: package not found"))
	err := Deploy(context.Background(), DeployInput{
		Target:  SSHTarget{Host: "dev-pi"},
		Unit:    "ghost",
		FeedURL: "http://laptop.local:8765/p",
		Out:     &bytes.Buffer{},
		SSH:     ssh,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDeployRequiresUnitAndURL(t *testing.T) {
	if err := Deploy(context.Background(), DeployInput{Unit: "x"}); err == nil {
		t.Error("expected error for empty FeedURL")
	}
	if err := Deploy(context.Background(), DeployInput{FeedURL: "http://x"}); err == nil {
		t.Error("expected error for empty Unit")
	}
}
