package internal

import (
	"fmt"
	"os/user"
	"testing"
)

func TestContainerRunArgs_Basic(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:    "echo hello",
		Image:      "yoe/toolchain-musl:15-x86_64",
		ProjectDir: "/home/user/myproject",
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	assertContains(t, args, "--rm")
	assertContains(t, args, "--privileged")

	u, _ := user.Current()
	assertContains(t, args, "--user")
	assertContains(t, args, fmt.Sprintf("%s:%s", u.Uid, u.Gid))

	assertContains(t, args, "-v")
	assertContains(t, args, "/home/user/myproject:/project")

	last3 := args[len(args)-3:]
	if last3[0] != "yoe/toolchain-musl:15-x86_64" {
		t.Errorf("expected image tag %q, got %q", "yoe/toolchain-musl:15-x86_64", last3[0])
	}
	if last3[1] != "sh" || last3[2] != "-c" {
		t.Errorf("expected 'sh -c', got %v", last3)
	}
}

func TestContainerRunArgs_Mounts(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:    "make",
		Image:      "yoe/toolchain-musl:15-x86_64",
		ProjectDir: "/project",
		Mounts: []Mount{
			{Host: "/tmp/src", Container: "/build/src", ReadOnly: false},
			{Host: "/tmp/sysroot", Container: "/build/sysroot", ReadOnly: true},
		},
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	assertContains(t, args, "/tmp/src:/build/src")
	assertContains(t, args, "/tmp/sysroot:/build/sysroot:ro")
}

func TestContainerRunArgs_Env(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:    "make",
		Image:      "yoe/toolchain-musl:15-x86_64",
		ProjectDir: "/project",
		Env:        map[string]string{"PREFIX": "/usr", "NPROC": "4"},
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	assertContains(t, args, "-e")
	found := false
	for _, a := range args {
		if a == "PREFIX=/usr" || a == "NPROC=4" {
			found = true
		}
	}
	if !found {
		t.Error("env vars not found in args")
	}
}

func TestContainerRunArgs_Interactive(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:     "qemu-system-x86_64",
		Image:       "yoe/toolchain-musl:15-x86_64",
		ProjectDir:  "/project",
		Interactive: true,
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	assertContains(t, args, "-it")
}

func TestContainerRunArgs_NoUser(t *testing.T) {
	cfg := ContainerRunConfig{
		Command:    "losetup /dev/loop0 image.img",
		Image:      "yoe/toolchain-musl:15-x86_64",
		ProjectDir: "/project",
		NoUser:     true,
	}

	args, err := containerRunArgs(cfg)
	if err != nil {
		t.Fatalf("containerRunArgs: %v", err)
	}

	for _, a := range args {
		if a == "--user" {
			t.Error("should not have --user when NoUser is true")
		}
	}
}

// A yoe-local image (yoe/ prefix) is built locally and never pushed, so it
// gets --pull=never to fail fast on an absent image. An external base image
// (e.g. golang:1.26 for the go build class) genuinely lives on a registry and
// must keep docker's default pull-if-missing policy, or a fresh runner can't
// build any unit whose container is an upstream image.
func TestContainerRunArgs_PullPolicy(t *testing.T) {
	local, err := containerRunArgs(ContainerRunConfig{
		Command: "true", Image: "yoe/toolchain-musl:15-x86_64", ProjectDir: "/p",
	})
	if err != nil {
		t.Fatalf("containerRunArgs (local): %v", err)
	}
	assertContains(t, local, "--pull=never")

	external, err := containerRunArgs(ContainerRunConfig{
		Command: "true", Image: "golang:1.26", ProjectDir: "/p",
	})
	if err != nil {
		t.Fatalf("containerRunArgs (external): %v", err)
	}
	assertNotContains(t, external, "--pull=never")
}

// The container platform is pinned explicitly on every run, even when the
// target arch equals the host. Docker keeps one image per tag, so a shared
// external tag (e.g. golang:1.26) can hold a foreign-arch image left by an
// earlier cross build; without --platform docker silently runs it and fails
// as "exec format error".
func TestContainerRunArgs_PlatformAlwaysSet(t *testing.T) {
	host, err := containerRunArgs(ContainerRunConfig{
		Command: "true", Image: "golang:1.26", ProjectDir: "/p", Arch: hostArch(),
	})
	if err != nil {
		t.Fatalf("containerRunArgs (host arch): %v", err)
	}
	assertContains(t, host, "--platform")
	assertContains(t, host, "linux/"+hostArch())

	cross, err := containerRunArgs(ContainerRunConfig{
		Command: "true", Image: "yoe/toolchain-musl:15-arm64", ProjectDir: "/p", Arch: "arm64",
	})
	if err != nil {
		t.Fatalf("containerRunArgs (cross arch): %v", err)
	}
	assertContains(t, cross, "--platform")
	assertContains(t, cross, "linux/arm64")
}

func assertContains(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args %v does not contain %q", args, want)
}

func assertNotContains(t *testing.T, args []string, unwanted string) {
	t.Helper()
	for _, a := range args {
		if a == unwanted {
			t.Errorf("args %v unexpectedly contains %q", args, unwanted)
			return
		}
	}
}
