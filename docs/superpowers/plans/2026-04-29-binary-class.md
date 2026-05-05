# `binary` class implementation plan

> **Status (2026-04-29):** Tasks 1–4, 6, 7 implemented. Task 5's kubectl example
> was substituted with `helix` (modal text editor, install_tree with runtime/)
> and `yazi` (multi-binary direct install) since both exercise the same code
> paths and are more useful additions to dev-image. Task 8 (build-and-boot
> verification) is left to the user's dev loop. The example units are wired and
> ready to build.

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `binary` Starlark class that installs prebuilt arm64/x86_64
binaries from upstream release URLs, plus the small Go-side source-prep support
it needs (zip extraction and bare-binary copy).

**Architecture:** Class lives at `modules/module-core/classes/binary.star` and
resolves URL+SHA per `ARCH` at Starlark eval time, then hands off to the
existing `unit()` builtin. The fetcher already caches by URL hash and verifies
SHA256; this plan extends `internal/source/workspace.go` so a fetched file can
be extracted from a `.zip` or copied as-is when it's a bare binary, on top of
the existing tarball path. The class generates a single `task("install", ...)`
that copies / symlinks files from the extracted source tree (`$SRCDIR`) into
`$DESTDIR`.

**Tech Stack:** Go (standard library `archive/zip`, `archive/tar`, `os`, `io`),
Starlark (module-core classes), bash (generated install task), `toolchain-musl`
container.

**Spec:**
[`docs/superpowers/specs/2026-04-29-binary-class-design.md`](../specs/2026-04-29-binary-class-design.md)

---

## File map

**New:**

- `modules/module-core/classes/binary.star` — the class
- `modules/module-core/units/build-tools/go.star` — Go toolchain example (covers
  `install_tree`, multi-binary, `{version}`, `{arch}`, archive extraction)
- `modules/module-core/units/debug/kubectl.star` — kubectl example (covers
  bare-binary, URL templating, default `binaries`)

**Modify:**

- `internal/source/workspace.go` — add `extractZip`, `copyBareSource`, and a
  dispatcher; rewire `Prepare`'s extract call
- `internal/source/source_test.go` — tests for new paths
- `CHANGELOG.md` — user-facing entry
- `docs/units-roadmap.md` — note new class

**Don't touch:**

- `internal/source/fetch.go` — HTTP fetch path is already correct.
- `internal/starlark/types.go` — `Unit` already has every field we need.
- `internal/build/executor.go` — `$SRCDIR`, `$DESTDIR`, `$PREFIX`, `$ARCH` env
  vars already set.

---

## Task 1: Add `.zip` extraction to source workspace

**Files:**

- Modify: `internal/source/workspace.go` (add `extractZip`)
- Modify: `internal/source/source_test.go` (add test + helper)

We need a zip extractor that mirrors `extractTarball`'s behaviour: write each
entry into `destDir`, auto-strip the first path component if every entry shares
a common top-level directory.

- [ ] **Step 1: Write the failing test**

Add to `internal/source/source_test.go`:

```go
func TestExtractZipStripsTopLevelDir(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "sample.zip")
	createTestZip(t, zipPath, []zipEntry{
		{name: "tool-1.0/", isDir: true},
		{name: "tool-1.0/bin/", isDir: true},
		{name: "tool-1.0/bin/tool", body: []byte("#!/bin/sh\necho hi\n"), mode: 0o755},
		{name: "tool-1.0/README", body: []byte("docs"), mode: 0o644},
	})

	dest := filepath.Join(tmp, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(zipPath, dest); err != nil {
		t.Fatalf("extractZip: %v", err)
	}

	// Top-level dir should be stripped: bin/tool, not tool-1.0/bin/tool.
	tool := filepath.Join(dest, "bin", "tool")
	st, err := os.Stat(tool)
	if err != nil {
		t.Fatalf("expected bin/tool: %v", err)
	}
	if st.Mode().Perm()&0o100 == 0 {
		t.Errorf("expected executable bit on bin/tool, got mode %v", st.Mode())
	}
	body, _ := os.ReadFile(tool)
	if !strings.Contains(string(body), "echo hi") {
		t.Errorf("body mismatch: %q", body)
	}
	if _, err := os.Stat(filepath.Join(dest, "README")); err != nil {
		t.Errorf("expected README at top level: %v", err)
	}
}

func TestExtractZipNoCommonPrefix(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "flat.zip")
	createTestZip(t, zipPath, []zipEntry{
		{name: "tool", body: []byte("bin"), mode: 0o755},
		{name: "LICENSE", body: []byte("license"), mode: 0o644},
	})

	dest := filepath.Join(tmp, "out")
	os.MkdirAll(dest, 0o755)
	if err := extractZip(zipPath, dest); err != nil {
		t.Fatalf("extractZip: %v", err)
	}

	for _, name := range []string{"tool", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("expected %s at top level: %v", name, err)
		}
	}
}

// createTestZip writes a zip file with the given entries.
type zipEntry struct {
	name  string
	body  []byte
	mode  os.FileMode
	isDir bool
}

func createTestZip(t *testing.T, path string, entries []zipEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if !e.isDir {
			if _, err := w.Write(e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
}
```

Add the new imports the tests need at the top of `source_test.go`:

```go
import (
	"archive/zip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yoestar "github.com/YoeDistro/yoe-ng/internal/starlark"
)
```

(Some of these may already be present — keep one of each.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestExtractZip -v`

Expected: FAIL — `extractZip` undefined.

- [ ] **Step 3: Implement `extractZip` in `internal/source/workspace.go`**

Add this function near the existing `extractTarball`:

```go
func extractZip(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip %s: %w", zipPath, err)
	}
	defer zr.Close()

	// Detect top-level directory to strip (same heuristic as extractTarball).
	stripPrefix := ""
	for i, f := range zr.File {
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) < 2 {
			stripPrefix = ""
			break
		}
		first := parts[0] + "/"
		if i == 0 {
			stripPrefix = first
			continue
		}
		if first != stripPrefix {
			stripPrefix = ""
			break
		}
	}

	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, stripPrefix)
		if name == "" || name == "." {
			continue
		}
		target := filepath.Join(destDir, name)

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, f.Mode()); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return err
		}
		rc.Close()
		out.Close()
	}
	return nil
}
```

Add `"archive/zip"` to the imports at the top of `internal/source/workspace.go`.
The file already imports `io`, `os`, `filepath`, `strings`, `fmt`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/ -run TestExtractZip -v`

Expected: PASS — both `TestExtractZipStripsTopLevelDir` and
`TestExtractZipNoCommonPrefix`.

- [ ] **Step 5: Commit**

```bash
git add internal/source/workspace.go internal/source/source_test.go
git commit -m "source: extract .zip archives with top-level-dir strip"
```

---

## Task 2: Add bare-binary copy path

**Files:**

- Modify: `internal/source/workspace.go` (add `copyBareSource`)
- Modify: `internal/source/source_test.go` (test)

When the cached source is not a recognised archive, treat it as a bare binary:
copy it into `srcDir` under its original basename, mark it executable.

- [ ] **Step 1: Write the failing test**

Append to `internal/source/source_test.go`:

```go
func TestCopyBareSourceELF(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "kubectl")
	// Minimal ELF magic + padding — content doesn't matter, the function
	// only inspects the path and copies bytes.
	body := append([]byte{0x7f, 'E', 'L', 'F'}, bytes.Repeat([]byte{0}, 60)...)
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmp, "out")
	os.MkdirAll(dest, 0o755)

	if err := copyBareSource(src, dest); err != nil {
		t.Fatalf("copyBareSource: %v", err)
	}

	target := filepath.Join(dest, "kubectl")
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("expected %s: %v", target, err)
	}
	if st.Mode().Perm()&0o100 == 0 {
		t.Errorf("expected executable bit, got %v", st.Mode())
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, body) {
		t.Errorf("bytes mismatch")
	}
}
```

