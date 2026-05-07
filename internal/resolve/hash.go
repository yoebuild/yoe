package resolve

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// hashStringMap writes a deterministic representation of a string→string map
// into h: keys sorted, then "k=v" pairs joined by `,`. Used for fields like
// Environment where iteration order would otherwise destabilize the hash.
func hashStringMap(h io.Writer, label string, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	fmt.Fprintf(h, "%s:%s\n", label, strings.Join(parts, ","))
}

// UnitHash computes the content-addressed cache key for a unit.
// The hash includes:
//   - Unit fields (name, version, class, source, sha256, deps, build steps, etc.)
//   - Machine architecture and build flags
//   - Dependency hashes (transitive, via depHashes map)
//
// This ensures any change to a unit, its source, or any of its dependencies
// produces a new hash and triggers a rebuild.
func UnitHash(unit *yoestar.Unit, arch string, depHashes map[string]string) string {
	h := sha256.New()

	// Unit identity
	fmt.Fprintf(h, "name:%s\n", unit.Name)
	fmt.Fprintf(h, "version:%s\n", unit.Version)
	fmt.Fprintf(h, "release:%d\n", unit.Release)
	fmt.Fprintf(h, "class:%s\n", unit.Class)
	fmt.Fprintf(h, "scope:%s\n", unit.Scope)
	fmt.Fprintf(h, "arch:%s\n", arch)

	// Apk metadata that lands in PKGINFO — editing must invalidate cache.
	fmt.Fprintf(h, "description:%s\n", unit.Description)
	fmt.Fprintf(h, "license:%s\n", unit.License)

	// Source
	fmt.Fprintf(h, "source:%s\n", unit.Source)
	fmt.Fprintf(h, "sha256:%s\n", unit.SHA256)
	fmt.Fprintf(h, "apk_checksum:%s\n", unit.APKChecksum)
	fmt.Fprintf(h, "passthrough_apk:%s\n", unit.PassthroughAPK)
	fmt.Fprintf(h, "tag:%s\n", unit.Tag)
	fmt.Fprintf(h, "branch:%s\n", unit.Branch)
	fmt.Fprintf(h, "patches:%s\n", strings.Join(unit.Patches, "|"))

	// Tasks — hash command text, callable name, and install-step payload so
	// any change to a build step invalidates the cache.
	for _, t := range unit.Tasks {
		fmt.Fprintf(h, "task:%s:%s\n", t.Name, t.Container)
		for _, s := range t.Steps {
			if s.Command != "" {
				fmt.Fprintf(h, "step:cmd:%s\n", s.Command)
			}
			if s.Fn != nil {
				fmt.Fprintf(h, "step:fn:%s\n", s.Fn.Name())
			}
			if s.Install != nil {
				fmt.Fprintf(h, "step:install:%s:%s:%s:%o:%s\n",
					s.Install.Kind, s.Install.Src, s.Install.Dest,
					s.Install.Mode, s.Install.BaseDir)
				// Hash the source file content too — editing a template
				// or static file should invalidate the unit.
				if src := filepath.Join(s.Install.BaseDir, s.Install.Src); src != "" {
					if data, err := os.ReadFile(src); err == nil {
						sum := sha256.Sum256(data)
						fmt.Fprintf(h, "step:install:src-sha256:%x\n", sum[:])
					}
				}
			}
		}
	}
	fmt.Fprintf(h, "container:%s\n", unit.Container)
	fmt.Fprintf(h, "container_arch:%s\n", unit.ContainerArch)
	fmt.Fprintf(h, "sandbox:%v\n", unit.Sandbox)
	fmt.Fprintf(h, "shell:%s\n", unit.Shell)
	fmt.Fprintf(h, "provides:%s\n", strings.Join(unit.Provides, ","))
	fmt.Fprintf(h, "replaces:%s\n", strings.Join(unit.Replaces, ","))
	fmt.Fprintf(h, "runtime_deps:%s\n", strings.Join(unit.RuntimeDeps, ","))
	fmt.Fprintf(h, "services:%s\n", strings.Join(unit.Services, ","))
	fmt.Fprintf(h, "conffiles:%s\n", strings.Join(unit.Conffiles, ","))
	hashStringMap(h, "environment", unit.Environment)

	// Extra kwargs — JSON-encoded with sorted keys for stability.
	// Go's encoding/json sorts map keys when marshaling map[string]any,
	// so the result is deterministic regardless of iteration order.
	if len(unit.Extra) > 0 {
		if b, err := json.Marshal(sortedMap(unit.Extra)); err == nil {
			fmt.Fprintf(h, "extra:%s\n", b)
		}
	}

	// Unit files directory: <DefinedIn>/<unit-name>/ — hash file contents
	// so template and static file edits invalidate the cache.
	if unit.DefinedIn != "" {
		filesDir := filepath.Join(unit.DefinedIn, unit.Name)
		hashFilesDir(h, filesDir)
	}

	// Dependencies — include their hashes for transitivity
	deps := make([]string, len(unit.Deps))
	copy(deps, unit.Deps)
	sort.Strings(deps)
	for _, dep := range deps {
		if dh, ok := depHashes[dep]; ok {
			fmt.Fprintf(h, "dep:%s:%s\n", dep, dh)
		}
	}

	// Image-specific fields
	if unit.Class == "image" {
		pkgs := make([]string, len(unit.Artifacts))
		copy(pkgs, unit.Artifacts)
		sort.Strings(pkgs)
		fmt.Fprintf(h, "packages:%s\n", strings.Join(pkgs, ","))
		fmt.Fprintf(h, "exclude:%s\n", strings.Join(unit.Exclude, ","))
		fmt.Fprintf(h, "hostname:%s\n", unit.Hostname)
		fmt.Fprintf(h, "timezone:%s\n", unit.Timezone)
		fmt.Fprintf(h, "locale:%s\n", unit.Locale)
		for i, p := range unit.Partitions {
			fmt.Fprintf(h, "partition:%d:%s:%s:%s:%v:%s\n",
				i, p.Label, p.Type, p.Size, p.Root,
				strings.Join(p.Contents, ","))
		}
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// ComputeAllHashes computes hashes for all units in build order.
// Returns a map of unit name -> hash.
func ComputeAllHashes(dag *DAG, arch, machine string) (map[string]string, error) {
	order, err := dag.TopologicalSort()
	if err != nil {
		return nil, err
	}

	hashes := make(map[string]string, len(order))
	for _, name := range order {
		node := dag.Nodes[name]
		unitArch := arch
		// Machine-scoped units include the machine name in the hash
		// so the same unit built for different machines caches separately.
		if node.Unit.Scope == "machine" {
			unitArch = arch + ":" + machine
		}
		hashes[name] = UnitHash(node.Unit, unitArch, hashes)
	}

	return hashes, nil
}

// sortedMap walks a map[string]any recursively and returns a structurally
// identical value with nested map keys enumerated in a deterministic order.
// Go's encoding/json sorts top-level map keys already; this helper covers
// nested containers so the whole tree serializes deterministically.
func sortedMap(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = sortedMap(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = sortedMap(e)
		}
		return out
	default:
		return v
	}
}

// hashFilesDir writes a deterministic digest of the files under dir into h.
// Paths are sorted so iteration order doesn't change the hash. Missing
// directories are silently skipped — not every unit has a files directory.
func hashFilesDir(h io.Writer, dir string) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return
	}
	var paths []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	sort.Strings(paths)
	for _, p := range paths {
		rel, _ := filepath.Rel(dir, p)
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(content)
		fmt.Fprintf(h, "file:%s:%x\n", rel, sum[:])
	}
}
