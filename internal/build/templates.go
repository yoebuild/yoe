package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// BuildTemplateContext builds the context map passed to Go templates, merging
// auto-populated fields (arch, machine, console, project, project_version)
// and unit identity fields (name, version, release) with the unit's Extra
// kwargs. Extra wins on key collision so explicit unit fields always override
// defaults.
//
// `version` is the unit's own version (e.g. base-files-1.0.0); use
// `project_version` for the project-wide value declared in PROJECT.star,
// which os-release.tmpl etc. surface to the booted system.
func BuildTemplateContext(u *yoestar.Unit, arch, machine, console, project, projectVersion string) map[string]any {
	m := map[string]any{
		"name":            u.Name,
		"version":         u.Version,
		"release":         int64(u.Release),
		"arch":            arch,
		"machine":         machine,
		"console":         console,
		"project":         project,
		"project_version": projectVersion,
	}
	for k, v := range u.Extra {
		m[k] = v
	}
	return m
}

// doInstallStep executes a single install-step against the filesystem. It is
// called from the executor's task step loop when step.Install != nil. The
// template data map and env are the same ones used for shell and fn steps in
// the enclosing task, so variable semantics stay consistent across step kinds.
func doInstallStep(u *yoestar.Unit, step *yoestar.InstallStep, data map[string]any, env map[string]string) error {
	srcPath, err := resolveTemplatePath(u, step)
	if err != nil {
		return fmt.Errorf("install %s: %w", step.Src, err)
	}
	destPath := expandEnv(step.Dest, env)

	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("install %s: reading %s: %w", step.Src, srcPath, err)
	}

	var out []byte
	switch step.Kind {
	case "file":
		out = raw
	case "template":
		tmpl, err := template.New(filepath.Base(srcPath)).
			Option("missingkey=error").
			Parse(string(raw))
		if err != nil {
			return fmt.Errorf("install_template %s: parsing: %w", srcPath, err)
		}
		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("install_template %s: rendering: %w", srcPath, err)
		}
		out = []byte(buf.String())
	default:
		return fmt.Errorf("install %s: unknown kind %q", step.Src, step.Kind)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("install %s: creating dest dir: %w", step.Src, err)
	}
	if err := os.WriteFile(destPath, out, os.FileMode(step.Mode)); err != nil {
		return fmt.Errorf("install %s: writing %s: %w", step.Src, destPath, err)
	}
	return nil
}

// installStepLabel returns a short human-readable label for an install step,
// used in the build log to identify which install action is executing.
func installStepLabel(s *yoestar.InstallStep) string {
	fn := "install_file"
	if s.Kind == "template" {
		fn = "install_template"
	}
	return fmt.Sprintf("%s: %s -> %s", fn, s.Src, s.Dest)
}

// resolveTemplatePath resolves the install step's source path against its
// captured base directory (set at the install_file()/install_template() call
// site — typically <dir(.star file)>/<basename(.star file) without extension>).
// Falls back to <DefinedIn>/<unit-name>/ for steps constructed directly in
// Go (tests, programmatic use). Rejects paths that escape the base directory
// (e.g. "../../etc/passwd").
func resolveTemplatePath(u *yoestar.Unit, step *yoestar.InstallStep) (string, error) {
	baseDir := step.BaseDir
	if baseDir == "" {
		baseDir = filepath.Join(u.DefinedIn, u.Name)
	}
	resolved := filepath.Join(baseDir, step.Src)
	rel, err := filepath.Rel(baseDir, resolved)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", step.Src, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes install base directory", step.Src)
	}
	return resolved, nil
}

// expandEnv expands $VAR and ${VAR} references using the provided build env.
// Unknown variables expand to the empty string — we deliberately do NOT fall
// back to the host process environment, because that would break
// reproducibility and content-addressed caching.
func expandEnv(s string, env map[string]string) string {
	return os.Expand(s, func(key string) string {
		return env[key]
	})
}
