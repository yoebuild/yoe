package source

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// Prepare sets up the build source directory for a unit:
// 1. Fetches source (from cache or network)
// 2. Extracts into build/<unit>/src/ as a git repo with yoe/pin tag
//    marking the pinned commit
// 3. Applies patches from the unit as git commits
//
// cachedSourceState is the unit's BuildMeta.SourceState from the previous
// build (empty for first-time builds). When it's in the dev* family, the
// existing src dir is the user's working tree — Prepare returns it
// untouched and logs a warning so .star edits surface explicitly. The
// "commits beyond upstream" fallback covers manually-committed src dirs
// from before the dev-mode toggle existed.
func Prepare(projectDir, scopeDir string, unit *yoestar.Unit, cachedSourceState string, w io.Writer) (string, error) {
	srcDir := filepath.Join(projectDir, "build", unit.Name+"."+scopeDir, "src")

	// If the cached state says dev* and the src dir still exists, the
	// user is actively editing it — never overwrite.
	if IsDev(State(cachedSourceState)) {
		if _, err := os.Stat(filepath.Join(srcDir, ".git")); err == nil {
			fmt.Fprintf(w, "Using local source for %s (state %s) — "+
				".star source/tag/patches changes won't apply until you switch back to pin\n",
				unit.Name, cachedSourceState)
			return srcDir, nil
		}
		// Cache is stale (user wiped the src dir). Fall through to a
		// fresh prep so the build can proceed instead of erroring.
	}

	// If the cached state says pin and the existing src dir is a valid
	// clone whose `upstream` git tag points at the unit's declared pin,
	// trust it. DevToPin produces exactly this state in place; without
	// this short-circuit, a cache-miss build (apk deleted, hash drift)
	// would tear down a freshly-reset dev → pin checkout via the
	// RemoveAll+clone path below — wasted work, and brittle when the
	// dir contains files RemoveAll can't handle.
	//
	// A .star tag bump invalidates the unit's hash so we wouldn't be
	// here at all on a cache hit; the check `upstream == unit.Tag`
	// also catches the cache-miss-with-stale-tag case (user bumped
	// tag, srcDir is still at the old commit) — that falls through to
	// clean+clone correctly.
	if cachedSourceState == string(StatePin) && unit.Tag != "" {
		if _, err := os.Stat(filepath.Join(srcDir, ".git")); err == nil {
			if upstreamMatchesTag(srcDir, unit.Tag) {
				fmt.Fprintf(w, "Using existing pin checkout for %s (upstream at %s)\n", unit.Name, unit.Tag)
				return srcDir, nil
			}
		}
	}

	// Legacy fallback: a src dir with commits beyond upstream pre-dates
	// the BuildMeta.SourceState mechanism. Treat it the same as a
	// dev-mod state so existing yoe-dev workflows keep working.
	if hasLocalCommits(srcDir) {
		fmt.Fprintf(w, "Using local source for %s (has commits beyond upstream)\n", unit.Name)
		return srcDir, nil
	}

	if unit.Source == "" {
		return "", fmt.Errorf("unit %q has no source", unit.Name)
	}

	// Fetch source into cache
	cachedPath, err := Fetch(unit, w)
	if err != nil {
		return "", err
	}

	// Remove old source dir and recreate. The chmod walk handles
	// read-only files Go's module cache leaves behind (mode 0400 on
	// every fetched module). Without it RemoveAll silently fails on
	// those entries, MkdirAll succeeds (dir already exists), and the
	// later git clone errors with "destination already exists and is
	// not an empty directory" — masking the real failure.
	makeRemovable(srcDir)
	if err := os.RemoveAll(srcDir); err != nil {
		return "", fmt.Errorf("removing existing %s: %w (file may be owned by a different user — try `sudo rm -rf %s`)", srcDir, err, srcDir)
	}
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return "", err
	}

	// Extract or checkout
	if isGitURL(unit.Source) {
		if err := checkoutGit(cachedPath, srcDir, unit); err != nil {
			return "", err
		}
		// Git source is already a repo — just tag current HEAD with
		// the yoe/pin marker.
		if err := tagUpstream(srcDir); err != nil {
			return "", err
		}
	} else {
		if err := prepareNonGitSource(cachedPath, srcDir, unit.Source); err != nil {
			return "", err
		}
		// Non-git sources need git init + commit + tag so the rest of
		// the pipeline (patches, tagUpstream invariants) can rely on a
		// real repo even when the upstream is a bare binary.
		if err := initGitRepo(srcDir); err != nil {
			return "", err
		}
	}

	// Apply patches
	if err := applyPatches(projectDir, srcDir, unit); err != nil {
		return "", err
	}

	return srcDir, nil
}

