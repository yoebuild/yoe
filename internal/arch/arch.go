// Package arch translates yoe's canonical architecture tokens into the
// spellings other ecosystems use.
//
// yoe names architectures the way Go and Docker do — x86_64, arm64,
// riscv64 — and every other name is a translation at a boundary: the
// Linux kernel and apk-tools say "aarch64", Debian says "amd64",
// multiarch library paths say "aarch64-linux-gnu", QEMU's binfmt
// handlers say "qemu-aarch64". Getting one of these wrong does not fail
// loudly; it publishes a package into a directory the target's package
// manager never looks in, or puts a sysroot path on a search list that
// does not exist.
//
// Keeping the tables here means a new architecture is added in one place
// and every boundary learns about it together.
//
// The translation functions pass an unrecognized token through rather
// than failing, because their results are interpolated into paths and
// package fields where there is no error to return. Rejecting a bad
// architecture is a separate step: machine() validates at registration,
// and a feed declaration calls Validate. Callers that accept an
// architecture from outside should Validate first.
package arch

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// apkTokens maps a yoe arch to the token apk-tools uses. It appears in
// repo directory names, the PKGINFO `arch =` field, and the APKINDEX
// `A:` field, all of which must agree or apk cannot find the package.
var apkTokens = map[string]string{
	"x86_64":  "x86_64",
	"arm64":   "aarch64",
	"riscv64": "riscv64",
}

// debTokens maps a yoe arch to the Debian architecture token, used in
// pool paths, the .deb Architecture field, and dists/<suite>/main/
// binary-<arch>/ index directories.
var debTokens = map[string]string{
	"x86_64":  "amd64",
	"arm64":   "arm64",
	"riscv64": "riscv64",
}

// multiarchTuples maps a yoe arch to Debian's multiarch tuple, the
// /usr/lib/<tuple>/ directory where a Debian package's shared objects
// and pkg-config files live. Compile-from-source units need this on
// their search paths to see feed-provided libraries.
var multiarchTuples = map[string]string{
	"x86_64":  "x86_64-linux-gnu",
	"arm64":   "aarch64-linux-gnu",
	"riscv64": "riscv64-linux-gnu",
}

// Supported returns yoe's architecture tokens, sorted. Suitable for the
// "supported: ..." half of an error message.
func Supported() []string {
	out := make([]string, 0, len(apkTokens))
	for a := range apkTokens {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// IsSupported reports whether a is one of yoe's architecture tokens.
func IsSupported(a string) bool {
	_, ok := apkTokens[a]
	return ok
}

// Validate returns an error naming the supported set when a is not one
// of yoe's architecture tokens. Use it where an architecture arrives
// from a declaration or a flag, before anything translates it.
func Validate(a string) error {
	if IsSupported(a) {
		return nil
	}
	return fmt.Errorf("unsupported arch %q (supported: %s)", a, strings.Join(Supported(), ", "))
}

// Apk returns the apk-tools token for a yoe arch, or a passthrough of
// the input when it isn't one yoe knows.
func Apk(a string) string {
	if v, ok := apkTokens[a]; ok {
		return v
	}
	return a
}

// Deb returns the Debian architecture token for a yoe arch, or a
// passthrough of the input when it isn't one yoe knows.
func Deb(a string) string {
	if v, ok := debTokens[a]; ok {
		return v
	}
	return a
}

// Multiarch returns Debian's multiarch tuple for a yoe arch, or "" when
// there isn't one. Callers join the result into a search path, where an
// empty component collapses to a harmless duplicate entry — better than
// a plausible-looking wrong directory.
func Multiarch(a string) string { return multiarchTuples[a] }

// Binfmt returns the /proc/sys/fs/binfmt_misc entry name for the QEMU
// user-mode handler that runs a foreign-arch container.
func Binfmt(a string) string { return "qemu-" + Apk(a) }

// DebRepoArches lists the Debian arch tokens the project repo emits a
// Packages index for. Deliberately narrower than the full translation
// table: it names the arches yoe's Debian and Ubuntu images are actually
// built for, and each entry costs a directory plus a Release checksum
// whether or not anything is published under it.
func DebRepoArches() []string { return []string{"amd64", "arm64"} }

var (
	hostOnce sync.Once
	hostArch string
)

// Host returns the machine's own architecture as a yoe token. The
// underlying `uname -m` runs once per process — the answer cannot change
// while yoe is running, and this is called from per-unit build paths.
// An unavailable uname reports x86_64 rather than failing: every caller
// is choosing a container platform or a default target, and a wrong
// guess there surfaces as a normal build error rather than silent
// corruption.
func Host() string {
	hostOnce.Do(func() {
		hostArch = "x86_64"
		out, err := exec.Command("uname", "-m").Output()
		if err != nil {
			return
		}
		switch a := strings.TrimSpace(string(out)); a {
		case "aarch64":
			hostArch = "arm64"
		case "":
			// keep the default
		default:
			hostArch = a
		}
	})
	return hostArch
}
