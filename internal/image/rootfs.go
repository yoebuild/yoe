package image

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yoebuild/yoe/internal/deb"
	"github.com/yoebuild/yoe/internal/repo"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// Assemble creates a root filesystem image from an image unit. The
// image's effective distro drives whether the rootfs is populated via
// apk (alpine path) or by extracting .deb data tars + running
// dpkg --configure -a (debian path).
func Assemble(unit *yoestar.Unit, proj *yoestar.Project, projectDir, outputDir, arch, machine string, w io.Writer) error {
	rootfs := filepath.Join(outputDir, "rootfs")
	os.RemoveAll(rootfs)
	if err := os.MkdirAll(rootfs, 0755); err != nil {
		return fmt.Errorf("creating rootfs dir: %w", err)
	}

	effectiveDistro, err := proj.EffectiveDistroForImage(unit.Name)
	if err != nil {
		return fmt.Errorf("Assemble: %w", err)
	}

	switch effectiveDistro {
	case "debian":
		if err := assembleDebian(rootfs, unit, proj, projectDir, arch, w); err != nil {
			return fmt.Errorf("assembling debian rootfs: %w", err)
		}
		toolchainImage, err := ResolveToolchainImage(proj, effectiveDistro, arch)
		if err != nil {
			return fmt.Errorf("toolchain for dpkg configure: %w", err)
		}
		if err := ConfigureDebianRootfs(rootfs, toolchainImage, projectDir, w); err != nil {
			return fmt.Errorf("configuring debian rootfs: %w", err)
		}
	default:
		// alpine path (current behavior)
		repoDir := repo.RepoDistroDir(proj, projectDir, effectiveDistro)
		allPackages := resolvePackageDeps(unit.Artifacts, proj, effectiveDistro)
		if err := installPackages(rootfs, repoDir, allPackages, w); err != nil {
			return fmt.Errorf("installing packages: %w", err)
		}
	}

	// Step 2: Apply configuration (hostname, timezone, locale, services)
	if err := applyConfig(rootfs, unit, w); err != nil {
		return fmt.Errorf("applying config: %w", err)
	}

	// Step 3: Apply overlays
	overlayDir := filepath.Join(projectDir, "overlays")
	if _, err := os.Stat(overlayDir); err == nil {
		if err := applyOverlays(rootfs, overlayDir, w); err != nil {
			return fmt.Errorf("applying overlays: %w", err)
		}
	}

	// Step 4: Generate disk image
	imgPath := filepath.Join(outputDir, unit.Name+".img")
	if err := generateImage(rootfs, imgPath, unit, proj, projectDir, arch, w); err != nil {
		return fmt.Errorf("generating image: %w", err)
	}

	fmt.Fprintf(w, "  → %s\n", imgPath)
	return nil
}

// assembleDebian populates rootfs by extracting each resolved deb from
// the project pool under repo/<project>/debian/pool/. Per R18, after
// all extracts the caller runs dpkg --configure -a under a binfmt
// sandbox to invoke maintainer scripts (U17 wires that step).
func assembleDebian(rootfs string, unit *yoestar.Unit, proj *yoestar.Project, projectDir, arch string, w io.Writer) error {
	debianPoolDir := filepath.Join(repo.RepoDistroDir(proj, projectDir, "debian"), "pool", "main")
	if _, err := os.Stat(debianPoolDir); err != nil {
		return fmt.Errorf("debian pool missing at %s — build the project's debian artifacts first", debianPoolDir)
	}
	allPackages := resolvePackageDeps(unit.Artifacts, proj, "debian")
	fmt.Fprintf(w, "  Installing %d debs into rootfs...\n", len(allPackages))
	for _, pkg := range allPackages {
		debFile, err := findDebInPool(debianPoolDir, pkg)
		if err != nil {
			return fmt.Errorf("locate %s.deb: %w", pkg, err)
		}
		if debFile == "" {
			fmt.Fprintf(w, "  (warning: no .deb for %s under pool; skipped)\n", pkg)
			continue
		}
		d, err := deb.ReadDeb(debFile)
		if err != nil {
			return fmt.Errorf("read %s: %w", debFile, err)
		}
		if err := deb.ExtractDataTar(d, rootfs); err != nil {
			d.Close()
			return fmt.Errorf("extract %s: %w", debFile, err)
		}
		d.Close()
	}
	return nil
}

// findDebInPool walks a Debian pool/<component> tree and returns the
// first .deb whose basename starts with "<pkg>_". Multiple matches
// (different versions) take the lexically last one — Debian filenames
// embed version strings that sort sensibly.
func findDebInPool(poolRoot, pkg string) (string, error) {
	var best string
	prefix := pkg + "_"
	err := filepath.WalkDir(poolRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".deb") {
			return nil
		}
		if !strings.HasPrefix(filepath.Base(p), prefix) {
			return nil
		}
		if best == "" || p > best {
			best = p
		}
		return nil
	})
	return best, err
}

