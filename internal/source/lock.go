package source

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// cacheLock guards one cache entry while it is being fetched. Two units
// sharing a source share an entry, and yoe builds them concurrently;
// without the lock the second unit picks up the first unit's
// half-populated entry.
//
// flock rather than a mutex, so the lock also covers two `yoe` invocations
// sharing a cache directory. flock is held per open file description, so
// separate Open calls exclude each other across goroutines and processes
// alike.
type cacheLock struct {
	f *os.File
}

// acquireCacheLock blocks until the lock on path is held, reporting waitMsg
// to w if another holder is already fetching.
//
// The lockfile stays in place on release. Unlinking it would let a waiter
// block on an inode that no longer backs the lock, handing the lock to two
// callers at once.
func acquireCacheLock(path string, w io.Writer, waitMsg string) (*cacheLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		if waitMsg != "" {
			fmt.Fprintf(w, "%s\n", waitMsg)
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}

	return &cacheLock{f: f}, nil
}

func (l *cacheLock) release() {
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
}