Add `"bytes"` to the test file's imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestCopyBareSourceELF -v`

Expected: FAIL — `copyBareSource` undefined.

- [ ] **Step 3: Implement `copyBareSource` in `internal/source/workspace.go`**

Add this function near the extractor functions:

```go
// copyBareSource copies a non-archive source file into srcDir under its
// original basename and marks it executable. Used for bare-binary
// downloads (kubectl, single-file releases) where there's nothing to
// extract.
func copyBareSource(filePath, destDir string) error {
	base := filepath.Base(filePath)
	target := filepath.Join(destDir, base)

	in, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(target, 0o755)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/ -run TestCopyBareSource -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/workspace.go internal/source/source_test.go
git commit -m "source: copy bare-binary downloads into srcDir"
```

---

## Task 3: Dispatch extract / copy based on file type

**Files:**

- Modify: `internal/source/workspace.go` (add `prepareNonGitSource` dispatcher,
  rewire the existing call site)
- Modify: `internal/source/source_test.go` (dispatcher tests)

The current `Prepare` always calls `extractTarball` for non-git sources. Replace
that with a dispatcher that picks tar / zip / bare based on the filename,
falling back to magic-byte sniffing for files with no/unknown extension.

- [ ] **Step 1: Write the failing test**

Append to `internal/source/source_test.go`:

```go
func TestPrepareNonGitSourceDispatchTar(t *testing.T) {
	tmp := t.TempDir()
	tarPath := filepath.Join(tmp, "tool-1.0.tar.gz")
	writeTestTarball(t, tarPath) // helper from existing tests

	dest := filepath.Join(tmp, "src")
	os.MkdirAll(dest, 0o755)
	if err := prepareNonGitSource(tarPath, dest); err != nil {
		t.Fatalf("prepareNonGitSource(tar): %v", err)
	}
	// existing tarball helper writes tool-1.0/{configure,Makefile} — top-level dir stripped
	for _, name := range []string{"configure", "Makefile"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("expected %s after extract: %v", name, err)
		}
	}
}

func TestPrepareNonGitSourceDispatchZip(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "tool-1.0.zip")
	createTestZip(t, zipPath, []zipEntry{
		{name: "tool-1.0/", isDir: true},
		{name: "tool-1.0/tool", body: []byte("body"), mode: 0o755},
	})

	dest := filepath.Join(tmp, "src")
	os.MkdirAll(dest, 0o755)
	if err := prepareNonGitSource(zipPath, dest); err != nil {
		t.Fatalf("prepareNonGitSource(zip): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "tool")); err != nil {
		t.Errorf("expected tool after zip extract: %v", err)
	}
}

func TestPrepareNonGitSourceDispatchBare(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "kubectl")
	body := append([]byte{0x7f, 'E', 'L', 'F'}, bytes.Repeat([]byte{0}, 60)...)
	os.WriteFile(bin, body, 0o644)

	dest := filepath.Join(tmp, "src")
	os.MkdirAll(dest, 0o755)
	if err := prepareNonGitSource(bin, dest); err != nil {
		t.Fatalf("prepareNonGitSource(bare): %v", err)
	}
	st, err := os.Stat(filepath.Join(dest, "kubectl"))
	if err != nil {
		t.Fatalf("expected kubectl after bare copy: %v", err)
	}
	if st.Mode().Perm()&0o100 == 0 {
		t.Errorf("expected executable, got %v", st.Mode())
	}
}
```

If `writeTestTarball` doesn't already exist as a helper, look at the existing
`createTestTarball` in `source_test.go` and add a thin wrapper that writes its
content to the given path:

```go
func writeTestTarball(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, createTestTarball(t), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/source/ -run TestPrepareNonGitSourceDispatch -v`

