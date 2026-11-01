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

// Retry only an explicitly scoped recovery I/O operation. Scanner locks may
// deny reads as well as renames. Each operation retains a two-second budget;
// persistent locks, access denial and integrity failures remain fail-closed.
func retryClientUpdateRecoveryIO(operation func() error) error {
	deadline := time.Now().Add(2 * time.Second)
	var err error
	for attempt := 0; ; attempt++ {
		if attempt > 0 && !time.Now().Before(deadline) {
			return err
		}
		err = operation()
		if err == nil || runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(32)) || attempt >= 20 {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		time.Sleep(min(100*time.Millisecond, remaining))
	}
}

func hashClientUpdateForRecovery(path string) ([sha256.Size]byte, error) {
	var hash [sha256.Size]byte
	err := retryClientUpdateRecoveryIO(func() error {
		var err error
		hash, err = hashUpdateExecutable(path)
		return err
	})
	return hash, err
}

// Retry only this exact move, never the entire rollback transaction. Recheck
// the source identity and bytes after every wait and never replace a target
// created between the existence check and the filesystem operation.
func renameClientUpdateForRecovery(source, destination string, expected [sha256.Size]byte) error {
	identity, err := os.Lstat(source)
	if err != nil {
		return err
	}
	return retryClientUpdateRecoveryIO(func() error {
		current, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if !current.Mode().IsRegular() || !os.SameFile(identity, current) {
			return fmt.Errorf("recovery source identity changed; refusing rename")
		}
		hash, err := hashUpdateExecutable(source)
		if err != nil {
			return err
		}
		after, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if hash != expected || !os.SameFile(identity, after) {
			return fmt.Errorf("recovery source changed; refusing rename")
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			return fmt.Errorf("recovery destination is no longer available")
		}
		return renameClientUpdateRecoveryNoReplace(source, destination)
	})
}
