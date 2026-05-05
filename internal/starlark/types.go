package starlark

import "go.starlark.net/starlark"

// Project represents an evaluated PROJECT.star.
type Project struct {
	Name      string
	Version   string
	Defaults  Defaults
	Cache     CacheConfig
	Sources   SourcesConfig
	Modules   []ModuleRef
	Machines  map[string]*Machine
	Units     map[string]*Unit

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
}

type Defaults struct {
	Machine string
	Image   string
}

type CacheConfig struct {
	Path      string
	Remote    []CacheRemote
	Retention int // days
	Signing   string
}

type CacheRemote struct {
	Name     string
	Bucket   string
	Endpoint string
	Region   string
	Prefix   string
}

type SourcesConfig struct {
	GoProxy       string
	CargoRegistry string
	NpmRegistry   string
	PypiMirror    string
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
	Bootloader  BootloaderConfig
	QEMU        *QEMUConfig // nil if not a QEMU machine
	Packages    []string    // packages added to every image for this machine
	Partitions  []Partition // default partition layout for images
}

type KernelConfig struct {
	Repo        string
	Branch      string
	Tag         string
	Defconfig   string
	DeviceTrees []string
	Unit        string
	Cmdline     string
	Provides    string // virtual package name (e.g., "linux")
}

type BootloaderConfig struct {
	Type      string
	Repo      string
	Branch    string
	Defconfig string
}

type QEMUConfig struct {
	Machine  string
	CPU      string
	Memory   string
	Firmware string
	Display  string
	Ports    []string // host:guest port mappings for user-mode networking
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

	// Source
	Source  string // URL or git repo
	SHA256  string
	// APKChecksum is Alpine's APKINDEX `C:` field — "Q1<base64-sha1>=".
	// Mutually exclusive with SHA256: a unit declares one or the other.
	// Used by module-alpine to verify against the hash Alpine itself
	// publishes, avoiding a per-package sha256 download at unit-gen time.
	APKChecksum string
	Tag     string
	Branch  string
	Patches []string // patch files applied after source fetch, before build

	// Dependencies
	Deps        []string
	RuntimeDeps []string

	// Build
	Container     string // default container for all tasks
	ContainerArch string // "target" or "host"
	Sandbox       bool   // use bwrap sandbox inside container (default false)
	Shell         string // shell for build commands: "sh" (default) or "bash"
	Tasks     []Task
	Provides    []string // virtual package names this unit satisfies (e.g., "linux", "ssh")
	Replaces    []string // package names whose files this unit may overwrite at install time
	Module      string   // module that registered this unit (empty = project root)
	ModuleIndex int    // priority for shadowing/provides resolution: modules use 1..N (declaration order, last wins among modules), project root uses N+1 (highest)
	DefinedIn   string // directory containing the .star file that defined this unit

	// Artifact metadata
	Services    []string
	Conffiles   []string
	Environment map[string]string
	CacheDirs   map[string]string // container_path:host_subdir cache mounts

	// Image-specific (class == "image")
	Artifacts          []string // artifacts to install in rootfs (full runtime closure, resolved by image())
	ArtifactsExplicit  []string // user-specified artifacts before runtime-closure expansion; for UX (TUI tree, etc.)
	Exclude    []string
	Hostname   string
	Timezone   string
	Locale     string
	Partitions []Partition

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

// InstallStep describes a file installation action produced by the Starlark
// install_file() / install_template() builtins. Executed by the build executor.
//
// BaseDir is the absolute directory captured from the .star file containing
// the install_file() / install_template() call (see InstallStepValue). The
// file to install lives at BaseDir/Src. Resolving relative to the call site
// — rather than to the unit() call site — lets helper functions package
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
