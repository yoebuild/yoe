package starlark

import (
	"fmt"
	"iter"
	"strings"

	"go.starlark.net/starlark"
)

// Project represents an evaluated PROJECT.star.
type Project struct {
	Name     string
	Version  string
	Defaults Defaults
	Cache    CacheConfig
	Modules  []ModuleRef
	Machines map[string]*Machine

	// Root is the absolute directory holding PROJECT.star. The loader
	// already walks up to find it, so carrying it on the project spares
	// every consumer from repeating that walk — and from disagreeing
	// about the answer when run from a subdirectory, which is how
	// `yoe config show` came to read local.star from the wrong place.
	Root string

	// UnitsByModule is the primary unit storage: [moduleName][unitName]*Unit.
	// Every unit registers under its declaring module's canonical name; same-
	// named units from different modules (e.g. libcap2 from alpine.main and
	// debian.main) coexist as separate entries here. Module name is the
	// canonical name from module_info(name=...) for real modules or the
	// synthetic module's Name (alpine.main, debian.main) for feed-materialized
	// units. The project root's units register under the empty string "".
	UnitsByModule map[string]map[string]*Unit

	// DistroViews maps a consuming image's effective distro to a per-distro
	// resolved view: [distro][unitName]*Unit. Precomputed at load time after
	// all registrations and prefer_modules pins settle. Read-only after
	// construction; LookupUnit consults this for O(1) per-distro lookup.
	// A distro key is present whenever any unit is registered for it (either
	// tagged or materialized), even if the view is sparse.
	DistroViews map[string]map[string]*Unit

	// ProvidesViews maps a distro to the virtual names units tagged for
	// that distro declare: [distro][virtual]unitName. Built alongside
	// DistroViews and read by ResolveProvidesForDistro, which otherwise
	// scans every unit in every module per lookup.
	ProvidesViews map[string]map[string]string

	// DefaultDistro is the project-wide effective-distro fallback used by
	// image units that don't set their own `distro` field. The cascade
	// resolves an image's effective distro as:
	//     image.distro -> DefaultDistroOverride -> DefaultDistro -> error
	// See EffectiveDistroForImage.
	DefaultDistro string

	// DefaultDistroOverride is the per-developer default-distro override
	// from local.star (not committed). Wins over DefaultDistro but loses
	// to an explicit image-level `distro`. Empty means "no override".
	// Surfaced via the TUI Default Distro picker.
	DefaultDistroOverride string

	// PreferModules pins a unit name to a specific module per distro,
	// overriding the default module-priority resolution at closure-walk
	// time. Outer key is the consuming image's effective distro; inner
	// key is the unit name; value is the pinned module name. Example
	// PROJECT.star:
	//
	//	prefer_modules = {
	//	    "alpine": {"xz": "alpine.main", "zstd": "alpine.main"},
	//	    "debian": {"libssl3": "debian.main"},
	//	}
	//
	// A pin only fires for closures whose effective distro matches the
	// outer key — pinning xz to alpine.main has no effect on a debian
	// closure walk. Pins are consulted by lookupOrMaterialize before
	// the default catalog lookup, so a pinned synthetic module wins
	// even when a higher-priority real module would otherwise satisfy
	// the name. Empty value (or missing key) leaves resolution to the
	// default module-priority order.
	PreferModules map[string]map[string]string

	// Provides maps a virtual package name (e.g. "linux") to the concrete
	// unit name that provides it after override resolution. Populated by
	// the loader after all units and the active machine's kernel have been
	// evaluated. Use resolve.RuntimeClosure to walk runtime_deps through
	// this map.
	Provides map[string]string

	// SigningKey is the path to an RSA private key used to sign apks and
	// APKINDEX. If empty at build time, yoe auto-generates a key under
	// ~/.config/yoe/keys/<project-name>.rsa and uses that. The matching
	// public key (.rsa.pub next to it) is shipped on-device under
	// /etc/apk/keys/ so apk verifies signatures without --allow-untrusted.
	SigningKey string

	// ResolvedModules is the list of modules from PROJECT.star after the
	// loader has resolved each ModuleRef to a canonical name and on-disk
	// path. Populated in declaration order. Modules that failed to locate
	// (not synced yet) are still listed but with Available=false and an
	// empty Dir.
	ResolvedModules []ResolvedModule

	// Diagnostics records non-fatal events the loader observed — currently
	// cross-module unit shadowing and duplicate `provides` claims. Surfaced
	// in the TUI's Diagnostics tab so the user can see when an included
	// module's unit is being overridden by another module or the project
	// root, or when multiple units claim the same virtual.
	Diagnostics Diagnostics

	// SyntheticModules carries the entries registered via `alpine_feed(...)`
	// (and the eventual apt_feed) during MODULE.star evaluation. Each
	// is a deferred-materialization source for the resolver — the closure
	// walk (U7) calls Lookup on these when a referenced name isn't already
	// in proj.Units. Ordered by Priority ascending (lowest first); within
	// the priority ladder synthetic modules always rank below every real
	// module per R5.
	SyntheticModules []*SyntheticModule
}

