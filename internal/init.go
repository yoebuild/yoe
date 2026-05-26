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
    defaults = defaults(machine = %q, image = "base-image"),
    sources = sources(go_proxy = "https://proxy.golang.org"),
    # modules listed in priority order: later entries shadow earlier ones,
    # so module-core wins over module-bsp and the Alpine/Jetson prebuilts.
    modules = [
        module("https://github.com/yoebuild/module-alpine.git",
               ref = "main"),
        module("https://github.com/yoebuild/module-jetson.git",
               ref = "main"),
        module("https://github.com/yoebuild/yoe.git",
               ref = "main",
               path = "modules/module-bsp"),
        module("https://github.com/yoebuild/yoe.git",
               ref = "main",
               path = "modules/module-core"),
    ],
    # Per-unit pins that override the default last-module-wins shadowing.
    # Use these when module-core's source-built version of a package is
    # broken or under-configured and Alpine's prebuilt is the right
    # answer (e.g. xz is built static-only in module-core, but kmod's
    # depmod needs the shared liblzma.so.5).
    prefer_modules = {
        "xz": "alpine.main",
        # module-core's source-built zstd ships libzstd.so.1 at its own
        # soversion. Alpine's nodejs links against libzstd.so.1 from
        # Alpine's zstd-libs, so mixing the two trips an apk conflict
        # (both packages own the same .so path with incompatible
        # versions). Pin zstd to Alpine so the .so and CLI come from one
        # source.
        "zstd": "alpine.main",
        # module-core's source-built util-linux is one monolithic apk that
        # bundles libblkid.so.1, libmount.so.1, and libuuid.so.1 (via
        # --enable-libblkid/--enable-libmount). Alpine splits those libs
        # into separate libblkid/libmount/libuuid packages, which get
        # pulled in transitively by eudev, glib, e2fsprogs, etc. as soon
        # as an image grows past the base set (e.g. jukebox-image's
        # navidrome closure). Both then claim ownership of the same
        # SONAMEs and apk refuses to install. Pin util-linux to Alpine so
        # util-linux and its split libs come from one coordinated source.
        "util-linux": "alpine.main",
        # module-core's source-built curl bundles its own libcurl.so.4 at
        # 8.11.1's soversion. Alpine ships libcurl as a separate package
        # at 8.14.1, and other Alpine packages (git, libcurl consumers)
        # link against it. Mixing both trips a so:libcurl.so.4 conflict
        # the moment an image pulls in git or any other libcurl consumer
        # from Alpine. Pin curl to Alpine so curl and libcurl come from
        # one coordinated source.
        "curl": "alpine.main",
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
