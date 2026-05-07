package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha256"
	"debug/elf"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// CreateAPK builds an .apk package from a unit's $DESTDIR contents.
//
// Alpine .apk files are concatenated gzip streams:
//   - Stream 1 (optional): signature block (.SIGN.RSA.*)
//   - Stream 2: control block (.PKGINFO + checksums)
//   - Stream 3: data block (actual files)
//
// When `signer` is non-nil, stream 1 is prepended; the signature is
// RSA-PKCS#1 v1.5 over the SHA-1 of the control stream's gzipped bytes.
// When `signer` is nil, the apk is unsigned and apk-tools needs
// --allow-untrusted to install it.
//
// The control block's PKGINFO carries a `datahash` field — the hex SHA-256
// of the *compressed* data stream bytes (the gzipped tar, not the raw tar).
// apk's mpart-gzip reader passes compressed bytes through the digest
// before decompressing them, so the hash is over the on-disk gzip blob.
// Without datahash apk reports "BAD signature" even with --allow-untrusted.
// arch is the value emitted as PKGINFO `arch=` and the directory the apk will
// later be published into (`<repo>/<arch>/<filename>.apk`). For arch-scoped
// and machine-scoped units this is the target architecture (e.g., x86_64,
// aarch64); for noarch units it is the literal string "noarch".
func CreateAPK(unit *yoestar.Unit, destDir, outputDir, arch, commit string, signer *Signer) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("creating output dir: %w", err)
	}

	// Filename matches Alpine's convention: <name>-<ver>-r<N>.apk. The arch
	// is recorded inside PKGINFO and reflected in the repo directory name,
	// not in the filename — apk-tools constructs fetch URLs from the index
	// as <repo>/<arch>/<pkgname>-<pkgver>.apk and won't find a file with
	// any extra suffix.
	apkName := fmt.Sprintf("%s-%s-r%d.apk", unit.Name, unit.Version, unit.Release)
	apkPath := filepath.Join(outputDir, apkName)

	// Materialise `services = [...]` as actual init-script symlinks inside
	// destDir before we tar it. The symlinks become regular package
	// content, so on-target `apk add` and image-time `apk add` produce the
	// same result — yoe never patches the rootfs after apk has run.
	if err := materializeServiceSymlinks(unit, destDir); err != nil {
		return "", fmt.Errorf("creating service symlinks: %w", err)
	}

	// Build the data tar (uncompressed), then gzip it and hash the
	// compressed bytes for PKGINFO's datahash.
	dataTar, err := buildDataTar(destDir)
	if err != nil {
		return "", fmt.Errorf("building data tar: %w", err)
	}
	var dataGz bytes.Buffer
	gw := gzip.NewWriter(&dataGz)
	if _, err := gw.Write(dataTar); err != nil {
		return "", fmt.Errorf("compressing data tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return "", fmt.Errorf("closing data tar gzip: %w", err)
	}
	dataHash := sha256.Sum256(dataGz.Bytes())
	dataHashHex := fmt.Sprintf("%x", dataHash[:])

	// Generate PKGINFO with the data hash baked in.
	pkginfo := generatePKGINFO(unit, destDir, dataHashHex, arch, commit)

	// Build the control stream (gzipped tar containing .PKGINFO).
	var controlGz bytes.Buffer
	if err := writeGzipTar(&controlGz, map[string][]byte{".PKGINFO": []byte(pkginfo)}); err != nil {
		return "", fmt.Errorf("building control stream: %w", err)
	}

	// Open output and write the streams in order: optional signature,
	// control, data. The signature is over the SHA-1 of the gzip-compressed
	// control stream (verified empirically against Alpine's own signed
	// apks: sha1(controlGz) is exactly what `openssl dgst -sha1 -verify`
	// accepts against the .SIGN.RSA.* signature). Data integrity flows
	// through PKGINFO `datahash`, which the control stream carries.
	f, err := os.Create(apkPath)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", apkPath, err)
	}
	defer f.Close()

	if signer != nil {
		sigGz, err := signer.SignStream(controlGz.Bytes())
		if err != nil {
			return "", fmt.Errorf("signing control stream: %w", err)
		}
		if _, err := f.Write(sigGz); err != nil {
			return "", fmt.Errorf("writing signature stream: %w", err)
		}
	}

	if _, err := f.Write(controlGz.Bytes()); err != nil {
		return "", fmt.Errorf("writing control stream: %w", err)
	}
	if _, err := f.Write(dataGz.Bytes()); err != nil {
		return "", fmt.Errorf("writing data stream: %w", err)
	}

	return apkPath, nil
}

