package starlark

import (
	"strings"
	"testing"
)

func TestEvalProject(t *testing.T) {
	src := `
project(
    name = "test-project",
    version = "0.1.0",
    defaults = defaults(machine = "qemu-arm64", image = "base-image"),
    cache = cache(path = "/var/cache/yoe/build"),
)
`
	eng := NewEngine()
	if err := eng.ExecString("PROJECT.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	proj := eng.Project()
	if proj == nil {
		t.Fatal("Project() returned nil")
	}
	if proj.Name != "test-project" {
		t.Errorf("Name = %q, want %q", proj.Name, "test-project")
	}
	if proj.Defaults.Machine != "qemu-arm64" {
		t.Errorf("Defaults.Machine = %q, want %q", proj.Defaults.Machine, "qemu-arm64")
	}
	if proj.Defaults.Image != "base-image" {
		t.Errorf("Defaults.Image = %q, want %q", proj.Defaults.Image, "base-image")
	}
	if proj.Cache.Path != "/var/cache/yoe/build" {
		t.Errorf("Cache.Path = %q, want %q", proj.Cache.Path, "/var/cache/yoe/build")
	}
}

func TestEvalMachine(t *testing.T) {
	src := `
machine(
    name = "beaglebone-black",
    arch = "arm64",
    description = "BeagleBone Black",
    kernel = kernel(
        repo = "https://github.com/beagleboard/linux.git",
        branch = "6.6",
        defconfig = "bb.org_defconfig",
        device_trees = ["am335x-boneblack.dtb"],
    ),
    uboot = uboot(
        repo = "https://github.com/beagleboard/u-boot.git",
        branch = "v2024.01",
        defconfig = "am335x_evm_defconfig",
    ),
)
`
	eng := NewEngine()
	if err := eng.ExecString("machines/bbb.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	machines := eng.Machines()
	m, ok := machines["beaglebone-black"]
	if !ok {
		t.Fatal("machine 'beaglebone-black' not found")
	}
	if m.Arch != "arm64" {
		t.Errorf("Arch = %q, want %q", m.Arch, "arm64")
	}
	if m.Kernel.Defconfig != "bb.org_defconfig" {
		t.Errorf("Kernel.Defconfig = %q, want %q", m.Kernel.Defconfig, "bb.org_defconfig")
	}
	if len(m.Kernel.DeviceTrees) != 1 {
		t.Errorf("Kernel.DeviceTrees = %v, want 1 entry", m.Kernel.DeviceTrees)
	}
	if m.Bootloader.Type != "u-boot" {
		t.Errorf("Bootloader.Type = %q, want %q", m.Bootloader.Type, "u-boot")
	}
	if m.Bootloader.Defconfig != "am335x_evm_defconfig" {
		t.Errorf("Bootloader.Defconfig = %q, want %q", m.Bootloader.Defconfig, "am335x_evm_defconfig")
	}
}

func TestEvalMachineQEMU(t *testing.T) {
	src := `
machine(
    name = "qemu-x86_64",
    arch = "x86_64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyS0"),
    qemu = qemu_config(machine = "q35", cpu = "host", memory = "1G", firmware = "ovmf"),
)
`
	eng := NewEngine()
	if err := eng.ExecString("machines/qemu.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	m := eng.Machines()["qemu-x86_64"]
	if m.QEMU == nil {
		t.Fatal("expected QEMU config, got nil")
	}
	if m.QEMU.Machine != "q35" {
		t.Errorf("QEMU.Machine = %q, want %q", m.QEMU.Machine, "q35")
	}
	if m.QEMU.Memory != "1G" {
		t.Errorf("QEMU.Memory = %q, want %q", m.QEMU.Memory, "1G")
	}
}

func TestEvalUnitDef(t *testing.T) {
	src := `
unit(
    name = "openssh",
    version = "9.6p1",
    source = "https://cdn.openbsd.org/pub/OpenBSD/OpenSSH/portable/openssh-9.6p1.tar.gz",
    sha256 = "abc123",
    deps = ["zlib", "openssl"],
    runtime_deps = ["zlib", "openssl"],
    tasks = [
        task("build", steps = [
            "./configure --prefix=$PREFIX",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
    services = ["sshd"],
    conffiles = ["/etc/ssh/sshd_config"],
)
`
	eng := NewEngine()
	if err := eng.ExecString("units/openssh.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	units := eng.Units()
	r, ok := units["openssh"]
	if !ok {
		t.Fatal("unit 'openssh' not found")
	}
	if r.Class != "unit" {
		t.Errorf("Class = %q, want %q", r.Class, "unit")
	}
	if r.Version != "9.6p1" {
		t.Errorf("Version = %q, want %q", r.Version, "9.6p1")
	}
	if len(r.Deps) != 2 {
		t.Errorf("Deps = %v, want 2 entries", r.Deps)
	}
	if len(r.Tasks) != 1 {
		t.Errorf("Tasks = %v, want 1 task", r.Tasks)
	} else if len(r.Tasks[0].Steps) != 3 {
		t.Errorf("Tasks[0].Steps = %v, want 3 steps", r.Tasks[0].Steps)
	}
	if len(r.Services) != 1 || r.Services[0] != "sshd" {
		t.Errorf("Services = %v, want [sshd]", r.Services)
	}
}

func TestEvalUnitWithTasks(t *testing.T) {
	src := `
unit(
    name = "zlib",
    version = "1.3.1",
    source = "https://zlib.net/zlib-1.3.1.tar.gz",
    tasks = [
        task("build", steps = [
            "./configure --prefix=$PREFIX",
            "make -j$NPROC",
            "make DESTDIR=$DESTDIR install",
        ]),
    ],
)
`
	eng := NewEngine()
	if err := eng.ExecString("units/zlib.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	r := eng.Units()["zlib"]
	if r.Class != "unit" {
		t.Errorf("Class = %q, want %q", r.Class, "unit")
	}
	if len(r.Tasks) != 1 {
		t.Fatalf("Tasks count = %d, want 1", len(r.Tasks))
	}
	if r.Tasks[0].Name != "build" {
		t.Errorf("Tasks[0].Name = %q, want %q", r.Tasks[0].Name, "build")
	}
	if len(r.Tasks[0].Steps) != 3 {
		t.Errorf("Tasks[0].Steps count = %d, want 3", len(r.Tasks[0].Steps))
	}
}

func TestEvalUnitWithTaskContainer(t *testing.T) {
	src := `
unit(
    name = "myapp",
    version = "1.2.3",
    source = "https://github.com/example/myapp.git",
    tag = "v1.2.3",
    tasks = [
        task("build", container = "golang:1.22",
            steps = ["go build -o $DESTDIR/usr/bin/myapp ./cmd/myapp"],
        ),
    ],
)
`
	eng := NewEngine()
	if err := eng.ExecString("units/myapp.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	r := eng.Units()["myapp"]
	if r.Class != "unit" {
		t.Errorf("Class = %q, want %q", r.Class, "unit")
	}
	if len(r.Tasks) != 1 {
		t.Fatalf("Tasks count = %d, want 1", len(r.Tasks))
	}
	if r.Tasks[0].Container != "golang:1.22" {
		t.Errorf("Tasks[0].Container = %q, want %q", r.Tasks[0].Container, "golang:1.22")
	}
	if len(r.Tasks[0].Steps) != 1 {
		t.Errorf("Tasks[0].Steps count = %d, want 1", len(r.Tasks[0].Steps))
	}
}

func TestEvalImageUnit(t *testing.T) {
	src := `
image(
    name = "base-image",
    version = "1.0.0",
    artifacts = ["openssh", "myapp"],
    hostname = "yoe",
    services = ["sshd"],
    partitions = [
        partition(label="boot", type="vfat", size="64M"),
        partition(label="rootfs", type="ext4", size="fill", root=True),
    ],
)
`
	eng := NewEngine()
	if err := eng.ExecString("units/base-image.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	units := eng.Units()
	r, ok := units["base-image"]
	if !ok {
		t.Fatal("unit 'base-image' not found")
	}
	if r.Class != "image" {
		t.Errorf("Class = %q, want %q", r.Class, "image")
	}
	if len(r.Artifacts) != 2 {
		t.Errorf("Packages = %v, want 2 entries", r.Artifacts)
	}
	if r.Hostname != "yoe" {
		t.Errorf("Hostname = %q, want %q", r.Hostname, "yoe")
	}
	if len(r.Partitions) != 2 {
		t.Errorf("Partitions = %v, want 2 entries", r.Partitions)
	}
	if !r.Partitions[1].Root {
		t.Error("Partitions[1].Root = false, want true")
	}
	if r.Partitions[0].Size != "64M" {
		t.Errorf("Partitions[0].Size = %q, want %q", r.Partitions[0].Size, "64M")
	}
}

func TestEvalInvalidArch(t *testing.T) {
	src := `machine(name = "bad", arch = "mips")`
	eng := NewEngine()
	err := eng.ExecString("machines/bad.star", src)
	if err == nil {
		t.Fatal("expected error for invalid arch, got nil")
	}
}

func TestEvalUnitWithPatches(t *testing.T) {
	src := `
unit(
    name = "busybox",
    version = "1.36.1",
    source = "https://busybox.net/downloads/busybox-1.36.1.tar.bz2",
    patches = [
        "patches/busybox/fix-ash-segfault.patch",
        "patches/busybox/add-custom-applet.patch",
    ],
    tasks = [
        task("build", steps = ["make -j$NPROC", "make DESTDIR=$DESTDIR install"]),
    ],
)
`
	eng := NewEngine()
	if err := eng.ExecString("units/busybox.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	r := eng.Units()["busybox"]
	if len(r.Patches) != 2 {
		t.Errorf("Patches = %v, want 2 entries", r.Patches)
	}
	if r.Patches[0] != "patches/busybox/fix-ash-segfault.patch" {
		t.Errorf("Patches[0] = %q, want fix-ash-segfault.patch", r.Patches[0])
	}
}

func TestEvalUnitNoTasks(t *testing.T) {
	// Units without tasks are valid — they may get tasks from a class in Starlark.
	src := `unit(name = "minimal", version = "1.0.0")`
	eng := NewEngine()
	if err := eng.ExecString("units/minimal.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	r := eng.Units()["minimal"]
	if len(r.Tasks) != 0 {
		t.Errorf("Tasks = %v, want empty", r.Tasks)
	}
}

func TestEvalProjectDuplicate(t *testing.T) {
	src := `
project(name = "first", version = "1.0.0")
project(name = "second", version = "2.0.0")
`
	eng := NewEngine()
	err := eng.ExecString("PROJECT.star", src)
	if err == nil {
		t.Fatal("expected error for duplicate project(), got nil")
	}
}

func TestEvalUnitCacheDirs(t *testing.T) {
	src := `
unit(
    name = "mygo",
    version = "1.0.0",
    cache_dirs = {"/go/cache": "go"},
)
`
	eng := NewEngine()
	if err := eng.ExecString("units/mygo.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	u := eng.Units()["mygo"]
	if u == nil {
		t.Fatal("unit 'mygo' not found")
	}
	if len(u.CacheDirs) != 1 {
		t.Fatalf("CacheDirs = %v, want 1 entry", u.CacheDirs)
	}
	if u.CacheDirs["/go/cache"] != "go" {
		t.Errorf("CacheDirs[/go/cache] = %q, want %q", u.CacheDirs["/go/cache"], "go")
	}
}

func TestEvalUnitDuplicate(t *testing.T) {
	src := `
unit(name = "foo", version = "1.0.0")
unit(name = "foo", version = "2.0.0")
`
	eng := NewEngine()
	err := eng.ExecString("units/foo.star", src)
	if err == nil {
		t.Fatal("expected error for duplicate unit name, got nil")
	}
	if !strings.Contains(err.Error(), "already defined") {
		t.Errorf("error = %q, want it to contain 'already defined'", err)
	}
}

func TestRegisterUnit_CapturesExtraKwargs(t *testing.T) {
	eng := NewEngine()
	src := `
unit(
    name = "my-app",
    version = "1.0.0",
    port = 8080,
    log_level = "info",
    enable_tls = True,
    workers = 4,
    tasks = [],
)
`
	if err := eng.ExecString("test.star", src); err != nil {
		t.Fatalf("ExecString: %v", err)
	}
	u := eng.Units()["my-app"]
	if u == nil {
		t.Fatal("unit not registered")
	}
	if u.Extra == nil {
		t.Fatal("Extra is nil")
	}
	if got := u.Extra["port"]; got != int64(8080) {
		t.Errorf("Extra[port] = %v (%T), want int64(8080)", got, got)
	}
	if got := u.Extra["log_level"]; got != "info" {
		t.Errorf("Extra[log_level] = %v, want \"info\"", got)
	}
	if got := u.Extra["enable_tls"]; got != true {
		t.Errorf("Extra[enable_tls] = %v, want true", got)
	}
	if got := u.Extra["workers"]; got != int64(4) {
		t.Errorf("Extra[workers] = %v (%T), want int64(4)", got, got)
	}
	// Known fields must NOT appear in Extra
	if _, ok := u.Extra["name"]; ok {
		t.Error("Extra[name] should not be set (name is a typed field)")
	}
	if _, ok := u.Extra["tasks"]; ok {
		t.Error("Extra[tasks] should not be set (tasks is a typed field)")
	}
}
