package internal

import (
	"fmt"
	"os"
	"path/filepath"
)

func RunInit(projectDir string, machine string) error {
	if _, err := os.Stat(filepath.Join(projectDir, "PROJECT.star")); err == nil {
		return fmt.Errorf("project already exists at %s (PROJECT.star found)", projectDir)
	}

	dirs := []string{"machines", "units", "classes", "overlays"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(projectDir, dir), 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	name := filepath.Base(projectDir)
	defaultMachine := machine
	if defaultMachine == "" {
		defaultMachine = "qemu-x86_64"
	}

	projectContent := fmt.Sprintf(`project(
    name = %q,
    version = "0.1.0",
    # defaults.distro selects the effective distro for any image that
    # doesn't set its own ` + "`distro`" + ` field. The cascade is
    #   image.distro -> local.default_distro_override -> defaults.distro
    # If all three are empty the closure walk errors at evaluation
    # time, so every project must declare at least one. Today every
    # base image ships through Alpine; change to "debian" or set
    # distro on individual images to mix in the Debian backend.
    defaults = defaults(
        machine = %q,
        image = "base-image",
        distro = "alpine",
    ),
    sources = sources(go_proxy = "https://proxy.golang.org"),
    # modules listed in priority order: later entries shadow earlier ones,
    # so module-core wins over module-bsp and the Alpine/Jetson prebuilts.
    modules = [
        module("https://github.com/yoebuild/module-alpine.git",
               ref = "main"),
        module("https://github.com/yoebuild/module-debian.git",
               ref = "trixie"),
        module("https://github.com/yoebuild/module-jetson.git",
               ref = "main"),
        module("https://github.com/yoebuild/yoe.git",
               ref = "main",
               path = "modules/module-bsp"),
        module("https://github.com/yoebuild/yoe.git",
               ref = "main",
               path = "modules/module-core"),
    ],
    # Per-unit pins that override the default last-module-wins
    # shadowing, scoped per distro. The outer key is the consuming
    # image's effective distro, so an "alpine" pin has no effect on
    # a debian closure walk and vice versa — mixed-distro projects
    # don't need to drop pins to keep one backend resolving.
    prefer_modules = {
        "alpine": {
            # xz is built static-only in module-core, but kmod's
            # depmod needs shared liblzma.so.5 — Alpine ships it.
            "xz": "alpine.main",
            # module-core's source-built zstd ships libzstd.so.1 at
            # its own soversion; Alpine's nodejs links against
            # libzstd.so.1 from Alpine's zstd-libs. Pin zstd to Alpine
            # so the .so and CLI come from one source.
            "zstd": "alpine.main",
            # module-core's source-built util-linux bundles
            # libblkid/libmount/libuuid into one apk; Alpine splits
            # them. Mixing trips SONAME ownership conflicts. Pin to
            # Alpine for the coordinated split.
            "util-linux": "alpine.main",
            # module-core's curl bundles libcurl.so.4 at 8.11.1's
            # soversion; Alpine's libcurl is 8.14.1 and other Alpine
            # packages link against it. Pin curl to Alpine so curl
            # and libcurl come from one coordinated source.
            "curl": "alpine.main",
        },
        # Most module-core source units consumed by a Debian image build
        # in the glibc container and package as .deb automatically (the
        # build-twice model), so no per-unit pin is needed just to pick
        # the Debian package format. Pins below are for the cases where a
        # monolithic module-core unit collides with Debian's split
        # packaging — same as the Alpine pins above, the lib and its
        # consumers must come from one coordinated source.
        "debian": {
            # module-core's source-built util-linux is a minimal
            # busybox-replacement build (--disable-all-programs) that
            # omits getopt, which Debian maintainer scripts
            # (update-initramfs) require, and it collides with Debian's
            # split util-linux/libuuid1/libmount1 family. Pin to Debian
            # so util-linux and its split libs come from one source.
            "util-linux": "debian.main",
            # module-core's source-built zstd is one package bundling
            # libzstd.so.1 and the CLI. Debian ships libzstd1 as a
            # separate package pulled transitively by libsystemd0,
            # libapt-pkg, etc.; both then own
            # /usr/lib/<tuple>/libzstd.so.1 and dpkg refuses to unpack.
            # Pin to Debian so the lib and CLI come from one source.
            "zstd": "debian.main",
            # module-core's source-built kmod bundles libkmod.so.2 with
            # the kmod tools. Debian splits libkmod2 from kmod and pulls
            # libkmod2 transitively (systemd, udev); both own
            # /usr/lib/<tuple>/libkmod.so.2. Pin to Debian so libkmod2
            # and the kmod tools come from one source.
            "kmod": "debian.main",
        },
    },
)
`, name, defaultMachine)

	if err := os.WriteFile(filepath.Join(projectDir, "PROJECT.star"), []byte(projectContent), 0644); err != nil {
		return fmt.Errorf("writing PROJECT.star: %w", err)
	}

	// Create .gitignore
	gitignore := "/build\n/cache\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".gitignore"), []byte(gitignore), 0644); err != nil {
		return fmt.Errorf("writing .gitignore: %w", err)
	}

	if machine != "" {
		if err := createMachineFile(projectDir, machine); err != nil {
			return err
		}
	}

	fmt.Printf("Created Yoe project at %s\n", projectDir)
	return nil
}

func createMachineFile(projectDir, name string) error {
	var content string

	switch {
	case name == "qemu-x86_64" || name == "x86_64":
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "x86_64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyS0 root=/dev/vda1 rw"),
    qemu = qemu_config(machine = "q35", cpu = "host", memory = "4G", display = "none"),
)
`, name)
	case name == "qemu-arm64" || name == "aarch64":
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "arm64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyAMA0 root=/dev/vda1 rw"),
    qemu = qemu_config(machine = "virt", cpu = "host", memory = "4G", display = "none"),
)
`, name)
	case name == "qemu-riscv64" || name == "riscv64":
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "riscv64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyS0 root=/dev/vda1 rw"),
    qemu = qemu_config(machine = "virt", cpu = "host", memory = "4G", display = "none"),
)
`, name)
	default:
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "arm64",
    description = "",
)
`, name)
	}

	path := filepath.Join(projectDir, "machines", name+".star")
	return os.WriteFile(path, []byte(content), 0644)
}
