//go:build !windows

package durable

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// lockStoreDir takes a flock on the store directory itself: shared for every
// store opener (held for the store's lifetime), exclusive for identity
// destruction. The destroy lock therefore also covers namespace files created
// after it was taken, and an opener blocked behind it re-checks the path
// afterwards: if the directory was renamed (a deletion tombstone), it fails
// instead of opening the deleted store.
func lockStoreDir(dir string, exclusive bool, timeout time.Duration) (func(), error) {
	f, err := os.Open(dir) // #nosec G304 -- dir is the application-owned store directory, never peer input.
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrInUse
		}
		time.Sleep(10 * time.Millisecond)
	}
	release := func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
	// The path must still name the locked directory (not renamed meanwhile).
	held, e1 := f.Stat()
	now, e2 := os.Stat(dir)
	if e1 != nil || e2 != nil || !os.SameFile(held, now) {
		release()
		return nil, ErrInUse
	}
	return release, nil
}