// ResolvedModule is one entry from project.modules after the loader has
// located it on disk and read its canonical name from MODULE.star.
type ResolvedModule struct {
	Name      string // canonical name (from module_info(name=...) or basename)
	URL       string // declared URL
	Ref       string // declared git ref / branch / tag
	Path      string // sub-path within the repo (declared)
	Local     string // local override path (declared)
	Dir       string // dir holding MODULE.star (clone-root + Path); empty when not synced
	CloneDir  string // git clone root (.git lives here); equals Dir when Path is empty
	Available bool   // false when the module has not been synced
}

// Diagnostics summarizes loader events the user may want to inspect.
type Diagnostics struct {
	// Shadows lists units that lost a name collision to a higher-priority
	// module's unit of the same name. The active unit is Winner; the
	// shadowed unit is Loser. Same-priority collisions are hard errors and
	// never reach this list.
	Shadows []ShadowEvent

	// DuplicateProvides lists every virtual package name claimed by more
	// than one unit. Active is the unit currently routed to by
	// proj.Provides; Others lists the alternate claimants in declaration
	// order.
	DuplicateProvides []ProvidesEvent
}

// ShadowEvent records that Loser was registered with the same name as
// Winner from a different module. WinnerModule and LoserModule are the
// module names ("" for project root). DefinedIn fields point at the
// directory of the .star file that registered each unit.
type ShadowEvent struct {
	Unit         string
	WinnerModule string
	WinnerDir    string
	LoserModule  string
	LoserDir     string
}

// ProvidesEvent records that more than one unit claimed Virtual.
type ProvidesEvent struct {
	Virtual string
	Active  string   // the unit currently selected in proj.Provides
	Others  []string // alternate claimants, sorted
}

type Defaults struct {
	Machine string
	Image   string
}

type CacheConfig struct {
	Path string
}

type ModuleRef struct {
	URL   string
	Ref   string
	Path  string // subdirectory within the repo containing MODULE.star
	Local string // local path override (like Go's replace directive)
}

// ModuleInfo represents an evaluated MODULE.star from an external module.
type ModuleInfo struct {
	Name        string
	Description string
	Deps        []ModuleRef
}

// Machine represents an evaluated machine() call.
type Machine struct {
	Name        string
	Arch        string
	Description string
	Kernel      KernelConfig
	QEMU        *QEMUConfig // nil if not a QEMU machine
	Packages    []string    // distro-neutral board packages merged into every image for this machine
	// DistroPackages adds per-distro board packages on top of Packages, e.g.
	// {"alpine": ["syslinux"]} on qemu-x86_64 — the from-source syslinux is an
	// Alpine-only rootfs package (apt images get extlinux from the toolchain
	// container), so it must never force-resolve into an apt closure. The
	// machine analog of an image's distro_artifacts. Empty for machines whose
	// board support is identical across distros.
	DistroPackages map[string][]string
	Partitions     []Partition // default partition layout for images
}