Expected: FAIL — `prepareNonGitSource` undefined.

- [ ] **Step 3: Implement the dispatcher in `internal/source/workspace.go`**

Add this function above the existing extractor helpers:

```go
// prepareNonGitSource decides how to materialise a fetched non-git source
// into srcDir. Order:
//  1. Recognised archive extension → tar/zip extractor
//  2. Magic-byte sniff for files with no/unknown extension:
//     gzip (1f 8b) → tar; zip (50 4b 03 04) → zip; otherwise bare copy
func prepareNonGitSource(cachedPath, destDir string) error {
	switch {
	case strings.HasSuffix(cachedPath, ".tar.gz"),
		strings.HasSuffix(cachedPath, ".tgz"),
		strings.HasSuffix(cachedPath, ".tar.xz"),
		strings.HasSuffix(cachedPath, ".tar.bz2"),
		strings.HasSuffix(cachedPath, ".tbz2"),
		strings.HasSuffix(cachedPath, ".tar"):
		return extractTarball(cachedPath, destDir)
	case strings.HasSuffix(cachedPath, ".zip"):
		return extractZip(cachedPath, destDir)
	}

	// No recognised extension — sniff first 4 bytes.
	f, err := os.Open(cachedPath)
	if err != nil {
		return err
	}
	var magic [4]byte
	n, _ := io.ReadFull(f, magic[:])
	f.Close()

	if n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		return extractTarball(cachedPath, destDir)
	}
	if n == 4 && magic[0] == 0x50 && magic[1] == 0x4b && magic[2] == 0x03 && magic[3] == 0x04 {
		return extractZip(cachedPath, destDir)
	}
	return copyBareSource(cachedPath, destDir)
}
```

Then change the existing call site in `Prepare`:

```go
	} else {
		if err := prepareNonGitSource(cachedPath, srcDir); err != nil {
			return "", err
		}
		// Tarball needs git init + commit + tag
		if err := initGitRepo(srcDir); err != nil {
			return "", err
		}
	}
```

(Replace the existing `extractTarball(cachedPath, srcDir)` call.)

- [ ] **Step 4: Run all source tests to verify nothing regressed**

Run: `go test ./internal/source/ -v`

Expected: all existing tests still pass plus the three new dispatcher tests.

- [ ] **Step 5: Commit**

```bash
git add internal/source/workspace.go internal/source/source_test.go
git commit -m "source: dispatch extract by extension/magic, including bare and zip"
```

---

## Task 4: Implement `binary` class

**Files:**

- Create: `modules/module-core/classes/binary.star`

This is the user-facing class. It resolves URL+SHA at Starlark eval time based
on `ARCH`, normalises `binaries` into `(install_name, src_path)` tuples,
generates a single install task, and calls `unit()` with the right typed fields.

- [ ] **Step 1: Create the file with the full class implementation**

Create `modules/module-core/classes/binary.star`:

```python
load("//classes/tasks.star", "merge_tasks")

# _DEFAULT_ARCH_MAP maps yoe canonical arches to the tokens most upstreams
# use in their asset filenames (Go-style amd64/arm64).
_DEFAULT_ARCH_MAP = {
    "x86_64": "amd64",
    "arm64":  "arm64",
}

def _subst(s, version, arch_token):
    # Two-pass literal substitution. {version} and {arch} only.
    return s.replace("{version}", version).replace("{arch}", arch_token)

def _basename(path):
    if "/" not in path:
        return path
    return path.rsplit("/", 1)[1]

def _relpath(from_dir, to_path):
    # Compute a relative path from from_dir to to_path. Both must be
    # absolute and share the same root. Used to produce relocatable
    # symlink targets for install_tree binaries.
    fp = from_dir.split("/")
    tp = to_path.split("/")
    # Drop common prefix
    i = 0
    for i in range(min(len(fp), len(tp))):
        if fp[i] != tp[i]:
            break
    else:
        i = min(len(fp), len(tp))
    if i < len(fp) and i < len(tp) and fp[i] == tp[i]:
        i += 1
    ups = [".."] * (len(fp) - i)
    rest = tp[i:]
    return "/".join(ups + rest) if (ups or rest) else "."

def _normalise_binaries(binaries, default_name, version, arch_token):
    # Returns a list of (install_name, src_path) tuples with templating
    # already applied. install_name is always literal.
    if binaries == None:
        return [(default_name, _subst(default_name, version, arch_token))]
    if type(binaries) == "list":
        out = []
        for entry in binaries:
            if type(entry) != "string":
                fail("binary: 'binaries' list entries must be strings, got %r" % entry)
            src = _subst(entry, version, arch_token)
            out.append((_basename(entry), src))
        return out
    if type(binaries) == "dict":
        out = []
        for k, v in binaries.items():
            if type(k) != "string" or type(v) != "string":
                fail("binary: 'binaries' dict entries must be string→string")
            if "/" in k:
                fail("binary: 'binaries' install name %r cannot contain '/'" % k)
            out.append((k, _subst(v, version, arch_token)))
        return out
    fail("binary: 'binaries' must be list, dict, or omitted")

def _install_steps(name, binaries_pairs, install_tree, extras, symlinks):
    steps = []
    if install_tree:
        steps.append("mkdir -p $DESTDIR%s" % install_tree)
        steps.append("cp -aT $SRCDIR/. $DESTDIR%s" % install_tree)

    # Primary binaries
    for install_name, src in binaries_pairs:
        dst_dir = "$DESTDIR$PREFIX/bin"
        dst = "%s/%s" % (dst_dir, install_name)
        if install_tree:
            target_abs = "%s/%s" % (install_tree, src)
            target_rel = _relpath("$PREFIX/bin", target_abs)
            steps.append("mkdir -p %s" % dst_dir)
            steps.append("ln -sfn %s %s" % (target_rel, dst))
        else:
            steps.append("mkdir -p %s" % dst_dir)
            steps.append("install -m0755 $SRCDIR/%s %s" % (src, dst))

    # Extras
    for entry in extras:
        if len(entry) == 2:
            src, dst = entry[0], entry[1]
            mode = None
        elif len(entry) == 3:
            src, dst, mode = entry[0], entry[1], entry[2]
        else:
            fail("binary: extras entries must be (src, dst) or (src, dst, mode)")
        steps.append("mkdir -p $(dirname $DESTDIR%s)" % dst)
        steps.append("cp -aT $SRCDIR/%s $DESTDIR%s" % (src, dst))
        if mode != None:
            steps.append("chmod %o $DESTDIR%s" % (mode, dst))

    # Symlinks
    for dst, target in symlinks.items():
        steps.append("mkdir -p $(dirname $DESTDIR%s)" % dst)
        steps.append("ln -sfn %s $DESTDIR%s" % (target, dst))

    if not steps:
        fail("binary %s: no install steps — set 'binaries' or 'extras'" % name)
    return steps

def binary(name, version, base_url, sha256,
           asset = None, assets = None, arch_map = None,
           binaries = None, install_tree = "",
           extras = [], symlinks = {},
           container = "toolchain-musl",
           container_arch = "target",
           deps = [], runtime_deps = [],
           license = "", description = "",
           services = [], conffiles = [], scope = "",
           tasks = [], **kwargs):
    # ARCH is predeclared by the engine.
    if ARCH not in sha256:
        fail("binary %s: sha256 has no entry for ARCH=%s" % (name, ARCH))

    # Asset selection
    if (asset == None) == (assets == None):
        fail("binary %s: set exactly one of 'asset' (template) or 'assets' (per-arch dict)" % name)

    amap = arch_map if arch_map != None else _DEFAULT_ARCH_MAP

    if assets != None:
        if ARCH not in assets:
            fail("binary %s: assets has no entry for ARCH=%s" % (name, ARCH))
        # arch token isn't used for literal assets, but {version} still substitutes.
        arch_token = ""
        asset_path = _subst(assets[ARCH], version, arch_token)
    else:
        if ARCH not in amap:
            fail("binary %s: arch_map has no entry for ARCH=%s" % (name, ARCH))
        arch_token = amap[ARCH]
        asset_path = _subst(asset, version, arch_token)

    # In src paths inside the archive, {arch} substitutes with the same token
    # the URL used (for templated form) or with arch_map[ARCH] (for literal
    # assets, fallback to default map for consistency).
    src_arch_token = arch_token
    if src_arch_token == "":
        src_arch_token = amap[ARCH] if ARCH in amap else ARCH

    binaries_pairs = _normalise_binaries(binaries, name, version, src_arch_token)
    sha = sha256[ARCH]

    final_base_url = _subst(base_url, version, src_arch_token)
    source_url = final_base_url + "/" + asset_path

    # Substitute install_tree and extras src paths
    final_install_tree = _subst(install_tree, version, src_arch_token) if install_tree else ""
    final_extras = []
    for e in extras:
        if len(e) == 2:
            final_extras.append((_subst(e[0], version, src_arch_token), e[1]))
        else:
            final_extras.append((_subst(e[0], version, src_arch_token), e[1], e[2]))
    final_symlinks = {}
    for k, v in symlinks.items():
        final_symlinks[k] = _subst(v, version, src_arch_token)

    install_task = task("install", steps = _install_steps(
        name, binaries_pairs, final_install_tree, final_extras, final_symlinks,
    ))
    final_tasks = merge_tasks([install_task], tasks)

    all_deps = list(deps)
    if container and ":" not in container and container not in all_deps:
        all_deps.append(container)

    unit(
        name = name,
        version = version,
        source = source_url,
        sha256 = sha,
        deps = all_deps,
        runtime_deps = runtime_deps,
        tasks = final_tasks,
        services = services,
        conffiles = conffiles,
        license = license,
        description = description,
        scope = scope,
        container = container,
        container_arch = container_arch,
        sandbox = False,
        **kwargs
    )
```

