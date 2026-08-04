package starlark

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"

	"go.starlark.net/starlark"
)

// InstallStep is the Starlark value returned by install_file() and
// install_template(): an immutable, frozen, hashable description of a
// file-install action. Execution happens later, when the build executor
// reaches the step in a task's steps= list. The struct itself is
// declared in types.go alongside the rest of the unit model.
var _ starlark.Value = (*InstallStep)(nil)

func (s *InstallStep) String() string {
	fn := "install_file"
	if s.Kind == "template" {
		fn = "install_template"
	}
	return fmt.Sprintf("%s(%q, %q, mode=0o%o)", fn, s.Src, s.Dest, s.Mode)
}

func (*InstallStep) Type() string         { return "InstallStep" }
func (*InstallStep) Freeze()              {}
func (*InstallStep) Truth() starlark.Bool { return starlark.True }

func (s *InstallStep) Hash() (uint32, error) {
	h := fnv.New32a()
	h.Write([]byte(s.Kind))
	h.Write([]byte{0})
	h.Write([]byte(s.Src))
	h.Write([]byte{0})
	h.Write([]byte(s.Dest))
	h.Write([]byte{0})
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(s.Mode))
	h.Write(buf[:])
	return h.Sum32(), nil
}

// fnInstallFile implements the Starlark builtin install_file(src, dest, mode=0o644).
// Returns an InstallStepValue; has no side effects.
func fnInstallFile(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return buildInstallStep(thread, "install_file", "file", args, kwargs, 0o644)
}

// fnInstallTemplate is identical to fnInstallFile but with Kind="template".
func fnInstallTemplate(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return buildInstallStep(thread, "install_template", "template", args, kwargs, 0o644)
}

func buildInstallStep(thread *starlark.Thread, name, kind string, args starlark.Tuple, kwargs []starlark.Tuple, defMode int) (starlark.Value, error) {
	var src, dest starlark.String
	if err := starlark.UnpackPositionalArgs(name, args, nil, 2, &src, &dest); err != nil {
		return nil, err
	}
	mode := defMode
	for _, kv := range kwargs {
		k := string(kv[0].(starlark.String))
		if k != "mode" {
			return nil, fmt.Errorf("%s: unexpected kwarg %q", name, k)
		}
		n, ok := kv[1].(starlark.Int)
		if !ok {
			return nil, fmt.Errorf("%s: mode must be int, got %s", name, kv[1].Type())
		}
		v, ok := n.Int64()
		if !ok {
			return nil, fmt.Errorf("%s: mode out of range", name)
		}
		mode = int(v)
	}
	return &InstallStep{
		Kind:    kind,
		Src:     string(src),
		Dest:    string(dest),
		Mode:    mode,
		BaseDir: callerBaseDir(thread),
	}, nil
}

// callerBaseDir returns the directory that holds the install step's source
// files. It is derived from the .star file containing the install_file() /
// install_template() call: <dir(file)>/<basename(file) without extension>/.
//
// Example: a call inside units/base/base-files.star resolves files from
// units/base/base-files/. This means a helper function defined in one .star
// file can generate install steps for units registered from other .star
// files, and the templates always come from the helper's directory.
//
// Returns "" if the thread has no Starlark caller (e.g. when called directly
// from Go without a parent .star frame).
func callerBaseDir(thread *starlark.Thread) string {
	if thread == nil || thread.CallStackDepth() < 2 {
		return ""
	}
	caller := thread.CallFrame(1).Pos.Filename()
	if caller == "" || caller == "<builtin>" {
		return ""
	}
	dir := filepath.Dir(caller)
	base := filepath.Base(caller)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, stem)
}