// RepackAPK takes an upstream-built .apk (typically from Alpine), strips its
// existing signature, re-signs the control stream with the project's key, and
// writes the result to outputDir under yoe's `<name>-<ver>-r<N>.apk` naming.
//
// PKGINFO and install scripts (.pre-install, .post-install, .trigger, ...)
// inside the control segment are passed through verbatim — that's the whole
// point: we let upstream's coordinated metadata (`replaces`, `provides`,
// `triggers`) and post-install hooks (busybox applet symlink creation,
// privsep user adds) flow into the on-target apk without yoe rewriting them.
//
// Layout assumption: an apk is concatenated gzip streams in order
// [signature?, control, data]. We detect whether the first stream is a
// signature by peeking at the tar inside it — signature tars contain a single
// `.SIGN.RSA.*` entry. If present we drop it; otherwise the first stream is
// already the control segment.
func RepackAPK(unit *yoestar.Unit, srcAPK, outputDir string, signer *Signer) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("RepackAPK requires a signer")
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("creating output dir: %w", err)
	}
	apkName := fmt.Sprintf("%s-%s-r%d.apk", unit.Name, unit.Version, unit.Release)
	apkPath := filepath.Join(outputDir, apkName)

	raw, err := os.ReadFile(srcAPK)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", srcAPK, err)
	}

	streams, err := splitGzipStreams(raw)
	if err != nil {
		return "", fmt.Errorf("splitting %s into gzip streams: %w", srcAPK, err)
	}
	if len(streams) < 2 {
		return "", fmt.Errorf("%s: expected at least 2 gzip streams (control+data), got %d", srcAPK, len(streams))
	}

	// Drop a leading signature stream if present.
	idx := 0
	if isSignatureStream(streams[0]) {
		idx = 1
	}
	if len(streams)-idx < 2 {
		return "", fmt.Errorf("%s: missing control or data stream after signature strip", srcAPK)
	}
	control := streams[idx]
	data := bytes.Join(streams[idx+1:], nil)

	sigGz, err := signer.SignStream(control)
	if err != nil {
		return "", fmt.Errorf("signing control stream: %w", err)
	}

	f, err := os.Create(apkPath)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", apkPath, err)
	}
	defer f.Close()
	if _, err := f.Write(sigGz); err != nil {
		return "", fmt.Errorf("writing signature stream: %w", err)
	}
	if _, err := f.Write(control); err != nil {
		return "", fmt.Errorf("writing control stream: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return "", fmt.Errorf("writing data stream: %w", err)
	}
	return apkPath, nil
}

// splitGzipStreams walks the byte slice, decoding one gzip stream at a time
// (Multistream(false) so we stop at each member boundary), and returns the
// raw compressed bytes of each stream in order. The Go gzip reader exposes
// the underlying bytes.Reader's position via its remaining length, which
// gives us a clean stream-end offset without needing to parse gzip headers
// by hand.
func splitGzipStreams(raw []byte) ([][]byte, error) {
	var out [][]byte
	r := bytes.NewReader(raw)
	for r.Len() > 0 {
		start := int64(len(raw)) - int64(r.Len())
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("gzip stream at offset %d: %w", start, err)
		}
		gr.Multistream(false)
		if _, err := io.Copy(io.Discard, gr); err != nil {
			gr.Close()
			return nil, fmt.Errorf("reading gzip stream at offset %d: %w", start, err)
		}
		if err := gr.Close(); err != nil {
			return nil, fmt.Errorf("closing gzip stream at offset %d: %w", start, err)
		}
		end := int64(len(raw)) - int64(r.Len())
		out = append(out, raw[start:end])
	}
	return out, nil
}

