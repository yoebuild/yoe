package build

import (
	"context"
	"fmt"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// fakeExecer records calls and returns a configurable result.
type fakeExecer struct {
	calls  []string
	result ExecResult
	err    error
}

func (f *fakeExecer) Run(_ context.Context, _ *SandboxConfig, command string, _ bool) (ExecResult, error) {
	f.calls = append(f.calls, command)
	return f.result, f.err
}

func (f *fakeExecer) RunHost(_ context.Context, command string, _ string) (ExecResult, error) {
	f.calls = append(f.calls, command)
	return f.result, f.err
}

func TestFnRun_Success(t *testing.T) {
	fake := &fakeExecer{result: ExecResult{ExitCode: 0}}
	cfg := &SandboxConfig{Arch: "x86_64"}
	thread := NewBuildThread(context.Background(), cfg, fake)

	predeclared := BuildPredeclared()
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "test.star", `
result = run("make -j4")
`, predeclared)
	if err != nil {
		t.Fatalf("ExecFile: %v", err)
	}

	if len(fake.calls) != 1 || fake.calls[0] != "make -j4" {
		t.Errorf("expected one call to 'make -j4', got %v", fake.calls)
	}

	r := globals["result"]
	if r == nil {
		t.Fatal("result not set")
	}

	exitCode, _ := r.(starlark.HasAttrs).Attr("exit_code")
	if exitCode.(starlark.Int).BigInt().Int64() != 0 {
		t.Errorf("expected exit_code 0, got %v", exitCode)
	}
}

func TestFnRun_FailureWithCheck(t *testing.T) {
	fake := &fakeExecer{
		result: ExecResult{ExitCode: 1, Stderr: "missing file"},
		err:    fmt.Errorf("command failed"),
	}
	cfg := &SandboxConfig{Arch: "x86_64"}
	thread := NewBuildThread(context.Background(), cfg, fake)

	predeclared := BuildPredeclared()
	_, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "test.star", `
run("false")
`, predeclared)
	if err == nil {
		t.Fatal("expected error from run() with check=True")
	}
}

func TestFnRun_FailureWithCheckFalse(t *testing.T) {
	fake := &fakeExecer{
		result: ExecResult{ExitCode: 1, Stderr: "missing file"},
		err:    fmt.Errorf("command failed"),
	}
	cfg := &SandboxConfig{Arch: "x86_64"}
	thread := NewBuildThread(context.Background(), cfg, fake)

	predeclared := BuildPredeclared()
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "test.star", `
result = run("false", check=False)
`, predeclared)
	if err != nil {
		t.Fatalf("expected no error with check=False, got: %v", err)
	}

	r := globals["result"]
	exitCode, _ := r.(starlark.HasAttrs).Attr("exit_code")
	if exitCode.(starlark.Int).BigInt().Int64() != 1 {
		t.Errorf("expected exit_code 1, got %v", exitCode)
	}
}

func TestFnRun_MultipleCalls(t *testing.T) {
	fake := &fakeExecer{result: ExecResult{ExitCode: 0}}
	cfg := &SandboxConfig{Arch: "arm64"}
	thread := NewBuildThread(context.Background(), cfg, fake)

	predeclared := BuildPredeclared()
	_, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "test.star", `
run("./configure")
run("make -j4")
run("make install DESTDIR=/build/destdir")
`, predeclared)
	if err != nil {
		t.Fatalf("ExecFile: %v", err)
	}

	if len(fake.calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(fake.calls))
	}
	expected := []string{"./configure", "make -j4", "make install DESTDIR=/build/destdir"}
	for i, cmd := range expected {
		if fake.calls[i] != cmd {
			t.Errorf("call %d: expected %q, got %q", i, cmd, fake.calls[i])
		}
	}
}
