package tui

import (
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yoebuild/yoe/internal/source"
)

// sourceTarget distinguishes a unit-side dev clone from a module-side
// dev clone. The watcher tracks both with the same machinery; the TUI
// dispatches the resulting refresh message to the appropriate cache.
type sourceTarget int

const (
	targetUnit sourceTarget = iota
	targetModule
)

// sourceStateChangedMsg is delivered to the Bubble Tea program when
// the watcher detects a transition (e.g. `git commit` flipped a unit
// from dev → dev-mod). The model's Update handler invalidates the
// cached state so the next render re-reads it.
type sourceStateChangedMsg struct {
	target sourceTarget
	name   string
	state  source.State
}

// watchedSource is one tracked dev* clone. `dir` is the directory
// passed to source.DetectState; for units that's the unit's `src`
// directory, for modules it's the module's clone dir. `last` is the
// most recently observed state — the watcher only sends a message
// when DetectState returns a different value.
type watchedSource struct {
	target sourceTarget
	name   string
	dir    string
	last   source.State
}

// sourceWatcher polls a fixed set of dev* clones for state changes.
// It deliberately watches only items the TUI has explicitly armed
// (i.e. items currently in dev* state) — polling every unit in a
// large project every couple seconds would burn CPU for no benefit.
//
// Polling-only for now: a future revision can layer fsnotify on top
// for sub-second feedback, but a 2-second poll already produces the
// "save the file, see the column flip" experience the requirements
// call for.
type sourceWatcher struct {
	mu       sync.Mutex
	items    map[string]*watchedSource
	stop     chan struct{}
	interval time.Duration

	// send is the function the watcher calls to deliver messages to
	// the Bubble Tea program. In production this is
	// tuiProgram.Send; tests inject a buffer-collecting closure.
	send func(tea.Msg)
}

// newSourceWatcher returns an unarmed watcher with a sensible default
// poll interval. Caller must set `send` and call Start before any
// arm / disarm calls take effect.
func newSourceWatcher() *sourceWatcher {
	return &sourceWatcher{
		items:    make(map[string]*watchedSource),
		stop:     make(chan struct{}),
		interval: 2 * time.Second,
	}
}

// Start spawns the polling goroutine. Safe to call once. The send
// callback receives the message inside the goroutine — keep it
// non-blocking (tea.Program.Send is, internally).
func (w *sourceWatcher) Start(send func(tea.Msg)) {
	w.mu.Lock()
	w.send = send
	w.mu.Unlock()
	go w.run()
}

// Stop signals the polling goroutine to exit. Idempotent: a second
// Stop on the same watcher is a no-op (the channel is closed under
// the lock to avoid the close-twice panic).
func (w *sourceWatcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.stop:
		return // already stopped
	default:
		close(w.stop)
	}
}

func (w *sourceWatcher) run() {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.tick()
		}
	}
}

// tick walks the watched set once, reading DetectState for each item
// outside the lock so a slow git invocation can't block arm / disarm.
// State changes are written back to the cached `last` field so the
// next tick only fires on the next change, and the message is sent
// to the program.
func (w *sourceWatcher) tick() {
	w.mu.Lock()
	send := w.send
	snapshot := make([]watchedSource, 0, len(w.items))
	for _, it := range w.items {
		snapshot = append(snapshot, *it)
	}
	w.mu.Unlock()

	for _, it := range snapshot {
		// Pass `it.last` as the cached state so DetectState can
		// distinguish pin from clean dev (their git state is identical
		// when origin is set, which is now the default for pin too).
		cur, _ := source.DetectState(it.dir, it.last)
		if cur == it.last {
			continue
		}
		w.mu.Lock()
		if cached, ok := w.items[watcherKey(it.target, it.name)]; ok {
			cached.last = cur
		}
		w.mu.Unlock()
		if send != nil {
			send(sourceStateChangedMsg{target: it.target, name: it.name, state: cur})
		}
	}
}

// Arm starts watching a unit/module clone. Calling Arm on an already
// armed item updates its directory and last-known state — handy when
// the dev clone moves between scope dirs (e.g. machine switch).
func (w *sourceWatcher) Arm(target sourceTarget, name, dir string, current source.State) {
	w.mu.Lock()
	w.items[watcherKey(target, name)] = &watchedSource{
		target: target,
		name:   name,
		dir:    dir,
		last:   current,
	}
	w.mu.Unlock()
}

// Disarm stops watching a unit/module. No-op if not armed.
func (w *sourceWatcher) Disarm(target sourceTarget, name string) {
	w.mu.Lock()
	delete(w.items, watcherKey(target, name))
	w.mu.Unlock()
}

// IsArmed returns true if the named target is currently being polled.
// Used by tests; the TUI never has a reason to ask.
func (w *sourceWatcher) IsArmed(target sourceTarget, name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.items[watcherKey(target, name)]
	return ok
}

func watcherKey(target sourceTarget, name string) string {
	switch target {
	case targetUnit:
		return "u:" + name
	case targetModule:
		return "m:" + name
	}
	return name
}