// upstreamMatchesTag reports whether the local yoe/pin git tag in
// srcDir resolves to the same commit as the unit's declared pin tag.
// Used to recognize a valid pin checkout (produced by DevToPin or by
// the freshly-cloned path below). Both refs must resolve cleanly; any
// git error returns false so the caller falls through to clean+clone.
func upstreamMatchesTag(srcDir, tag string) bool {
	upstream, err := exec.Command("git", "-C", srcDir, "rev-parse", PinTag+"^{commit}").Output()
	if err != nil {
		return false
	}
	pin, err := exec.Command("git", "-C", srcDir, "rev-parse", tag+"^{commit}").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(upstream)) == strings.TrimSpace(string(pin))
}

// makeRemovable walks dir and chmods every entry so a subsequent
// os.RemoveAll can delete it. The Go module cache fetches dependencies
// with mode 0400 (read-only) by design — RemoveAll fails silently on
// those without a prior chmod. Best-effort: any error from chmod is
// swallowed, since the user's only signal is whether RemoveAll later
// succeeds.
func makeRemovable(dir string) {
	if _, err := os.Stat(dir); err != nil {
		return
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// Make every entry user-rwx so unlinkat (used by RemoveAll)
		// works on read-only files inside writable parent dirs, and
		// can recurse into read-only directories.
		_ = os.Chmod(path, 0o700)
		return nil
	})
}

// hasLocalCommits checks if a source directory is a git repo with commits
// beyond the upstream tag.
func hasLocalCommits(srcDir string) bool {
	gitDir := filepath.Join(srcDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return false
	}

	cmd := exec.Command("git", "rev-list", "--count", PinTag+"..HEAD")
	cmd.Dir = srcDir
	out, err := cmd.Output()
	if err != nil {
		return false
	}

	count := strings.TrimSpace(string(out))
	return count != "0"
}

func checkoutGit(barePath, srcDir string, unit *yoestar.Unit) error {
	// Determine ref to checkout
	ref := "HEAD"
	if unit.Tag != "" {
		ref = unit.Tag
	} else if unit.Branch != "" {
		ref = unit.Branch
	}

	// Clone from bare cache into srcDir
	cmd := exec.Command("git", "clone", "--shared", barePath, srcDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %s\n%s", err, out)
	}

	// Checkout the right ref
	cmd = exec.Command("git", "checkout", ref)
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout %s: %s\n%s", ref, err, out)
	}

	return nil
}