type KernelConfig struct {
	Defconfig string
	Unit      string
	Cmdline   string
	Provides  string // virtual package name (e.g., "linux")
	// DistroUnit selects the kernel unit per distro, e.g.
	// {"alpine": "linux-qemu", "debian": "linux-image-amd64"}. Empty for
	// single-form machines (which set Unit). image() resolves the entry
	// for the build's effective distro, since the global provides table is
	// distro-blind. Mutually exclusive with Unit.
	DistroUnit map[string]string
}

// HasKernel reports whether the machine declares a kernel at all — either the
// flat single-Unit form or the per-distro DistroUnit map. Callers that gate
// "does this machine boot a kernel" (e.g. QEMU direct-kernel boot) must use
// this rather than `Unit != ""`, which is empty for distro_unit machines.
func (k KernelConfig) HasKernel() bool {
	return k.Unit != "" || len(k.DistroUnit) > 0
}

type QEMUConfig struct {
	Machine  string
	CPU      string
	Memory   string
	Firmware string
	Display  string
	Ports    []string // host:guest port mappings for user-mode networking
}

// EffectiveDistroForImage returns the effective distro for the named
// image after applying the cascade:
//
//	image.distro -> DefaultDistroOverride -> DefaultDistro -> error
//
// When multiple modules ship same-named images (alpine.dev-image AND
// debian.dev-image), variant selection consults the project's
// effective distro first: the variant whose Distro matches the
// project default (or override) wins, even when a higher-priority
// module ships a different-distro variant. Only when the project
// default has no visible variant does AnyUnit's module-priority
// pick decide — that's the "debian-only image inside an alpine
// project" case where the user explicitly named the cross-distro
// image.
//
// Error: the named image isn't an image-class unit, isn't in the
// project, or has no resolvable distro after the cascade.
func (p *Project) EffectiveDistroForImage(imageName string) (string, error) {
	if p == nil {
		return "", fmt.Errorf("EffectiveDistroForImage: nil project")
	}
	// Prefer the variant visible in the project's effective-distro
	// view so an alpine project picks alpine.dev-image even when
	// module-debian sits at higher module priority and also ships a
	// dev-image.
	projDistro := p.DefaultDistroOverride
	if projDistro == "" {
		projDistro = p.DefaultDistro
	}
	var u *Unit
	if projDistro != "" {
		u = p.LookupUnit(projDistro, imageName)
	}
	if u == nil {
		u = p.AnyUnit(imageName)
	}
	if u == nil {
		return "", fmt.Errorf("EffectiveDistroForImage: unit %q not found", imageName)
	}
	if u.Class != "image" {
		return "", fmt.Errorf("EffectiveDistroForImage: unit %q is not an image (class=%q)", imageName, u.Class)
	}
	if u.Distro != "" {
		return u.Distro, nil
	}
	if p.DefaultDistroOverride != "" {
		return p.DefaultDistroOverride, nil
	}
	if p.DefaultDistro != "" {
		return p.DefaultDistro, nil
	}
	return "", fmt.Errorf("image %q has no distro and project has no defaults.distro (set distro on the image or defaults.distro on project)", imageName)
}

// SetFlatUnits is a test helper that registers a name→*Unit map under
// the project-root module key in UnitsByModule. Production code never
// calls this — the loader builds UnitsByModule through per-module
// registration. Tests that hand-construct a Project use this to
// populate the catalog without going through the loader.
func (p *Project) SetFlatUnits(units map[string]*Unit) {
	if p == nil {
		return
	}
	if p.UnitsByModule == nil {
		p.UnitsByModule = make(map[string]map[string]*Unit, 1)
	}
	p.UnitsByModule[""] = units
}

