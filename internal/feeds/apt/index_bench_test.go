package apt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yoebuild/yoe/internal/dpkg"
)

// realIndex locates a Packages file from the e2e project's module cache.
// These are the real thing — Ubuntu main is ~7.5MB and tens of thousands
// of stanzas — which is the size that decides whether an extra layer of
// indirection in this path is affordable.
func realIndex(tb testing.TB) string {
	tb.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..",
		"testdata", "e2e-project", "cache", "modules", "module-ubuntu",
		"feeds", "main", "amd64", "Packages"))
	if err != nil {
		tb.Skip("cannot resolve index path")
	}
	if _, err := os.Stat(root); err != nil {
		tb.Skip("e2e module cache not present; run a build in testdata/e2e-project first")
	}
	return root
}

// BenchmarkParseIndexFile measures the cost this package's Lookup path
// is actually dominated by. Any proposal to share the feed scaffolding
// between the apk and apt backends behind an interface has to be
// weighed against this number, not against the line count it saves.
func BenchmarkParseIndexFile(b *testing.B) {
	path := realIndex(b)
	b.ReportAllocs()
	for b.Loop() {
		entries, err := dpkg.ParseIndexFile(path)
		if err != nil {
			b.Fatal(err)
		}
		if len(entries) == 0 {
			b.Fatal("no entries parsed")
		}
	}
}

// BenchmarkBuildProvidesTable measures the other half of feed load: the
// provides index built over every parsed entry.
func BenchmarkBuildProvidesTable(b *testing.B) {
	path := realIndex(b)
	entries, err := dpkg.ParseIndexFile(path)
	if err != nil {
		b.Skipf("parse: %v", err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		if t := dpkg.BuildProvidesTable(entries); t == nil {
			b.Fatal("nil table")
		}
	}
}

// BenchmarkNames measures the accessor a shared-backend refactor would
// put an interface call in front of. It is called once per synthetic
// module during closure enumeration.
func BenchmarkNames(b *testing.B) {
	path := realIndex(b)
	entries, err := dpkg.ParseIndexFile(path)
	if err != nil {
		b.Skipf("parse: %v", err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		n := 0
		for i := range entries {
			if entries[i].Package != "" {
				n++
			}
		}
		if n == 0 {
			b.Fatal("no names")
		}
	}
}
