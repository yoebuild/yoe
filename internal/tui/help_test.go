package tui

import (
	"strings"
	"testing"
)

// Every view must answer `?` with keys that apply to it. The source
// progress screen used to fall through to the Units branch and offer
// build/flash/deploy shortcuts during a blocking git fetch, none of
// which did anything.
func TestHelpSections_CoversEveryView(t *testing.T) {
	views := []struct {
		kind viewKind
		name string
	}{
		{viewUnits, "viewUnits"},
		{viewDetail, "viewDetail"},
		{viewSetup, "viewSetup"},
		{viewFlash, "viewFlash"},
		{viewDeploy, "viewDeploy"},
		{viewSourcePrompt, "viewSourcePrompt"},
		{viewSourceProgress, "viewSourceProgress"},
	}
	for _, v := range views {
		m := model{view: v.kind}
		title, sections := m.helpSections()
		if title == "" {
			t.Errorf("%s: empty help title", v.name)
		}
		if len(sections) == 0 {
			t.Errorf("%s: no help sections", v.name)
		}
	}
}

// The source progress screen is a blocking operation with no keys. Its
// help must say so rather than listing the unit-list actions.
func TestHelpSections_SourceProgressIsNotTheUnitsHelp(t *testing.T) {
	progress := model{view: viewSourceProgress}
	units := model{view: viewUnits}

	progressTitle, progressSections := progress.helpSections()
	unitsTitle, _ := units.helpSections()

	if progressTitle == unitsTitle {
		t.Errorf("source progress reuses the units help title %q", progressTitle)
	}
	if !strings.Contains(strings.ToLower(progressTitle), "progress") &&
		!strings.Contains(strings.ToLower(progressTitle), "working") {
		t.Errorf("source progress help title does not describe the screen: %q", progressTitle)
	}
	for _, s := range progressSections {
		for _, e := range s.entries {
			for _, unitKey := range []string{"b", "f", "D", "r"} {
				if e.keys == unitKey {
					t.Errorf("source progress help offers %q, which does nothing there", unitKey)
				}
			}
		}
	}
}
