package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// BuildMeta records metadata about a unit's build.
// Stored as build.json in the unit's build directory.
type BuildMeta struct {
	Status         string     `json:"status"`               // "building", "complete", "failed", "cancelled"
	Started        *time.Time `json:"started,omitempty"`    // when the build started
	Finished       *time.Time `json:"finished,omitempty"`   // when the build finished
	Duration       float64    `json:"duration_seconds"`     // wall-clock seconds
	DiskBytes      int64      `json:"disk_bytes"`           // total size of build directory
	InstalledBytes int64      `json:"installed_bytes"`      // size of destdir (what goes into the apk)
	Hash           string     `json:"hash"`                 // input hash
	Error          string     `json:"error,omitempty"`      // error message if failed
	// SourceState is the cached source state token for the unit's
	// build/<name>/src/ checkout — pin / dev / dev-mod / dev-dirty.
	// Advisory: callers fall through to source.DetectState on miss.
	// Empty for units not yet seen by the dev-mode machinery.
	SourceState string `json:"source_state,omitempty"`
}

const metaFile = "build.json"

// MetaPath returns the path to the build metadata file.
func MetaPath(buildDir string) string {
	return filepath.Join(buildDir, metaFile)
}

// WriteMeta writes build metadata to build.json.
func WriteMeta(buildDir string, meta *BuildMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(MetaPath(buildDir), data, 0644)
}

// ReadMeta reads build metadata from build.json.
// Returns nil if the file doesn't exist or can't be parsed.
func ReadMeta(buildDir string) *BuildMeta {
	data, err := os.ReadFile(MetaPath(buildDir))
	if err != nil {
		return nil
	}
	var meta BuildMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	return &meta
}

// DirSize returns the total size of all files in a directory tree.
func DirSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}