// LookupUnit returns the unit visible to a closure walk in the given
// distro, or nil if no such unit exists. Consults the precomputed
// DistroViews built at load time — O(1) per lookup, no module-priority
// rescan, no synthetic walk. For callers without a distro context
// (TUI list-all, single-unit CLI helpers), pass the project's
// effective distro; for image-scoped consumers, pass
// EffectiveDistroForImage(imageName).
//
// When DistroViews is entirely empty — hand-constructed test
// fixtures skip the buildDistroViews pass that the loader runs —
// LookupUnit falls through to AnyUnit so unit tests keep working
// without per-test view wiring. Once a project has ANY DistroViews
// entry the fallback turns off, so "unknown distro" returns nil
// rather than silently matching some other distro's variant.
//
// DistroViews is a cache of resolveForDistro over UnitsByModule, so a
// miss on a KNOWN distro must consult the catalog rather than return
// nil: UnitsByModule shares the engine's live map, and any unit
// materialized after buildDistroViews ran (lazy feed units pulled in
// by a late closure walk) lands in the catalog but not the frozen
// view. Returning nil there silently drops a legitimate unit from the
// DAG, closure filters, and dependency trees. resolveForDistro applies
// the same prefer_modules pin + module-priority + R21a visibility rules
// buildDistroViews uses, so the fallthrough yields exactly what the
// view would have held — self-healing without matching a wrong-distro
// variant.
func (p *Project) LookupUnit(distro, name string) *Unit {
	if p == nil {
		return nil
	}
	if view, ok := p.DistroViews[distro]; ok {
		if u, ok := view[name]; ok {
			return u
		}
		return resolveForDistro(p, distro, name)
	}
	if len(p.DistroViews) > 0 {
		return nil
	}
	return p.AnyUnit(name)
}

// AnyUnit returns the unit registered under `name` from the highest-
// priority module that has one, or nil if no module has one. Used by
// callers that need to inspect a unit before knowing its consuming
// distro — most notably EffectiveDistroForImage, which reads the
// image's own Distro field to compute the very distro a LookupUnit
// call would need. Module priority (highest ModuleIndex wins;
// project root is strictly above any declared module) mirrors the
// shadow-resolution semantics of the legacy flat catalog so "the
// unit named X" is unambiguous when modules overlap.
func (p *Project) AnyUnit(name string) *Unit {
	if p == nil {
		return nil
	}
	var best *Unit
	for _, byName := range p.UnitsByModule {
		u, ok := byName[name]
		if !ok {
			continue
		}
		if best == nil || u.ModuleIndex > best.ModuleIndex {
			best = u
		}
	}
	return best
}

// AllUnits returns an iterator over every (name, *Unit) pair across
// every module in UnitsByModule. Used by callers that need to enumerate
// the catalog regardless of distro (TUI search, diagnostic dumps). The
// same name may yield multiple times when different modules registered
// it for different distros — consumers that want one entry per name
// should deduplicate themselves or use DistroViews[distro] iteration.
func (p *Project) AllUnits() iter.Seq2[string, *Unit] {
	return func(yield func(string, *Unit) bool) {
		if p == nil {
			return
		}
		for _, byName := range p.UnitsByModule {
			for name, u := range byName {
				if !yield(name, u) {
					return
				}
			}
		}
	}
}

// ResolveProvidesForDistro finds the unit that provides `virtual` whose
// Distro matches `effectiveDistro`. When effectiveDistro is non-empty,
// the fallback to the global proj.Provides table is FILTERED: a global
// provider whose Distro tag conflicts with the walk's distro is treated
// as a miss (untagged providers still satisfy the lookup since they're
// distro-neutral).
//
// Used by the closure walker to dispatch a virtual reference like
// Container="toolchain" to the concrete container unit matching the
// consuming image's effective distro — R9 dispatch via the provides
// table plus R21a's per-unit visibility filter.
//
// Returns "" when no provider exists for the effective distro. Empty
// effectiveDistro mirrors the global table (no distro filtering).
func (p *Project) ResolveProvidesForDistro(virtual, effectiveDistro string) string {
	if p == nil || virtual == "" {
		return ""
	}
	// First: a unit with the same name visible in the per-distro view
	// wins (R21a tagged-wins on direct name lookup). Untagged units
	// satisfy every distro; tagged units only their own.
	if effectiveDistro != "" {
		if u := p.LookupUnit(effectiveDistro, virtual); u != nil && (u.Distro == effectiveDistro || u.Distro == "") {
			if u.Distro == effectiveDistro {
				return u.Name
			}
		}
	}
	// Second: a unit tagged for effectiveDistro that declares this
	// virtual. ProvidesViews indexes exactly that; when it is not built
	// yet (during loading, before the views are computed) fall back to
	// the scan it replaces so the answer does not depend on timing.
	if effectiveDistro != "" {
		if view, ok := p.ProvidesViews[effectiveDistro]; ok {
			if name, ok := view[virtual]; ok {
				return name
			}
		} else if p.ProvidesViews == nil {
			for _, byName := range p.UnitsByModule {
				for _, u := range byName {
					if u.Distro != effectiveDistro {
						continue
					}
					for _, v := range u.Provides {
						if v == virtual {
							return u.Name
						}
					}
				}
			}
		}
	}
	// Fall back to the global mapping. Untagged providers serve every
	// distro; cross-distro tagged providers reach this fall-through
	// when the closure walker has no per-distro alternative yet.
	return p.Provides[virtual]
}

