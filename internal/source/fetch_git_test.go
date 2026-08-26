package source

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

// gitSource creates a repo carrying tag `v1` and returns a source URL for
// it. The `.git` suffix is what routes Fetch down the git path.
func gitSource(t *testing.T) string {
	t.Helper()
	work := initRepo(t)
	run(t, work, "git", "tag", "v1")

	bare := filepath.Join(t.TempDir(), "upstream.git")
	run(t, work, "git", "clone", "-q", "--bare", work, bare)
	return bare
}

func fetchTagged(t *testing.T, src, tag string) (string, string, error) {
	t.Helper()
	var log strings.Builder
	p, err := Fetch(&yoestar.Unit{Name: "t", Source: src, Tag: tag}, nil, &log)
	return p, log.String(), err
}

// Two units sharing a repo and ref share one cache entry and build
// concurrently. Every one of them must come away with a clone that
// actually holds the ref — the beagleplay u-boot failure was a unit
// reporting "Using cached" against a clone still being populated, then
// failing to check the ref out.
func TestFetchGitConcurrentSharedSource(t *testing.T) {
	cacheIn(t)
	src := gitSource(t)

	const n = 8
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], _, errs[i] = fetchTagged(t, src, "v1")
		}()
	}
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Fatalf("fetch %d: %v", i, errs[i])
		}
		if !gitHasRef(paths[i], "v1") {
			t.Errorf("fetch %d: clone at %s does not contain ref v1", i, paths[i])
		}
	}
}

// A clone interrupted partway leaves a directory that exists but has no
// ref in it. That entry must be re-cloned, not reported as cached.
func TestFetchGitReplacesPartialClone(t *testing.T) {
	cacheIn(t)
	src := gitSource(t)

	// Seed the cache entry a first fetch would create, then hollow it out.
	path, _, err := fetchTagged(t, src, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}

	got, log, err := fetchTagged(t, src, "v1")
	if err != nil {
		t.Fatalf("fetch over partial clone: %v", err)
	}
	if strings.Contains(log, "Using cached") {
		t.Errorf("partial clone reported as cached:\n%s", log)
	}
	if !strings.Contains(log, "Discarding incomplete clone") {
		t.Errorf("discarding the partial clone was not reported:\n%s", log)
	}
	if !gitHasRef(got, "v1") {
		t.Errorf("clone at %s does not contain ref v1", got)
	}
}

// A complete entry is reused rather than re-cloned.
func TestFetchGitReusesCompleteClone(t *testing.T) {
	cacheIn(t)
	src := gitSource(t)

	if _, _, err := fetchTagged(t, src, "v1"); err != nil {
		t.Fatal(err)
	}
	_, log, err := fetchTagged(t, src, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "Using cached") {
		t.Errorf("second fetch re-cloned instead of reusing the cache:\n%s", log)
	}
}

// The lock is what serializes concurrent fetches, so it has to actually
// exclude: a fetch cannot proceed while another holder has the entry.
func TestFetchGitWaitsForLockHolder(t *testing.T) {
	cacheIn(t)
	src := gitSource(t)

	cacheDir, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	// Same key derivation fetchGit uses, reached through a throwaway fetch
	// so the test does not restate the hashing.
	path, _, err := fetchTagged(t, src, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != cacheDir {
		t.Fatalf("clone landed outside the cache: %s", path)
	}

	var log strings.Builder
	held, err := acquireCacheLock(path+".lock", &log, "")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		fetchTagged(t, src, "v1")
	}()

	select {
	case <-done:
		held.release()
		t.Fatal("fetch completed while another holder had the lock")
	case <-time.After(100 * time.Millisecond):
	}

	held.release()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("fetch did not proceed after the lock was released")
	}
}

// A run killed mid-clone leaves a temp directory behind. The next fetch of
// that key holds the lock that guarded it, so it can clear the debris.
func TestFetchGitSweepsStaleTempDirs(t *testing.T) {
	cacheIn(t)
	src := gitSource(t)

	cacheDir, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	stale, err := os.MkdirTemp(cacheDir, "deadbeef.tmp-")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := fetchTagged(t, src, "v1"); err != nil {
		t.Fatal(err)
	}

	// The sweep is scoped to the fetched key, so a temp dir belonging to a
	// different key — one another unit could still be cloning into — stays.
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("sweep removed another key's temp dir: %v", err)
	}

	left, _ := filepath.Glob(filepath.Join(cacheDir, "*.tmp-*"))
	if len(left) != 1 {
		t.Errorf("got %d temp dirs, want only the unrelated one: %v", len(left), left)
	}
}
