package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// writeMeta is a test helper that mirrors build.WriteMeta's on-disk
// layout without dragging the build package in for what we want to
// test (TUI's read side of the same file).
func writeMeta(t *testing.T, buildDir string, installedBytes int64) {
	t.Helper()
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatalf("mkdir buildDir: %v", err)
	}
	data, _ := json.Marshal(map[string]any{
		"status":          "complete",
		"installed_bytes": installedBytes,
		"hash":            "deadbeef",
	})
	if err := os.WriteFile(filepath.Join(buildDir, "build.json"), data, 0o644); err != nil {
		t.Fatalf("write build.json: %v", err)
	}
}

func TestInstalledSize_ReadsMeta(t *testing.T) {
	dir := t.TempDir()
	writeMeta(t, dir, 12345)

	got := installedSize(dir)
	if got != 12345 {
		t.Fatalf("installedSize = %d, want 12345", got)
	}
}

func TestInstalledSize_Unbuilt_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	if got := installedSize(dir); got != 0 {
		t.Fatalf("installedSize = %d, want 0", got)
	}
}

func TestRefreshUnitSize_PicksUpFreshlyWrittenMeta(t *testing.T) {
	projDir := t.TempDir()
	// build/foo.x86_64/build.json
	buildDir := filepath.Join(projDir, "build", "foo.x86_64")
	writeMeta(t, buildDir, 4096)

	m := &model{
		projectDir: projDir,
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"foo": {Name: "foo", Class: "unit"},
			},
		},
	}
	if m.unitSize["foo"] != 0 {
		t.Fatalf("expected empty initial size, got %d", m.unitSize["foo"])
	}

	m.refreshUnitSize("foo")
	if m.unitSize["foo"] != 4096 {
		t.Fatalf("after refresh: unitSize[foo] = %d, want 4096", m.unitSize["foo"])
	}
}

func TestRefreshUnitSize_UnknownUnit_NoOp(t *testing.T) {
	m := &model{
		projectDir: t.TempDir(),
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units:    map[string]*yoestar.Unit{},
		},
	}
	// Should not panic, should not allocate spurious entries.
	m.refreshUnitSize("does-not-exist")
	if _, ok := m.unitSize["does-not-exist"]; ok {
		t.Fatalf("refreshUnitSize created entry for unknown unit")
	}
}

// writeMetaWithSourceState mirrors writeMeta but sets the source_state
// field used by the SRC column.
func writeMetaWithSourceState(t *testing.T, buildDir, state, describe string) {
	t.Helper()
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatalf("mkdir buildDir: %v", err)
	}
	data, _ := json.Marshal(map[string]any{
		"status":          "complete",
		"installed_bytes": 0,
		"hash":            "deadbeef",
		"source_state":    state,
		"source_describe": describe,
	})
	if err := os.WriteFile(filepath.Join(buildDir, "build.json"), data, 0o644); err != nil {
		t.Fatalf("write build.json: %v", err)
	}
}

func TestSrcStateToken_AllStates(t *testing.T) {
	cases := []struct {
		state source.State
		want  string
	}{
		{source.StateEmpty, ""},
		{source.StatePin, "pin"},
		{source.StateDev, "dev"},
		{source.StateDevMod, "dev-mod"},
		{source.StateDevDirty, "dev-dirty"},
		{source.StateLocal, "local"},
	}
	for _, c := range cases {
		if got := srcStateToken(c.state); got != c.want {
			t.Errorf("srcStateToken(%q) = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestUnitSourceState_ReadsBuildMeta(t *testing.T) {
	projDir := t.TempDir()
	buildDir := filepath.Join(projDir, "build", "foo.x86_64")
	writeMetaWithSourceState(t, buildDir, "dev-mod", "v1.0-3-gabc1234")

	m := model{
		projectDir: projDir,
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"foo": {Name: "foo", Class: "unit"},
			},
		},
		unitSrcStates: map[string]source.State{},
	}
	if got := m.unitSourceState("foo"); got != source.StateDevMod {
		t.Errorf("unitSourceState = %q, want dev-mod", got)
	}
	// Subsequent call should hit cache (same answer).
	if got := m.unitSourceState("foo"); got != source.StateDevMod {
		t.Errorf("cached call returned %q, want dev-mod", got)
	}
}

func TestUnitSourceState_NoBuildMeta_ReturnsEmpty(t *testing.T) {
	m := model{
		projectDir: t.TempDir(),
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"foo": {Name: "foo", Class: "unit"},
			},
		},
		unitSrcStates: map[string]source.State{},
	}
	if got := m.unitSourceState("foo"); got != source.StateEmpty {
		t.Errorf("unitSourceState = %q, want empty", got)
	}
}