// EffectiveDistro returns the project's effective distro without an
// image scope: DefaultDistroOverride -> DefaultDistro -> error.
//
// Used by callers that operate on a single unit rather than an image
// (`yoe deploy <unit>`, TUI single-unit deploy) — they still need a
// distro to filter the runtime closure walk per R21a, but the unit
// itself doesn't carry a distro driver. The project's default is the
// best the caller can do.
func (p *Project) EffectiveDistro() (string, error) {
	if p == nil {
		return "", fmt.Errorf("EffectiveDistro: nil project")
	}
	if p.DefaultDistroOverride != "" {
		return p.DefaultDistroOverride, nil
	}
	if p.DefaultDistro != "" {
		return p.DefaultDistro, nil
	}
	return "", fmt.Errorf("project has no defaults.distro (set defaults.distro on project)")
}

// AptFamilyDistros is the set of distros that use the apt/dpkg/glibc
// backend (mmdebstrap rootfs assembly, .deb packaging, apt sources on
// device). Ubuntu rides Debian's machinery, so both share this family;
// only the feed identity, suite, and mirror differ. Anything not in this
// set (e.g. "alpine") takes the apk path.
var AptFamilyDistros = map[string]bool{
	"debian": true,
	"ubuntu": true,
}

// IsAptFamily reports whether distro uses the apt/dpkg backend.
func IsAptFamily(distro string) bool {
	return AptFamilyDistros[distro]
}

// CodenameForDistro returns the upstream release codename a given
// apt-family distro targets, read from the `codename` kwarg of the
// matching apt_feed(...) declarations — the source of the codename that
// the project repo emitter (dists/<codename>/), image assembly (the
// mmdebstrap target), and the on-device apt sources.list all stamp.
//
// Every apt feed in one distro's closure must agree on the codename: the
// toolchain container pins one release, and libc from a different release
// can't safely mix. This enforces that, and reports which feeds disagree.
//
// The guard deliberately keys on `codename`, not `suite`. A feed's suite
// is only an archive path segment: Debian serves one release at both
// dists/trixie and dists/stable, its security and updates pockets live at
// dists/trixie-security and dists/trixie-updates, and a vendor overlay
// repo may name its channel anything at all (Arduino's is "stable"). Only
// the codename identifies the release whose ABI the packages are built
// against, so only the codename can express this invariant.
//
// Errors when no feed for distro declares a codename: an apt-family image
// build needs one to bootstrap against.
func (p *Project) CodenameForDistro(distro string) (string, error) {
	if p == nil {
		return "", fmt.Errorf("CodenameForDistro: nil project")
	}
	codename, from := "", ""
	for _, sm := range p.SyntheticModules {
		if sm == nil || sm.Codename == "" || sm.Distro != distro {
			continue // not an apt feed for this distro
		}
		if codename == "" {
			codename, from = sm.Codename, sm.Name
		} else if sm.Codename != codename {
			return "", fmt.Errorf("project mixes %s releases: feed %q targets codename %q but feed %q targets %q; "+
				"every apt feed for one distro must target the same release (a feed for a different archive suite of the same "+
				"release, or a vendor overlay repo, still declares the release its packages are built for)",
				distro, from, codename, sm.Name, sm.Codename)
		}
	}
	if codename == "" {
		return "", fmt.Errorf("no apt_feed declares a codename for distro %q; an %s image build needs an apt_feed(distro=%q, codename=..., ...) in a module", distro, distro, distro)
	}
	return codename, nil
}

