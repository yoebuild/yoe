package starlark

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// expandTransitiveDeps implements R3 + R4: walk each module's
// `module_info(deps=...)` recursively, syncing newly discovered deps,
// until the dep set is stable. Detects cycles and same-name conflicts.
//
// Returns the expanded, deduplicated module list in declaration order
// (project-declared modules first, then transitive deps in the order
// they were discovered). The original project-declared modules are
// always kept; transitive modules are added behind them.
//
// `sync` is the loader's existing module-sync callback. When nil
// (tests that pre-stage their modules), the walker still peeks the
// already-on-disk MODULE.star files for transitive deps.
func expandTransitiveDeps(initial []ModuleRef, projectRoot string,
	sync func([]ModuleRef, io.Writer) error, w io.Writer) ([]ModuleRef, error) {

	// Track every module ref we've already incorporated, keyed by
	// canonical identity. The project-level set is recorded first so
	// the project-wins-over-transitive rule has a base to check
	// against.
	seen := map[string]*moduleRecord{}
	depGraph := map[string][]string{}
	combined := append([]ModuleRef(nil), initial...)

	for i := range combined {
		id, err := canonicalIdentity(combined[i], projectRoot)
		if err != nil {
			return nil, err
		}
		seen[id] = &moduleRecord{ref: combined[i], projectLevel: true, id: id}
	}

	// Bound the fixpoint loop. A pathological MODULE.star that
	// expanded forever (somehow eluding the visited check) shouldn't
	// hang the loader.
	const maxRounds = 16

	for range maxRounds {
		// Sync whatever's on the list before peeking — the peek reads
		// from the synced on-disk MODULE.star files.
		if sync != nil {
			if err := sync(combined, w); err != nil {
				return nil, fmt.Errorf("syncing modules: %w", err)
			}
		}

		var newRefs []ModuleRef
		for i := range combined {
			ref := combined[i]
			modulePath, _, ok := locateModulePath(ref, projectRoot)
			if !ok {
				continue
			}
			info := peekModuleInfo(modulePath)
			if info == nil {
				continue
			}
			parentName := info.Name
			if parentName == "" {
				parentName = pathBasename(ref)
			}
			// Record the dep edges for cycle detection. Use the
			// declared name (from module_info(name=...)) so the graph
			// matches what the user sees in `module_info(deps=...)`.
			for _, dep := range info.Deps {
				depID, err := canonicalIdentity(dep, projectRoot)
				if err != nil {
					return nil, fmt.Errorf("module %s deps: %w", parentName, err)
				}
				depName := dep.peekName(projectRoot)
				depGraph[parentName] = appendUnique(depGraph[parentName], depName)

				existing, alreadySeen := seen[depID]
				if alreadySeen {
					continue
				}

				// Conflict check: a different ID may resolve to the
				// same canonical name (basename collision with a
				// different URL/path). Project-level wins; transitive
				// vs transitive errors.
				if conflict := findNameConflict(seen, dep, depName); conflict != nil {
					if conflict.projectLevel {
						// Project pins the canonical name to its own
						// ref; transitive declaration is silently
						// overridden.
						continue
					}
					return nil, fmt.Errorf(
						"module %q is declared by two transitive deps at incompatible refs: %s and %s (pin one explicitly at the project level)",
						depName, refDesc(conflict.ref), refDesc(dep))
				}

				seen[depID] = &moduleRecord{ref: dep, projectLevel: false, id: depID}
				newRefs = append(newRefs, dep)
				// Avoid relying on Go's address of loop variable —
				// `existing` may be a stale reference here.
				_ = existing
			}
		}
		if len(newRefs) == 0 {
			break
		}
		combined = append(combined, newRefs...)
	}

	if err := DetectCycles(depGraph); err != nil {
		return nil, err
	}
	return combined, nil
}

// moduleRecord carries the bookkeeping for one entry in the
// visited-set: the canonical ID, the ref that contributed it, and
// whether the project root declared it (project-level entries win
// against transitive collisions).
type moduleRecord struct {
	ref          ModuleRef
	projectLevel bool
	id           string
}

// canonicalIdentity returns a stable string identity for a ModuleRef so
// the visited set can dedup URL aliases and tag/SHA pairs that resolve
// to the same on-disk content.
//
//   - Local modules: filepath.EvalSymlinks(absolute path). Two relative
//     paths that resolve to the same directory dedup.
//   - Remote modules: (URL, Ref, Path) — same URL with the same ref/tag
//     points at the same commit at sync time. Different URLs with the
//     same basename still clone to the same cache dir; that natural
//     dedup happens below in the loader's locateModulePath path.
//
// True commit-SHA-based dedup (https vs git@ on the same repo) is left
// to follow-up work. The basename-dedup already covers the common case
// where two ModuleRefs differ only in URL form.
func canonicalIdentity(m ModuleRef, projectRoot string) (string, error) {
	if m.Local != "" {
		abs := m.Local
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(projectRoot, abs)
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			// Not yet synced or doesn't exist: fall back to the
			// declared path string. Dedup against another declaration
			// of the same path string still works.
			resolved = abs
		}
		if m.Path != "" {
			resolved = filepath.Join(resolved, m.Path)
		}
		return "local:" + resolved, nil
	}
	// Remote: (URL, Ref, Path) triple. Two ModuleRefs differing only
	// in cosmetic URL form (.git suffix) collapse via TrimSuffix.
	url := strings.TrimSuffix(m.URL, ".git")
	ref := m.Ref
	return "git:" + url + "@" + ref + "#" + m.Path, nil
}

// findNameConflict returns the existing record (if any) that already
// owns the canonical name `depName` under a different canonical ID.
// nil means no conflict.
func findNameConflict(seen map[string]*moduleRecord, candidate ModuleRef, depName string) *moduleRecord {
	if depName == "" {
		return nil
	}
	candidateName := depName
	for _, rec := range seen {
		existingName := pathBasename(rec.ref)
		// Prefer the declared module_info(name=...) when the record
		// has resolved one — but for the dep-resolution path the
		// declared name isn't available without peeking. Falling
		// back to basename matches what the loader uses elsewhere
		// (`locateModulePath`), so consumers see a coherent view.
		if existingName == candidateName {
			cID, _ := canonicalIdentity(rec.ref, "")
			candID, _ := canonicalIdentity(candidate, "")
			if cID != candID {
				return rec
			}
		}
	}
	return nil
}

// peekName returns the canonical name a ModuleRef will register
// under. For local modules it's the directory name (basename of
// path/local). For remote modules it's the URL basename minus `.git`.
//
// Used by the dep-graph and conflict-check paths so they're keyed on
// the same string the loader uses in its module name → priority
// assignment. projectRoot is unused here (kept for signature symmetry
// with canonicalIdentity).
func (m ModuleRef) peekName(_ string) string {
	return pathBasename(m)
}

// refDesc formats a ModuleRef for error messages: prefers URL + ref
// for remote modules and the local path for overrides.
func refDesc(m ModuleRef) string {
	if m.Local != "" {
		return fmt.Sprintf("local=%s", m.Local)
	}
	if m.Ref != "" {
		return fmt.Sprintf("%s @ %s", m.URL, m.Ref)
	}
	return m.URL
}

// appendUnique appends s to ss if it isn't already present. Used to
// build dep-graph adjacency lists without spurious duplicates.
func appendUnique(ss []string, s string) []string {
	if slices.Contains(ss, s) {
		return ss
	}
	return append(ss, s)
}
