package feed

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/yoebuild/yoe/internal/repo"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// discoverTimeout bounds the mDNS browse used to find an already-running
// feed. Long enough for a responder on the same LAN, short enough that
// starting the TUI or a deploy does not visibly stall on it.
const discoverTimeout = 500 * time.Millisecond

// ConfigForProject fills in the parts of a Config that come from the
// project: what to serve, under what name, and which architectures to
// advertise. The caller supplies the rest — bind address, whether to
// advertise over mDNS, where to log — since those differ between a
// long-running `yoe serve`, the feed the TUI brings up at startup, and
// the ephemeral one a deploy spins up.
//
// The served root is the parent of repo/<project>/, so URLs read
// <feed>/<project>/<distro>/<arch>/ and one server can carry every
// distro in the project.
//
// Architectures are read from the project's effective distro only. A
// project holding both an Alpine and a Debian tree advertises just the
// default one's arches; serving every distro's arches together needs a
// TXT-record shape that distinguishes them, which the current one does
// not.
func ConfigForProject(proj *yoestar.Project, projectDir string) (Config, error) {
	if proj == nil || proj.Name == "" {
		return Config{}, fmt.Errorf("feed: project has no name")
	}
	distro, err := proj.EffectiveDistro()
	if err != nil {
		return Config{}, fmt.Errorf("feed: resolve effective distro: %w", err)
	}
	archs, err := repo.ArchDirs(repo.RepoDistroDir(proj, projectDir, distro))
	if err != nil {
		return Config{}, fmt.Errorf("feed: list arch dirs: %w", err)
	}
	return Config{
		RepoDir: filepath.Dir(repo.RepoDir(proj, projectDir)),
		Project: proj.Name,
		Archs:   archs,
	}, nil
}

// DiscoverForProject returns the URL of a feed already advertising this
// project on the LAN, or "" when none answers.
//
// Callers use this to avoid starting a second server for a project that
// already has one: `yoe deploy` reuses it rather than binding an
// ephemeral port, and the TUI reuses it rather than advertising a
// competing instance. A browse failure is indistinguishable from nothing
// found, and both mean the same thing to every caller — start your own.
func DiscoverForProject(proj *yoestar.Project) string {
	if proj == nil || proj.Name == "" {
		return ""
	}
	results, _ := BrowseMDNS(discoverTimeout)
	for _, r := range results {
		if r.Project == proj.Name {
			return r.URL()
		}
	}
	return ""
}
