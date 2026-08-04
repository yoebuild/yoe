package tui

import (
	"fmt"
	"io"

	"github.com/yoebuild/yoe/internal/feed"
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

	if url := feed.DiscoverForProject(proj); url != "" {
		return stop, fmt.Sprintf("reusing %s", url)
	}

	cfg, err := feed.ConfigForProject(proj, projectDir)
	if err != nil {
		return stop, fmt.Sprintf("skipped: %v", err)
	}
	cfg.BindAddr = "0.0.0.0:8765"
	cfg.LogW = io.Discard

	srv, err := feed.Start(cfg)
	if err != nil {
		return stop, fmt.Sprintf("skipped: %v", err)
	}
	return func() { _ = srv.Stop() }, fmt.Sprintf("serving %s", srv.URL())
}