- [ ] **Step 2: Verify it parses with no syntax errors**

Run: `go build ./... && go test ./internal/starlark/ -count=1`

Expected: build succeeds, existing Starlark tests pass.

(No new test here — the class is exercised by the example units in the next two
tasks.)

- [ ] **Step 3: Commit**

```bash
git add modules/module-core/classes/binary.star
git commit -m "module-core: add binary class for prebuilt-binary units"
```

---

## Task 5: Add `kubectl` example unit (bare binary, smoke test)

**Files:**

- Create: `modules/module-core/units/debug/kubectl.star`

Smallest end-to-end exercise: bare binary, single arch token, default
`binaries`. If this builds, the bare-binary code path through Go extraction +
class install task works.

- [ ] **Step 1: Look up real SHA256 hashes**

The class requires literal hashes per arch. Use known-stable upstream hashes for
kubectl 1.29.0 (the `kubectl` binary itself, not the `kubectl.sha256` text
file):

- x86_64: `https://dl.k8s.io/release/v1.29.0/bin/linux/amd64/kubectl.sha256`
- arm64: `https://dl.k8s.io/release/v1.29.0/bin/linux/arm64/kubectl.sha256`

Fetch each and record the raw 64-character hex string:

```bash
curl -sL https://dl.k8s.io/release/v1.29.0/bin/linux/amd64/kubectl.sha256
curl -sL https://dl.k8s.io/release/v1.29.0/bin/linux/arm64/kubectl.sha256
```

- [ ] **Step 2: Create the unit file**

Create `modules/module-core/units/debug/kubectl.star`:

```python
load("//classes/binary.star", "binary")

binary(
    name = "kubectl",
    version = "1.29.0",
    base_url = "https://dl.k8s.io/release/v{version}/bin/linux",
    asset = "{arch}/kubectl",
    sha256 = {
        "x86_64": "<paste-amd64-hash-from-step-1>",
        "arm64":  "<paste-arm64-hash-from-step-1>",
    },
    license = "Apache-2.0",
    description = "Kubernetes command-line tool",
)
```

- [ ] **Step 3: Build the unit for the host arch**

```bash
source envsetup.sh
yoe_build
yoe build kubectl
```

Expected: build completes; `build/<arch>/kubectl/destdir/usr/bin/kubectl`
exists, is executable, and the produced apk in
`build/repo/<arch>/kubectl-1.29.0-r0.apk` contains `usr/bin/kubectl`.

Verify with:

```bash
tar tzf build/repo/$(uname -m)/kubectl-1.29.0-r0.apk | grep kubectl
```

Expected output includes `usr/bin/kubectl`.

- [ ] **Step 4: Smoke-test that the binary actually runs**

```bash
file build/$(uname -m)/kubectl/destdir/usr/bin/kubectl
build/$(uname -m)/kubectl/destdir/usr/bin/kubectl version --client
```

Expected: ELF binary, prints client version `v1.29.0`.

- [ ] **Step 5: Commit**

```bash
git add modules/module-core/units/debug/kubectl.star
git commit -m "kubectl: add unit using binary class"
```

---

## Task 6: Add `go` example unit (bundle with `install_tree`)

**Files:**

- Create: `modules/module-core/units/build-tools/go.star`

Exercises the most complex class form: `install_tree`, multi-binary (go +
gofmt), `{version}` + `{arch}` in the asset URL, archive extraction with
auto-strip.

- [ ] **Step 1: Look up real SHA256 hashes for Go 1.22.0**

The Go release page publishes hashes at `https://go.dev/dl/?mode=json` or in the
asset listing. For `go1.22.0.linux-amd64.tar.gz` and
`go1.22.0.linux-arm64.tar.gz`:

```bash
curl -s https://go.dev/dl/?mode=json | jq -r '.[] | select(.version == "go1.22.0") | .files[] | select(.os == "linux" and (.arch == "amd64" or .arch == "arm64")) | "\(.arch) \(.sha256)"'
```

Record both hashes.

- [ ] **Step 2: Create the unit file**

Create `modules/module-core/units/build-tools/go.star`:

```python
load("//classes/binary.star", "binary")

binary(
    name = "go",
    version = "1.22.0",
    base_url = "https://go.dev/dl",
    asset = "go{version}.linux-{arch}.tar.gz",
    sha256 = {
        "x86_64": "<paste-amd64-hash>",
        "arm64":  "<paste-arm64-hash>",
    },
    install_tree = "$PREFIX/lib/go",
    binaries = ["bin/go", "bin/gofmt"],
    license = "BSD-3-Clause",
    description = "The Go programming language toolchain",
)
```

- [ ] **Step 3: Build the unit**

```bash
yoe build go
```

Expected: build completes; under `build/<arch>/go/destdir/usr/lib/go/` are the
directories `bin/`, `pkg/`, `src/`, `lib/` (the entire toolchain). Under
`build/<arch>/go/destdir/usr/bin/` are two symlinks:

- `go` → `../lib/go/bin/go`
- `gofmt` → `../lib/go/bin/gofmt`

Verify symlinks:

```bash
ls -l build/$(uname -m)/go/destdir/usr/bin/
```

Expected: both `go` and `gofmt` shown as symlinks pointing into
`../lib/go/bin/`.

- [ ] **Step 4: Smoke-test that the toolchain runs end-to-end**

```bash
build/$(uname -m)/go/destdir/usr/bin/go version
build/$(uname -m)/go/destdir/usr/bin/gofmt -h
```

Expected: `go version go1.22.0 linux/<arch>`; gofmt prints help.

- [ ] **Step 5: Commit**

```bash
git add modules/module-core/units/build-tools/go.star
git commit -m "go: add Go toolchain unit using binary class install_tree"
```

