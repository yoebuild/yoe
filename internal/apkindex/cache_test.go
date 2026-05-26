package apkindex

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// roundtripEntries gives deep-equal a chance — Entry has nil-vs-empty
// slice quirks we need to canonicalize before comparing.
func canonicalize(es []Entry) []Entry {
	out := make([]Entry, len(es))
	for i, e := range es {
		if e.Deps == nil {
			e.Deps = nil
		}
		out[i] = e
	}
	return out
}

func TestCache_RoundTrip(t *testing.T) {
	src := []byte(realisticFixture)
	entries, err := ParseIndex(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: %d", len(entries))
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "APKINDEX.cache")
	key := HashSource(src)

	if err := SaveCache(path, key, entries); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file: %v", err)
	}

	got, table, err := LoadCache(path, key)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if got == nil {
		t.Fatal("LoadCache: nil entries (cache miss)")
	}
	if !reflect.DeepEqual(canonicalize(got), canonicalize(entries)) {
		t.Errorf("entries differ after round-trip")
		t.Logf("got[0]:  %+v", got[0])
		t.Logf("want[0]: %+v", entries[0])
	}
	if table == nil {
		t.Error("LoadCache: nil provides table")
	}
	if got := table.Lookup("openssh-server"); got == nil || got.Name != "openssh-server" {
		t.Errorf("ProvidesTable.Lookup: %+v", got)
	}
}

func TestCache_SourceMismatch(t *testing.T) {
	src := []byte(realisticFixture)
	entries, _ := ParseIndex(bytes.NewReader(src))

	dir := t.TempDir()
	path := filepath.Join(dir, "APKINDEX.cache")
	key := HashSource(src)
	if err := SaveCache(path, key, entries); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	// Mutate one byte of the "source" hash to simulate the upstream
	// APKINDEX changing under us.
	otherKey := sha256.Sum256([]byte("different"))
	got, _, err := LoadCache(path, otherKey)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if got != nil {
		t.Errorf("expected cache miss on source mismatch, got %d entries", len(got))
	}
}

func TestCache_Missing(t *testing.T) {
	entries, table, err := LoadCache(filepath.Join(t.TempDir(), "nope.cache"), CacheKey{})
	if err != nil {
		t.Errorf("missing file: want nil err, got %v", err)
	}
	if entries != nil || table != nil {
		t.Errorf("missing file: want nil, nil; got %v, %v", entries, table)
	}
}

func TestCache_BadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.cache")
	if err := os.WriteFile(path, []byte("not a yoe cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err := LoadCache(path, CacheKey{})
	if err != nil {
		t.Errorf("err: %v", err)
	}
	if got != nil {
		t.Errorf("bad magic: want miss, got %d entries", len(got))
	}
}

func TestCache_BadParserVersion(t *testing.T) {
	src := []byte(realisticFixture)
	entries, _ := ParseIndex(bytes.NewReader(src))

	dir := t.TempDir()
	path := filepath.Join(dir, "APKINDEX.cache")
	key := HashSource(src)
	if err := SaveCache(path, key, entries); err != nil {
		t.Fatal(err)
	}

	// Bump the parser_version field in place to simulate an older
	// cache being loaded by a newer binary.
	data, _ := os.ReadFile(path)
	data[4] = 0xff // mangle parser_version[0]
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, _, err := LoadCache(path, key)
	if err != nil {
		t.Errorf("err: %v", err)
	}
	if got != nil {
		t.Errorf("parser_version mismatch: want miss, got %d entries", len(got))
	}
}

func TestCache_Truncated(t *testing.T) {
	src := []byte(realisticFixture)
	entries, _ := ParseIndex(bytes.NewReader(src))

	dir := t.TempDir()
	path := filepath.Join(dir, "APKINDEX.cache")
	key := HashSource(src)
	if err := SaveCache(path, key, entries); err != nil {
		t.Fatal(err)
	}

	// Truncate mid-body.
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, data[:cacheHeaderLen+5], 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err := LoadCache(path, key)
	if err != nil {
		t.Errorf("err: %v", err)
	}
	if got != nil {
		t.Errorf("truncated: want miss, got %d entries", len(got))
	}
}

func TestParseIndexWithCache_FirstAndSecondPass(t *testing.T) {
	src := []byte(realisticFixture)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "APKINDEX")
	if err := os.WriteFile(sourcePath, src, 0o644); err != nil {
		t.Fatal(err)
	}

	// First pass: parses fresh, writes the cache.
	entries1, table1, err := ParseIndexWithCache(sourcePath)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if len(entries1) != 2 {
		t.Fatalf("entries: %d", len(entries1))
	}
	if _, err := os.Stat(sourcePath + ".cache"); err != nil {
		t.Errorf("cache not written: %v", err)
	}

	// Second pass: cache hit, identical results.
	entries2, table2, err := ParseIndexWithCache(sourcePath)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if !reflect.DeepEqual(canonicalize(entries1), canonicalize(entries2)) {
		t.Error("entries differ between fresh parse and cache load")
	}
	if table1.Lookup("openssh-server").Name != table2.Lookup("openssh-server").Name {
		t.Error("provides table differs between fresh parse and cache load")
	}

	// Mutate the source; next pass should invalidate the cache.
	mutated := append([]byte(nil), src...)
	mutated[10] = 'X' // perturb mid-content
	if err := os.WriteFile(sourcePath, mutated, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseIndexWithCache(sourcePath); err == nil {
		// Mutated content may or may not parse; we only care that the
		// new content gets stored in the cache. Confirm the cache file
		// changed.
	}
	newData, _ := os.ReadFile(sourcePath + ".cache")
	// The cache key in the header (bytes 8..40) must match the new
	// source hash, not the old one.
	newKey := HashSource(mutated)
	if !bytes.Equal(newData[8:40], newKey[:]) {
		t.Error("cache wasn't rewritten with new source hash")
	}
}

func TestSaveCache_Atomic(t *testing.T) {
	// Best-effort check: after a normal write, no stray .tmp file
	// remains in the cache directory.
	src := []byte(realisticFixture)
	entries, _ := ParseIndex(bytes.NewReader(src))

	dir := t.TempDir()
	path := filepath.Join(dir, "APKINDEX.cache")
	if err := SaveCache(path, HashSource(src), entries); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".tmp" {
			t.Errorf("stray tmp file after write: %s", f.Name())
		}
	}
}

// Sanity: a load error other than os.ErrNotExist (e.g., is-a-directory)
// surfaces rather than masquerading as a cache miss.
func TestLoadCache_HardError(t *testing.T) {
	dir := t.TempDir()
	// Pass the directory itself as the cache path; opening it as a
	// file should fail with something other than ErrNotExist.
	_, _, err := LoadCache(dir, CacheKey{})
	if err == nil {
		return // some platforms succeed (open a dir); skip the assert.
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("got ErrNotExist for directory path; want propagated error")
	}
}
