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
    cache = cache(path = "build/cache"),
    sources = sources(go_proxy = "https://proxy.golang.org"),
    modules = [
        module("https://github.com/yoebuild/yoe.git",
               ref = "main",
               path = "modules/module-core"),
        module("https://github.com/yoebuild/yoe.git",
               ref = "main",
               path = "modules/module-rpi"),
        module("https://github.com/yoebuild/module-alpine.git",
               ref = "main"),
        module("https://github.com/yoebuild/module-jetson.git",
               ref = "main"),
    ],
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
    qemu = qemu_config(machine = "q35", cpu = "host", memory = "1G", display = "none"),
)
`, name)
	case name == "qemu-arm64" || name == "aarch64":
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "arm64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyAMA0 root=/dev/vda1 rw"),
    qemu = qemu_config(machine = "virt", cpu = "host", memory = "1G", display = "none"),
)
`, name)
	case name == "qemu-riscv64" || name == "riscv64":
		content = fmt.Sprintf(`machine(
    name = %q,
    arch = "riscv64",
    kernel = kernel(unit = "linux-qemu", cmdline = "console=ttyS0 root=/dev/vda1 rw"),
    qemu = qemu_config(machine = "virt", cpu = "host", memory = "1G", display = "none"),
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