// BaseVersionForDistro returns the upstream release identifier the given
// distro's feed declares — the apt release codename (e.g. "trixie",
// "resolute") or, for Alpine, the feed branch (e.g. "v3.21"). Unlike
// CodenameForDistro this spans every backend and never errors: it returns
// "" when no feed for the distro declares a version, so callers
// (os-release stamping) can degrade gracefully rather than fail the build.
func (p *Project) BaseVersionForDistro(distro string) string {
	if p == nil {
		return ""
	}
	// Apt feeds agree on one codename per distro (CodenameForDistro
	// enforces it and fails the build first), so take the agreed value
	// rather than whichever feed happens to be first in registration
	// order — an overlay feed must not decide what lands in os-release.
	if codename, err := p.CodenameForDistro(distro); err == nil {
		return codename
	}
	for _, sm := range p.SyntheticModules {
		if sm == nil || sm.Distro != distro {
			continue
		}
		if sm.Release != "" {
			return sm.Release
		}
	}
	return ""
}

// QEMUPorts returns the port mappings from the machine's QEMU config, or nil.
func (m *Machine) QEMUPorts() []string {
	if m.QEMU == nil {
		return nil
	}
	return m.QEMU.Ports
}

// Unit represents an evaluated unit(), image(), etc. call.
type Unit struct {
	Name        string
	Version     string
	Release     int    // packaging revision (apk -r<N>), default 0
	Class       string // "unit", "image", etc.
	Scope       string // "arch" (default), "machine", or "noarch"
	Description string
	License     string

	// Distro carries two semantics by class:
	//   - On a class=="image" unit it drives toolchain selection,
	//     packaging format, repo subtree, and build-dir prefix.
	//   - On any other class it is a compatibility tag: the closure
	//     walker filters out units whose Distro is set and !=
	//     consuming-image effective distro. An empty Distro means
	//     "visible to every distro" (the common case).
	// The hash key includes the image's effective distro (driven by the
	// image), NOT the unit's compatibility tag — adding a tag to an
	// existing unit must stay cache-neutral.
	Distro string

	// Source
	Source string // URL or git repo
	SHA256 string
	// APKChecksum is Alpine's APKINDEX `C:` field — "Q1<base64-sha1>=".
	// Mutually exclusive with SHA256: a unit declares one or the other.
	// Used by module-alpine to verify against the hash Alpine itself
	// publishes, avoiding a per-package sha256 download at unit-gen time.
	APKChecksum string
	Tag         string
	Branch      string
	Patches     []string // patch files applied after source fetch, before build

	// PassthroughAPK names a .apk file fetched as the unit's source whose
	// contents should be republished verbatim instead of repackaged from
	// $DESTDIR. Used by alpine_pkg to ship upstream Alpine .apks (with
	// their PKGINFO and install scripts intact) under yoe's signing key.
	// The path is relative to the unit's srcDir.
	PassthroughAPK string

	// PassthroughDeb is the Debian sibling of PassthroughAPK: a .deb
	// file fetched as the unit's source whose bytes flow into the
	// project's pool/ verbatim (mirror-verbatim per R15). When set,
	// the build executor skips dpkg-deb --build and just copies the
	// upstream .deb into the project repo's pool/.
	PassthroughDeb string

	// Dependencies
	Deps        []string
	RuntimeDeps []string

	// Per-consumer-distro dep additions. Combined with Deps /
	// RuntimeDeps at closure walk and build time via
	// DepsForDistro / RuntimeDepsForDistro. Lets a single source
	// unit express that it needs python3 on alpine but python3.11
	// on debian, libzstd1 on debian but zstd on alpine, etc. —
	// without baking one distro's names in at registration time and
	// breaking closure walks for the other distro.
	DistroDeps        map[string][]string
	DistroRuntimeDeps map[string][]string

	// Build
	Container     string // default container for all tasks
	ContainerArch string // "target" or "host"
	Sandbox       bool   // use bwrap sandbox inside container (default false)
	Shell         string // shell for build commands: "sh" (default) or "bash"
	Tasks         []Task
	Provides      []string // virtual package names this unit satisfies (e.g., "linux", "ssh")
	Replaces      []string // package names whose files this unit may overwrite at install time
	Module        string   // module that registered this unit (empty = project root)
	ModuleIndex   int      // priority for shadowing/provides resolution: modules use 1..N (declaration order, last wins among modules), project root uses N+1 (highest)
	DefinedIn     string   // directory containing the .star file that defined this unit

	// Artifact metadata
	Services    []string
	Conffiles   []string
	Environment map[string]string
	CacheDirs   map[string]string // container_path:host_subdir cache mounts

	// Image-specific (class == "image")
	Artifacts         []string // artifacts to install in rootfs (full runtime closure, resolved by image())
	ArtifactsExplicit []string // user-specified artifacts before runtime-closure expansion; for UX (TUI tree, etc.)
	Exclude           []string
	Hostname          string
	Timezone          string
	Locale            string
	Partitions        []Partition

	// UnbuildableMachine names the selected machine when this image was
	// registered WITHOUT being resolved, because the machine's kernel has
	// no entry for the image's distro. Empty on every buildable unit.
	//
	// A machine's kernel `distro_unit` keys declare which distros the board
	// can boot; an image targeting any other distro is registered inert —
	// no artifacts, no deps, no tasks — so selecting a single-distro
	// machine doesn't break evaluation for unrelated images. The marker is
	// what distinguishes "not buildable here" from "genuinely empty": a
	// rootfs-only image with no packages is legal and must not be confused
	// with this. Never infer the state from an empty artifact list.
	//
	// Deliberately absent from UnitHash: a marked image's hash never keys a
	// build, so hashing the field buys nothing and cache neutrality holds
	// by omission.
	UnbuildableMachine string
	// MachineKernelDistros is the sorted distro set that machine's kernel
	// does support, carried so the refusal at the point of use can name it
	// instead of saying only "no".
	MachineKernelDistros []string

	// Arbitrary kwargs passed to unit() that don't map to a typed field.
	// Used for template context rendering and will be included in the unit
	// hash (see docs/superpowers/plans/2026-04-23-file-templates.md Task 6).
	Extra map[string]any
}

