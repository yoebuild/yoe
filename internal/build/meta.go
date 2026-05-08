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

	// SourceDescribe is `git describe --dirty --always` against the
	// unit's src dir at build time. Useful for the TUI's SOURCE line
	// and for the build log ("building openssl @ v3.4.1-3-g abcdef-dirty").
	// Empty for units never built in dev mode.
	SourceDescribe string `json:"source_describe,omitempty"`
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

// initBuildMeta returns a fresh "building" meta for buildDir, but
// preserves SourceState and SourceDescribe from any prior meta. The
// dev-mode toggle (internal/dev.go) writes those fields out-of-band;
// dropping them on every build start would erase the marker
// source.Prepare uses to skip its fetch/extract step — re-fetching
// over a dev-dirty src tree and destroying the user's work.
func initBuildMeta(buildDir, hash string, started time.Time) *BuildMeta {
	meta := &BuildMeta{
		Status:  "building",
		Started: &started,
		Hash:    hash,
	}
	if prev := ReadMeta(buildDir); prev != nil {
		meta.SourceState = prev.SourceState
		meta.SourceDescribe = prev.SourceDescribe
	}
	return meta
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
