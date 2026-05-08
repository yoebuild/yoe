package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yoebuild/yoe/internal/source"
)

// runSh runs `cmd args...` in dir and fails the test on non-zero exit.
func runSh(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// makeDevClone produces a directory that source.DetectState classifies
// as StateDev: real .git, an `origin` remote, an `upstream` tag at
// HEAD, and a clean work tree.
func makeDevClone(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runSh(t, dir, "git", "init", "-q", "-b", "main")
	runSh(t, dir, "git", "config", "user.email", "test@test.com")
	runSh(t, dir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSh(t, dir, "git", "add", "-A")
	runSh(t, dir, "git", "commit", "-q", "-m", "initial")
	// origin must point at *something* — a self-loop file:// URL is
	// fine for DetectState's purposes (it only checks that origin is
	// configured, never actually fetches).
	runSh(t, dir, "git", "remote", "add", "origin", "file://"+dir)
	runSh(t, dir, "git", "tag", "-f", "upstream", "HEAD")
	return dir
}

func TestSourceWatcher_ArmAndDisarm(t *testing.T) {
	w := newSourceWatcher()
	w.Arm(targetUnit, "foo", "/some/dir", source.StateDev)
	if !w.IsArmed(targetUnit, "foo") {
		t.Fatal("expected foo to be armed")
	}
	w.Disarm(targetUnit, "foo")
	if w.IsArmed(targetUnit, "foo") {
		t.Fatal("expected foo to be disarmed")
	}
}

func TestSourceWatcher_DetectsDirty(t *testing.T) {
	dir := makeDevClone(t)
	// Speed: poll every 50ms so the test finishes quickly.
	w := newSourceWatcher()
	w.interval = 50 * time.Millisecond

	var mu sync.Mutex
	var got []sourceStateChangedMsg
	w.Start(func(msg tea.Msg) {
		if m, ok := msg.(sourceStateChangedMsg); ok {
			mu.Lock()
			got = append(got, m)
			mu.Unlock()
		}
	})
	defer w.Stop()

	w.Arm(targetUnit, "foo", dir, source.StateDev)

	// Dirty the work tree — should trigger a state-change event.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Allow up to 1 second for the polling tick to fire.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected a state-change event after dirtying the work tree")
	}
	last := got[len(got)-1]
	if last.target != targetUnit || last.name != "foo" {
		t.Errorf("event target/name = (%v, %s), want (unit, foo)", last.target, last.name)
	}
	if last.state != source.StateDevDirty {
		t.Errorf("event state = %q, want dev-dirty", last.state)
	}
}

func TestSourceWatcher_NoEventWhenStateUnchanged(t *testing.T) {
	dir := makeDevClone(t)
	w := newSourceWatcher()
	w.interval = 30 * time.Millisecond

	var mu sync.Mutex
	var got []sourceStateChangedMsg
	w.Start(func(msg tea.Msg) {
		if m, ok := msg.(sourceStateChangedMsg); ok {
			mu.Lock()
			got = append(got, m)
			mu.Unlock()
		}
	})
	defer w.Stop()

	w.Arm(targetUnit, "foo", dir, source.StateDev)

	// Let several poll ticks elapse; no work-tree change → no event.
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Errorf("expected zero events for an unchanged clone, got %d: %+v", len(got), got)
	}
}

func TestSourceWatcher_StopIsIdempotent(t *testing.T) {
	w := newSourceWatcher()
	w.Start(func(tea.Msg) {})
	w.Stop()
	// Second Stop must not panic on double-close.
	w.Stop()
}

func TestSourceWatcher_DisarmStopsEvents(t *testing.T) {
	dir := makeDevClone(t)
	w := newSourceWatcher()
	w.interval = 30 * time.Millisecond

	var mu sync.Mutex
	var got []sourceStateChangedMsg
	w.Start(func(msg tea.Msg) {
		if m, ok := msg.(sourceStateChangedMsg); ok {
			mu.Lock()
			got = append(got, m)
			mu.Unlock()
		}
	})
	defer w.Stop()

	w.Arm(targetUnit, "foo", dir, source.StateDev)
	w.Disarm(targetUnit, "foo")

	// Dirty the work tree — disarmed item should not fire.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Errorf("disarmed item should not fire, got %d events", len(got))
	}
}