type Partition struct {
	Label    string
	Type     string // "vfat", "ext4", etc.
	Size     string // "64M", "fill", etc.
	Root     bool
	Contents []string
}

// Step is a single build action — shell command, Starlark function, or install step.
type Step struct {
	Command string            // shell command
	Fn      starlark.Callable // Starlark function
	Install *InstallStep      // install_file / install_template step
}

// InstallStep describes a file installation action produced by the
// Starlark install_file() / install_template() builtins and executed by
// the build executor.
//
// It is also the Starlark value those builtins return — see
// install_step.go for the starlark.Value methods. One struct serves both
// roles because they describe the same thing; two structs with identical
// fields and a copy between them only created somewhere for the two to
// drift apart.
//
// BaseDir is the absolute directory captured from the .star file
// containing the install_file() / install_template() call. The file to
// install lives at BaseDir/Src. Resolving relative to the call site —
// rather than to the unit() call site — lets helper functions package
// templates next to themselves and reuse them across many units.
type InstallStep struct {
	Kind    string // "file" or "template"
	Src     string // path relative to BaseDir
	Dest    string // env-expanded at execution time
	Mode    int
	BaseDir string // absolute directory; file to install lives at BaseDir/Src
}

// Task is a named build phase containing one or more steps.
type Task struct {
	Name      string
	Container string // optional container image override
	Steps     []Step
}