// prepareNonGitSource decides how to materialise a fetched non-git source
// into srcDir. Picks an extractor by filename extension first, falling back
// to magic-byte sniffing for files with no/unknown extension. Bare files
// that aren't recognised archives are copied as-is (binary class case).
//
// sourceURL is the original upstream URL — used so that bare-copied files
// land in srcDir under their URL-derived basename (e.g. "musl-1.2.5-r11.apk")
// rather than the cache's URL-hash filename, since install tasks reference
// the file by name.
func prepareNonGitSource(cachedPath, destDir, sourceURL string) error {
	switch {
	case strings.HasSuffix(cachedPath, ".tar.gz"),
		strings.HasSuffix(cachedPath, ".tgz"),
		strings.HasSuffix(cachedPath, ".tar.xz"),
		strings.HasSuffix(cachedPath, ".tar.bz2"),
		strings.HasSuffix(cachedPath, ".tbz2"),
		strings.HasSuffix(cachedPath, ".tar"):
		return extractTarball(cachedPath, destDir)
	case strings.HasSuffix(cachedPath, ".zip"):
		return extractZip(cachedPath, destDir)
	case strings.HasSuffix(cachedPath, ".apk"):
		// .apk files are multi-stream gzipped tars (signature + control +
		// data). Bare-copy so the install task can extract with `tar -xzpf`
		// (GNU tar handles the multi-stream concatenation correctly);
		// passing it through extractTarball here would only see the
		// signature segment.
		return copyBareSource(cachedPath, destDir, urlBasename(sourceURL))
	}

	// No recognised extension — sniff the first 4 bytes.
	f, err := os.Open(cachedPath)
	if err != nil {
		return err
	}
	var magic [4]byte
	n, _ := io.ReadFull(f, magic[:])
	f.Close()

	if n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		return extractTarball(cachedPath, destDir)
	}
	if n == 4 && magic[0] == 0x50 && magic[1] == 0x4b && magic[2] == 0x03 && magic[3] == 0x04 {
		return extractZip(cachedPath, destDir)
	}
	return copyBareSource(cachedPath, destDir, urlBasename(sourceURL))
}

// urlBasename returns the filename portion of a URL — the segment after the
// final '/', with any query string stripped. Used so bare-copied sources
// land in srcDir under a stable name the unit's install task can reference.
func urlBasename(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		rawURL = rawURL[:i]
	}
	if i := strings.LastIndexByte(rawURL, '/'); i >= 0 {
		return rawURL[i+1:]
	}
	return rawURL
}

func extractTarball(tarPath, destDir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var reader io.Reader = f

	// Detect compression
	switch {
	case strings.HasSuffix(tarPath, ".gz") || strings.HasSuffix(tarPath, ".tgz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		reader = gz
	case strings.HasSuffix(tarPath, ".bz2"):
		reader = bzip2.NewReader(f)
	case strings.HasSuffix(tarPath, ".xz"):
		// Go stdlib doesn't have xz; shell out
		return extractWithTar(tarPath, destDir)
	}

	tr := tar.NewReader(reader)
	// Strip the first path component (most tarballs have a top-level dir)
	stripPrefix := ""

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tarball: %w", err)
		}

		// Detect top-level directory to strip
		if stripPrefix == "" {
			parts := strings.SplitN(hdr.Name, "/", 2)
			if len(parts) > 1 {
				stripPrefix = parts[0] + "/"
			}
		}

		name := strings.TrimPrefix(hdr.Name, stripPrefix)
		if name == "" || name == "." {
			continue
		}

		target := filepath.Join(destDir, name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, os.FileMode(hdr.Mode))
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(target), 0755)
			os.Symlink(hdr.Linkname, target)
		}
	}

	return nil
}

func extractZip(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip %s: %w", zipPath, err)
	}
	defer zr.Close()

	stripPrefix := ""
	for i, f := range zr.File {
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) < 2 {
			stripPrefix = ""
			break
		}
		first := parts[0] + "/"
		if i == 0 {
			stripPrefix = first
			continue
		}
		if first != stripPrefix {
			stripPrefix = ""
			break
		}
	}

	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, stripPrefix)
		if name == "" || name == "." {
			continue
		}
		target := filepath.Join(destDir, name)

		if f.FileInfo().IsDir() {
			// Ensure user can traverse and write the directory regardless of
			// the recorded mode (some zip tools write 0666 for dir entries).
			if err := os.MkdirAll(target, f.Mode()|0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return err
		}
		rc.Close()
		out.Close()
	}
	return nil
}