// ReadAPKArch returns the value of the `arch =` field in the apk's PKGINFO.
// Used by the passthrough path to redirect noarch packages to the `noarch/`
// repo directory: apk-tools constructs fetch URLs from the APKINDEX as
// `<repo>/<A:>/<P>-<V>.apk`, where `A:` mirrors PKGINFO's `arch =`. If yoe
// publishes a noarch package under `<repo>/x86_64/`, apk's solver looks for
// it in `<repo>/noarch/` and 404s.
func ReadAPKArch(srcAPK string) (string, error) {
	raw, err := os.ReadFile(srcAPK)
	if err != nil {
		return "", err
	}
	streams, err := splitGzipStreams(raw)
	if err != nil {
		return "", err
	}
	idx := 0
	if len(streams) > 0 && isSignatureStream(streams[0]) {
		idx = 1
	}
	if idx >= len(streams) {
		return "", fmt.Errorf("%s: no control stream", srcAPK)
	}
	gr, err := gzip.NewReader(bytes.NewReader(streams[idx]))
	if err != nil {
		return "", err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Name != ".PKGINFO" {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "arch = ") {
				return strings.TrimSpace(strings.TrimPrefix(line, "arch = ")), nil
			}
		}
		break
	}
	return "", fmt.Errorf("%s: arch not found in PKGINFO", srcAPK)
}

