package internal

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// hostArch returns the host machine architecture in Yoe format.
// HostArch returns the host machine's architecture (e.g., "x86_64", "arm64").
func HostArch() string {
	return hostArch()
}

func hostArch() string {
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		return "x86_64"
	}
	arch := strings.TrimSpace(string(out))
	switch arch {
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}


// Mount describes a bind mount for the container.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// ContainerRunConfig configures a single command execution inside the container.
type ContainerRunConfig struct {
	Shell       string            // shell to use: "sh" (default) or "bash"
	Ctx         context.Context   // optional; nil means background
	Arch        string            // target architecture (empty = host arch)
	Image       string            // Docker image tag (overrides default containerTag)
	Command     string            // shell command to run
	ProjectDir  string            // mounted as /project
	Mounts      []Mount           // additional bind mounts
	Env         map[string]string // environment variables
	Interactive bool              // attach TTY (-it)
	NoUser      bool              // run as root (for losetup/mount)
	Stdout      io.Writer         // override stdout (default: os.Stdout)
	Stderr      io.Writer         // override stderr (default: os.Stderr)
	Quiet       bool              // suppress the "[yoe] container: ..." trace line
}

// OnNotify is an optional callback for global notifications (e.g., TUI).
// Non-empty string = show notification, empty string = clear it.
var OnNotify func(string)

// DefaultContainerImage returns the Docker image tag for the toolchain-musl
// container unit using the host architecture. Used by callers outside the build
// executor (QEMU, shell, etc.) that need a container but don't have a per-unit
// resolution context.
func DefaultContainerImage(units map[string]*yoestar.Unit) string {
	arch := HostArch()
	if cu, ok := units["toolchain-musl"]; ok {
		return fmt.Sprintf("yoe/toolchain-musl:%s-%s", cu.Version, arch)
	}
	return fmt.Sprintf("yoe/toolchain-musl:15-%s", arch)
}

// RunInContainer executes a shell command inside a container.
// cfg.Image must be set to the Docker image tag to use.
func RunInContainer(cfg ContainerRunConfig) error {
	if cfg.Image == "" {
		return fmt.Errorf("no container image specified")
	}

	runtime, err := detectRuntime()
	if err != nil {
		return err
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		return err
	}

	// Assign a unique container name so we can stop it on cancellation.
	// docker run --rm + docker stop is safe: --rm removes the container
	// after it exits, and docker stop gracefully terminates it.
	name := fmt.Sprintf("yoe-%d", rand.Int())
	// Insert --name after "run" (args[0])
	args = append(args[:1], append([]string{"--name", name}, args[1:]...)...)

	args = append(args, cfg.Command)

	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	if !cfg.Quiet {
		fmt.Fprintf(stderr, "[yoe] container: %s\n", cfg.Command)
	}

	ctx := cfg.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// When the context is cancelled, stop the container explicitly.
	// exec.CommandContext only kills the docker CLI client, not the
	// container itself.
	done := make(chan struct{})
	if ctx != context.Background() {
		go func() {
			select {
			case <-ctx.Done():
				//nolint:gosec // best-effort cleanup
				exec.Command(runtime, "stop", "-t", "3", name).Run()
			case <-done:
			}
		}()
	}

	cmd := exec.Command(runtime, args...)
	cmd.Stdout = cfg.Stdout
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = stderr
	if cfg.Interactive {
		cmd.Stdin = os.Stdin
	}

	err = cmd.Run()
	close(done)

	// If the context was cancelled, the error is expected.
	if ctx.Err() != nil {
		return fmt.Errorf("build cancelled")
	}
	return err
}

// containerRunArgs builds the docker/podman run arguments (without the
// runtime binary name and without the trailing shell command string).
// The returned args end with "bash" "-c" so the caller only needs to
// append the command string.
func containerRunArgs(cfg ContainerRunConfig) ([]string, error) {
	arch := cfg.Arch
	if arch == "" {
		arch = hostArch()
	}

	args := []string{"run", "--rm", "--privileged"}

	// Add platform for cross-arch containers
	if arch != hostArch() {
		args = append(args, "--platform", "linux/"+arch)
	}

	if !cfg.NoUser {
		u, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("getting current user: %w", err)
		}
		args = append(args, "--user", fmt.Sprintf("%s:%s", u.Uid, u.Gid))
	}

	if cfg.ProjectDir != "" {
		args = append(args, "-v", cfg.ProjectDir+":/project")
	}

	for _, m := range cfg.Mounts {
		mount := m.Host + ":" + m.Container
		if m.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	for k, v := range cfg.Env {
		args = append(args, "-e", k+"="+v)
	}

	if cfg.Interactive {
		args = append(args, "-it")
	}

	args = append(args, "-w", "/project")
	args = append(args, cfg.Image)
	shell := cfg.Shell
	if shell == "" {
		shell = "sh"
	}
	args = append(args, shell, "-c")

	return args, nil
}


// checkBinfmt verifies that binfmt_misc is registered for the given arch.
// CheckBinfmt verifies that binfmt_misc is registered for the given
// architecture. Returns nil if registered or if arch matches the host.
func CheckBinfmt(arch string) error {
	if arch == "" || arch == hostArch() {
		return nil
	}
	return checkBinfmt(arch)
}

func checkBinfmt(arch string) error {
	binfmtName := binfmtArchName(arch)
	path := filepath.Join("/proc/sys/fs/binfmt_misc", binfmtName)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return fmt.Errorf(
		"binfmt_misc not registered for %s.\nRun 'yoe container binfmt' to enable cross-architecture builds",
		arch)
}

func binfmtArchName(arch string) string {
	switch arch {
	case "arm64":
		return "qemu-aarch64"
	case "riscv64":
		return "qemu-riscv64"
	default:
		return "qemu-" + arch
	}
}

// RegisterBinfmt registers QEMU user-mode emulation for foreign architectures
// using the tonistiigi/binfmt Docker image. Requires --privileged.
func RegisterBinfmt(w io.Writer) error {
	runtime, err := detectRuntime()
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "[yoe] registering binfmt_misc handlers...")
	cmd := exec.Command(runtime, "run", "--privileged", "--rm",
		"tonistiigi/binfmt", "--install", "arm64,riscv64")
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("registering binfmt: %w", err)
	}

	fmt.Fprintln(w, "Done. Registered: arm64, riscv64")
	return nil
}


func detectRuntime() (string, error) {
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err == nil {
			return rt, nil
		}
	}
	return "", fmt.Errorf("neither docker nor podman found — install one to use yoe")
}
