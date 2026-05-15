# Content-Addressed Cache Implementation Plan

**Goal:** Replace the current rebuild-avoidance marker
(`build/<unit>/.yoe-hash`) with a proper content-addressed object store that
caches both source archives and built `.apk` packages. Enable sharing across
machines via S3-compatible remote backends.

**Context:** Today, the "cache" is a single file containing a hash. If you
`yoe clean` or build on a different machine, everything rebuilds from source.
Source downloads are cached locally by URL hash but not content-addressable and
not shareable. The design spec in `docs/build-environment.md` describes the full
architecture — this plan implements it.

---

## Phase 1: Local Object Store

**Files to create/modify:**

- `internal/cache/store.go` — new package
- `internal/cache/store_test.go`
- `internal/source/fetch.go` — refactor to use object store
- `internal/build/executor.go` — refactor to use object store

### Step 1: Object store package (`internal/cache/store.go`)

```go
type Store struct {
    Root string // e.g. ~/.cache/yoe-ng/objects/
}

// Key operations:
func (s *Store) Has(kind, hash string) bool
func (s *Store) Path(kind, hash string) string
func (s *Store) Put(kind, hash string, src string) error  // atomic: write to tmp/, rename
func (s *Store) Get(kind, hash string) (string, error)    // returns path or ErrNotFound
```

- `kind` is `"sources"` or `"packages/<arch>"`
- Two-char prefix directories: `ab/cd1234...5678.apk`
- Atomic writes via temp file + rename
- No locking needed — content-addressed means concurrent writes are idempotent

### Step 2: Integrate source fetching with object store

Refactor `internal/source/fetch.go`:

- **HTTP tarballs**: After download + SHA256 verification,
  `store.Put("sources", contentHash, tmpFile)`. Lookup uses content hash (from
  `unit.SHA256`), not URL hash.
- **Git repos**: Keep URL#ref hash as key (git repos are directories, not single
  files). Store under `sources/<hash>.git/`.
- Source fetch checks `store.Has()` before downloading.

### Step 3: Integrate package builds with object store

Refactor `internal/build/executor.go`:

- Before building: `store.Has("packages/"+arch, inputHash)` — if hit, copy
  `.apk` to `build/repo/`, skip build entirely.
- After building: `store.Put("packages/"+arch, inputHash, apkPath)` — store the
  built `.apk`.
- Remove the `.yoe-hash` marker file approach.

### Step 4: Repo population from cache

When a cache hit provides an `.apk`, it must be published to the local
`build/repo/<arch>/` directory so image assembly can find packages by name. This
is a file copy (or hardlink) from the object store to the repo.

### Step 5: `yoe cache` CLI commands

```
yoe cache status      # show cache size, object counts
yoe cache list        # list cached objects (sources + packages)
yoe cache clean       # remove all cached objects
yoe cache clean --older-than 30d  # evict old entries
```

---

## Phase 2: Remote Cache (S3-Compatible)

**Files to create/modify:**

- `internal/cache/remote.go`
- `internal/cache/remote_test.go`
- `internal/starlark/project.go` — add cache config to PROJECT.star

### Step 6: S3 client

Minimal S3 client (or use `github.com/aws/aws-sdk-go-v2`):

```go
type Remote struct {
    Bucket   string
    Endpoint string // for MinIO
    Region   string
}

func (r *Remote) Has(key string) bool          // HEAD object
func (r *Remote) Get(key string, dst string) error  // GET → file
func (r *Remote) Put(key string, src string) error  // PUT file →
```

Keep it simple — S3 GET/PUT/HEAD is all we need. No multipart, no listing.

### Step 7: Multi-level lookup

Update the build flow:

```go
func (s *Store) Resolve(kind, hash string, remote *Remote) (string, error) {
    // 1. Check local
    if path, err := s.Get(kind, hash); err == nil {
        return path, nil
    }
    // 2. Check remote
    if remote != nil {
        key := kind + "/" + hash[:2] + "/" + hash + ext
        localPath := s.Path(kind, hash)
        if err := remote.Get(key, localPath); err == nil {
            return localPath, nil
        }
    }
    return "", ErrNotFound
}
```

### Step 8: Push after build

After a successful build, push the `.apk` to the remote cache (if configured).
This runs in a background goroutine — the build continues without waiting for
the upload.

### Step 9: PROJECT.star configuration

```python
project(
    cache = cache(
        url = "s3://my-bucket/yoe-cache",
        # or: url = "http://minio.local:9000/yoe-cache",
        region = "us-east-1",      # optional
    ),
)
```

---

## Phase 3: Cache Signing (Optional, Later)

### Step 10: Sign packages on push

- Ed25519 key pair
- Sign the `.apk` content hash, store signature as `<hash>.sig` alongside
- Verify on pull before using

This is important for production but not needed for initial implementation.

---

## Verification

1. `go test ./internal/cache/...` — unit tests for store operations
2. Build a unit, verify `.apk` appears in object store under correct hash
3. `yoe clean openssh && yoe build openssh` — verify cache hit (no rebuild)
4. Copy object store to another machine, verify it builds without network
5. Set up MinIO, configure `cache.url`, verify push/pull works
6. Build on machine A, build same unit on machine B — verify B gets cache hit

## Current State vs. Target

| Aspect               | Current                                   | Target                                |
| -------------------- | ----------------------------------------- | ------------------------------------- |
| Source cache         | URL hash, local only                      | Content hash, shared                  |
| Package cache        | `.yoe-hash` marker, no stored objects     | Object store with `.apk` files        |
| Cache hit            | Skip build steps, reuse in-tree artifacts | Pull `.apk` from store, skip entirely |
| Remote sharing       | None                                      | S3-compatible push/pull               |
| Cross-machine        | Full rebuild                              | Pull from remote cache                |
| `yoe clean` recovery | Full rebuild                              | Re-pull from local object store       |
