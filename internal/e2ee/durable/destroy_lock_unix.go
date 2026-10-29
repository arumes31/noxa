//go:build !windows

package durable

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// LockForDestroy takes an exclusive flock on every store file in dir (the
// lock bbolt itself uses on POSIX: shared for read-only, exclusive for
// read-write openers) and holds it until release. It reports ErrInUse when
// any file is held by another opener. Held across the rename of the store
// directory, it closes the check-then-act gap of ProbeUnlocked: no other
// process can open a store between the check and the rename.
func LockForDestroy(dir string) (release func(), err error) {
	if _, e := os.Stat(dir); os.IsNotExist(e) {
		return func() {}, nil
	}
	// The exclusive directory lock keeps every current (shared holder) and
	// future store opener out, including namespace files created later.
	unlockDir, e := lockStoreDir(dir, true, 150*time.Millisecond)
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		unlockDir()
		return nil, e
	}
	var held []*os.File
	release = func() {
		for _, f := range held {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		}
		held = nil
		unlockDir()
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		f, e := os.Open(filepath.Join(dir, entry.Name())) // #nosec G304 -- a store file listed from the application-owned store directory.
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			release()
			return nil, e // never treat an unopenable file as unused
		}
		deadline := time.Now().Add(150 * time.Millisecond)
		for {
			e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if e == nil {
				held = append(held, f)
				break
			}
			if !errors.Is(e, syscall.EWOULDBLOCK) && !errors.Is(e, syscall.EINTR) {
				_ = f.Close()
				release()
				return nil, e
			}
			if time.Now().After(deadline) {
				_ = f.Close()
				release()
				return nil, ErrInUse
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return release, nil
}
