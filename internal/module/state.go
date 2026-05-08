package module

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/yoebuild/yoe/internal/source"
)

// stateFile holds the persisted source-mode state for a module clone.
// Lives at <moduleDir>/.yoe-state.json — sibling of the .git dir, so a
// `git clean -fdx` against the module clone leaves it alone.
const stateFile = ".yoe-state.json"

type modState struct {
	State string `json:"state"`
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
