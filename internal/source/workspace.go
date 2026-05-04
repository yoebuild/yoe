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
// 2. Extracts into build/<unit>/src/ as a git repo with "upstream" tag
// 3. Applies patches from the unit as git commits
//
// If the source directory already exists with local commits beyond upstream,
// it is left untouched (yoe dev workflow).
func Prepare(projectDir, scopeDir string, unit *yoestar.Unit, w io.Writer) (string, error) {
	srcDir := filepath.Join(projectDir, "build", unit.Name+"."+scopeDir, "src")

	// If source dir exists and has local commits, don't touch it (dev mode)
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

	// Remove old source dir and recreate
	os.RemoveAll(srcDir)
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return "", err
	}

	// Extract or checkout
	if isGitURL(unit.Source) {
		if err := checkoutGit(cachedPath, srcDir, unit); err != nil {
			return "", err
		}
		// Git source is already a repo — just tag current HEAD as upstream
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

// hasLocalCommits checks if a source directory is a git repo with commits
// beyond the upstream tag.
func hasLocalCommits(srcDir string) bool {
	gitDir := filepath.Join(srcDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return false
	}

	cmd := exec.Command("git", "rev-list", "--count", "upstream..HEAD")
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

// tagUpstream tags the current HEAD as "upstream" in an existing git repo.
// Used for git-sourced recipes where the checkout is already a git repo.
func tagUpstream(srcDir string) error {
	// Ensure we're on a branch (shallow clones may be detached)
	branchCmd := exec.Command("git", "checkout", "-b", "yoe-work")
	branchCmd.Dir = srcDir
	branchCmd.Run() // ignore error if branch already exists
	cmd := exec.Command("git", "tag", "-f", "upstream")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git tag upstream: %s\n%s", err, out)
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
		{"git", "tag", "upstream"},
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

func applyPatches(projectDir, srcDir string, unit *yoestar.Unit) error {
	for _, patchFile := range unit.Patches {
		patchPath := filepath.Join(projectDir, patchFile)
		if _, err := os.Stat(patchPath); os.IsNotExist(err) {
			return fmt.Errorf("patch file not found: %s", patchFile)
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