// resolvePackageDeps expands a list of package names to include all transitive
// runtime dependencies. Build-time-only deps (unit.Deps) are excluded — they
// are needed to compile but not to run. The returned list is in dependency
// order (deps before dependents), with image-class units excluded.
// effectiveDistro restricts resolution to units visible to the
// consuming image — cross-distro siblings of the same name don't
// pollute the walk.
func resolvePackageDeps(packages []string, proj *yoestar.Project, effectiveDistro string) []string {
	seen := make(map[string]bool)
	var result []string

	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true

		if unit := proj.LookupUnit(effectiveDistro, name); unit != nil {
			// Skip image units — they aren't installable artifacts
			if unit.Class == "image" {
				return
			}
			for _, dep := range unit.RuntimeDeps {
				walk(dep)
			}
		}
		result = append(result, name)
	}

	for _, pkg := range packages {
		walk(pkg)
	}
	return result
}

// installPackages installs packages from an Alpine-layout repo
// (<repo>/<arch>/<pkg>.apk) into the rootfs.
func installPackages(rootfs, repoDir string, packages []string, w io.Writer) error {
	if len(packages) == 0 {
		fmt.Fprintln(w, "  (no packages to install)")
		return nil
	}

	fmt.Fprintf(w, "  Installing %d packages into rootfs...\n", len(packages))

	absRepo, _ := filepath.Abs(repoDir)

	for _, pkg := range packages {
		apkFile := findAPK(absRepo, pkg)
		if apkFile == "" {
			return fmt.Errorf("package %q not found in %s", pkg, absRepo)
		}

		fmt.Fprintf(w, "    %s\n", filepath.Base(apkFile))

		cmd := exec.Command("tar", "xzf", apkFile, "-C", rootfs, "--exclude=.PKGINFO")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("extracting %s: %s\n%s", pkg, err, out)
		}
	}

	return nil
}

// findAPK locates a package across every per-arch subdirectory under repoDir.
// Matches by package-name prefix (e.g., "busybox" matches
// "busybox-1.36.1-r0.apk"). Returns the first match in directory-name sort
// order, which gives noarch and arch dirs deterministic priority.
func findAPK(repoDir, pkgName string) string {
	archDirs, err := os.ReadDir(repoDir)
	if err != nil {
		return ""
	}
	for _, ad := range archDirs {
		if !ad.IsDir() {
			continue
		}
		archPath := filepath.Join(repoDir, ad.Name())
		entries, err := os.ReadDir(archPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), pkgName+"-") && strings.HasSuffix(e.Name(), ".apk") {
				return filepath.Join(archPath, e.Name())
			}
		}
	}
	return ""
}


func applyConfig(rootfs string, unit *yoestar.Unit, w io.Writer) error {
	if unit.Hostname != "" {
		fmt.Fprintf(w, "  Setting hostname: %s\n", unit.Hostname)
		os.MkdirAll(filepath.Join(rootfs, "etc"), 0755)
		os.WriteFile(filepath.Join(rootfs, "etc", "hostname"),
			[]byte(unit.Hostname+"\n"), 0644)
	}

	if unit.Timezone != "" {
		fmt.Fprintf(w, "  Setting timezone: %s\n", unit.Timezone)
		os.MkdirAll(filepath.Join(rootfs, "etc"), 0755)
		// Create symlink for localtime
		localtime := filepath.Join(rootfs, "etc", "localtime")
		os.Remove(localtime)
		os.Symlink("/usr/share/zoneinfo/"+unit.Timezone, localtime)
	}

	if unit.Locale != "" {
		os.MkdirAll(filepath.Join(rootfs, "etc"), 0755)
		os.WriteFile(filepath.Join(rootfs, "etc", "locale.conf"),
			[]byte("LANG="+unit.Locale+"\n"), 0644)
	}

	// Enable systemd services
	for _, svc := range unit.Services {
		fmt.Fprintf(w, "  Enabling service: %s\n", svc)
		svcDir := filepath.Join(rootfs, "etc", "systemd", "system", "multi-user.target.wants")
		os.MkdirAll(svcDir, 0755)
		link := filepath.Join(svcDir, svc+".service")
		target := "/usr/lib/systemd/system/" + svc + ".service"
		os.Symlink(target, link)
	}

	return nil
}

func applyOverlays(rootfs, overlayDir string, w io.Writer) error {
	return filepath.WalkDir(overlayDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == overlayDir {
			return nil
		}

		rel, _ := filepath.Rel(overlayDir, path)
		dst := filepath.Join(rootfs, rel)

		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}

		fmt.Fprintf(w, "  Overlay: %s\n", rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		os.MkdirAll(filepath.Dir(dst), 0755)
		return os.WriteFile(dst, data, 0644)
	})
}

func generateImage(rootfs, imgPath string, unit *yoestar.Unit, proj *yoestar.Project, projectDir, arch string, w io.Writer) error {
	fmt.Fprintln(w, "  Generating disk image...")
	return GenerateDiskImage(rootfs, imgPath, unit, proj, projectDir, arch, w)
}