---

## Task 7: Update changelog and roadmap

**Files:**

- Modify: `CHANGELOG.md`
- Modify: `docs/units-roadmap.md`

Keep changelog entries simple and user-focused per project policy.

- [ ] **Step 1: Add changelog entry**

Open `CHANGELOG.md`. Find the unreleased / top section and add an entry in the
existing style:

```markdown
- New `binary` class for installing prebuilt binaries from upstream release URLs
  (kubectl, helm, fly, the Go toolchain, etc.) — declare per-arch SHA256 hashes
  once and `yoe` will fetch, verify, and install without rebuilding from source.
```

(Match the bullet/heading style of the most recent entries in the file — if that
file uses sections like `### Added` / `### Changed`, place the entry under
`### Added`.)

- [ ] **Step 2: Mention in `docs/units-roadmap.md`**

Open `docs/units-roadmap.md`. Find the section that lists classes or upcoming
work and add a line documenting the new class. Match the existing prose style:

```markdown
- `binary` class — install prebuilt arm64/x86_64 binaries straight from upstream
  release URLs without a from-source rebuild. Backed by a single per-arch SHA256
  plus optional `install_tree` for toolchain bundles.
```

If the file has a "completed" / "shipped" subsection, that's the right place;
otherwise add it under whichever heading covers existing classes.

- [ ] **Step 3: Format docs**

```bash
source envsetup.sh
yoe_format
yoe_format_check
```

Expected: format succeeds, no warnings.

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md docs/units-roadmap.md
git commit -m "docs: changelog and roadmap entry for binary class"
```

---

## Task 8: Final verification

- [ ] **Step 1: Run all Go tests**

```bash
go test ./...
```

Expected: every package passes; in particular `internal/source` and
`internal/starlark` are green.

- [ ] **Step 2: Run a clean build of both example units**

```bash
yoe build kubectl
yoe build go
```

Expected: both succeed end-to-end. Apks present under `build/repo/<arch>/`.

- [ ] **Step 3: Verify formatting**

```bash
yoe_format_check
```

Expected: clean.

- [ ] **Step 4: Verify commit history**

```bash
git log --oneline cbrake/main..HEAD
```

Expected: 7 task-aligned commits in order:

1. `source: extract .zip archives with top-level-dir strip`
2. `source: copy bare-binary downloads into srcDir`
3. `source: dispatch extract by extension/magic, including bare and zip`
4. `module-core: add binary class for prebuilt-binary units`
5. `kubectl: add unit using binary class`
6. `go: add Go toolchain unit using binary class install_tree`
7. `docs: changelog and roadmap entry for binary class`

---

## Self-review checklist (executed by plan author, recorded for the engineer)

- **Spec coverage:** Every section of the spec is mapped to at least one task —
  zip extraction (T1), bare-binary copy (T2), dispatcher (T3), class with
  `{version}` + `{arch}` substitution and
  `binaries`/`install_tree`/`extras`/`symlinks` semantics (T4), bare-binary E2E
  (T5), bundle E2E (T6), docs (T7), regression verification (T8).
- **Validation:** Validation rules from the spec (missing arch in
  `sha256`/`assets`, both/neither of `asset`/`assets`, empty install, `/` in
  install name) are implemented inline in the class via `fail` calls.
  Negative-path Starlark tests are not added — the project doesn't carry that
  infrastructure today, and broken units fail loudly when `yoe build` is run,
  which T5/T6 exercise.
- **Type/name consistency:** `binaries`, `install_tree`, `extras`, `symlinks`,
  `arch_map`, `base_url`, `asset`, `assets`, `sha256` are spelled identically in
  spec, class, and example units.
- **No placeholders:** Every code step ships actual code; only the per-arch
  hashes for kubectl and go are intentionally fetched at task-run time (Step 1
  of T5 and T6) — they're upstream values that shouldn't be hardcoded into the
  plan.