func TestRenderSrcCell_DevMod_RendersYellow(t *testing.T) {
	projDir := t.TempDir()
	buildDir := filepath.Join(projDir, "build", "foo.x86_64")
	writeMetaWithSourceState(t, buildDir, "dev-mod", "")
	m := model{
		projectDir: projDir,
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"foo": {Name: "foo", Class: "unit"},
			},
		},
		unitSrcStates: map[string]source.State{},
	}
	got := m.renderSrcCell("foo")
	if !strings.Contains(got, "dev-mod") {
		t.Errorf("expected token in cell, got %q", got)
	}
	// Width is fixed at 9 cells (matches the SRC column width); the
	// padded form is `"dev-mod  "` (7 char token + 2 spaces). Lipgloss
	// may or may not wrap it in SGR escapes depending on the test
	// environment's terminal profile, so assert layout, not styling.
	if !strings.Contains(got, "dev-mod  ") {
		t.Errorf("expected token padded to width 9, got %q", got)
	}
}

func TestRenderSrcCell_Image_RendersBlank(t *testing.T) {
	m := model{
		projectDir: t.TempDir(),
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"my-img": {Name: "my-img", Class: "image"},
			},
		},
		unitSrcStates: map[string]source.State{},
	}
	got := m.renderSrcCell("my-img")
	// Should be 9 spaces, no SGR escapes.
	if strings.TrimSpace(got) != "" {
		t.Errorf("image SRC cell should be blank, got %q", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("image SRC cell should carry no styling, got %q", got)
	}
}

func TestModuleSourceState_LocalAlwaysReturnsLocal(t *testing.T) {
	m := model{moduleSrcStates: map[string]source.State{}}
	rm := yoestar.ResolvedModule{Name: "foo", Local: "../foo"}
	if got := m.moduleSourceState(rm); got != source.StateLocal {
		t.Errorf("moduleSourceState = %q, want local", got)
	}
}

func TestModuleSourceState_ReadsStateFile(t *testing.T) {
	dir := t.TempDir()
	// .yoe-state.json with "dev" state.
	data, _ := json.Marshal(map[string]string{"state": "dev"})
	if err := os.WriteFile(filepath.Join(dir, ".yoe-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	m := model{moduleSrcStates: map[string]source.State{}}
	rm := yoestar.ResolvedModule{Name: "foo", Dir: dir, CloneDir: dir}
	if got := m.moduleSourceState(rm); got != source.StateDev {
		t.Errorf("moduleSourceState = %q, want dev", got)
	}
}

func TestDetailSourceLine_ImageReturnsEmpty(t *testing.T) {
	m := model{
		projectDir: t.TempDir(),
		arch:       "x86_64",
		detailUnit: "my-img",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"my-img": {Name: "my-img", Class: "image"},
			},
		},
		unitSrcStates: map[string]source.State{},
	}
	if got := m.detailSourceLine(); got != "" {
		t.Errorf("image detail SOURCE line should be empty, got %q", got)
	}
}

func TestDetailSourceLine_DevModSurfacesDescribe(t *testing.T) {
	projDir := t.TempDir()
	buildDir := filepath.Join(projDir, "build", "foo.x86_64")
	writeMetaWithSourceState(t, buildDir, "dev-mod", "v3.4.1-3-gabc1234")
	m := model{
		projectDir: projDir,
		arch:       "x86_64",
		detailUnit: "foo",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"foo": {Name: "foo", Class: "unit", Source: "https://example.com/foo.git"},
			},
		},
		unitSrcStates: map[string]source.State{},
	}
	got := m.detailSourceLine()
	// Plain-text view of the rendered line — strip ANSI escapes.
	// Don't actually need to fully parse them, just check substrings
	// pass through.
	if !strings.Contains(got, "SOURCE") {
		t.Errorf("missing SOURCE label: %q", got)
	}
	if !strings.Contains(got, "dev-mod") {
		t.Errorf("missing dev-mod token: %q", got)
	}
	if !strings.Contains(got, "v3.4.1-3-gabc1234") {
		t.Errorf("missing source_describe: %q", got)
	}
}

