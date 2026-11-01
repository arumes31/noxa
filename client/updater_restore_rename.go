package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Windows scanners can retain a sharing lock briefly after a child exits or a
// preceding rename succeeds. Retry only this exact move, never the entire
// rollback transaction, and retain the source if its ownership changes.
func renameClientUpdateForRecovery(source, destination string, expected [sha256.Size]byte) error {
	identity, err := os.Lstat(source)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for attempt := 0; ; attempt++ {
		current, statErr := os.Lstat(source)
		hash, hashErr := hashUpdateExecutable(source)
		if statErr != nil || !current.Mode().IsRegular() || !os.SameFile(identity, current) || hashErr != nil || hash != expected {
			return fmt.Errorf("recovery source changed; refusing rename")
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			return fmt.Errorf("recovery destination is no longer available")
		}
		// Hashing is not cancellable. Do not start a retry after its budget expires.
		if attempt > 0 && !time.Now().Before(deadline) {
			return err
		}
		err = os.Rename(source, destination)
		if err == nil {
			return nil
		}
		var link *os.LinkError
		if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(32)) ||
			!errors.As(err, &link) || link.Op != "rename" || link.Old != source || link.New != destination || attempt >= 20 {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		time.Sleep(min(100*time.Millisecond, remaining))
	}
}
