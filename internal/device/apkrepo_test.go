package device

import (
	"strings"
	"testing"
)

// The deploy path writes a block and `device repo remove` strips one.
// If their markers ever disagree, remove silently leaves the entry in
// place. Pin that a block written under a name is matched by the strip
// for that same name.
func TestApkRepoBlockRoundTrip(t *testing.T) {
	write := apkRepoWriteBlock("dev", "http://feed.local/proj/alpine/x86_64")
	strip := apkRepoStripBlock("dev")

	for _, want := range []string{"# >>> yoe-dev", "# <<< yoe-dev", "http://feed.local/proj/alpine/x86_64"} {
		if !strings.Contains(write, want) {
			t.Errorf("written block missing %q:\n%s", want, write)
		}
	}
	// The strip's sed address range must name the same markers.
	for _, want := range []string{"# >>> yoe-dev", "# <<< yoe-dev"} {
		if !strings.Contains(strip, want) {
			t.Errorf("strip missing %q:\n%s", want, strip)
		}
	}
	// A write is idempotent because it strips first.
	if !strings.Contains(write, strings.TrimSpace(strip)) {
		t.Errorf("write does not begin by stripping:\nwrite=%s\nstrip=%s", write, strip)
	}
}

func TestAlpineDeployScriptUsesSharedBlock(t *testing.T) {
	got := alpineDeployScript("http://feed.local/proj/alpine/x86_64", "hello")
	if !strings.Contains(got, apkRepoStripBlock("dev")) {
		t.Errorf("deploy script does not use the shared marker block:\n%s", got)
	}
	if !strings.Contains(got, "apk add hello") {
		t.Errorf("deploy script missing the install:\n%s", got)
	}
}