// helper: build a model with one pinned unit and the source-state cache wired up.
func newModelWithUnit(t *testing.T, projDir, unitName string, state source.State) model {
	t.Helper()
	m := model{
		projectDir: projDir,
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				unitName: {Name: unitName, Class: "unit", Source: "https://example.com/" + unitName + ".git"},
			},
		},
		unitSrcStates:   map[string]source.State{unitName: state},
		moduleSrcStates: map[string]source.State{},
		view:            viewDetail,
	}
	return m
}

func TestOpenSourcePromptForUnit_Pin_OpensSSHHTTPSPicker(t *testing.T) {
	m := newModelWithUnit(t, t.TempDir(), "foo", source.StatePin)
	updated, _ := m.openSourcePromptForUnit("foo")
	got, ok := updated.(model)
	if !ok {
		t.Fatalf("expected model, got %T", updated)
	}
	if got.view != viewSourcePrompt {
		t.Errorf("view = %v, want viewSourcePrompt", got.view)
	}
	if got.sourcePrompt == nil {
		t.Fatal("expected sourcePrompt to be set")
	}
	if got.sourcePrompt.kind != promptSSHHTTPS {
		t.Errorf("kind = %v, want promptSSHHTTPS", got.sourcePrompt.kind)
	}
	// Three options: HTTPS, SSH, cancel.
	if len(got.sourcePrompt.options) != 3 {
		t.Errorf("options = %d, want 3", len(got.sourcePrompt.options))
	}
}

func TestOpenSourcePromptForUnit_DevDirty_OpensDiscardConfirm(t *testing.T) {
	m := newModelWithUnit(t, t.TempDir(), "foo", source.StateDevDirty)
	updated, _ := m.openSourcePromptForUnit("foo")
	got := updated.(model)
	if got.sourcePrompt == nil || got.sourcePrompt.kind != promptDiscardDev {
		t.Fatalf("expected discard-confirm prompt, got %+v", got.sourcePrompt)
	}
}

func TestOpenSourcePromptForUnit_Image_NoOpsWithMessage(t *testing.T) {
	m := model{
		projectDir: t.TempDir(),
		arch:       "x86_64",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"my-img": {Name: "my-img", Class: "image"},
			},
		},
		unitSrcStates:   map[string]source.State{},
		moduleSrcStates: map[string]source.State{},
	}
	updated, _ := m.openSourcePromptForUnit("my-img")
	got := updated.(model)
	if got.sourcePrompt != nil {
		t.Errorf("image unit should not open a prompt")
	}
	if got.message == "" {
		t.Errorf("expected an explanatory message")
	}
}

func TestOpenPromotePrompt_PinHasHint(t *testing.T) {
	m := newModelWithUnit(t, t.TempDir(), "foo", source.StatePin)
	updated, _ := m.openPromotePrompt("foo")
	got := updated.(model)
	if got.sourcePrompt != nil {
		t.Errorf("promote should be a no-op outside dev-mod, got prompt")
	}
	if got.message == "" {
		t.Errorf("expected a hint message about state")
	}
}

func TestSourcePromptCursor_SkipsDisabled(t *testing.T) {
	m := model{
		sourcePrompt: &sourcePrompt{
			options: []sourcePromptOption{
				{label: "tag", value: "tag", disabled: true},
				{label: "hash", value: "hash"},
				{label: "branch", value: "branch", disabled: true},
				{label: "cancel", value: "cancel"},
			},
			cursor: 1, // start on hash
		},
	}
	m.moveSourcePromptCursor(1)
	// Should skip "branch" (disabled) and land on "cancel".
	if m.sourcePrompt.cursor != 3 {
		t.Errorf("cursor = %d, want 3", m.sourcePrompt.cursor)
	}
	m.moveSourcePromptCursor(1)
	// Wraps past the end, skips disabled "tag", lands on "hash" again.
	if m.sourcePrompt.cursor != 1 {
		t.Errorf("cursor wrap = %d, want 1", m.sourcePrompt.cursor)
	}
}

