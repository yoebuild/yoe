package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/yoebuild/yoe/internal/feed"
	"github.com/yoebuild/yoe/internal/repo"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// startProjectFeed brings up a `yoe serve` equivalent for the TUI session
// unless one is already advertising this project on the LAN. Returns a
// teardown func and a status string for the UI ("reusing", "started",
// or "skipped: <reason>"). Failures here never block TUI startup.
func startProjectFeed(proj *yoestar.Project, projectDir string) (stop func(), status string) {
	stop = func() {}
	if proj == nil || proj.Name == "" {
		return stop, "skipped: project has no name"
	}

	results, _ := feed.BrowseMDNS(500 * time.Millisecond)
	for _, r := range results {
		if r.Project == proj.Name {
			return stop, fmt.Sprintf("reusing %s", r.URL())
		}
	}

	projRepoDir := repo.RepoDir(proj, projectDir)
	httpRoot := filepath.Dir(projRepoDir)
	archs, _ := repo.ArchDirs(projRepoDir)

	srv, err := feed.Start(feed.Config{
		RepoDir:  httpRoot,
		BindAddr: "0.0.0.0:8765",
		Project:  proj.Name,
		Archs:    archs,
		LogW:     io.Discard,
	})
	if err != nil {
		return stop, fmt.Sprintf("skipped: %v", err)
	}
	return func() { _ = srv.Stop() }, fmt.Sprintf("serving %s", srv.URL())
}
