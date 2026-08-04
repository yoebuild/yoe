package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// clipFixed pads and truncates the fixed-width columns in the unit,
// module, and diagnostics tables. Unit names, module paths and versions
// come from package feeds and the filesystem, so they are not
// guaranteed ASCII.
func TestClipFixed(t *testing.T) {
	cases := []struct {
		name string
		in   string
		w    int
	}{
		{"short ascii", "curl", 12},
		{"exact fit", "abcdefghijkl", 12},
		{"long ascii", "a-very-long-unit-name-indeed", 12},
		{"multibyte fits", "café", 12},
		{"multibyte truncated", strings.Repeat("é", 40), 12},
		{"cjk truncated", strings.Repeat("世界", 20), 12},
		{"mixed", "libssl3-日本語-dev", 12},
	}
	for _, c := range cases {
		got := clipFixed(c.in, c.w)
		if !utf8.ValidString(got) {
			t.Errorf("%s: clipFixed produced invalid UTF-8: %q", c.name, got)
		}
		if w := ansi.StringWidth(got); w != c.w {
			t.Errorf("%s: clipFixed(%q, %d) has display width %d, want %d (got %q)",
				c.name, c.in, c.w, w, c.w, got)
		}
	}
}

func TestClipFixed_ZeroWidth(t *testing.T) {
	if got := clipFixed("anything", 0); got != "" {
		t.Errorf("clipFixed(_, 0) = %q, want empty", got)
	}
}

// wrapLine hard-wraps build-log lines, which carry ANSI color from the
// compilers and tools that produced them. Every wrapped line must stay
// within the terminal width and must not be cut inside an escape
// sequence.
func TestWrapLine(t *testing.T) {
	const width = 40
	m := model{width: width}

	plain := strings.Repeat("abcdefghij", 12)
	styled := "\x1b[31m" + strings.Repeat("error: something went wrong. ", 6) + "\x1b[0m"

	for name, line := range map[string]string{"plain": plain, "styled": styled} {
		got := m.wrapLine(line)
		if len(got) < 2 {
			t.Errorf("%s: expected the line to wrap, got %d line(s)", name, len(got))
		}
		for i, l := range got {
			if !utf8.ValidString(l) {
				t.Errorf("%s line %d: invalid UTF-8: %q", name, i, l)
			}
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("%s line %d: display width %d exceeds %d: %q", name, i, w, width, l)
			}
		}
		// Nothing may be dropped: the visible text of the wrapped lines
		// must add up to the visible text of the input.
		var joined strings.Builder
		for _, l := range got {
			joined.WriteString(strings.TrimLeft(ansi.Strip(l), " "))
		}
		wantVisible := strings.ReplaceAll(ansi.Strip(line), " ", "")
		gotVisible := strings.ReplaceAll(joined.String(), " ", "")
		if gotVisible != wantVisible {
			t.Errorf("%s: wrapping changed the visible text\n got: %q\nwant: %q", name, gotVisible, wantVisible)
		}
	}
}

// A line that already fits is returned untouched, styling included.
func TestWrapLine_ShortLineUnchanged(t *testing.T) {
	m := model{width: 80}
	line := "\x1b[32mok\x1b[0m  github.com/yoebuild/yoe/internal/tui"
	got := m.wrapLine(line)
	if len(got) != 1 || got[0] != line {
		t.Errorf("wrapLine reformatted a line that already fits: %q", got)
	}
}