func TestApplySourcePromptChoice_Cancel_RestoresPrevView(t *testing.T) {
	m := model{
		view: viewSourcePrompt,
		sourcePrompt: &sourcePrompt{
			kind:     promptSSHHTTPS,
			prevView: viewDetail,
			options: []sourcePromptOption{
				{label: "cancel", value: "cancel"},
			},
		},
	}
	updated, _ := m.applySourcePromptChoice("cancel")
	got := updated.(model)
	if got.sourcePrompt != nil {
		t.Errorf("prompt should be cleared")
	}
	if got.view != viewDetail {
		t.Errorf("view should restore to viewDetail, got %v", got.view)
	}
}

func TestUpdateSourcePrompt_Esc_RestoresPrevView(t *testing.T) {
	m := model{
		view: viewSourcePrompt,
		sourcePrompt: &sourcePrompt{
			prevView: viewUnits,
			options: []sourcePromptOption{
				{label: "https", value: "https"},
				{label: "ssh", value: "ssh"},
			},
		},
	}
	updated, _ := m.updateSourcePrompt(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(model)
	if got.view != viewUnits {
		t.Errorf("esc should restore viewUnits, got %v", got.view)
	}
	if got.sourcePrompt != nil {
		t.Errorf("prompt should be cleared after esc")
	}
}

// TestApplySourcePromptChoice_SSHOpensDepthStage verifies the
// two-stage flow: picking https/ssh in stage 1 doesn't fire the
// toggle, it opens the fetch-depth picker as stage 2.
func TestPreviewHTTPSToSSH(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
		want   string
	}{
		{"https://github.com/foo/bar.git", true, "git@github.com:foo/bar.git"},
		{"https://gitlab.com/foo/bar.git", true, "git@gitlab.com:foo/bar.git"},
		{"https://example.com/", false, ""},
		{"git@github.com:foo/bar.git", false, ""},
		{"http://github.com/foo/bar.git", false, ""},
	}
	for _, c := range cases {
		got, ok := previewHTTPSToSSH(c.in)
		if ok != c.wantOK {
			t.Errorf("previewHTTPSToSSH(%q) ok=%v, want %v", c.in, ok, c.wantOK)
		}
		if c.wantOK && got != c.want {
			t.Errorf("previewHTTPSToSSH(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSchemePromptOptions_ShowsURLs(t *testing.T) {
	opts := schemePromptOptions("https://github.com/openssl/openssl.git")
	if len(opts) != 3 {
		t.Fatalf("expected 3 options, got %d", len(opts))
	}
	// HTTPS option should show the original URL.
	if !strings.Contains(opts[0].desc, "https://github.com/openssl/openssl.git") {
		t.Errorf("HTTPS desc missing URL: %q", opts[0].desc)
	}
	// SSH option should show the rewritten URL.
	if !strings.Contains(opts[1].desc, "git@github.com:openssl/openssl.git") {
		t.Errorf("SSH desc missing rewritten URL: %q", opts[1].desc)
	}
	if opts[1].disabled {
		t.Errorf("SSH should be enabled for github URLs")
	}
}

func TestSchemePromptOptions_DisablesSSHForUnmappable(t *testing.T) {
	// http:// (not https) — no rewrite available.
	opts := schemePromptOptions("http://example.com/foo.git")
	sshOpt := opts[1]
	if !sshOpt.disabled {
		t.Errorf("SSH should be disabled for non-https URL, got enabled: %+v", sshOpt)
	}
}

func TestApplySourcePromptChoice_SSHOpensDepthStage(t *testing.T) {
	m := model{
		view: viewSourcePrompt,
		sourcePrompt: &sourcePrompt{
			kind:       promptSSHHTTPS,
			target:     "unit",
			targetName: "foo",
			prevView:   viewDetail,
			options:    []sourcePromptOption{{label: "ssh", value: "ssh"}},
		},
	}
	updated, _ := m.applySourcePromptChoice("ssh")
	got := updated.(model)
	if got.sourcePrompt == nil {
		t.Fatal("expected stage-2 prompt to be open")
	}
	if got.sourcePrompt.kind != promptHistoryDepth {
		t.Errorf("kind = %v, want promptHistoryDepth", got.sourcePrompt.kind)
	}
	if !got.sourcePrompt.chosenSSH {
		t.Errorf("chosenSSH = false, want true (carried from stage 1)")
	}
	// Stage 2's options should include all/depth=N/since/cancel.
	labels := make([]string, 0, len(got.sourcePrompt.options))
	for _, o := range got.sourcePrompt.options {
		labels = append(labels, o.label)
	}
	if !strings.Contains(strings.Join(labels, " "), "all") {
		t.Errorf("expected an 'all' option, got %v", labels)
	}
}

func TestDepthOptionToFetch(t *testing.T) {
	cases := []struct {
		in   string
		want depthFetchSpec
	}{
		{"all", depthFetchSpec{}},
		{"depth=1000", depthFetchSpec{FetchDepth: 1000}},
		{"depth=100", depthFetchSpec{FetchDepth: 100}},
		{"unknown", depthFetchSpec{}},
	}
	for _, c := range cases {
		got := depthOptionToFetch(c.in)
		if got != c.want {
			t.Errorf("depthOptionToFetch(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestDepthLabel_HumanForms(t *testing.T) {
	if got := depthLabel(depthFetchSpec{}); !strings.Contains(got, "full") {
		t.Errorf("zero spec should mention full history, got %q", got)
	}
	if got := depthLabel(depthFetchSpec{FetchDepth: 100}); !strings.Contains(got, "100") {
		t.Errorf("depth label should include the count, got %q", got)
	}
}

func TestRunDevToUpstream_ParksInProgressView(t *testing.T) {
	m := newModelWithUnit(t, t.TempDir(), "foo", source.StatePin)
	updated, cmd := m.runDevToUpstream("foo", false, depthFetchSpec{})
	got := updated.(model)
	if got.view != viewSourceProgress {
		t.Errorf("view = %v, want viewSourceProgress", got.view)
	}
	if got.sourceOp == nil {
		t.Fatal("expected sourceOp to be set")
	}
	if got.sourceOp.target != targetUnit || got.sourceOp.name != "foo" {
		t.Errorf("op target/name = (%v, %s), want (unit, foo)",
			got.sourceOp.target, got.sourceOp.name)
	}
	if !strings.Contains(got.sourceOp.label, "Fetching") {
		t.Errorf("label should mention fetching, got %q", got.sourceOp.label)
	}
	if cmd == nil {
		t.Fatal("expected a tea.Cmd to dispatch the goroutine work")
	}
}

func TestSourceOpDoneMsg_Success_RestoresPrevView(t *testing.T) {
	m := model{
		view: viewSourceProgress,
		sourceOp: &sourceOp{
			target:   targetUnit,
			name:     "foo",
			prevView: viewDetail,
		},
		unitSrcStates: map[string]source.State{"foo": source.StatePin},
	}
	updated, _ := m.Update(sourceOpDoneMsg{
		target:     targetUnit,
		name:       "foo",
		successMsg: "foo switched to dev mode",
	})
	got := updated.(model)
	if got.view != viewDetail {
		t.Errorf("view = %v, want viewDetail", got.view)
	}
	if got.sourceOp != nil {
		t.Errorf("sourceOp should be cleared after done")
	}
	if got.message != "foo switched to dev mode" {
		t.Errorf("message = %q, want success message", got.message)
	}
	if _, cached := got.unitSrcStates["foo"]; cached {
		t.Errorf("unit cache entry should be invalidated after success")
	}
}

func TestSourceOpDoneMsg_Error_SurfacesError(t *testing.T) {
	m := model{
		view: viewSourceProgress,
		sourceOp: &sourceOp{
			target:   targetUnit,
			name:     "foo",
			prevView: viewDetail,
		},
	}
	updated, _ := m.Update(sourceOpDoneMsg{
		target: targetUnit,
		name:   "foo",
		err:    fmt.Errorf("dev mode failed: network timeout"),
	})
	got := updated.(model)
	if got.view != viewDetail {
		t.Errorf("view = %v, want viewDetail (restore on error)", got.view)
	}
	if !strings.Contains(got.message, "network timeout") {
		t.Errorf("error message should be in status, got %q", got.message)
	}
}

func TestViewSourceProgress_RendersLabelAndSpinner(t *testing.T) {
	m := model{
		view: viewSourceProgress,
		sourceOp: &sourceOp{
			label:    "Fetching upstream history for foo",
			spinner:  spinner.New(),
			prevView: viewDetail,
		},
	}
	got := m.viewSourceProgress()
	if !strings.Contains(got, "Working") {
		t.Errorf("expected Working header: %q", got)
	}
	if !strings.Contains(got, "Fetching upstream history for foo") {
		t.Errorf("expected label: %q", got)
	}
}

func TestViewSourcePrompt_RendersHeaderAndOptions(t *testing.T) {
	m := model{
		view: viewSourcePrompt,
		sourcePrompt: &sourcePrompt{
			kind:   promptSSHHTTPS,
			header: "Switch foo to dev mode?",
			options: []sourcePromptOption{
				{label: "HTTPS", desc: "use https://", value: "https"},
				{label: "SSH", desc: "use git@", value: "ssh"},
				{label: "cancel", value: "cancel"},
			},
			cursor: 0,
		},
	}
	got := m.viewSourcePrompt()
	if !strings.Contains(got, "Switch foo to dev mode?") {
		t.Errorf("header missing: %q", got)
	}
	if !strings.Contains(got, "HTTPS") || !strings.Contains(got, "SSH") {
		t.Errorf("options missing: %q", got)
	}
	if !strings.Contains(got, "cancel") {
		t.Errorf("cancel option missing: %q", got)
	}
}

// TestRefreshDetailFiles_WalksDestdir verifies the Files tab walker:
// directories are skipped, regular files are listed with their byte
// size, and symlinks are flagged so the renderer can dim them.
func TestRefreshDetailFiles_WalksDestdir(t *testing.T) {
	projDir := t.TempDir()
	// build/foo.x86_64/destdir/...
	destDir := filepath.Join(projDir, "build", "foo.x86_64", "destdir")
	if err := os.MkdirAll(filepath.Join(destDir, "usr", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(destDir, "usr", "lib"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "usr", "bin", "foo"), make([]byte, 200), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "usr", "lib", "libfoo.so.1"), make([]byte, 5000), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink("libfoo.so.1", filepath.Join(destDir, "usr", "lib", "libfoo.so")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	m := &model{
		projectDir: projDir,
		arch:       "x86_64",
		detailUnit: "foo",
		proj: &yoestar.Project{
			Defaults: yoestar.Defaults{Machine: "qemu-x86_64"},
			Units: map[string]*yoestar.Unit{
				"foo": {Name: "foo", Class: "unit"},
			},
		},
	}
	m.refreshDetailFiles()

	if len(m.detailFiles) != 3 {
		t.Fatalf("got %d files, want 3: %+v", len(m.detailFiles), m.detailFiles)
	}
	// Default sort is by name ascending.
	want := []string{"/usr/bin/foo", "/usr/lib/libfoo.so", "/usr/lib/libfoo.so.1"}
	for i, w := range want {
		if m.detailFiles[i].Path != w {
			t.Fatalf("files[%d] = %q, want %q", i, m.detailFiles[i].Path, w)
		}
	}
	// Symlink flagged.
	if !m.detailFiles[1].Link {
		t.Fatalf("expected /usr/lib/libfoo.so to be flagged Link")
	}
	if m.detailFiles[0].Link || m.detailFiles[2].Link {
		t.Fatalf("regular files should not be flagged Link")
	}
	if m.detailFiles[0].Size != 200 || m.detailFiles[2].Size != 5000 {
		t.Fatalf("unexpected sizes: %d, %d", m.detailFiles[0].Size, m.detailFiles[2].Size)
	}

	// Sort by size descending — biggest first, ties broken by name.
	m.detailFilesSortCol = filesSortBySize
	m.detailFilesSortDesc = true
	m.sortDetailFiles()
	if m.detailFiles[0].Path != "/usr/lib/libfoo.so.1" {
		t.Fatalf("size-desc top = %q, want /usr/lib/libfoo.so.1", m.detailFiles[0].Path)
	}
	if m.detailFiles[len(m.detailFiles)-1].Path == "/usr/lib/libfoo.so.1" {
		t.Fatalf("size-desc should not put libfoo.so.1 last")
	}
}
