package main

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/minio/selfupdate"
)

// Retry only the first, still-pre-move commit operation. Replaying Apply would
// consume an exhausted asset reader, and retrying a later rename can destroy a
// recovery backup even when the library reports that rollback succeeded.
func retryClientUpdateCommit(err error, options selfupdate.Options, oldHash, newHash [sha256.Size]byte) error {
	deadline := time.Now().Add(2 * time.Second)
	for attempt := 0; attempt < 20 && retryableClientUpdateRename(err, options); attempt++ {
		if remaining := time.Until(deadline); remaining <= 0 {
			break
		} else {
			time.Sleep(min(100*time.Millisecond, remaining))
		}
		if !time.Now().Before(deadline) {
			break
		}
		// CommitBinary removes OldSavePath before its first rename. The failed
		// first rename must have left that name absent and both binaries intact.
		// Lstat rejects even a dangling symlink at the reserved backup name.
		if _, statErr := os.Lstat(options.OldSavePath); !os.IsNotExist(statErr) {
			return err
		}
		staged := filepath.Join(filepath.Dir(options.TargetPath), "."+filepath.Base(options.TargetPath)+".new")
		targetHash, targetErr := hashUpdateExecutable(options.TargetPath)
		stagedHash, stagedErr := hashUpdateExecutable(staged)
		if targetErr != nil || stagedErr != nil || targetHash != oldHash || stagedHash != newHash {
			return err
		}
		// Disk reads are not cancellable here. Do not begin another commit if
		// they exhausted the retry budget or a backup appeared while hashing.
		if !time.Now().Before(deadline) {
			return err
		}
		if _, statErr := os.Lstat(options.OldSavePath); !os.IsNotExist(statErr) {
			return err
		}
		err = selfupdate.CommitBinary(options)
	}
	return err
}

func removeClientUpdateReservation(path string, reservation os.FileInfo) {
	// The identity was captured from the still-open reservation handle. A
	// later, unrelated empty file at that name is never updater-owned cleanup.
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() == 0 && os.SameFile(info, reservation) {
		_ = os.Remove(path)
	}
}

func retryableClientUpdateRename(err error, options selfupdate.Options) bool {
	// ERROR_SHARING_VIOLATION is Windows error 32. On other operating systems
	// that errno has unrelated meanings and must never enable this retry.
	if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(32)) || selfupdate.RollbackError(err) != nil {
		return false
	}
	var rename *os.LinkError
	return errors.As(err, &rename) && rename.Op == "rename" && rename.Old == options.TargetPath && rename.New == options.OldSavePath
}