// copyBareSource copies a non-archive source file into srcDir under
// targetName (typically derived from the upstream URL so the install task
// can reference the file by its expected name) and marks it executable.
// Used for bare-binary downloads (kubectl, single-file releases) and for
// .apk files that need GNU tar to handle their multi-stream gzip layout.
//
// targetName falls back to the cache file's basename when empty, but in
// practice every bare-source caller passes the URL basename through.
func copyBareSource(filePath, destDir, targetName string) error {
	if targetName == "" {
		targetName = filepath.Base(filePath)
	}
	target := filepath.Join(destDir, targetName)

	in, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(target, 0o755)
}

func extractWithTar(tarPath, destDir string) error {
	cmd := exec.Command("tar", "xf", tarPath, "--strip-components=1", "-C", destDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar extract: %s\n%s", err, out)
	}
	return nil
}

// tagUpstream tags the current HEAD as the yoe-internal pin marker
// in an existing git repo. The tag name is namespaced (yoe/pin) so it
// can never collide with real upstream tags — important for
// DevPromoteToPin's "pick a tag pointing at HEAD" logic.
func tagUpstream(srcDir string) error {
	// Ensure we're on a branch (shallow clones may be detached)
	branchCmd := exec.Command("git", "checkout", "-b", "yoe-work")
	branchCmd.Dir = srcDir
	branchCmd.Run() // ignore error if branch already exists
	cmd := exec.Command("git", "tag", "-f", PinTag)
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git tag %s: %s\n%s", PinTag, err, out)
	}
	return nil
}

func initGitRepo(srcDir string) error {
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "yoe@yoe.local"},
		{"git", "config", "user.name", "yoe"},
		{"git", "add", "-A"},
		{"git", "commit", "-m", "upstream source"},
		{"git", "tag", PinTag},
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = srcDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	return nil
}

// ApplyPatches applies the unit's patches list as commits on top of the
// current HEAD. Exported so callers outside the build flow (e.g.
// internal/dev.go's reset-in-place pin transition) can re-apply patches
// without re-running the entire Prepare flow.
func ApplyPatches(projectDir, srcDir string, unit *yoestar.Unit) error {
	return applyPatches(projectDir, srcDir, unit)
}

func applyPatches(projectDir, srcDir string, unit *yoestar.Unit) error {
	// Patches resolve relative to the directory containing the unit's .star
	// file (unit.DefinedIn). This lets a module ship patches alongside the
	// unit that uses them. We fall back to projectDir only when DefinedIn
	// is unset (e.g., units constructed programmatically in tests).
	baseDir := unit.DefinedIn
	if baseDir == "" {
		baseDir = projectDir
	}
	for _, patchFile := range unit.Patches {
		patchPath := filepath.Join(baseDir, patchFile)
		if _, err := os.Stat(patchPath); os.IsNotExist(err) {
			return fmt.Errorf("patch file not found: %s", patchFile)
		}
		// git am/apply runs with cmd.Dir = srcDir, so a project-relative
		// path won't resolve. Convert to absolute before invoking git.
		if abs, err := filepath.Abs(patchPath); err == nil {
			patchPath = abs
		}

		// Apply with git am (preserves commit message from patch)
		cmd := exec.Command("git", "am", "--3way", patchPath)
		cmd.Dir = srcDir
		if out, err := cmd.CombinedOutput(); err != nil {
			// Fallback to git apply
			cmd = exec.Command("git", "apply", patchPath)
			cmd.Dir = srcDir
			if out2, err2 := cmd.CombinedOutput(); err2 != nil {
				return fmt.Errorf("applying %s: git am: %s\ngit apply: %s\n%s\n%s",
					patchFile, err, err2, out, out2)
			}
			// Commit the applied patch
			commitMsg := fmt.Sprintf("patch: %s", filepath.Base(patchFile))
			cmds := [][]string{
				{"git", "add", "-A"},
				{"git", "commit", "-m", commitMsg},
			}
			for _, args := range cmds {
				c := exec.Command(args[0], args[1:]...)
				c.Dir = srcDir
				if out, err := c.CombinedOutput(); err != nil {
					return fmt.Errorf("%s: %s\n%s", strings.Join(args, " "), err, out)
				}
			}
		}
	}

	return nil
}
