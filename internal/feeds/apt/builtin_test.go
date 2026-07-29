package apt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

func kwargs(pairs map[string]string) []starlark.Tuple {
	var out []starlark.Tuple
	for k, v := range pairs {
		out = append(out, starlark.Tuple{starlark.String(k), starlark.String(v)})
	}
	out = append(out, starlark.Tuple{
		starlark.String("arches"),
		starlark.NewList([]starlark.Value{starlark.String("amd64")}),
	})
	return out
}

func baseKwargs() map[string]string {
	return map[string]string{
		"name":      "main",
		"distro":    "debian",
		"url":       "https://deb.debian.org/debian",
		"suite":     "trixie",
		"codename":  "trixie",
		"component": "main",
		"index":     "feeds/main",
		"keyring":   "keys/k.gpg",
	}
}

func TestParseKwargs_SuiteAndCodenameAreDistinct(t *testing.T) {
	// A vendor overlay repo: fetched from dists/stable, but its packages
	// are built for trixie. Both values must survive parsing separately.
	k := baseKwargs()
	k["suite"] = "stable"
	a, err := parseKwargs(kwargs(k))
	if err != nil {
		t.Fatalf("parseKwargs: %v", err)
	}
	if a.suite != "stable" {
		t.Errorf("suite = %q, want %q", a.suite, "stable")
	}
	if a.codename != "trixie" {
		t.Errorf("codename = %q, want %q", a.codename, "trixie")
	}
}

func TestParseKwargs_CodenameRequired(t *testing.T) {
	k := baseKwargs()
	delete(k, "codename")
	_, err := parseKwargs(kwargs(k))
	if err == nil {
		t.Fatal("want error when codename is omitted, got nil")
	}
	// The message should tell a module author what to write, since the
	// common case is a base feed where codename equals suite.
	if !strings.Contains(err.Error(), "codename") || !strings.Contains(err.Error(), "trixie") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestParseKwargs_SuiteRequired(t *testing.T) {
	k := baseKwargs()
	delete(k, "suite")
	if _, err := parseKwargs(kwargs(k)); err == nil {
		t.Fatal("want error when suite is omitted, got nil")
	}
}

func TestPeekFeedDecls_RecordsCodename(t *testing.T) {
	dir := t.TempDir()
	src := `module_info(name = "qcom", description = "test")

apt_feed(
    name = "arduino",
    distro = "debian",
    url = "https://apt-repo.arduino.cc",
    suite = "stable",
    codename = "trixie",
    component = "main",
    arches = ["amd64", "arm64"],
    index = "feeds/arduino",
    keyring = "keys/arduino-release-keyring.gpg",
)
`
	if err := os.WriteFile(filepath.Join(dir, "MODULE.star"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	decls, err := PeekFeedDecls(dir)
	if err != nil {
		t.Fatalf("PeekFeedDecls: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("got %d decls, want 1", len(decls))
	}
	// update-feeds fetches from Suite and reports Codename; conflating
	// them would send it to dists/trixie on a repo that has no such path.
	if decls[0].Suite != "stable" {
		t.Errorf("Suite = %q, want %q", decls[0].Suite, "stable")
	}
	if decls[0].Codename != "trixie" {
		t.Errorf("Codename = %q, want %q", decls[0].Codename, "trixie")
	}
}