// scanSONAMEs walks destDir, opens every regular file as an ELF, and
// returns the deduped list of DT_SONAME values found. Used to auto-emit
// `provides = so:<soname>=…` lines in PKGINFO so that Alpine prebuilt
// packages depending on `so:libfoo.so.N` can resolve against
// yoe-source-built libraries without the unit author maintaining SONAME
// lists by hand.
//
// We open files unconditionally and let `elf.NewFile` reject non-ELF
// content via its magic-byte check — cheaper and more correct than
// pattern-matching filenames. Symlinks are skipped: the symlink resolves
// to its target which carries the SONAME directly, so following them
// would just emit duplicates the dedupe map would discard anyway.
func scanSONAMEs(destDir string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	err := filepath.WalkDir(destDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		ef, err := elf.NewFile(f)
		if err != nil {
			return nil
		}
		defer ef.Close()
		sonames, err := ef.DynString(elf.DT_SONAME)
		if err != nil {
			return nil
		}
		for _, s := range sonames {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// isSignatureStream returns true if the gzipped tar contains a `.SIGN.RSA.*`
// entry as its first member. Apk signatures live in single-entry tars with
// that name; control streams start with `.PKGINFO` instead.
func isSignatureStream(streamGz []byte) bool {
	gr, err := gzip.NewReader(bytes.NewReader(streamGz))
	if err != nil {
		return false
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	hdr, err := tr.Next()
	if err != nil {
		return false
	}
	return strings.HasPrefix(hdr.Name, ".SIGN.RSA.")
}

// normalizeOwnership resets a tar header to root:root. Package artifacts are
// built under the host user's uid/gid (docker --user uid:gid); without this,
// those uids leak into installed rootfs content and the booted system sees
// files owned by a nonexistent user.
func normalizeOwnership(h *tar.Header) {
	h.Uid = 0
	h.Gid = 0
	h.Uname = "root"
	h.Gname = "root"
}

// buildDataTar creates an uncompressed tar archive of the destDir contents.
//
// apk-tools verifies the integrity of every file in the data tar via a
// `APK-TOOLS.checksum.SHA1` PaX extended-header record carrying the hex
// SHA-1 of the file's content. Without this record apk reports
// "BAD archive" and refuses to install. We emit it on every regular file.
// Symlinks and directories are not checksummed (Alpine's apks don't
// either — checksums only protect file content).
func buildDataTar(destDir string) ([]byte, error) {
	var paths []string
	if err := filepath.WalkDir(destDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == destDir {
			return nil
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(paths)

	// Write to a temp file (packages can be large)
	tmp, err := os.CreateTemp("", "yoe-data-*.tar")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	tw := tar.NewWriter(tmp)
	for _, path := range paths {
		rel, _ := filepath.Rel(destDir, path)
		info, err := os.Lstat(path)
		if err != nil {
			tmp.Close()
			return nil, err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			tmp.Close()
			return nil, err
		}
		header.Name = rel
		normalizeOwnership(header)

		if info.Mode()&os.ModeSymlink != 0 {
			link, _ := os.Readlink(path)
			header.Linkname = link
			header.Typeflag = tar.TypeSymlink
		}

		// apk-tools needs an `APK-TOOLS.checksum.SHA1` PaX record on
		// every regular file *and* symlink — for files it's the SHA-1
		// of the content, for symlinks it's the SHA-1 of the target
		// string. Without this on symlinks apk warns
		// "support for packages without embedded checksums...".
		var content []byte
		if info.Mode().IsRegular() {
			content, err = os.ReadFile(path)
			if err != nil {
				tmp.Close()
				return nil, err
			}
			sum := sha1.Sum(content)
			if header.PAXRecords == nil {
				header.PAXRecords = map[string]string{}
			}
			header.PAXRecords["APK-TOOLS.checksum.SHA1"] = fmt.Sprintf("%x", sum[:])
		} else if info.Mode()&os.ModeSymlink != 0 {
			sum := sha1.Sum([]byte(header.Linkname))
			if header.PAXRecords == nil {
				header.PAXRecords = map[string]string{}
			}
			header.PAXRecords["APK-TOOLS.checksum.SHA1"] = fmt.Sprintf("%x", sum[:])
		}

		if err := tw.WriteHeader(header); err != nil {
			tmp.Close()
			return nil, err
		}

		if content != nil {
			if _, err := tw.Write(content); err != nil {
				tmp.Close()
				return nil, err
			}
		}
	}
	// Close writes the 2-block tar trailer. Alpine's data tar carries
	// the trailer (only the inner control stream omits it), and apk's
	// `datahash` is computed over the bytes including the trailer.
	if err := tw.Close(); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	return os.ReadFile(tmpName)
}

// writeGzipTar writes a single gzip stream containing a tar with the given
// files. Used for the apk control block (`.PKGINFO`).
//
// The tar is written *without* its 2-block zero trailer — apk's multi-stream
// format expects to concatenate this onto the data tar, and a tar reader
// (and apk itself) will stop at the first all-zero block. We write the
// entries and flush, then close the gzip stream cleanly.
func writeGzipTar(w io.Writer, files map[string][]byte) error {
	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)

	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, name := range keys {
		content := files[name]
		header := &tar.Header{
			Name:    name,
			Size:    int64(len(content)),
			Mode:    0644,
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tw.Write(content); err != nil {
			return err
		}
	}

	// Flush, but do not Close — Close would write the 2-block trailer.
	if err := tw.Flush(); err != nil {
		return err
	}
	return gw.Close()
}

// generatePKGINFO creates the .PKGINFO metadata file content.
//
// Field order follows Alpine's convention (pkgname, pkgver, pkgdesc, url,
// builddate, packager, size, arch, origin, commit, depend, ...). apk-tools
// is order-tolerant; matching ordering keeps diffs sane.
func generatePKGINFO(unit *yoestar.Unit, destDir, dataHashHex, arch, commit string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "pkgname = %s\n", unit.Name)
	fmt.Fprintf(&b, "pkgver = %s-r%d\n", unit.Version, unit.Release)

	if unit.Description != "" {
		fmt.Fprintf(&b, "pkgdesc = %s\n", unit.Description)
	}
	if unit.License != "" {
		fmt.Fprintf(&b, "license = %s\n", unit.License)
	}

	fmt.Fprintf(&b, "arch = %s\n", arch)
	fmt.Fprintf(&b, "builddate = %d\n", time.Now().Unix())

	// origin = source-package name. For yoe today every binary package is
	// built from a single same-named source unit, so origin == pkgname.
	// When split packages land, origin will refer to the parent unit.
	fmt.Fprintf(&b, "origin = %s\n", unit.Name)

	// commit = project repo's HEAD at build time. Optional — apk treats it
	// as informational provenance. Only emit when the caller knows it.
	if commit != "" {
		fmt.Fprintf(&b, "commit = %s\n", commit)
	}

	// Compute installed size
	var size int64
	filepath.WalkDir(destDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		return nil
	})
	fmt.Fprintf(&b, "size = %d\n", size)

	// Data hash (SHA256 of the uncompressed data tar)
	if dataHashHex != "" {
		fmt.Fprintf(&b, "datahash = %s\n", dataHashHex)
	}

	// Runtime dependencies
	for _, dep := range unit.RuntimeDeps {
		fmt.Fprintf(&b, "depend = %s\n", dep)
	}

	// Virtual package names this unit satisfies — apk consumers can depend
	// on the virtual name and apk picks any package that provides it.
	for _, p := range unit.Provides {
		fmt.Fprintf(&b, "provides = %s\n", p)
	}

	// Auto-emit `provides = so:<soname>=<ver>-r<rel>` for every shared
	// library this unit ships, matching Alpine's abuild convention. Lets
	// Alpine prebuilt apks (e.g. apk-tools, openrc) whose upstream PKGINFO
	// declares `depend = so:libcrypto.so.3` resolve cleanly against
	// yoe-source-built openssl/zlib/etc. without the unit author having
	// to maintain SONAME tables by hand.
	soVersion := fmt.Sprintf("%s-r%d", unit.Version, unit.Release)
	if sonames, err := scanSONAMEs(destDir); err == nil {
		for _, s := range sonames {
			fmt.Fprintf(&b, "provides = so:%s=%s\n", s, soVersion)
		}
	}

	// Packages whose files this one is allowed to overwrite at install time.
	// apk reads this to scope file-conflict overrides — without it, a
	// shadowing package (e.g. util-linux over busybox's /bin/dmesg) fails
	// install instead of emitting a warning.
	for _, r := range unit.Replaces {
		fmt.Fprintf(&b, "replaces = %s\n", r)
	}

	// Note: yoe's `services = [...]` declaration becomes actual OpenRC
	// runlevel symlinks (/etc/runlevels/default/<svc>) in the data tar
	// (see materializeServiceSymlinks). We don't emit a custom `service =`
	// PKGINFO field because apk-tools 2.x silently discards unknown fields
	// when populating `/lib/apk/db/installed`, so it would never round-trip
	// to the target.

	return b.String()
}

// materializeServiceSymlinks turns the unit's `services = [...]` declaration
// into OpenRC runlevel symlinks inside destDir, so the apk's data tar carries
// them as regular files. This lets `apk add` (image-time or on-target) produce
// a rootfs with `/etc/runlevels/default/<svc>` already in place — yoe never
// has to patch the rootfs after the install.
//
// OpenRC walks /etc/runlevels/<runlevel>/ to discover which services to start,
// resolving each entry as a symlink to the script in /etc/init.d/. For each
// `svc` in the list we create:
//
//	/etc/runlevels/default/<svc> -> /etc/init.d/<svc>
//
// The target script (`<destDir>/etc/init.d/<svc>`) must already exist; if it
// doesn't, that's the unit's bug — we fail loudly rather than ship a dangling
// symlink.
func materializeServiceSymlinks(unit *yoestar.Unit, destDir string) error {
	if len(unit.Services) == 0 {
		return nil
	}
	initd := filepath.Join(destDir, "etc", "init.d")
	runlevel := filepath.Join(destDir, "etc", "runlevels", "default")
	for _, svc := range unit.Services {
		targetPath := filepath.Join(initd, svc)
		if _, err := os.Stat(targetPath); err != nil {
			return fmt.Errorf("service %q declared but %s missing in destdir", svc, filepath.Join("/etc/init.d", svc))
		}
		linkPath := filepath.Join(runlevel, svc)
		if _, err := os.Lstat(linkPath); err == nil {
			continue
		}
		if err := os.MkdirAll(runlevel, 0755); err != nil {
			return err
		}
		// Absolute symlink target — OpenRC's own rc-update writes absolute
		// targets here, and an absolute path resolves correctly regardless
		// of where the rootfs is mounted at boot.
		if err := os.Symlink("/etc/init.d/"+svc, linkPath); err != nil {
			return err
		}
	}
	return nil
}

// APKHash computes the SHA256 hash of an .apk file.
func APKHash(apkPath string) (string, error) {
	f, err := os.Open(apkPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// APKSha1 computes the SHA1 hash of an .apk file (for APKINDEX C: field).
func APKSha1(apkPath string) ([]byte, error) {
	f, err := os.Open(apkPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}

	return h.Sum(nil), nil
}
