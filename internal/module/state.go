package module

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/yoebuild/yoe/internal/source"
)

// stateFile holds the persisted source-mode state for a module clone.
// Lives at <moduleDir>/.yoe-state.json — sibling of the .git dir, so a
// `git clean -fdx` against the module clone leaves it alone.
const stateFile = ".yoe-state.json"
const syncInfoFile = ".yoe-sync.json"

type modState struct {
	State string `json:"state"`
}

type syncInfo struct {
	LastSync time.Time `json:"last_sync"`
}

// StatePath returns the on-disk path of the module's state file.
func StatePath(moduleDir string) string {
	return filepath.Join(moduleDir, stateFile)
}

// ReadState returns the cached source state for the module clone at
// moduleDir. Empty (StateEmpty) when the file doesn't exist or is
// unparseable — callers fall through to source.DetectState.
//
// Advisory only: a stale or missing state file is normal, not an error.
func ReadState(moduleDir string) source.State {
	data, err := os.ReadFile(StatePath(moduleDir))
	if err != nil {
		return source.StateEmpty
	}
	var s modState
	if err := json.Unmarshal(data, &s); err != nil {
		return source.StateEmpty
	}
	return source.State(s.State)
}

// WriteState persists state to the module's state file. Writing
// StateEmpty deletes the file rather than leaving an empty token
// behind — keeps the on-disk shape clean for users who toggle a
// module to dev and back.
func WriteState(moduleDir string, state source.State) error {
	if state == source.StateEmpty {
		err := os.Remove(StatePath(moduleDir))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(modState{State: string(state)}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(StatePath(moduleDir), data, 0o644)
}

// SyncInfoPath returns the on-disk path for persisted module sync metadata.
func SyncInfoPath(moduleDir string) string {
	return filepath.Join(moduleDir, syncInfoFile)
}

// WriteSyncInfo records the time yoe last completed a sync for moduleDir.
func WriteSyncInfo(moduleDir string, lastSync time.Time) error {
	data, err := json.MarshalIndent(syncInfo{LastSync: lastSync}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(SyncInfoPath(moduleDir), data, 0o644)
}

// LastSyncTime returns the last completed yoe sync time for moduleDir. Newer
// clones use .yoe-sync.json; older clones fall back to common git metadata
// mtimes so `yoe module info` can still show a useful best-effort value.
func LastSyncTime(moduleDir string) (time.Time, bool) {
	if data, err := os.ReadFile(SyncInfoPath(moduleDir)); err == nil {
		var info syncInfo
		if err := json.Unmarshal(data, &info); err == nil && !info.LastSync.IsZero() {
			return info.LastSync, true
		}
	}

	var latest time.Time
	for _, rel := range []string{
		filepath.Join(".git", "FETCH_HEAD"),
		filepath.Join(".git", "HEAD"),
		filepath.Join(".git", "index"),
	} {
		st, err := os.Stat(filepath.Join(moduleDir, rel))
		if err != nil {
			continue
		}
		if st.ModTime().After(latest) {
			latest = st.ModTime()
		}
	}
	if latest.IsZero() {
		return time.Time{}, false
	}
	return latest, true
}