// Command represents a user-defined CLI command from commands/*.star.
type Command struct {
	Name        string
	Description string
	Args        []CommandArg
	RunFn       string // name of the run function in the .star file
	SourceFile  string // path to the .star file
}

// CommandArg describes a command-line argument for a custom command.
type CommandArg struct {
	Name     string
	Help     string
	Default  string
	Required bool
	IsBool   bool
}

var validArchitectures = map[string]bool{
	"arm64":   true,
	"riscv64": true,
	"x86_64":  true,
}

// validScopes enumerates the accepted values of a unit's `scope` kwarg.
// Both "" and "arch" mean per-architecture scoping — the default — so a
// unit can either leave scope out or spell the default it wants. Anything
// else is a typo, and a typo that silently fell through to arch scoping
// would build and publish to the wrong place.
var validScopes = map[string]bool{
	"":        true,
	"arch":    true,
	"machine": true,
	"noarch":  true,
}

// NotBuildable reports whether this unit was registered inert because
// the selected machine cannot boot its distro. Callers that gate on the
// state (build refusal, `yoe desc`, TUI status) must use this rather
// than checking for an empty artifact list — a rootfs-only image with no
// packages is a legitimate, buildable unit.
func (u *Unit) NotBuildable() bool {
	return u != nil && u.UnbuildableMachine != ""
}

// UnbuildableMachineClause names the machine and the distro set its
// kernel does support:
//
//	machine "arduino-uno-q", whose kernel supports: debian
//
// It carries no verb, so callers phrase their own ("not buildable on …",
// "Buildable: no — …"). Empty on a buildable unit.
func (u *Unit) UnbuildableMachineClause() string {
	if !u.NotBuildable() {
		return ""
	}
	supported := strings.Join(u.MachineKernelDistros, ", ")
	if supported == "" {
		supported = "(none)"
	}
	return fmt.Sprintf("machine %q, whose kernel supports: %s", u.UnbuildableMachine, supported)
}

// UnbuildableReason renders the whole sentence — image, distro, machine,
// supported set — for surfaces that refuse outright. This message carries
// the diagnostic burden that the evaluation-time crash used to, so it
// names every part of the mismatch. Empty on a buildable unit.
func (u *Unit) UnbuildableReason() string {
	if !u.NotBuildable() {
		return ""
	}
	return fmt.Sprintf("image %q (distro %q) is not buildable on %s",
		u.Name, u.Distro, u.UnbuildableMachineClause())
}

// DepsForDistro returns the build-time deps that apply to a closure
// walk in the given distro: unit.Deps (always) plus any
// distro_deps[distro] additions. Returns Deps unchanged when no
// per-distro entry exists for the target. Pass "" for distro-less
// callers (TUI list-all) — they get plain Deps and may miss
// per-distro additions, which is fine for a search-as-you-type
// surface.
func (u *Unit) DepsForDistro(distro string) []string {
	if u == nil {
		return nil
	}
	if distro == "" || len(u.DistroDeps) == 0 {
		return u.Deps
	}
	extra, ok := u.DistroDeps[distro]
	if !ok || len(extra) == 0 {
		return u.Deps
	}
	out := make([]string, 0, len(u.Deps)+len(extra))
	out = append(out, u.Deps...)
	out = append(out, extra...)
	return out
}

// RuntimeDepsForDistro is the runtime-deps sibling of
// DepsForDistro. Same merge rule: RuntimeDeps + DistroRuntimeDeps[distro].
func (u *Unit) RuntimeDepsForDistro(distro string) []string {
	if u == nil {
		return nil
	}
	if distro == "" || len(u.DistroRuntimeDeps) == 0 {
		return u.RuntimeDeps
	}
	extra, ok := u.DistroRuntimeDeps[distro]
	if !ok || len(extra) == 0 {
		return u.RuntimeDeps
	}
	out := make([]string, 0, len(u.RuntimeDeps)+len(extra))
	out = append(out, u.RuntimeDeps...)
	out = append(out, extra...)
	return out
}
